package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
)

const testMessageID = "msg_0123456789abcdef0123456789abcdef"

func TestMessageStatusReportsTransportPhase(t *testing.T) {
	for _, fixture := range []struct{ transport, outcome string }{
		{"retrying", "unknown"},
		{"recovering", "pending"},
		{"running", "pending"},
	} {
		t.Run(fixture.transport, func(t *testing.T) {
			appID := "app_0123456789abcdef0123456789abcdef"
			workspaceID := "wsp_0123456789abcdef0123456789abcdef"
			at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
			control := &fakeControl{
				workspaces: []Workspace{testWorkspace(appID, workspaceID, "production", 0, 600)},
				messageDeliveries: []MessageDelivery{{
					ID: testMessageID, WorkspaceID: workspaceID, LogicalSessionID: "agent:primary",
					State: "accepted", WorkState: "busy", AcceptedAt: at,
					Execution: &controlclient.MessageExecution{Phase: messageprotocol.ExecutionPhase(fixture.transport), PhaseAt: at, SandboxID: "sbx_test", ExecutionDeadline: at.Add(time.Minute)},
				}},
			}
			handler, err := newTestHandler(control, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			request := trustedEdgeRequest(http.MethodGet,
				"http://edge-origin.cantelop.dev/__cantelop/v1/sessions/agent:primary/messages/"+testMessageID,
				appID, "agent.cantelop.dev")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			var body struct {
				State     string `json:"state"`
				Execution struct {
					Phase          string `json:"phase"`
					TransportPhase string `json:"transport_phase"`
					Outcome        string `json:"outcome"`
				} `json:"execution"`
			}
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil ||
				body.State != "accepted" || body.Execution.Phase != fixture.transport ||
				body.Execution.TransportPhase != "" || body.Execution.Outcome != fixture.outcome {
				t.Fatalf("message status misreported transport phase: %d %s", response.Code, response.Body.String())
			}
			t.Logf("Transport status fixture: %s", strings.TrimSpace(response.Body.String()))
		})
	}
}

func TestTrustedEdgePersistsMessageWithoutStartingSession(t *testing.T) {
	appID := "app_0123456789abcdef0123456789abcdef"
	workspaceID := "wsp_0123456789abcdef0123456789abcdef"
	control := &fakeControl{workspaces: []Workspace{testWorkspace(appID, workspaceID, "production", 0, 600)}}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest(
		http.MethodPost, "http://edge-origin.cantelop.dev/__cantelop/v1/messages",
		appID, "agent.cantelop.dev")

	request.Body = io.NopCloser(strings.NewReader(`{"session":{"id":"agent:primary","workspace_id":"` + workspaceID + `","keep_alive_seconds":300},"message":{"id":"` + testMessageID + `","payload":{"type":"prompt"}}}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted ||
		!strings.Contains(response.Body.String(), `"status":"accepted"`) ||
		!strings.Contains(response.Body.String(), `"id":"`+testMessageID+`"`) {
		t.Fatalf("POST Message = %d: %s", response.Code, response.Body.String())
	}
	stored := control.messageDelivery(testMessageID)
	if stored.LogicalSessionID != "agent:primary" || stored.WorkspaceID != workspaceID ||
		stored.RequestID != "req_0123456789abcdef0123456789abcdef" ||
		string(stored.Payload) != `{"type":"prompt"}` {
		t.Fatalf("stored Message = %#v", stored)
	}
	if control.creates != 0 || len(control.sessionRecords) != 0 {
		t.Fatalf("Message acceptance started Session: creates=%d Sessions=%#v", control.creates, control.sessionRecords)
	}
}

func TestTrustedEdgeSessionRequestReturnsPersistedReply(t *testing.T) {
	appID := "app_0123456789abcdef0123456789abcdef"
	workspaceID := "wsp_0123456789abcdef0123456789abcdef"
	control := &fakeControl{
		workspaces: []Workspace{testWorkspace(appID, workspaceID, "production", 0, 600)},
		messageDeliveries: []MessageDelivery{{
			ID: testMessageID, WorkspaceID: workspaceID, LogicalSessionID: "agent:auth",
			KeepAliveSeconds: 300, Payload: json.RawMessage(`{"type":"auth.check"}`),
			ReplyRequested: true, Reply: json.RawMessage(`{"authenticated":true}`), State: "handled",
		}},
	}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest(http.MethodPost, "http://edge-origin.cantelop.dev/__cantelop/v1/requests", appID, "agent.cantelop.dev")
	request.Body = io.NopCloser(strings.NewReader(`{"session":{"id":"agent:auth","workspace_id":"` + workspaceID + `","keep_alive_seconds":300},"message":{"id":"` + testMessageID + `","payload":{"type":"auth.check"}},"timeout_ms":1000}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != `{"id":"`+testMessageID+`","reply":{"authenticated":true}}`+"\n" {
		t.Fatalf("POST request = %d: %s", response.Code, response.Body.String())
	}
}

func TestTrustedEdgeReadsMessageStatusProjection(t *testing.T) {
	appID := "app_0123456789abcdef0123456789abcdef"
	workspaceID := "wsp_0123456789abcdef0123456789abcdef"
	acceptedAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	startedAt := acceptedAt.Add(time.Second)
	handledAt := startedAt.Add(time.Second)
	control := &fakeControl{
		workspaces: []Workspace{testWorkspace(appID, workspaceID, "production", 0, 600)},
		messageDeliveries: []MessageDelivery{{
			ID: testMessageID, WorkspaceID: workspaceID, LogicalSessionID: "agent:primary",
			State: "failed", WorkState: "quiescent", ErrorCode: "message_handler_failed",
			AcceptedAt: acceptedAt, StartedAt: &startedAt, HandledAt: &handledAt,
		}},
	}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest(
		http.MethodGet,
		"http://edge-origin.cantelop.dev/__cantelop/v1/sessions/agent:primary/messages/"+testMessageID,
		appID, "agent.cantelop.dev")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"state":"failed"`) ||
		!strings.Contains(response.Body.String(), `"failed_at":"2026-08-26T12:00:02Z"`) ||
		!strings.Contains(response.Body.String(), `"code":"message_handler_failed"`) ||
		strings.Contains(response.Body.String(), "work_state") {
		t.Fatalf("GET Message status = %d: %s", response.Code, response.Body.String())
	}
}

func TestMessageStatusIsUnknownOutsideOwningSession(t *testing.T) {
	appID := "app_0123456789abcdef0123456789abcdef"
	workspaceID := "wsp_0123456789abcdef0123456789abcdef"
	control := &fakeControl{
		workspaces: []Workspace{testWorkspace(appID, workspaceID, "production", 0, 600)},
		messageDeliveries: []MessageDelivery{{
			ID: testMessageID, WorkspaceID: workspaceID, LogicalSessionID: "agent:primary",
			State: "accepted", AcceptedAt: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC),
		}},
	}
	handler, _ := newTestHandler(control, nil, nil)
	request := trustedEdgeRequest(
		http.MethodGet,
		"http://edge-origin.cantelop.dev/__cantelop/v1/sessions/agent:other/messages/"+testMessageID,
		appID, "agent.cantelop.dev")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != `{"id":"`+testMessageID+`","state":"unknown"}`+"\n" {
		t.Fatalf("GET mismatched Message = %d: %s", response.Code, response.Body.String())
	}
}
