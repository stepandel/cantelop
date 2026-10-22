package gateway

import (
	"net/http"

	"github.com/stepandel/cantelop/fire-fuse/edgeprotocol"
)

// NewComputePoolHandler selects a runtime only from broker-authenticated
// metadata. Public App code cannot select a pool or supply its credentials.
func NewComputePoolHandler(standard, managed http.Handler, edgeToken string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pools := r.Header.Values(edgeprotocol.ComputePoolHeader)
		if len(pools) == 0 {
			standard.ServeHTTP(w, r)
			return
		}
		authorization := r.Header.Values(edgeprotocol.AuthorizationHeader)
		dispatch := r.Header.Values(edgeprotocol.DispatchHeader)
		if len(authorization) != 1 || !validEdgeAuthorization(authorization[0], edgeToken) || len(dispatch) != 1 || dispatch[0] != "1" {
			writeError(w, http.StatusUnauthorized, "edge_authentication_required", "edge authentication is required")
			return
		}
		if _, err := edgeDispatchTarget(r.Header); err != nil || len(pools) != 1 || (pools[0] != "standard" && pools[0] != "managed") {
			writeError(w, http.StatusBadRequest, "invalid_edge_request", "edge dispatch metadata is invalid")
			return
		}
		if pools[0] == "standard" {
			standard.ServeHTTP(w, r)
			return
		}
		if managed == nil {
			writeError(w, http.StatusServiceUnavailable, "managed_compute_unavailable", "managed compute is unavailable")
			return
		}
		managed.ServeHTTP(w, r)
	})
}
