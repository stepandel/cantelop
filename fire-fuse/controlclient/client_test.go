package controlclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

func TestClientLifecycleRequests(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, ""
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/apps":
			body = `{"items":[{"id":"app","slug":"agent-app"}]}`
		case "GET /v1/workspaces":
			if got := r.URL.Query().Get("app_id"); got != "app" {
				t.Errorf("workspace app_id = %q", got)
			}
			body = `{"items":[{"id":"wsp_agent","app_id":"app","app_slug":"agent-app","slug":"production"}]}`
		case "GET /v1/apps/app/workspaces/by-slug/production":
			body = `{"id":"wsp_agent","app_id":"app","app_slug":"agent-app","slug":"production"}`
		case "GET /v1/workspaces/wsp_agent":
			body = `{"id":"wsp_agent","app_id":"app","app_slug":"agent-app","slug":"production"}`
		case "GET /v1/sessions":
			body = `{"items":[]}`
		case "GET /v1/sessions/ses_agent_test":
			body = `{"id":"ses_agent_test","workspace_id":"wsp_agent","keep_alive_seconds":300,"state":"active"}`
		case "GET /v1/sessions/ses_agent_test/route":
			body = `{"session":{"id":"ses_agent_test","workspace_id":"wsp_agent","build_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state":"active","created_at":"2026-08-11T12:00:00Z","updated_at":"2026-08-11T12:00:01Z"},"sandbox_id":"sbx-0123456789abcdef0123456789abcdef","endpoint":{"scheme":"http","authority":"172.16.0.2:3000"}}`
		case "POST /v1/sessions/ses_agent_test/work-leases", "POST /v1/sessions/ses_agent_test/work-leases/lease_0123456789abcdef0123456789abcdef/renew":
			body = `{"session_id":"ses_agent_test","lease_id":"lease_0123456789abcdef0123456789abcdef","expires_at":"2026-08-26T12:01:00Z","created_at":"2026-08-26T12:00:00Z","updated_at":"2026-08-26T12:00:00Z"}`
		case "DELETE /v1/sessions/ses_agent_test/work-leases/lease_0123456789abcdef0123456789abcdef":
			body = `{"id":"ses_agent_test","workspace_id":"wsp_agent","keep_alive_seconds":300,"state":"active"}`
		case "POST /v1/message-deliveries":
			var input MessageDelivery
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.RequestID != "req_0123456789abcdef0123456789abcdef" {
				t.Fatalf("Message delivery input = %#v, %v", input, err)
			}
			body = `{"id":"msg_0123456789abcdef0123456789abcdef","workspace_id":"wsp_0123456789abcdef0123456789abcdef","logical_session_id":"agent:primary","request_id":"req_0123456789abcdef0123456789abcdef","keep_alive_seconds":300,"state":"accepted","work_state":"pending","attempts":0,"accepted_at":"2026-08-26T12:00:00Z","updated_at":"2026-08-26T12:00:00Z"}`
		case "GET /v1/message-deliveries/msg_0123456789abcdef0123456789abcdef":
			body = `{"id":"msg_0123456789abcdef0123456789abcdef","workspace_id":"wsp_0123456789abcdef0123456789abcdef","logical_session_id":"agent:primary","request_id":"req_0123456789abcdef0123456789abcdef","keep_alive_seconds":300,"state":"accepted","work_state":"pending","attempts":0,"accepted_at":"2026-08-26T12:00:00Z","updated_at":"2026-08-26T12:00:00Z"}`
		case "POST /v1/sessions":
			var spec CreateSessionSpec
			if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
				t.Fatal(err)
			}
			status = http.StatusAccepted
			document, err := json.Marshal(Session{
				ID: spec.ID, WorkspaceID: spec.WorkspaceID,
				KeepAliveSeconds: spec.KeepAliveSeconds, State: session.StateActive,
			})
			if err != nil {
				t.Fatal(err)
			}
			body = string(document)
		case "DELETE /v1/sessions/ses_agent_test":
			body = `{"id":"ses_agent_test","workspace_id":"wsp_agent","state":"idle"}`
		case "POST /v1/request-events":
			var event struct {
				EventID string `json:"event_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
				t.Fatal(err)
			}
			status, body = http.StatusCreated, `{"event_id":"`+event.EventID+`"}`
		default:
			status, body = http.StatusNotFound, `{}`
		}
		return &http.Response{
			StatusCode: status, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(body)), Request: r,
		}, nil
	})
	client, err := New("http://127.0.0.1:8080", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	workspaces, err := client.ListWorkspaces(context.Background())
	if err != nil || len(workspaces) != 1 || workspaces[0].AppSlug != "agent-app" {
		t.Fatalf("ListWorkspaces() = %#v, %v", workspaces, err)
	}
	resolved, err := client.ResolveAppWorkspace(context.Background(), "app", "production")
	if err != nil || resolved.ID != "wsp_agent" {
		t.Fatalf("ResolveAppWorkspace() = %#v, %v", resolved, err)
	}
	loaded, err := client.GetWorkspace(context.Background(), "wsp_agent")
	if err != nil || loaded.ID != "wsp_agent" {
		t.Fatalf("GetWorkspace() = %#v, %v", loaded, err)
	}
	if records, err := client.ListSessions(context.Background(), "wsp_agent"); err != nil || len(records) != 0 {
		t.Fatalf("ListSessions() = %#v, %v", records, err)
	}
	record, err := client.GetSession(context.Background(), "ses_agent_test")
	if err != nil || record.State != session.StateActive {
		t.Fatalf("GetSession() = %#v, %v", record, err)
	}
	route, err := client.GetSessionRoute(context.Background(), "ses_agent_test")
	if err != nil || route.SandboxID != "sbx-0123456789abcdef0123456789abcdef" || route.Endpoint.Authority != "172.16.0.2:3000" {
		t.Fatalf("GetSessionRoute() = %#v, %v", route, err)
	}
	record, err = client.CreateSession(context.Background(), CreateSessionSpec{
		ID: "ses_agent_test", WorkspaceID: "wsp_agent", KeepAliveSeconds: 300,
	})
	if err != nil || record.State != session.StateActive {
		t.Fatalf("CreateSession() = %#v, %v", record, err)
	}
	leaseID := "lease_0123456789abcdef0123456789abcdef"
	lease, err := client.AcquireSessionWorkLease(context.Background(), "ses_agent_test", leaseID, time.Minute)
	if err != nil || lease.LeaseID != leaseID {
		t.Fatalf("AcquireSessionWorkLease() = %#v, %v", lease, err)
	}
	if lease, err = client.RenewSessionWorkLease(context.Background(), "ses_agent_test", leaseID, time.Minute); err != nil || lease.LeaseID != leaseID {
		t.Fatalf("RenewSessionWorkLease() = %#v, %v", lease, err)
	}
	if record, err = client.ReleaseSessionWorkLease(context.Background(), "ses_agent_test", leaseID); err != nil || record.ID == "" {
		t.Fatalf("ReleaseSessionWorkLease() = %#v, %v", record, err)
	}
	messageID := "msg_0123456789abcdef0123456789abcdef"
	delivery, err := client.EnqueueMessageDelivery(context.Background(), MessageDelivery{
		ID: messageID, WorkspaceID: "wsp_0123456789abcdef0123456789abcdef",
		LogicalSessionID: "agent:primary", KeepAliveSeconds: 300,
		RequestID: "req_0123456789abcdef0123456789abcdef",
		Payload:   json.RawMessage(`{"type":"prompt"}`),
	})
	if err != nil || delivery.ID != messageID || delivery.State != "accepted" {
		t.Fatalf("EnqueueMessageDelivery() = %#v, %v", delivery, err)
	}
	if delivery, err = client.GetMessageDelivery(context.Background(), messageID); err != nil || delivery.ID != messageID {
		t.Fatalf("GetMessageDelivery() = %#v, %v", delivery, err)
	}
	if record, err = client.TerminateSession(context.Background(), "ses_agent_test"); err != nil || record.State != session.StateIdle {
		t.Fatalf("TerminateSession() = %#v, %v", record, err)
	}
	if err := client.RecordRequestEvent(context.Background(), ProxyRequestEvent{
		EventID: "request-1", WorkspaceID: "wsp_agent", SessionID: "ses_agent_test",
		ReceivedAt: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
		StatusCode: http.StatusOK, Duration: 1500 * time.Microsecond,
	}); err != nil {
		t.Fatalf("RecordRequestEvent() error = %v", err)
	}
}

func TestClientRejectsInvalidSessionLookup(t *testing.T) {
	client, err := New("http://127.0.0.1:8080", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetSession(context.Background(), "../sandbox"); err == nil {
		t.Fatal("GetSession() error = nil")
	}
	if _, err := client.GetSessionRoute(context.Background(), "../sandbox"); err == nil {
		t.Fatal("GetSessionRoute() error = nil")
	}
}

func TestClientMessageDeliveryTransitions(t *testing.T) {
	messageID := "msg_0123456789abcdef0123456789abcdef"
	var requests []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		body := `{"id":"` + messageID + `","workspace_id":"wsp_0123456789abcdef0123456789abcdef","logical_session_id":"agent:primary","keep_alive_seconds":300,"state":"handling","work_state":"delivering","attempts":1,"accepted_at":"2026-08-26T12:00:00Z","started_at":"2026-08-26T12:00:01Z","updated_at":"2026-08-26T12:00:01Z"}`
		switch r.URL.Path {
		case "/v1/message-deliveries/claim":
			body = `{"message":` + body + `}`
		case "/v1/message-deliveries/requeue-handling":
			body = `{"requeued":1}`
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(body)), Request: r,
		}, nil
	})
	client, err := New("http://127.0.0.1:8080", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := client.ClaimNextMessageDelivery(context.Background()); err != nil || value == nil || value.ID != messageID {
		t.Fatalf("ClaimNextMessageDelivery() = %#v, %v", value, err)
	}
	if count, err := client.RequeueHandlingMessageDeliveries(context.Background()); err != nil || count != 1 {
		t.Fatalf("RequeueHandlingMessageDeliveries() = %d, %v", count, err)
	}
	for name, operation := range map[string]func() error{
		"requeue": func() error { return client.RequeueMessageDelivery(context.Background(), messageID) },
		"fail": func() error {
			return client.FailMessageDelivery(context.Background(), messageID, "ses_actor", "delivery_failed")
		},
	} {
		if err := operation(); err != nil {
			t.Fatalf("%s transition: %v", name, err)
		}
	}
	if len(requests) != 4 {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestClientRequiresLoopbackOrigin(t *testing.T) {
	for _, endpoint := range []string{
		"https://127.0.0.1:8080", "http://example.com:8080", "http://127.0.0.1:8080/path",
	} {
		if _, err := New(endpoint, nil); err == nil {
			t.Errorf("New(%q) error = nil", endpoint)
		}
	}
}

func TestClientClassifiesAdmissionLimit(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"code":"admission_limit","message":"capacity exhausted"}}`,
			)), Request: r,
		}, nil
	})
	client, err := New("http://127.0.0.1:8080", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateSession(context.Background(), CreateSessionSpec{
		ID: "ses_agent_test", WorkspaceID: "wsp_agent",
	})
	if !errors.Is(err, ErrAdmissionLimit) {
		t.Fatalf("CreateSession() error = %v, want ErrAdmissionLimit", err)
	}
	var controlErr *Error
	if !errors.As(err, &controlErr) || controlErr.StatusCode != http.StatusTooManyRequests || controlErr.Code != "admission_limit" {
		t.Fatalf("CreateSession() error = %#v, want classified Error", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
