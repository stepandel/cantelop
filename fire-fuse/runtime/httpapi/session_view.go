package httpapi

import (
	"errors"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
	"net/http"
)

func readSessionView(store sessionevents.Store) http.HandlerFunc {
	views, _ := store.(interface {
		View(sessionevents.Key) (sessionevents.View, error)
	})
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		if len(values) != 2 || len(values["workspace_id"]) != 1 || len(values["session_id"]) != 1 || !validSessionEventTarget(values.Get("workspace_id"), values.Get("session_id")) {
			writeError(w, 400, "invalid_request", "Invalid view target")
			return
		}
		if views == nil {
			writeError(w, 503, "session_view_unavailable", "Durable views unavailable")
			return
		}
		value, err := views.View(sessionevents.Key{WorkspaceID: values.Get("workspace_id"), SessionID: values.Get("session_id")})
		if errors.Is(err, sessionevents.ErrViewNotFound) {
			writeError(w, 404, "session_view_not_found", "No view committed")
			return
		}
		if err != nil {
			writeError(w, 503, "session_view_unavailable", "Durable views unavailable")
			return
		}
		writeJSON(w, 200, value)
	}
}
