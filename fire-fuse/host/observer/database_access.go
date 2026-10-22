package observer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

// DatabaseAccess contains only the current assignment needed by the credential broker.
type DatabaseAccess struct {
	SandboxID   string `json:"sandboxId"`
	AppID       string `json:"appId"`
	WorkspaceID string `json:"workspaceId"`
	Active      bool   `json:"active"`
}

type DatabaseAccessSource interface {
	DatabaseAccess(context.Context, string) (DatabaseAccess, int, error)
}

// DatabaseAccess reads one persisted Sandbox on every request, without collecting
// host inventories, usage, or dashboard readiness.
func (c *ControlClient) DatabaseAccess(ctx context.Context, id string) (DatabaseAccess, int, error) {
	if !sandboxIDPattern.MatchString(id) {
		return DatabaseAccess{}, 0, errors.New("invalid Sandbox ID")
	}
	var sandbox struct {
		ID          string      `json:"id"`
		AppID       string      `json:"app_id"`
		WorkspaceID string      `json:"workspace_id"`
		State       model.State `json:"state"`
	}
	status, err := c.get(ctx, c.baseURL, "/v1/sandboxes/"+id, &sandbox)
	if err != nil {
		return DatabaseAccess{}, status, err
	}
	if sandbox.ID != id {
		return DatabaseAccess{}, 0, errors.New("invalid Sandbox assignment")
	}
	// Inactive Sandboxes may have released their assignment; deny them before
	// treating missing identities as an upstream fault.
	if !model.CanAccessDatabase(sandbox.State) {
		return DatabaseAccess{}, http.StatusNotFound, errors.New("inactive Sandbox")
	}
	if sandbox.AppID == "" || sandbox.WorkspaceID == "" {
		return DatabaseAccess{}, 0, errors.New("invalid Sandbox assignment")
	}
	return DatabaseAccess{SandboxID: id, AppID: sandbox.AppID, WorkspaceID: sandbox.WorkspaceID, Active: true}, http.StatusOK, nil
}

func serveDatabaseAccess(w http.ResponseWriter, r *http.Request, source any) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/sandboxes/"), "/database-access")
	if !sandboxIDPattern.MatchString(id) || r.URL.RawQuery != "" || r.URL.ForceQuery {
		http.Error(w, "invalid Sandbox", http.StatusBadRequest)
		return
	}
	reader, ok := source.(DatabaseAccessSource)
	if !ok {
		http.Error(w, "Sandbox verification is unavailable", http.StatusBadGateway)
		return
	}
	access, status, err := reader.DatabaseAccess(r.Context(), id)
	if err != nil {
		if status == http.StatusNotFound {
			// A JSON error code distinguishes an inactive Sandbox from a proxy or
			// observer that does not serve this route yet.
			document := struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}{}
			document.Error.Code = "not_found"
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(document)
			return
		}
		http.Error(w, "Sandbox verification is unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(access)
}
