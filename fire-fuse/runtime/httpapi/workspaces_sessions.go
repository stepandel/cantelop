package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/runtime/runtimeprovider"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

var (
	workspaceStoreUnavailable = unavailable("workspace_store_unavailable", "Workspace storage is unavailable")
	sessionServiceUnavailable = unavailable("runtime_unavailable", "Session service is unavailable")
)

type WorkspaceController interface {
	GetWorkspaceConfig(string) (microvm.WorkspaceRuntimeConfig, error)
	GetWorkspaceConfigByAppIDAndSlug(appID, workspaceSlug string) (microvm.WorkspaceRuntimeConfig, error)
	ListWorkspaceConfigs(string) ([]microvm.WorkspaceRuntimeConfig, error)
}

func resolveAppWorkspaceBySlug(controller WorkspaceController) http.HandlerFunc {
	if controller == nil {
		return workspaceStoreUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		config, err := controller.GetWorkspaceConfigByAppIDAndSlug(
			r.PathValue("app_id"), r.PathValue("workspace_slug"),
		)
		if err != nil {
			writeWorkspaceError(w, err, "could not resolve Workspace")
			return
		}
		writeJSON(w, http.StatusOK, newWorkspaceResponse(config))
	}
}

type workspaceResponse struct {
	ID             string                    `json:"id"`
	AppID          string                    `json:"app_id"`
	AppSlug        string                    `json:"app_slug"`
	Slug           string                    `json:"slug"`
	SessionRuntime microvm.AppSessionRuntime `json:"session_runtime"`
	CreatedAt      time.Time                 `json:"created_at"`
	UpdatedAt      time.Time                 `json:"updated_at"`
	ArchivedAt     *time.Time                `json:"archived_at,omitempty"`
}

type workspaceListResponse struct {
	Items []workspaceResponse `json:"items"`
}

type createSessionRequest struct {
	ID               string `json:"id"`
	WorkspaceID      string `json:"workspace_id"`
	KeepAliveSeconds int    `json:"keep_alive_seconds"`
	KeyHash          string `json:"key_hash,omitempty"`
}

type sessionResponse struct {
	ID               string           `json:"id"`
	WorkspaceID      string           `json:"workspace_id"`
	BuildID          string           `json:"build_id"`
	KeepAliveSeconds int              `json:"keep_alive_seconds"`
	State            session.State    `json:"state"`
	Startup          *startupResponse `json:"startup,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

type sessionListResponse struct {
	Items []sessionResponse `json:"items"`
}

type workLeaseRequest struct {
	LeaseID    string `json:"lease_id,omitempty"`
	TTLSeconds int    `json:"ttl_seconds"`
}

func listWorkspaces(controller WorkspaceController) http.HandlerFunc {
	if controller == nil {
		return workspaceStoreUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		if len(values) != 1 || len(values["app_id"]) != 1 || values.Get("app_id") == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "app_id is required")
			return
		}
		configs, err := controller.ListWorkspaceConfigs(values.Get("app_id"))
		if err != nil {
			writeWorkspaceError(w, err, "could not list Workspaces")
			return
		}
		items := make([]workspaceResponse, len(configs))
		for i := range configs {
			items[i] = newWorkspaceResponse(configs[i])
		}
		writeJSON(w, http.StatusOK, workspaceListResponse{Items: items})
	}
}

func getWorkspace(controller WorkspaceController) http.HandlerFunc {
	if controller == nil {
		return workspaceStoreUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		config, err := controller.GetWorkspaceConfig(r.PathValue("id"))
		if err != nil {
			writeWorkspaceError(w, err, "could not retrieve Workspace")
			return
		}
		writeJSON(w, http.StatusOK, newWorkspaceResponse(config))
	}
}

func createSession(runtimes runtimeprovider.Provider) http.HandlerFunc {
	if runtimes == nil {
		return sessionServiceUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var request createSessionRequest
		if err := decodeRequest(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		sessionRecord, err := runtimes.Create(r.Context(), runtimeprovider.CreateRequest{
			ID: request.ID, WorkspaceID: request.WorkspaceID,
			KeepAliveSeconds: request.KeepAliveSeconds,
			KeyHash:          request.KeyHash,
		})
		if err != nil {
			writeSessionError(w, err, "could not create Session")
			return
		}
		writeJSON(w, http.StatusAccepted, newSessionResponse(sessionRecord))
	}
}

func listSessions(runtimes runtimeprovider.Provider) http.HandlerFunc {
	if runtimes == nil {
		return sessionServiceUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		if len(values) != 1 || len(values["workspace_id"]) != 1 || values.Get("workspace_id") == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "workspace_id is required")
			return
		}
		sessionRecords, err := runtimes.List(r.Context(), values.Get("workspace_id"))
		if err != nil {
			writeSessionError(w, err, "could not list Sessions")
			return
		}
		items := make([]sessionResponse, len(sessionRecords))
		for i := range sessionRecords {
			items[i] = newSessionResponse(sessionRecords[i])
		}
		writeJSON(w, http.StatusOK, sessionListResponse{Items: items})
	}
}

func getSession(runtimes runtimeprovider.Provider) http.HandlerFunc {
	if runtimes == nil {
		return sessionServiceUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		sessionRecord, err := runtimes.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeSessionError(w, err, "could not retrieve Session")
			return
		}
		writeJSON(w, http.StatusOK, newSessionResponse(sessionRecord))
	}
}

func getSessionRoute(runtimes runtimeprovider.Provider) http.HandlerFunc {
	if runtimes == nil {
		return unavailable("runtime_unavailable", "Session routing is unavailable")
	}
	pinned, _ := runtimes.(interface {
		PinnedRoute(context.Context, string, string) (session.Route, error)
	})
	return func(w http.ResponseWriter, r *http.Request) {
		var route session.Route
		var err error
		if sandboxID := r.URL.Query().Get("sandbox_id"); sandboxID != "" {
			if pinned == nil {
				writeError(w, 503, "runtime_unavailable", "Pinned routing unavailable")
				return
			}
			route, err = pinned.PinnedRoute(r.Context(), r.PathValue("id"), sandboxID)
		} else {
			route, err = runtimes.Route(r.Context(), r.PathValue("id"))
		}
		if err != nil {
			writeSessionError(w, err, "could not retrieve Session route")
			return
		}
		writeJSON(w, http.StatusOK, route)
	}
}

func acquireSessionWorkLease(runtimes runtimeprovider.Provider) http.HandlerFunc {
	if runtimes == nil {
		return sessionServiceUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var request workLeaseRequest
		if err := decodeRequest(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		value, err := runtimes.AcquireWorkLease(r.Context(), r.PathValue("id"), request.LeaseID, time.Duration(request.TTLSeconds)*time.Second)
		if err != nil {
			writeSessionError(w, err, "could not acquire Session work lease")
			return
		}
		writeJSON(w, http.StatusOK, value)
	}
}

func renewSessionWorkLease(runtimes runtimeprovider.Provider) http.HandlerFunc {
	if runtimes == nil {
		return sessionServiceUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var request workLeaseRequest
		if err := decodeRequest(w, r, &request); err != nil || request.LeaseID != "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid work lease renewal")
			return
		}
		value, err := runtimes.RenewWorkLease(r.Context(), r.PathValue("id"), r.PathValue("lease_id"), time.Duration(request.TTLSeconds)*time.Second)
		if err != nil {
			writeSessionError(w, err, "could not renew Session work lease")
			return
		}
		writeJSON(w, http.StatusOK, value)
	}
}

func releaseSessionWorkLease(runtimes runtimeprovider.Provider) http.HandlerFunc {
	if runtimes == nil {
		return sessionServiceUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		sessionRecord, err := runtimes.ReleaseWorkLease(r.Context(), r.PathValue("id"), r.PathValue("lease_id"))
		if err != nil {
			writeSessionError(w, err, "could not release Session work lease")
			return
		}
		writeJSON(w, http.StatusOK, newSessionResponse(sessionRecord))
	}
}

func terminateSession(runtimes runtimeprovider.Provider) http.HandlerFunc {
	if runtimes == nil {
		return sessionServiceUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		sessionRecord, err := runtimes.Terminate(r.Context(), r.PathValue("id"))
		if err != nil {
			writeSessionError(w, err, "could not release Session Sandbox")
			return
		}
		writeJSON(w, http.StatusOK, newSessionResponse(sessionRecord))
	}
}

func writeWorkspaceError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, microvm.ErrWorkspaceNotFound), errors.Is(err, microvm.ErrAppNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Workspace not found")
	case errors.Is(err, microvm.ErrInvalidWorkspace):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid Workspace")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", fallback)
	}
}

func writeSessionError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, microvm.ErrManagedAdmission):
		writeError(w, 429, "managed_admission_closed", "managed capacity or allowance unavailable")
	case errors.Is(err, microvm.ErrSessionStorageQuota):
		writeError(w, http.StatusTooManyRequests, "storage_quota", "Session storage quota would be exceeded")
	case errors.Is(err, microvm.ErrReleaseAdmissionClosed):
		writeError(w, http.StatusServiceUnavailable, "release_transition", "App release is changing; retry Session creation")
	case errors.Is(err, microvm.ErrSessionNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Session not found")
	case errors.Is(err, microvm.ErrInvalidSession), errors.Is(err, microvm.ErrInvalidWorkspace):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid Session request")
	case errors.Is(err, microvm.ErrInvalidWorkLease):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid Session work lease")
	case errors.Is(err, microvm.ErrWorkLeaseNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Session work lease not found")
	case errors.Is(err, microvm.ErrWorkspaceNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Workspace not found")
	case errors.Is(err, microvm.ErrWorkspaceUnavailable):
		writeError(w, http.StatusConflict, "workspace_unavailable", "Workspace is not active")
	case errors.Is(err, microvm.ErrSessionConflict):
		writeError(w, http.StatusConflict, "session_conflict", "Session ID belongs to another Workspace")
	case errors.Is(err, microvm.ErrAdmissionLimit):
		writeError(w, http.StatusTooManyRequests, "admission_limit", "App or host admission limits would be exceeded")
	case errors.Is(err, microvm.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "invalid_state", "Session cannot transition from its current state")
	case errors.Is(err, microvm.ErrRuntimeUnavailable), errors.Is(err, microvm.ErrSupervisorUnavailable):
		writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "Session runtime is unavailable")
	case errors.Is(err, microvm.ErrStateStore):
		writeError(w, http.StatusInternalServerError, "internal_error", "could not persist Session state")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", fallback)
	}
}

func newWorkspaceResponse(config microvm.WorkspaceRuntimeConfig) workspaceResponse {
	workspace := config.Workspace
	return workspaceResponse{
		ID: workspace.ID, AppID: workspace.AppID,
		AppSlug: config.App.Slug, Slug: workspace.Slug,
		SessionRuntime: config.SessionRuntime,
		CreatedAt:      workspace.CreatedAt, UpdatedAt: workspace.UpdatedAt,
		ArchivedAt: workspace.ArchivedAt,
	}
}

func newSessionResponse(sessionRecord session.Session) sessionResponse {
	response := sessionResponse{
		ID: sessionRecord.ID, WorkspaceID: sessionRecord.WorkspaceID, BuildID: sessionRecord.BuildID,
		KeepAliveSeconds: sessionRecord.KeepAliveSeconds,
		State:            sessionRecord.State, CreatedAt: sessionRecord.CreatedAt, UpdatedAt: sessionRecord.UpdatedAt,
	}
	if sessionRecord.Startup != nil {
		response.Startup = &startupResponse{
			CompletedAt: sessionRecord.Startup.CompletedAt,
			DurationMS:  float64(sessionRecord.Startup.Duration) / float64(time.Millisecond),
		}
	}
	return response
}
