package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/stepandel/cantelop/fire-fuse/edgeprotocol"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/sessionbroker"
	"github.com/stepandel/cantelop/internal/resourceid"
)

const integrationPrefix = "/__cantelop/integration/v2/"

var integrationSessionPath = regexp.MustCompile(`^/__cantelop/integration/v2/sessions/([A-Za-z0-9][A-Za-z0-9._:-]{0,127})(/events|/view|/requests|/messages/msg_[0-9a-f]{32})?$`)
var integrationWorkspacePath = regexp.MustCompile(`^/__cantelop/v1/workspaces/(wsp_[0-9a-f]{32})$`)

// The app-to-Edge union is implemented by the generated Worker. These are
// private gateway operations bound to the dispatcher's authoritative App.
func (h *Handler) serveIntegration(w http.ResponseWriter, r *http.Request, target edgeprotocol.DispatchTarget) bool {
	if match := integrationWorkspacePath.FindStringSubmatch(r.URL.Path); len(match) == 2 && r.URL.RawQuery == "" {
		if !integrationMethod(w, r, http.MethodGet) {
			return true
		}
		workspace, ok := h.edgeWorkspace(w, r, target.AppID, match[1])
		if ok {
			writeEdgeJSON(w, http.StatusOK, edgeWorkspaceEnvelope(workspace, target.Hostname))
		}
		return true
	}
	if !strings.HasPrefix(r.URL.Path, integrationPrefix) {
		return false
	}
	if (r.URL.Path == integrationPrefix+"messages" || r.URL.Path == integrationPrefix+"requests") && r.URL.RawQuery == "" {
		if !integrationMethod(w, r, http.MethodPost) {
			return true
		}
		var input struct {
			messageprotocol.Request
			TimeoutMS *int `json:"timeout_ms,omitempty"`
		}
		if err := decodeEdgeJSON(w, r, &input, 1<<20); err != nil || input.Request.Validate() != nil || input.Reply {
			writeError(w, http.StatusBadRequest, "invalid_message_request", "Message input is invalid")
			return true
		}
		request := r.Clone(r.Context())
		document, _ := json.Marshal(input.Request)
		if r.URL.Path == integrationPrefix+"requests" {
			if input.TimeoutMS == nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "Request timeout is required")
				return true
			}
			document, _ = json.Marshal(struct {
				messageprotocol.Request
				TimeoutMS int `json:"timeout_ms"`
			}{input.Request, *input.TimeoutMS})
		} else if input.TimeoutMS != nil {
			writeError(w, http.StatusBadRequest, "invalid_message_request", "Unexpected request timeout")
			return true
		}
		request.Body = io.NopCloser(bytes.NewReader(document))
		request.ContentLength = int64(len(document))
		if r.URL.Path == integrationPrefix+"requests" {
			h.serveEdgeSessionRequest(w, request, target)
		} else {
			h.serveEdgeMessageAcceptance(w, request, target)
		}
		return true
	}
	match := integrationSessionPath.FindStringSubmatch(r.URL.Path)
	if len(match) != 3 {
		writeError(w, http.StatusNotFound, "runtime_route_not_found", "Unknown runtime operation")
		return true
	}
	suffix := match[2]
	method := http.MethodGet
	if suffix == "" {
		method = http.MethodDelete
	} else if suffix == "/requests" {
		method = http.MethodPost
	}
	if !integrationMethod(w, r, method) {
		return true
	}
	workspace, ok := h.integrationWorkspace(w, r, target.AppID, suffix == "/events")
	if !ok {
		return true
	}
	request := r.Clone(r.Context())
	request.URL.Path = "/__cantelop/v1/sessions/" + match[1] + suffix
	query := request.URL.Query()
	query.Del("workspace_slug")
	query.Set("workspace_id", workspace.ID)
	request.URL.RawQuery = query.Encode()
	if suffix == "/requests" {
		// Selector-bound generic control delivery. Payload remains opaque.
		existing, ok := h.edgeSessionForTermination(w, request, target.AppID, match[1])
		if !ok {
			return true
		}
		if existing.WorkspaceID != workspace.ID || existing.ID != sessionbroker.LogicalID(target.AppID, match[1]) {
			writeError(w, http.StatusNotFound, "resource_not_found", "Session does not belong to Workspace")
			return true
		}
		var input struct {
			Message          messageprotocol.Message `json:"message"`
			KeepAliveSeconds int                     `json:"keep_alive_seconds"`
			TimeoutMS        int                     `json:"timeout_ms"`
		}
		if err := decodeEdgeJSON(w, r, &input, 1<<20); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_message_request", "Message input is invalid")
			return true
		}
		document, _ := json.Marshal(struct {
			messageprotocol.Request
			TimeoutMS int `json:"timeout_ms"`
		}{
			Request: messageprotocol.Request{
				Session: messageprotocol.SessionEnvelope{ID: match[1], WorkspaceID: workspace.ID, KeepAliveSeconds: input.KeepAliveSeconds},
				Message: input.Message,
			},
			TimeoutMS: input.TimeoutMS,
		})
		request.Body = io.NopCloser(bytes.NewReader(document))
		request.ContentLength = int64(len(document))
		h.serveEdgeSessionRequest(w, request, target)
		return true
	}
	switch {
	case suffix == "":
		session, ok := h.edgeSessionForTermination(w, request, target.AppID, match[1])
		if !ok {
			return true
		}
		if session.WorkspaceID != workspace.ID || session.ID != sessionbroker.LogicalID(target.AppID, match[1]) {
			writeError(w, http.StatusNotFound, "resource_not_found", "Session does not belong to Workspace")
			return true
		}
		idle, err := h.sessionBroker.Release(r.Context(), session.ID)
		if err != nil {
			h.writeEdgeSessionError(w, err, "could not release Session Sandbox")
		} else {
			writeEdgeJSON(w, http.StatusOK, idle)
		}
	case suffix == "/events":
		h.serveEdgeSessionEvents(w, request, target, match[1])
	case suffix == "/view":
		h.serveIntegrationView(w, r, workspace.ID, match[1])
	default:
		message := strings.TrimPrefix(suffix, "/messages/")
		// Apply the caller's Workspace scope before the existing status handler.
		delivery, err := h.messages.GetMessageDelivery(r.Context(), message)
		if err != nil {
			var controlErr *ControlError
			if errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusNotFound {
				writeUnknownMessage(w, message)
			} else {
				writeError(w, http.StatusServiceUnavailable, "message_queue_unavailable", "Message status is temporarily unavailable")
			}
			return true
		}
		if delivery.WorkspaceID != workspace.ID || delivery.LogicalSessionID != match[1] {
			writeUnknownMessage(w, message)
			return true
		}
		h.serveEdgeMessageStatus(w, request, target, match[1], message)
	}
	return true
}
func integrationMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeError(w, http.StatusMethodNotAllowed, "runtime_method_not_allowed", "Incorrect runtime method")
	return false
}
func (h *Handler) integrationWorkspace(w http.ResponseWriter, r *http.Request, appID string, events bool) (Workspace, bool) {
	var extra []string
	if events {
		extra = []string{"after", "stream_id"}
	}
	id, slug, ok := workspaceSelector(r.URL.Query(), extra...)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_workspace_selector", "One canonical ID or App-scoped slug is required")
		return Workspace{}, false
	}
	if id != "" {
		return h.edgeWorkspace(w, r, appID, id)
	}
	return h.edgeWorkspaceBySelector(w, r, appID, slug)
}

// workspaceSelector reads exactly one canonical Workspace ID or App-scoped
// slug. Any other query key must be listed in extra, and every key appears once.
func workspaceSelector(values url.Values, extra ...string) (id, slug string, ok bool) {
	for key, entries := range values {
		if len(entries) != 1 || (key != "workspace_id" && key != "workspace_slug" && !slices.Contains(extra, key)) {
			return "", "", false
		}
	}
	id, slug = values.Get("workspace_id"), values.Get("workspace_slug")
	if (id == "") == (slug == "") || (id != "" && !resourceid.ValidWorkspaceID(id)) ||
		(slug != "" && !resourceid.ValidSlug(slug)) {
		return "", "", false
	}
	return id, slug, true
}
