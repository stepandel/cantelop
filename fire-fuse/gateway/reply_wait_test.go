package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type replyWaitMessages struct {
	MessageDeliveries
	wait func(context.Context, string) (MessageDelivery, error)
}

func (m replyWaitMessages) WaitForMessageReply(ctx context.Context, id string) (MessageDelivery, error) {
	return m.wait(ctx, id)
}

func TestSessionRequestUsesReplyNotification(t *testing.T) {
	for _, mode := range []string{"completed", "timeout", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			appID := "app_0123456789abcdef0123456789abcdef"
			workspaceID := "wsp_0123456789abcdef0123456789abcdef"
			control := &fakeControl{workspaces: []Workspace{testWorkspace(appID, workspaceID, "production", 0, 600)}}
			handler, err := newTestHandler(control, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			handler.messages = replyWaitMessages{MessageDeliveries: control, wait: func(ctx context.Context, id string) (MessageDelivery, error) {
				calls++
				if id != testMessageID {
					t.Fatal("wrong Message")
				}
				switch mode {
				case "timeout":
					<-ctx.Done()
					return MessageDelivery{}, ctx.Err()
				case "unavailable":
					return MessageDelivery{}, &ControlError{StatusCode: http.StatusNotFound}
				default:
					return MessageDelivery{ID: id, State: "handled", Reply: []byte(`"ready"`)}, nil
				}
			}}
			request := trustedEdgeRequest(http.MethodPost, "http://edge-origin.cantelop.dev/__cantelop/v1/requests", appID, "agent.cantelop.dev")
			request.Body = io.NopCloser(strings.NewReader(`{"session":{"id":"agent:primary","workspace_id":"` + workspaceID + `","keep_alive_seconds":300},"message":{"id":"` + testMessageID + `","payload":{}},"timeout_ms":100}`))
			response := httptest.NewRecorder()
			started := time.Now()
			handler.ServeHTTP(response, request)
			expected := map[string]int{"completed": http.StatusOK, "timeout": http.StatusGatewayTimeout, "unavailable": http.StatusServiceUnavailable}[mode]
			if response.Code != expected || calls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
			}
			if mode == "completed" && time.Since(started) > 300*time.Millisecond {
				t.Fatal("notification still waited for polling timer")
			}
		})
	}
}
