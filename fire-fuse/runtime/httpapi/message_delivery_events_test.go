package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
)

type messageEventControllerStub struct {
	query messagequeue.EventQuery
	page  messagequeue.EventPage
	err   error
}

func (stub *messageEventControllerStub) ListMessageDeliveryEvents(query messagequeue.EventQuery) (messagequeue.EventPage, error) {
	stub.query = query
	return stub.page, stub.err
}

func TestMessageDeliveryEventAPIListsOnlySafeLifecycleData(t *testing.T) {
	at := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	stub := &messageEventControllerStub{page: messagequeue.EventPage{Items: []messagequeue.Event{{
		Sequence: 17, MessageID: "msg_0123456789abcdef0123456789abcdef",
		WorkspaceID: "wsp_0123456789abcdef0123456789abcdef", LogicalSessionID: "agent:primary",
		SessionID: "ses_primary", RequestID: "req_0123456789abcdef0123456789abcdef",
		Attempt: 1, Generation: 4, Transition: messagequeue.TransitionSettled,
		Status: messagequeue.StatusHandled, WorkState: messagequeue.WorkAwaitingQuiescence, OccurredAt: at,
	}}, NextBeforeSequence: 17}}
	handler := NewHandler(stub)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/v1/message-delivery-events?workspace_id=wsp_0123456789abcdef0123456789abcdef&message_id=msg_0123456789abcdef0123456789abcdef&before_sequence=18&limit=25", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	items, ok := document["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %#v", document["items"])
	}
	event := items[0].(map[string]any)
	if event["transition"] != "settled" || event["request_id"] != "req_0123456789abcdef0123456789abcdef" {
		t.Fatalf("event = %#v", event)
	}
	for _, forbidden := range []string{"payload", "keep_alive_seconds"} {
		if _, exists := event[forbidden]; exists {
			t.Fatalf("safe event contains %q: %#v", forbidden, event)
		}
	}
	if stub.query.WorkspaceID != "wsp_0123456789abcdef0123456789abcdef" ||
		stub.query.MessageID != "msg_0123456789abcdef0123456789abcdef" ||
		stub.query.BeforeSequence != 18 || stub.query.Limit != 25 {
		t.Fatalf("query = %#v", stub.query)
	}
}

func TestMessageDeliveryEventAPIRejectsUnboundedQueries(t *testing.T) {
	handler := NewHandler(&messageEventControllerStub{})
	for _, target := range []string{
		"/v1/message-delivery-events?limit=501",
		"/v1/message-delivery-events?before_sequence=0",
		"/v1/message-delivery-events?workspace_id=",
		"/v1/message-delivery-events?unknown=true",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, body = %s", target, response.Code, response.Body.String())
		}
	}
}
