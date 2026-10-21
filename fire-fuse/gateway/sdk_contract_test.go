package gateway

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/internal/runtimecontract"
)

func TestRemotePublicSDKContract(t *testing.T) {
	runtimecontract.Run(t, func(t *testing.T, seed runtimecontract.Seed) http.Handler {
		control := &fakeControl{workspaces: []Workspace{testWorkspace(runtimecontract.AppID, runtimecontract.WorkspaceID, "production", 0, 600)}}
		if seed.State != "" {
			var input messageprotocol.Request
			if err := json.Unmarshal([]byte(runtimecontract.Body), &input); err != nil {
				t.Fatal(err)
			}
			control.messageDeliveries = []MessageDelivery{{ID: input.Message.ID, WorkspaceID: input.Session.WorkspaceID, LogicalSessionID: input.Session.ID, KeepAliveSeconds: input.Session.KeepAliveSeconds, Payload: input.Message.Payload, ReplyRequested: true, Reply: seed.Reply, State: seed.State, ErrorCode: seed.ErrorCode}}
		}
		handler, err := newTestHandler(control, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			trusted := trustedEdgeRequest(r.Method, r.URL.String(), runtimecontract.AppID, "agent.cantelop.dev")
			for k, v := range trusted.Header {
				r.Header[k] = v
			}
			handler.ServeHTTP(w, r)
		})
	})
}
