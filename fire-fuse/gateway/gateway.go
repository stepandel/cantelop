// Package gateway accepts authenticated App coordination requests for Fire
// Fuse's loopback control plane.
package gateway

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/edgeprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"github.com/stepandel/cantelop/fire-fuse/sessionbroker"
	"github.com/stepandel/cantelop/internal/resourceid"
)

var (
	logicalConcretePattern      = regexp.MustCompile(`^ses_[0-9a-f]{32}$`)
	sessionKeyPattern           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	sdkSessionPathPattern       = regexp.MustCompile(`^/__cantelop/v1/sessions/([A-Za-z0-9][A-Za-z0-9._:-]{0,127})$`)
	sdkSessionEventsPathPattern = regexp.MustCompile(`^/__cantelop/v1/sessions/([A-Za-z0-9][A-Za-z0-9._:-]{0,127})/events$`)
	sdkMessageStatusPathPattern = regexp.MustCompile(`^/__cantelop/v1/sessions/([A-Za-z0-9][A-Za-z0-9._:-]{0,127})/messages/(msg_[0-9a-f]{32})$`)
	edgeTokenPattern            = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Handler struct {
	syncFiles           controlclient.WorkspaceSync
	databaseCredentials DatabaseCredentialsProvider
	environment         EnvironmentProvider
	workspaces          WorkspaceDirectory
	workspaceCreator    WorkspaceCreator
	sessionBroker       *sessionbroker.Broker
	messages            MessageDeliveries
	events              SessionEvents
	logger              *slog.Logger
	now                 func() time.Time
	edgeToken           string
	appDomain           string
}

type Dependencies struct {
	SyncFiles           controlclient.WorkspaceSync
	DatabaseCredentials DatabaseCredentialsProvider
	Environment         EnvironmentProvider
	Workspaces          WorkspaceDirectory
	WorkspaceCreator    WorkspaceCreator
	SessionBroker       *sessionbroker.Broker
	Messages            MessageDeliveries
	Events              SessionEvents
	EdgeToken           string
	AppDomain           string
}

func NewHandler(dependencies Dependencies, logger *slog.Logger) (*Handler, error) {
	if dependencies.AppDomain == "" {
		dependencies.AppDomain = edgeprotocol.PublicAppDomain
	}
	if err := edgeprotocol.ValidateAppDomain(dependencies.AppDomain); err != nil {
		return nil, err
	}
	if dependencies.Workspaces == nil || dependencies.WorkspaceCreator == nil || dependencies.SessionBroker == nil ||
		dependencies.Messages == nil || dependencies.Events == nil || !edgeTokenPattern.MatchString(dependencies.EdgeToken) {
		return nil, errors.New("Workspace directory, Workspace creator, Session broker, Message queue, Session events, and edge token are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		syncFiles:           dependencies.SyncFiles,
		databaseCredentials: dependencies.DatabaseCredentials,
		environment:         dependencies.Environment,
		workspaces:          dependencies.Workspaces, workspaceCreator: dependencies.WorkspaceCreator,
		sessionBroker: dependencies.SessionBroker, messages: dependencies.Messages,
		events: dependencies.Events,
		logger: logger, now: time.Now,
		edgeToken: dependencies.EdgeToken,
		appDomain: dependencies.AppDomain,
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.serveLocalStatus(w, r) {
		return
	}
	if h.serveEdgeDispatch(w, r) {
		return
	}
	writeError(w, http.StatusNotFound, "sdk_route_required", "runtime ingress requires a supported trusted SDK route")
}

func (h *Handler) serveEdgeDispatch(w http.ResponseWriter, r *http.Request) bool {
	dispatchValues := r.Header.Values(edgeprotocol.DispatchHeader)
	if len(dispatchValues) == 0 {
		return false
	}
	if len(dispatchValues) != 1 || dispatchValues[0] != "1" {
		writeError(w, http.StatusBadRequest, "invalid_edge_request", "edge dispatch metadata is invalid")
		return true
	}
	authorization := r.Header.Values(edgeprotocol.AuthorizationHeader)
	if len(authorization) != 1 || !validEdgeAuthorization(authorization[0], h.edgeToken) {
		writeError(w, http.StatusUnauthorized, "edge_authentication_required", "edge authentication is required")
		return true
	}
	target, err := edgeDispatchTarget(r.Header, h.appDomain)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_edge_request", "edge dispatch metadata is invalid")
		return true
	}
	if h.serveEnvironment(w, r, target) {
		return true
	}
	if h.serveWorkspaceSync(w, r, target) || h.serveWorkspaceLifecycle(w, r, target) || h.serveIntegration(w, r, target) {
		return true
	}
	if r.URL.Path == "/__cantelop/v1/workspaces/database/credentials" && r.URL.RawQuery == "" {
		h.serveDatabaseCredentials(w, r, target)
		return true
	}
	if r.URL.Path == "/__cantelop/v1/workspaces" && r.URL.RawQuery == "" {
		h.serveEdgeWorkspaceCreate(w, r, target)
		return true
	}
	if r.URL.Path == "/__cantelop/v1/workspaces/open" && r.URL.RawQuery == "" {
		h.serveEdgeWorkspaceOpen(w, r, target)
		return true
	}
	if r.URL.Path == "/__cantelop/v1/sessions" && r.URL.RawQuery == "" {
		h.serveEdgeSessionCreate(w, r, target)
		return true
	}
	if r.URL.Path == "/__cantelop/v1/sessions/open" && r.URL.RawQuery == "" {
		h.serveEdgeSessionOpen(w, r, target)
		return true
	}
	if r.URL.Path == "/__cantelop/v1/messages" && r.URL.RawQuery == "" {
		h.serveEdgeMessageAcceptance(w, r, target)
		return true
	}
	if r.URL.Path == "/__cantelop/v1/requests" && r.URL.RawQuery == "" {
		h.serveEdgeSessionRequest(w, r, target)
		return true
	}
	if match := sdkMessageStatusPathPattern.FindStringSubmatch(r.URL.Path); len(match) == 3 {
		h.serveEdgeMessageStatus(w, r, target, match[1], match[2])
		return true
	}
	if match := sdkSessionEventsPathPattern.FindStringSubmatch(r.URL.Path); len(match) == 2 {
		h.serveEdgeSessionEvents(w, r, target, match[1])
		return true
	}
	if match := sdkSessionPathPattern.FindStringSubmatch(r.URL.Path); len(match) == 2 && r.URL.RawQuery == "" {
		h.serveEdgeSessionTerminate(w, r, target, match[1])
		return true
	}
	writeError(w, http.StatusNotFound, "runtime_route_not_found", "edge dispatch requires a Workspace or Session operation")
	return true
}

func validEdgeAuthorization(value, token string) bool {
	presented, found := strings.CutPrefix(value, "Bearer ")
	if !found || len(presented) != len(token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1
}

func edgeDispatchTarget(headers http.Header, domains ...string) (edgeprotocol.DispatchTarget, error) {
	value := func(name string) (string, bool) {
		values := headers.Values(name)
		return strings.Join(values, ""), len(values) == 1
	}
	requestID, requestOK := value(edgeprotocol.RequestIDHeader)
	appID, appOK := value(edgeprotocol.AppIDHeader)
	hostname, hostnameOK := value(edgeprotocol.HostnameHeader)
	if !requestOK || !appOK || !hostnameOK {
		return edgeprotocol.DispatchTarget{}, errors.New("edge dispatch headers must occur exactly once")
	}
	target := edgeprotocol.DispatchTarget{RequestID: requestID, AppID: appID, Hostname: hostname}
	return target, target.Validate(domains...)
}

func (h *Handler) serveEdgeWorkspaceCreate(
	w http.ResponseWriter,
	r *http.Request,
	target edgeprotocol.DispatchTarget,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "runtime_method_not_allowed", "Workspace creation requires POST")
		return
	}
	var input struct {
		Slug string `json:"slug"`
	}
	if err := decodeEdgeJSON(w, r, &input, 4<<10); err != nil ||
		!resourceid.ValidSlug(input.Slug) {
		writeError(w, http.StatusBadRequest, "invalid_workspace_request", "Workspace creation input is invalid")
		return
	}
	workspace, err := h.workspaceCreator.CreateWorkspace(r.Context(), target.AppID, input.Slug)
	if err != nil {
		var controlErr *ControlError
		switch {
		case errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusNotFound:
			writeError(w, http.StatusNotFound, "app_not_found", "App does not exist")
		case errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusConflict:
			writeError(w, http.StatusConflict, "workspace_conflict", "Workspace slug is already in use")
		default:
			h.logger.Error("could not create App Workspace", "app_id", target.AppID, "error", err)
			writeError(w, http.StatusServiceUnavailable, "control_unavailable", "Workspace control is temporarily unavailable")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(edgeWorkspaceEnvelope(workspace, target.Hostname)); err != nil {
		h.logger.Error("could not encode Workspace response", "workspace_id", workspace.ID, "error", err)
	}
}

func (h *Handler) serveEdgeWorkspaceOpen(
	w http.ResponseWriter,
	r *http.Request,
	target edgeprotocol.DispatchTarget,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "runtime_method_not_allowed", "Workspace open requires POST")
		return
	}
	var input struct {
		Slug string `json:"slug"`
	}
	if err := decodeEdgeJSON(w, r, &input, 4<<10); err != nil ||
		!resourceid.ValidSlug(input.Slug) {
		writeError(w, http.StatusBadRequest, "invalid_workspace_request", "Workspace open input is invalid")
		return
	}
	// Registered Workspaces are already available to Message admission. Avoid
	// a product-control round trip for each new Session opened by slug.
	workspace, err := h.workspaces.ResolveAppWorkspace(r.Context(), target.AppID, input.Slug)
	if err == nil {
		if workspace.ID == "" || workspace.AppID != target.AppID || workspace.Slug != input.Slug {
			h.logger.Error("resolved Workspace differs from requested App and slug")
			writeError(w, http.StatusServiceUnavailable, "control_unavailable", "Workspace control is temporarily unavailable")
			return
		}
		if workspace.ArchivedAt == nil {
			writeEdgeJSON(w, http.StatusOK, edgeWorkspaceEnvelope(workspace, target.Hostname))
			return
		}
		// An archived slug may be reopened as a replacement by product control.
	} else {
		var controlErr *ControlError
		if !errors.As(err, &controlErr) || controlErr.StatusCode != http.StatusNotFound {
			h.logger.Error("could not resolve registered Workspace", "app_id", target.AppID, "workspace_slug", input.Slug)
			writeError(w, http.StatusServiceUnavailable, "control_unavailable", "Workspace control is temporarily unavailable")
			return
		}
	}
	workspace, err = h.workspaceCreator.OpenWorkspace(r.Context(), target.AppID, input.Slug)
	if err != nil {
		var controlErr *ControlError
		if errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusNotFound {
			writeError(w, http.StatusNotFound, "app_not_found", "App does not exist")
		} else {
			h.logger.Error("could not open App Workspace", "app_id", target.AppID, "slug", input.Slug, "error", err)
			writeError(w, http.StatusServiceUnavailable, "control_unavailable", "Workspace control is temporarily unavailable")
		}
		return
	}
	if !h.waitForOpenedWorkspace(w, r, workspace) {
		return
	}
	writeEdgeJSON(w, http.StatusOK, edgeWorkspaceEnvelope(workspace, target.Hostname))
}

// The product control API commits the Workspace before the control worker
// registers it on this host. Only acknowledge open once Message admission can
// resolve that same Workspace locally.
func (h *Handler) waitForOpenedWorkspace(w http.ResponseWriter, r *http.Request, expected Workspace) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		workspace, err := h.workspaces.GetWorkspace(ctx, expected.ID)
		if err == nil {
			if workspace.ID != expected.ID || workspace.AppID != expected.AppID || workspace.Slug != expected.Slug {
				h.logger.Error("registered Workspace differs from product control", "workspace_id", expected.ID)
				writeError(w, http.StatusServiceUnavailable, "control_unavailable", "Workspace control is temporarily unavailable")
				return false
			}
			if workspace.ArchivedAt != nil {
				writeError(w, http.StatusConflict, "workspace_unavailable", "Workspace is archived")
				return false
			}
			return true
		}
		if ctx.Err() == nil {
			var controlErr *ControlError
			if !errors.As(err, &controlErr) || controlErr.StatusCode != http.StatusNotFound {
				h.logger.Error("could not resolve opened Workspace", "workspace_id", expected.ID, "error", err)
				writeError(w, http.StatusServiceUnavailable, "control_unavailable", "Workspace control is temporarily unavailable")
				return false
			}
		}
		select {
		case <-ctx.Done():
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusServiceUnavailable, "workspace_not_ready", "Workspace registration is still pending")
			return false
		case <-ticker.C:
		}
	}
}

type workspaceEnvelope struct {
	ID         string     `json:"id"`
	AppID      string     `json:"app_id"`
	Slug       string     `json:"slug"`
	Hostname   string     `json:"hostname"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func edgeWorkspaceEnvelope(workspace Workspace, hostname string) workspaceEnvelope {
	return workspaceEnvelope{
		ID: workspace.ID, AppID: workspace.AppID, Slug: workspace.Slug, Hostname: hostname,
		ArchivedAt: workspace.ArchivedAt, CreatedAt: workspace.CreatedAt, UpdatedAt: workspace.UpdatedAt,
	}
}

func (h *Handler) serveEdgeSessionCreate(
	w http.ResponseWriter,
	r *http.Request,
	target edgeprotocol.DispatchTarget,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "runtime_method_not_allowed", "Session creation requires POST")
		return
	}
	var input struct {
		ID               string `json:"id"`
		WorkspaceID      string `json:"workspace_id"`
		KeepAliveSeconds int    `json:"keep_alive_seconds"`
	}
	if err := decodeEdgeJSON(w, r, &input, 4<<10); err != nil ||
		!resourceid.ValidSessionID(input.ID) ||
		input.KeepAliveSeconds < model.MinSessionKeepAliveSeconds || input.KeepAliveSeconds > model.MaxSessionKeepAliveSeconds {
		writeError(w, http.StatusBadRequest, "invalid_session_request", "Session creation input is invalid")
		return
	}
	workspace, ok := h.edgeWorkspace(w, r, target.AppID, input.WorkspaceID)
	if !ok {
		return
	}
	activeSession, err := h.sessionBroker.Create(r.Context(), sessionbroker.CreateSpec{
		ID: input.ID, WorkspaceID: workspace.ID, KeepAliveSeconds: input.KeepAliveSeconds,
	})
	if err != nil {
		h.writeEdgeSessionError(w, err, "could not create Session")
		return
	}
	writeEdgeJSON(w, http.StatusAccepted, activeSession)
}

func (h *Handler) serveEdgeSessionOpen(
	w http.ResponseWriter,
	r *http.Request,
	target edgeprotocol.DispatchTarget,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "runtime_method_not_allowed", "Session open requires POST")
		return
	}
	var input struct {
		Key              string `json:"key"`
		WorkspaceID      string `json:"workspace_id"`
		Workspace        string `json:"workspace"`
		KeepAliveSeconds *int   `json:"keep_alive_seconds"`
	}
	if err := decodeEdgeJSON(w, r, &input, 4<<10); err != nil ||
		!sessionKeyPattern.MatchString(input.Key) ||
		(input.WorkspaceID != "" && input.Workspace != "") ||
		input.KeepAliveSeconds == nil ||
		*input.KeepAliveSeconds < model.MinSessionKeepAliveSeconds || *input.KeepAliveSeconds > model.MaxSessionKeepAliveSeconds {
		writeError(w, http.StatusBadRequest, "invalid_session_request", "Session open input is invalid")
		return
	}
	var workspace Workspace
	var ok bool
	if input.WorkspaceID != "" {
		workspace, ok = h.edgeWorkspace(w, r, target.AppID, input.WorkspaceID)
	} else if resourceid.ValidSlug(input.Workspace) {
		workspace, ok = h.edgeWorkspaceBySelector(w, r, target.AppID, input.Workspace)
	} else {
		writeError(w, http.StatusBadRequest, "invalid_session_request", "Session open input is invalid")
		return
	}
	if !ok {
		return
	}
	activeSession, err := h.sessionBroker.OpenKeyed(r.Context(), sessionbroker.KeyedSpec{
		WorkspaceID: workspace.ID, WorkspaceSlug: workspace.Slug, Key: input.Key,
		KeepAliveSeconds: *input.KeepAliveSeconds,
	})
	if err != nil {
		h.writeEdgeSessionError(w, err, "could not open Session")
		return
	}
	writeEdgeJSON(w, http.StatusOK, activeSession)
}

func (h *Handler) serveEdgeSessionTerminate(
	w http.ResponseWriter,
	r *http.Request,
	target edgeprotocol.DispatchTarget,
	sessionID string,
) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		writeError(w, http.StatusMethodNotAllowed, "runtime_method_not_allowed", "releasing a Session Sandbox requires DELETE")
		return
	}
	activeSession, ok := h.edgeSessionForTermination(w, r, target.AppID, sessionID)
	if !ok {
		return
	}
	idle, err := h.sessionBroker.Release(r.Context(), activeSession.ID)
	if err != nil {
		h.writeEdgeSessionError(w, err, "could not release Session Sandbox")
		return
	}
	// Release only the Sandbox. Event replay and subscriptions belong to the
	// logical Session and must survive its next Sandbox allocation.
	writeEdgeJSON(w, http.StatusOK, idle)
}

func (h *Handler) edgeSessionForTermination(
	w http.ResponseWriter,
	r *http.Request,
	appID, sessionID string,
) (Session, bool) {
	activeSession, err := h.sessionBroker.ResolvePresented(r.Context(), appID, sessionID)
	if err == nil {
		if _, ok := h.edgeWorkspace(w, r, appID, activeSession.WorkspaceID); !ok {
			return Session{}, false
		}
		return activeSession, true
	}
	h.writeEdgeSessionError(w, err, "could not resolve Session")
	return Session{}, false
}

func (h *Handler) edgeWorkspace(
	w http.ResponseWriter,
	r *http.Request,
	appID, workspaceID string,
) (Workspace, bool) {
	workspace, err := h.workspaces.GetWorkspace(r.Context(), workspaceID)
	if err != nil || workspace.AppID != appID {
		var controlErr *ControlError
		if err == nil || (errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusNotFound) {
			writeError(w, http.StatusNotFound, "resource_not_found", "App resource does not exist")
			return Workspace{}, false
		}
		h.logger.Error("could not resolve App Workspace", "app_id", appID, "workspace_id", workspaceID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "control_unavailable", "App resources are temporarily unavailable")
		return Workspace{}, false
	}
	if workspace.ArchivedAt != nil {
		writeError(w, http.StatusConflict, "workspace_unavailable", "Workspace is archived")
		return Workspace{}, false
	}
	return workspace, true
}

func (h *Handler) edgeWorkspaceBySelector(
	w http.ResponseWriter,
	r *http.Request,
	appID, slug string,
) (Workspace, bool) {
	workspace, err := h.workspaces.ResolveAppWorkspace(r.Context(), appID, slug)
	if err != nil {
		var controlErr *ControlError
		if errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusNotFound {
			writeError(w, http.StatusNotFound, "resource_not_found", "App resource does not exist")
			return Workspace{}, false
		}
		h.logger.Error("could not resolve App Workspace", "app_id", appID, "workspace_slug", slug, "error", err)
		writeError(w, http.StatusServiceUnavailable, "control_unavailable", "App resources are temporarily unavailable")
		return Workspace{}, false
	}
	if workspace.AppID != appID || workspace.Slug != slug {
		writeError(w, http.StatusNotFound, "resource_not_found", "App resource does not exist")
		return Workspace{}, false
	}
	if workspace.ArchivedAt != nil {
		writeError(w, http.StatusConflict, "workspace_unavailable", "Workspace is archived")
		return Workspace{}, false
	}
	return workspace, true
}

func (h *Handler) writeEdgeSessionError(w http.ResponseWriter, err error, operation string) {
	var controlErr *ControlError
	switch {
	case errors.As(err, &controlErr) && controlErr.Code == "managed_admission_closed":
		writeError(w, 429, "managed_admission_closed", "managed capacity or allowance unavailable")
	case errors.Is(err, ErrAdmissionLimit):
		writeError(w, http.StatusTooManyRequests, "admission_limit", "Session capacity is exhausted")
	case errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusTooManyRequests && controlErr.Code == "storage_quota":
		writeError(w, http.StatusTooManyRequests, "storage_quota", "Session storage quota would be exceeded")
	case errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusNotFound:
		writeError(w, http.StatusNotFound, "session_not_found", "Session does not exist")
	case errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusConflict:
		writeError(w, http.StatusConflict, controlErr.Code, "Session request conflicts with its current state")
	default:
		h.logger.Error(operation, "error", err)
		writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "Session control is temporarily unavailable")
	}
}

func writeEdgeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeEdgeJSON(w http.ResponseWriter, r *http.Request, destination any, limit int64) error {
	return decodeEdgeJSONWithin(w, r, destination, limit, 10*time.Second)
}

func decodeEdgeJSONWithin(w http.ResponseWriter, r *http.Request, destination any, limit int64, timeout time.Duration) error {
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	reader := io.LimitReader(r.Body, limit+1)
	document, err := io.ReadAll(reader)
	if err != nil || int64(len(document)) > limit {
		return errors.New("request body exceeds limit")
	}
	// Only clear after EOF. A rejected/partial body must not leave net/http
	// draining an unbounded slow upload after this handler returns.
	_ = controller.SetReadDeadline(time.Time{})
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func (h *Handler) serveLocalStatus(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet ||
		(r.URL.Path != "/healthz" && r.URL.Path != "/readyz") ||
		!isDirectLoopbackRequest(r) {
		return false
	}
	if r.URL.Path == "/readyz" {
		if _, err := h.workspaces.ListWorkspaces(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "not_ready", "control plane is unavailable")
			return true
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
	return true
}

// forwardedRequestHeaders are set by the front proxy on everything it passes
// through. A caller cannot suppress them, so their absence distinguishes a
// direct loopback client from public traffic.
var forwardedRequestHeaders = []string{
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"Forwarded",
}

// isDirectLoopbackRequest reports whether a request came straight from a
// loopback client rather than through the front proxy. The Host header alone
// cannot decide this: the proxy also connects over loopback, so a public caller
// that sends "Host: 127.0.0.1" would otherwise reach operator-only status.
func isDirectLoopbackRequest(r *http.Request) bool {
	if !isLoopbackAddress(r.RemoteAddr) || !isLoopbackHost(r.Host) {
		return false
	}
	for _, header := range forwardedRequestHeaders {
		if len(r.Header.Values(header)) > 0 {
			return false
		}
	}
	return true
}

func isLoopbackAddress(rawAddress string) bool {
	host, _, err := net.SplitHostPort(rawAddress)
	if err != nil {
		host = rawAddress
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.Unmap().IsLoopback()
}

func isLoopbackHost(rawHost string) bool {
	host := rawHost
	if parsedHost, _, err := net.SplitHostPort(rawHost); err == nil {
		host = parsedHost
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.Unmap().IsLoopback()
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
