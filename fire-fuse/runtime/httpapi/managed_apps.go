package httpapi

import (
	"encoding/json"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"io"
	"net/http"
)

type managedController interface {
	ManagedHostID() string
	ManagedActivityForUser(string, string) (int, error)
	ProjectManagedForUser(string, string, microvm.ManagedProjection) error
	ReadManagedForUser(string, string) (microvm.ManagedState, []microvm.ManagedUsage, error)
	AckManagedForUser(string, string, string) error
}

func managedRoutes(mux *http.ServeMux, control PreparedBuildController) {
	c, ok := control.(managedController)
	if !ok {
		return
	}
	path := "/v1/internal/users/{user_id}/apps/{app_id}/managed-policy"
	mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		state, usage, err := c.ReadManagedForUser(r.PathValue("user_id"), r.PathValue("app_id"))
		if err != nil {
			writeError(w, 503, "policy_unavailable", "managed policy unavailable")
			return
		}
		activity, err := c.ManagedActivityForUser(r.PathValue("user_id"), r.PathValue("app_id"))
		if err != nil {
			writeError(w, 503, "activity_unavailable", "activity unavailable")
			return
		}
		writeJSON(w, 200, map[string]any{"state": state, "usage": usage, "activity": activity, "hostId": c.ManagedHostID()})
	})
	mux.HandleFunc("PUT "+path, func(w http.ResponseWriter, r *http.Request) {
		var next microvm.ManagedProjection
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		d.DisallowUnknownFields()
		if r.URL.RawQuery != "" || !isJSONContentType(r.Header.Get("Content-Type")) || d.Decode(&next) != nil || d.Decode(&struct{}{}) != io.EOF {
			writeError(w, 400, "invalid_policy", "invalid managed policy")
			return
		}
		if err := c.ProjectManagedForUser(r.PathValue("user_id"), r.PathValue("app_id"), next); err != nil {
			writeError(w, 409, "policy_rejected", "managed policy rejected")
			return
		}
		writeJSON(w, 200, map[string]any{"revision": next.Revision})
	})
	mux.HandleFunc("DELETE "+path+"/usage/{usage_id}", func(w http.ResponseWriter, r *http.Request) {
		if err := c.AckManagedForUser(r.PathValue("user_id"), r.PathValue("app_id"), r.PathValue("usage_id")); err != nil {
			writeError(w, 503, "usage_unavailable", "usage unavailable")
			return
		}
		writeJSON(w, 200, map[string]any{"acknowledged": true})
	})
}
