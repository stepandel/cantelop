package gateway

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/stepandel/cantelop/fire-fuse/edgeprotocol"
	"github.com/stepandel/cantelop/internal/resourceid"
)

var integrationWorkspaceDeletePath = regexp.MustCompile(`^/__cantelop/integration/v2/workspaces/(wsp_[0-9a-f]{32})$`)

func (h *Handler) serveWorkspaceLifecycle(w http.ResponseWriter, r *http.Request, target edgeprotocol.DispatchTarget) bool {
	if r.URL.Path == integrationPrefix+"workspaces" {
		if !integrationMethod(w, r, http.MethodGet) {
			return true
		}
		id, slug, ok := workspaceSelector(r.URL.Query())
		if !ok {
			writeError(w, 400, "invalid_workspace_selector", "One canonical ID or App-scoped slug is required")
			return true
		}
		var workspace Workspace
		var err error
		if id != "" {
			workspace, err = h.workspaces.GetWorkspace(r.Context(), id)
		} else {
			workspace, err = h.workspaces.ResolveAppWorkspace(r.Context(), target.AppID, slug)
		}
		if err != nil {
			var controlErr *ControlError
			if errors.As(err, &controlErr) && controlErr.StatusCode == 404 {
				writeEdgeJSON(w, 200, map[string]any{"workspace": nil})
			} else {
				h.logger.Error("could not inspect App Workspace", "app_id", target.AppID, "workspace_id", id, "workspace_slug", slug, "error", err)
				writeError(w, 503, "control_unavailable", "Workspace lookup is temporarily unavailable")
			}
			return true
		}
		if workspace.AppID != target.AppID || workspace.ArchivedAt != nil || id != "" && workspace.ID != id || slug != "" && workspace.Slug != slug {
			writeEdgeJSON(w, 200, map[string]any{"workspace": nil})
			return true
		}
		if !resourceid.ValidWorkspaceID(workspace.ID) {
			h.logger.Error("inspected Workspace has an invalid identity", "app_id", target.AppID, "workspace_slug", slug)
			writeError(w, 503, "control_unavailable", "Workspace identity is invalid")
			return true
		}
		writeEdgeJSON(w, 200, map[string]any{"workspace": edgeWorkspaceEnvelope(workspace, target.Hostname)})
		return true
	}
	match := integrationWorkspaceDeletePath.FindStringSubmatch(r.URL.Path)
	if len(match) != 2 {
		return false
	}
	if !integrationMethod(w, r, http.MethodDelete) {
		return true
	}
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_workspace_selector", "Deletion requires a canonical ID without a query")
		return true
	}
	result, err := h.workspaceCreator.DeleteWorkspace(r.Context(), target.AppID, match[1])
	if err != nil {
		var controlErr *ControlError
		if errors.As(err, &controlErr) && controlErr.StatusCode == 404 {
			writeError(w, 404, "resource_not_found", "App resource does not exist")
		} else {
			h.logger.Error("could not delete App Workspace", "app_id", target.AppID, "workspace_id", match[1], "error", err)
			writeError(w, 503, "control_unavailable", "Workspace deletion is temporarily unavailable")
		}
		return true
	}
	if result.ID != match[1] || result.Status != "deleting" {
		h.logger.Error("product control returned a different Workspace deletion", "app_id", target.AppID, "workspace_id", match[1])
		writeError(w, 503, "control_unavailable", "Workspace deletion identity is invalid")
		return true
	}
	writeEdgeJSON(w, http.StatusAccepted, result)
	return true
}
