package gateway

import (
	"context"
	"net/http"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
)

// SessionEvents streams Session output and reads its durable view.
type SessionEvents interface {
	controlclient.SessionEvents
	SessionView(context.Context, string, string) (sessionevents.View, error)
}

func (h *Handler) serveIntegrationView(w http.ResponseWriter, r *http.Request, workspaceID, sessionID string) {
	value, err := h.events.SessionView(r.Context(), workspaceID, sessionID)
	if err != nil {
		h.writeEdgeSessionError(w, err, "read Session view")
		return
	}
	writeEdgeJSON(w, 200, map[string]any{"workspaceId": workspaceID, "sessionId": sessionID, "revision": value.Revision, "updatedAt": value.UpdatedAt, "state": value.State, "cursor": value.Cursor})
}
