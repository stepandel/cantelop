package devruntime

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/clients/cli/internal/localhttp"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
)

const localIntegrationPrefix = "/__cantelop/integration/v2/"

var localIntegrationSessionPath = regexp.MustCompile(`^/__cantelop/integration/v2/sessions/([A-Za-z0-9][A-Za-z0-9._:-]{0,127})(/events|/view|/requests|/messages/msg_[0-9a-f]{32})?$`)

var localIntegrationWorkspacePath = regexp.MustCompile(`^/__cantelop/v1/workspaces/(wsp_[0-9a-f]{32})$`)

var localWorkspaceDeletePath = regexp.MustCompile(`^/__cantelop/integration/v2/workspaces/(wsp_[0-9a-f]{32})$`)

func (runtime *localRuntime) serveIntegration(response http.ResponseWriter, request *http.Request) bool {
	if request.URL.Path == localIntegrationPrefix+"workspaces" {
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", "GET")
			localhttp.WriteError(response, 405, "runtime_method_not_allowed")
			return true
		}
		id, slug, ok := localWorkspaceSelector(request.URL.Query())
		if !ok {
			localhttp.WriteError(response, 400, "invalid_workspace_selector")
			return true
		}
		runtime.mu.Lock()
		if slug != "" {
			id = runtime.slugs[slug]
		}
		workspace, exists := runtime.workspaces[id]
		runtime.mu.Unlock()
		if !exists {
			localhttp.WriteJSON(response, 200, map[string]any{"workspace": nil})
		} else {
			localhttp.WriteJSON(response, 200, map[string]any{"workspace": runtime.workspaceResponse(workspace)})
		}
		return true
	}
	if match := localWorkspaceDeletePath.FindStringSubmatch(request.URL.Path); len(match) == 2 {
		if request.Method != http.MethodDelete {
			response.Header().Set("Allow", "DELETE")
			localhttp.WriteError(response, 405, "runtime_method_not_allowed")
			return true
		}
		if request.URL.RawQuery != "" {
			localhttp.WriteError(response, 400, "invalid_workspace_selector")
			return true
		}
		// The dev runtime has no durable workspace purge coordinator. Preserve its
		// data instead of claiming to perform the hosted lifecycle's deletion.
		localhttp.WriteError(response, 501, "workspace_deletion_unavailable_in_local_dev")
		return true
	}
	if match := localIntegrationWorkspacePath.FindStringSubmatch(request.URL.Path); len(match) == 2 && request.URL.RawQuery == "" {
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", "GET")
			localhttp.WriteError(response, 405, "runtime_method_not_allowed")
			return true
		}
		runtime.mu.Lock()
		workspace, exists := runtime.workspaces[match[1]]
		runtime.mu.Unlock()
		if !exists {
			localhttp.WriteError(response, 404, "resource_not_found")
		} else {
			localhttp.WriteJSON(response, 200, runtime.workspaceResponse(workspace))
		}
		return true
	}
	if !strings.HasPrefix(request.URL.Path, localIntegrationPrefix) {
		return false
	}
	if (request.URL.Path == localIntegrationPrefix+"messages" || request.URL.Path == localIntegrationPrefix+"requests") && request.URL.RawQuery == "" {
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", "POST")
			localhttp.WriteError(response, 405, "runtime_method_not_allowed")
			return true
		}
		var input struct {
			messageprotocol.Request
			TimeoutMS *int `json:"timeout_ms,omitempty"`
		}
		if decodeLocalRequest(request, &input) != nil || input.Request.Validate() != nil || input.Reply {
			localhttp.WriteError(response, 400, "invalid_message_request")
			return true
		}

		adapted := request.Clone(request.Context())
		document, _ := json.Marshal(input.Request)
		if request.URL.Path == localIntegrationPrefix+"requests" {
			if input.TimeoutMS == nil {
				localhttp.WriteError(response, 400, "invalid_request")
				return true
			}
			document, _ = json.Marshal(struct {
				messageprotocol.Request
				TimeoutMS int `json:"timeout_ms"`
			}{input.Request, *input.TimeoutMS})
			adapted.URL.Path = "/__cantelop/v1/requests"
		} else {
			if input.TimeoutMS != nil {
				localhttp.WriteError(response, 400, "invalid_message_request")
				return true
			}
			adapted.URL.Path = "/__cantelop/v1/messages"
		}
		adapted.Body = io.NopCloser(bytes.NewReader(document))
		adapted.ContentLength = int64(len(document))
		runtime.ServeHTTP(response, adapted)
		return true
	}
	match := localIntegrationSessionPath.FindStringSubmatch(request.URL.Path)
	if len(match) != 3 {
		localhttp.WriteError(response, 404, "runtime_route_not_found")
		return true
	}
	suffix := match[2]
	method := http.MethodGet
	if suffix == "" {
		method = http.MethodDelete
	} else if suffix == "/requests" {
		method = http.MethodPost
	}
	if request.Method != method {
		response.Header().Set("Allow", method)
		localhttp.WriteError(response, 405, "runtime_method_not_allowed")
		return true
	}
	values := request.URL.Query()
	var extra []string
	if suffix == "/events" {
		extra = []string{"after", "stream_id"}
	}
	id, slug, ok := localWorkspaceSelector(values, extra...)
	if !ok {
		localhttp.WriteError(response, 400, "invalid_workspace_selector")
		return true
	}
	runtime.mu.Lock()
	if slug != "" {
		id = runtime.slugs[slug]
	}
	workspace, exists := runtime.workspaces[id]
	runtime.mu.Unlock()
	if !exists {
		localhttp.WriteError(response, 404, "resource_not_found")
		return true
	}
	if suffix == "/view" {
		runtime.integrationView(response, request, id, match[1])
		return true
	}
	adapted := request.Clone(request.Context())
	adapted.URL.Path = "/__cantelop/v1/sessions/" + match[1] + suffix
	values.Del("workspace_slug")
	values.Set("workspace_id", workspace.ID)
	adapted.URL.RawQuery = values.Encode()
	if suffix == "/requests" {
		existing := runtime.session(localLogicalSessionID(runtime.appID, match[1]))
		if existing == nil || existing.workspaceID != workspace.ID {
			localhttp.WriteError(response, 404, "resource_not_found")
			return true
		}
		var input struct {
			Message          messageprotocol.Message `json:"message"`
			KeepAliveSeconds int                     `json:"keep_alive_seconds"`
			TimeoutMS        int                     `json:"timeout_ms"`
		}
		if decodeLocalRequest(request, &input) != nil {
			localhttp.WriteError(response, 400, "invalid_message_request")
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
		adapted.URL.Path = "/__cantelop/v1/requests"
		adapted.URL.RawQuery = ""
		adapted.Body = io.NopCloser(bytes.NewReader(document))
		adapted.ContentLength = int64(len(document))
	}
	if suffix == "" {
		session := runtime.session(localLogicalSessionID(runtime.appID, match[1]))
		if session == nil || session.workspaceID != workspace.ID {
			localhttp.WriteError(response, 404, "resource_not_found")
			return true
		}
		adapted.URL.RawQuery = ""
	}
	runtime.ServeHTTP(response, adapted)
	return true
}

// localWorkspaceSelector mirrors the gateway's selector: exactly one canonical
// Workspace ID or slug, any other key listed in extra, and every key once.
func localWorkspaceSelector(values url.Values, extra ...string) (id, slug string, ok bool) {
	for key, entries := range values {
		if len(entries) != 1 || (key != "workspace_id" && key != "workspace_slug" && !slices.Contains(extra, key)) {
			return "", "", false
		}
	}
	id, slug = values.Get("workspace_id"), values.Get("workspace_slug")
	if (id == "") == (slug == "") || (id != "" && !ids.ValidLocalWorkspaceID(id)) || (slug != "" && !ids.ValidRoutingSlug(slug)) {
		return "", "", false
	}
	return id, slug, true
}
