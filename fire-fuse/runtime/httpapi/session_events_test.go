package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
)

func TestSessionEventsControlAPI(t *testing.T) {
	client, err := newSessionEventTestClient()
	if err != nil {
		t.Fatal(err)
	}
	input := controlclient.SessionEventAppend{
		WorkspaceID: "wsp_a", SessionID: "orders", MessageID: "msg_0123456789abcdef0123456789abcdef",
		Data: json.RawMessage(`{"token":"hello"}`),
	}
	event, err := client.AppendSessionEvent(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if event.Sequence != 1 || event.SessionID != input.SessionID || event.MessageID != input.MessageID || event.CreatedAt.IsZero() {
		t.Fatalf("unexpected event: %#v", event)
	}
	batch, err := client.ReadSessionEvents(context.Background(), input.WorkspaceID, input.SessionID, 0, false)
	if err != nil || len(batch.Events) != 1 || string(batch.Events[0].Data) != string(input.Data) {
		t.Fatalf("unexpected batch: %#v, %v", batch, err)
	}
	if err := client.CloseSessionEvents(context.Background(), input.WorkspaceID, input.SessionID); err != nil {
		t.Fatal(err)
	}
	batch, err = client.ReadSessionEvents(context.Background(), input.WorkspaceID, input.SessionID, 1, false)
	if err != nil || !batch.Closed {
		t.Fatalf("expected closed stream: %#v, %v", batch, err)
	}
}

func TestSessionEventCapacityReturnsExplicitUnavailable(t *testing.T) {
	b := sessionevents.NewBrokerWithGlobalLimits(2, 1024, 128, 1, 2048)
	_, err := b.Append(sessionevents.Append{Key: sessionevents.Key{WorkspaceID: "victim", SessionID: "real"}, MessageID: "m", Data: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "&wait=0"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/?workspace_id=other&session_id=fake&after=0"+query, nil)
		readSessionEvents(b)(w, r)
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "event_broker_capacity") {
			t.Fatalf("response = %d %s", w.Code, w.Body.String())
		}
	}
	appendResponse := httptest.NewRecorder()
	appendRequest := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"workspace_id":"other","session_id":"fake","message_id":"msg_0123456789abcdef0123456789abcdef","data":{}}`))
	appendSessionEvent(b)(appendResponse, appendRequest)
	if appendResponse.Code != http.StatusServiceUnavailable || !strings.Contains(appendResponse.Body.String(), "event_broker_capacity") {
		t.Fatalf("append response = %d %s", appendResponse.Code, appendResponse.Body.String())
	}
	closeResponse := httptest.NewRecorder()
	closeRequest := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"workspace_id":"other","session_id":"fake"}`))
	closeSessionEvents(b)(closeResponse, closeRequest)
	if closeResponse.Code != http.StatusServiceUnavailable || !strings.Contains(closeResponse.Body.String(), "event_broker_capacity") {
		t.Fatalf("close response = %d %s", closeResponse.Code, closeResponse.Body.String())
	}
}

func newSessionEventTestClient() (*controlclient.Client, error) {
	handler := NewHandler(nil)
	return controlclient.New("http://127.0.0.1:1", &http.Client{Transport: sessionEventRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		response := recorder.Result()
		response.Request = request
		return response, nil
	})})
}

type sessionEventRoundTripFunc func(*http.Request) (*http.Response, error)

func (function sessionEventRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

// SDK output is already JSON. Transport escaping must not change its byte budget.
func TestSessionEventsPreserveSDKPayloadBudget(t *testing.T) {
	for _, tc := range []struct{ name, text string }{{"html", "<>&"}, {"unicode_separators", "\u2028\u2029"}, {"script_tag", "</script>&"}} {
		t.Run(tc.name, func(t *testing.T) {
			text := tc.text
			client, err := newSessionEventTestClient()
			if err != nil {
				t.Fatal(err)
			}
			const limit = sessionevents.DefaultMaxEventBytes
			contentBytes := limit - len(`{"text":""}`)
			content := strings.Repeat(text, contentBytes/len(text)) + strings.Repeat("x", contentBytes%len(text))
			data := json.RawMessage(`{"text":"` + content + `"}`)
			input := controlclient.SessionEventAppend{
				WorkspaceID: "wsp_a", SessionID: "orders", MessageID: "msg_0123456789abcdef0123456789abcdef",
				SourceSandboxID: "sbx-0123456789abcdef0123456789abcdef", SourceCursor: 1, Data: data,
			}
			sdkBatch := messageprotocol.OutputBatch{Events: []messageprotocol.OutputEvent{{Cursor: 1, MessageID: input.MessageID, Data: data}}}
			if err := sdkBatch.Validate(0, false); err != nil {
				t.Fatalf("SDK-valid boundary payload rejected by collector: %v", err)
			}
			// Eight full events fit in replay memory, but HTML expansion would exceed
			// the control client's response limit even if append admission were fixed.
			for i := uint64(1); i <= 8; i++ {
				input.SourceCursor = i
				event, err := client.AppendSessionEvent(context.Background(), input)
				if err != nil {
					t.Fatalf("append %d (%d bytes): %v", i, len(data), err)
				}
				if event.Sequence != i || string(event.Data) != string(data) {
					t.Fatalf("append %d changed event bytes or sequence", i)
				}
			}
			retry, err := client.AppendSessionEvent(context.Background(), input)
			if err != nil || retry.Sequence != 8 {
				t.Fatalf("source retry was not deduplicated: sequence=%d, error=%v", retry.Sequence, err)
			}
			batch, err := client.ReadSessionEvents(context.Background(), input.WorkspaceID, input.SessionID, 0, false)
			if err != nil {
				t.Fatalf("read bounded replay: %v", err)
			}
			if len(batch.Events) != 8 {
				t.Fatalf("replayed %d events, want 8", len(batch.Events))
			}
			for _, event := range batch.Events {
				if string(event.Data) != string(data) {
					t.Fatal("replay changed event bytes")
				}
			}

			input.SourceCursor++
			input.Data = json.RawMessage(`{"text":"` + content + `x"}`)
			_, err = client.AppendSessionEvent(context.Background(), input)
			var response *controlclient.Error
			if !errors.As(err, &response) || response.StatusCode != http.StatusBadRequest || response.Code != "invalid_session_event" {
				t.Fatalf("oversized event should still be rejected: %v", err)
			}
		})
	}
}
