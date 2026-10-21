package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

const computeRegistrationMaxBytes = 16 << 10
const computeConfigProjectionMaxBytes = 1 << 20

type ComputeAppRegistrar interface {
	RegisterComputeAppForUser(string, microvm.RegisterComputeAppSpec) (microvm.App, bool, error)
}

type ComputeWorkspaceRegistrar interface {
	RegisterComputeWorkspaceForUser(string, microvm.RegisterComputeWorkspaceSpec) (microvm.Workspace, bool, error)
}

type ComputeAppArchiver interface {
	ArchiveAppForUser(context.Context, string, string) (bool, error)
}

type ComputeWorkspaceArchiver interface {
	ArchiveWorkspaceForUser(context.Context, string, string, string) (bool, error)
}

type ComputeWorkspacePurger interface {
	PurgeWorkspaceForUser(context.Context, string, string, string) error
}

type ComputeConfigProjector interface {
	ReplaceComputeAppConfigForUser(string, string, int64, []microvm.ConfigValue) (bool, error)
}

type ComputeDesiredStateController interface {
	ComputeAppRegistrar
	ComputeWorkspaceRegistrar
	ComputeWorkspaceArchiver
	ComputeWorkspacePurger
	ComputeAppArchiver
	ComputeConfigProjector
}

func purgeComputeWorkspace(purger ComputeWorkspacePurger) http.HandlerFunc {
	if purger == nil {
		return unavailable("not_ready", "compute purge is unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.Body == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "compute purge is invalid")
			return
		}
		if body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1)); err != nil || len(body) != 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "compute purge has no request body")
			return
		}
		err := purger.PurgeWorkspaceForUser(
			r.Context(), r.PathValue("user_id"), r.PathValue("app_id"), r.PathValue("workspace_id"),
		)
		switch {
		case errors.Is(err, microvm.ErrWorkspaceNotFound), errors.Is(err, microvm.ErrAppNotFound), errors.Is(err, microvm.ErrComputeTenantMissing):
			// Desired absence is already satisfied.
		case err != nil:
			writeError(w, http.StatusServiceUnavailable, "cleanup_pending", "compute Workspace purge must be retried")
			return
		}
		writeJSON(w, http.StatusOK, struct {
			WorkspaceID string `json:"workspace_id"`
			Status      string `json:"status"`
		}{WorkspaceID: r.PathValue("workspace_id"), Status: "purged"})
	}
}

type computeRegistrationRequest struct {
	Managed        bool   `json:"managed,omitempty"`
	Slug           string `json:"slug"`
	BuildID        string `json:"build_id,omitempty"`
	SessionRuntime struct {
		Runtime struct {
			InternalPort int    `json:"internal_port"`
			Profile      string `json:"profile"`
		} `json:"runtime"`
	} `json:"session_runtime"`
}

func registerComputeApp(registrar ComputeAppRegistrar) http.HandlerFunc {
	if registrar == nil {
		return unavailable("not_ready", "compute registration is unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || !isJSONContentType(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusBadRequest, "invalid_request", "compute registration must be JSON")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, computeRegistrationMaxBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request computeRegistrationRequest
		if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid_request", "compute registration is invalid")
			return
		}
		app, created, err := registrar.RegisterComputeAppForUser(
			r.PathValue("user_id"),
			microvm.RegisterComputeAppSpec{CreateAppSpec: microvm.CreateAppSpec{
				ID: r.PathValue("app_id"), Slug: request.Slug, BuildID: request.BuildID, Managed: request.Managed,
				SessionRuntime: microvm.AppSessionRuntime{
					Runtime: microvm.AppRuntimeSpec{
						InternalPort: request.SessionRuntime.Runtime.InternalPort,
						Profile:      request.SessionRuntime.Runtime.Profile,
					},
				},
			}},
		)
		switch {
		case errors.Is(err, microvm.ErrAlreadyExists):
			writeError(w, http.StatusConflict, "desired_state_conflict", "compute App already exists with different desired state")
		case errors.Is(err, microvm.ErrProviderCapacity):
			writeError(w, http.StatusConflict, "provider_capacity", "compute provider cannot reserve App capacity")
		case errors.Is(err, microvm.ErrInvalidComputeTenant), errors.Is(err, microvm.ErrInvalidApp),
			errors.Is(err, microvm.ErrInvalidAppSessionRuntime),
			errors.Is(err, microvm.ErrUnknownImage),
			errors.Is(err, microvm.ErrUnknownProfile), errors.Is(err, microvm.ErrUnsupportedShape):
			writeError(w, http.StatusBadRequest, "invalid_request", "compute registration is invalid")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "internal_error", "could not register compute App")
		default:
			status := http.StatusOK
			if created {
				status = http.StatusCreated
			}
			writeJSON(w, status, struct {
				AppID  string `json:"app_id"`
				Status string `json:"status"`
			}{AppID: app.ID, Status: "registered"})
		}
	}
}

type computeWorkspaceRegistrationRequest struct {
	Slug string `json:"slug"`
}

func registerComputeWorkspace(registrar ComputeWorkspaceRegistrar) http.HandlerFunc {
	if registrar == nil {
		return unavailable("not_ready", "compute registration is unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || !isJSONContentType(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusBadRequest, "invalid_request", "compute registration must be JSON")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, computeRegistrationMaxBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request computeWorkspaceRegistrationRequest
		if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid_request", "compute registration is invalid")
			return
		}
		workspace, created, err := registrar.RegisterComputeWorkspaceForUser(
			r.PathValue("user_id"),
			microvm.RegisterComputeWorkspaceSpec{
				ID: r.PathValue("workspace_id"), AppID: r.PathValue("app_id"),
				Slug: request.Slug,
			},
		)
		switch {
		case errors.Is(err, microvm.ErrAlreadyExists):
			writeError(w, http.StatusConflict, "desired_state_conflict", "compute Workspace already exists with different desired state")
		case errors.Is(err, microvm.ErrProviderCapacity):
			writeError(w, http.StatusConflict, "provider_capacity", "compute provider cannot reserve Workspace capacity")
		case errors.Is(err, microvm.ErrAppNotFound):
			writeError(w, http.StatusNotFound, "app_not_registered", "compute App is not registered")
		case errors.Is(err, microvm.ErrInvalidComputeTenant), errors.Is(err, microvm.ErrInvalidWorkspace):
			writeError(w, http.StatusBadRequest, "invalid_request", "compute registration is invalid")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "internal_error", "could not register compute Workspace")
		default:
			status := http.StatusOK
			if created {
				status = http.StatusCreated
			}
			writeJSON(w, status, struct {
				WorkspaceID string `json:"workspace_id"`
				Status      string `json:"status"`
			}{WorkspaceID: workspace.ID, Status: "registered"})
		}
	}
}

func archiveComputeWorkspace(archiver ComputeWorkspaceArchiver) http.HandlerFunc {
	if archiver == nil {
		return unavailable("not_ready", "compute archival is unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.Body == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "compute archival is invalid")
			return
		}
		if body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1)); err != nil || len(body) != 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "compute archival has no request body")
			return
		}
		_, err := archiver.ArchiveWorkspaceForUser(
			r.Context(), r.PathValue("user_id"), r.PathValue("app_id"), r.PathValue("workspace_id"),
		)
		switch {
		case errors.Is(err, microvm.ErrWorkspaceNotFound), errors.Is(err, microvm.ErrAppNotFound), errors.Is(err, microvm.ErrComputeTenantMissing):
			// Desired absence is already satisfied.
		case err != nil:
			writeError(w, http.StatusServiceUnavailable, "cleanup_pending", "compute Workspace cleanup must be retried")
			return
		}
		writeJSON(w, http.StatusOK, struct {
			WorkspaceID string `json:"workspace_id"`
			Status      string `json:"status"`
		}{WorkspaceID: r.PathValue("workspace_id"), Status: "archived"})
	}
}

func archiveComputeApp(archiver ComputeAppArchiver) http.HandlerFunc {
	if archiver == nil {
		return unavailable("not_ready", "compute archival is unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.Body == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "compute archival is invalid")
			return
		}
		if body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1)); err != nil || len(body) != 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "compute archival has no request body")
			return
		}
		_, err := archiver.ArchiveAppForUser(r.Context(), r.PathValue("user_id"), r.PathValue("app_id"))
		switch {
		case errors.Is(err, microvm.ErrAppNotFound), errors.Is(err, microvm.ErrComputeTenantMissing):
			// Desired absence is already satisfied.
		case errors.Is(err, microvm.ErrAppBuildUploadPending):
			writeError(w, http.StatusConflict, "build_pending", "compute App has a pending Build")
			return
		case err != nil:
			writeError(w, http.StatusServiceUnavailable, "cleanup_pending", "compute App cleanup must be retried")
			return
		}
		writeJSON(w, http.StatusOK, struct {
			AppID  string `json:"app_id"`
			Status string `json:"status"`
		}{AppID: r.PathValue("app_id"), Status: "archived"})
	}
}

type computeConfigProjectionRequest struct {
	Revision int64 `json:"revision"`
	Values   []struct {
		Name  string                  `json:"name"`
		Kind  microvm.ConfigValueKind `json:"kind"`
		Value string                  `json:"value"`
	} `json:"values"`
}

func replaceComputeConfig(projector ComputeConfigProjector) http.HandlerFunc {
	if projector == nil {
		return unavailable("not_ready", "configuration projection is unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || !isJSONContentType(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusBadRequest, "invalid_request", "configuration projection must be JSON")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, computeConfigProjectionMaxBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request computeConfigProjectionRequest
		if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || request.Values == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "configuration projection is invalid")
			return
		}
		values := make([]microvm.ConfigValue, len(request.Values))
		for index, value := range request.Values {
			values[index] = microvm.ConfigValue{Name: value.Name, Kind: value.Kind, Value: value.Value, UpdatedAt: time.Now()}
		}
		_, err := projector.ReplaceComputeAppConfigForUser(
			r.PathValue("user_id"), r.PathValue("app_id"), request.Revision, values,
		)
		switch {
		case errors.Is(err, microvm.ErrStaleConfigProjection):
			writeError(w, http.StatusConflict, "stale_revision", "configuration projection revision is stale")
		case errors.Is(err, microvm.ErrConfigQuotaExceeded):
			writeError(w, http.StatusConflict, "provider_capacity", "configuration projection exceeds provider capacity")
		case errors.Is(err, microvm.ErrAppNotFound), errors.Is(err, microvm.ErrComputeTenantMissing):
			writeError(w, http.StatusNotFound, "app_not_registered", "compute App is not registered")
		case errors.Is(err, microvm.ErrInvalidConfigValue), errors.Is(err, microvm.ErrInvalidComputeTenant):
			writeError(w, http.StatusBadRequest, "invalid_request", "configuration projection is invalid")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "internal_error", "could not apply configuration projection")
		default:
			writeJSON(w, http.StatusOK, struct {
				AppID    string `json:"app_id"`
				Revision int64  `json:"revision"`
				Status   string `json:"status"`
			}{AppID: r.PathValue("app_id"), Revision: request.Revision, Status: "applied"})
		}
	}
}

func isJSONContentType(value string) bool {
	for i, character := range value {
		if character == ';' {
			value = value[:i]
			break
		}
	}
	return strings.EqualFold(strings.TrimSpace(value), "application/json")
}
