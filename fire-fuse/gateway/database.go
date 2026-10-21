package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/edgeprotocol"
	"github.com/stepandel/cantelop/internal/resourceid"
)

type DatabaseCredentials struct {
	URL       string `json:"url"`
	AuthToken string `json:"authToken"`
	ExpiresAt string `json:"expiresAt"`
}
type DatabaseCredentialsProvider interface {
	DatabaseCredentials(context.Context, string, string) (DatabaseCredentials, error)
}

func (c *ProductClient) DatabaseCredentials(ctx context.Context, appID, workspaceID string) (DatabaseCredentials, error) {
	if !resourceid.ValidAppID(appID) || !resourceid.ValidWorkspaceID(workspaceID) {
		return DatabaseCredentials{}, &ControlError{StatusCode: http.StatusNotFound}
	}
	document, _ := json.Marshal(map[string]string{"workspace_id": workspaceID})
	var credentials DatabaseCredentials
	err := controlclient.Do(ctx, c.client, c.baseURL, http.MethodPost, "/internal/v1/apps/"+appID+"/workspaces/database/credentials", bytes.NewReader(document), &credentials)
	return credentials, err
}
func (h *Handler) serveDatabaseCredentials(w http.ResponseWriter, r *http.Request, target edgeprotocol.DispatchTarget) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "runtime_method_not_allowed", "database credentials require POST")
		return
	}
	var input struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if decodeEdgeJSON(w, r, &input, 4096) != nil || !resourceid.ValidWorkspaceID(input.WorkspaceID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid database request")
		return
	}
	if h.databaseCredentials == nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "database credentials unavailable")
		return
	}
	credentials, err := h.databaseCredentials.DatabaseCredentials(r.Context(), target.AppID, input.WorkspaceID)
	if err != nil {
		var controlErr *ControlError
		switch {
		case errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusNotFound:
			writeError(w, http.StatusNotFound, "workspace_not_found", "workspace not found")
		case errors.As(err, &controlErr) && controlErr.Code == "database_not_ready":
			writeError(w, http.StatusServiceUnavailable, "database_not_ready", "workspace database is not ready")
		default:
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "database credentials unavailable")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(credentials)
}
