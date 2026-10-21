package gateway

import (
	"context"
	"encoding/json"
	"github.com/stepandel/cantelop/fire-fuse/sessionbroker"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIntegrationDispatchAndReadsAreWorkspaceScoped(t *testing.T) {
	app := "app_0123456789abcdef0123456789abcdef"
	workspace := "wsp_0123456789abcdef0123456789abcdef"
	other := "wsp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	control := &fakeControl{workspaces: []Workspace{testWorkspace(app, workspace, "production", 0, 600), testWorkspace(app, other, "other", 0, 600)}}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest("POST", "http://edge-origin.cantelop.dev"+integrationPrefix+"messages", app, "agent.cantelop.dev")
	request.Body = io.NopCloser(strings.NewReader(`{"session":{"id":"thread","workspace_id":"` + workspace + `","keep_alive_seconds":900},"message":{"id":"` + testMessageID + `","payload":{"prompt":"hello"}}}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 202 || control.creates != 0 {
		t.Fatalf("dispatch %d %s", response.Code, response.Body)
	}
	for _, fixture := range []struct {
		query string
		known bool
	}{{"workspace_slug=production", true}, {"workspace_id=" + workspace, true}, {"workspace_slug=other", false}} {
		request = trustedEdgeRequest("GET", "http://edge-origin.cantelop.dev"+integrationPrefix+"sessions/thread/messages/"+testMessageID+"?"+fixture.query, app, "agent.cantelop.dev")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 200 || strings.Contains(response.Body.String(), `"state":"unknown"`) == fixture.known {
			t.Fatalf("status %s: %d %s", fixture.query, response.Code, response.Body)
		}
	}
	request = trustedEdgeRequest("GET", "http://edge-origin.cantelop.dev/__cantelop/v1/workspaces/"+workspace, app, "agent.cantelop.dev")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("Workspace read %d %s", response.Code, response.Body)
	}
}

func TestIntegrationReadRejectsAmbiguousAndForeignSelectors(t *testing.T) {
	app := "app_0123456789abcdef0123456789abcdef"
	workspace := "wsp_0123456789abcdef0123456789abcdef"
	control := &fakeControl{workspaces: []Workspace{testWorkspace("app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", workspace, "foreign", 0, 600)}}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "workspace_id=" + workspace + "&workspace_slug=foreign", "workspace_slug=foreign", "workspace_id=" + workspace + "&workspace_id=" + workspace} {
		request := trustedEdgeRequest("GET", "http://edge-origin.cantelop.dev"+integrationPrefix+"sessions/thread/view?"+query, app, "agent.cantelop.dev")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 400 && response.Code != 404 {
			t.Fatalf("query %s: %d", query, response.Code)
		}
	}
}
func TestIntegrationRequestReusesDurableReply(t *testing.T) {
	app := "app_0123456789abcdef0123456789abcdef"
	workspace := "wsp_0123456789abcdef0123456789abcdef"
	control := &fakeControl{workspaces: []Workspace{testWorkspace(app, workspace, "production", 0, 600)}, messageDeliveries: []MessageDelivery{{ID: testMessageID, WorkspaceID: workspace, LogicalSessionID: "thread", KeepAliveSeconds: 900, Payload: []byte(`{}`), ReplyRequested: true, Reply: []byte(`"hello"`), State: "handled", AcceptedAt: time.Now()}}}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest("POST", "http://edge-origin.cantelop.dev"+integrationPrefix+"requests", app, "agent.cantelop.dev")
	request.Body = io.NopCloser(strings.NewReader(`{"timeout_ms":1000,"session":{"id":"thread","workspace_id":"` + workspace + `","keep_alive_seconds":900},"message":{"id":"` + testMessageID + `","payload":{}}}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"reply":"hello"`) {
		t.Fatalf("reply %d %s", response.Code, response.Body)
	}
}

type integrationStateControl struct {
	*fakeControl
	workspaceID, sessionID string
	reads, cancels         int
}

func (c *integrationStateControl) SessionView(_ context.Context, workspaceID, sessionID string) (sessionevents.View, error) {
	c.workspaceID = workspaceID
	c.sessionID = sessionID
	c.reads++
	value := sessionevents.View{Revision: "1", UpdatedAt: time.Now().UTC(), State: json.RawMessage(`{"text":"snapshot"}`)}
	value.Cursor.StreamID = strings.Repeat("a", 32)
	value.Cursor.Sequence = 1
	return value, nil
}
func TestIntegrationViewPreserveScopeAndDoNotActivate(t *testing.T) {
	app := "app_0123456789abcdef0123456789abcdef"
	workspace := "wsp_0123456789abcdef0123456789abcdef"
	base := &fakeControl{workspaces: []Workspace{testWorkspace(app, workspace, "production", 0, 600)}}
	control := &integrationStateControl{fakeControl: base}
	handler, err := newTestHandler(base, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler.messages = control
	handler.events = control
	for _, fixture := range []struct{ method, path, body string }{{"GET", "sessions/thread/view?workspace_slug=production", ""}} {
		request := trustedEdgeRequest(fixture.method, "http://edge-origin.cantelop.dev"+integrationPrefix+fixture.path, app, "agent.cantelop.dev")
		request.Body = io.NopCloser(strings.NewReader(fixture.body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 200 || base.creates != 0 || control.workspaceID != workspace || control.sessionID != "thread" {
			t.Fatalf("command: %d %s", response.Code, response.Body)
		}
	}
	if control.reads != 1 {
		t.Fatal("commands did not reach scoped control operations")
	}
}

func TestIntegrationScopedRequestReusesOpaqueReplyAndRejectsForeignScope(t *testing.T) {
	app := "app_0123456789abcdef0123456789abcdef"
	workspace := "wsp_0123456789abcdef0123456789abcdef"
	other := "wsp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	payload := []byte(`{"__cantelop_command":2,"type":"cancel"}`)
	control := &fakeControl{
		workspaces:        []Workspace{testWorkspace(app, workspace, "production", 0, 600), testWorkspace(app, other, "other", 0, 600)},
		sessionRecords:    []Session{{ID: sessionbroker.LogicalID(app, "thread"), WorkspaceID: workspace}, {ID: "ses_direct", WorkspaceID: workspace}},
		messageDeliveries: []MessageDelivery{{ID: testMessageID, WorkspaceID: workspace, LogicalSessionID: "thread", KeepAliveSeconds: 900, Payload: payload, ReplyRequested: true, Reply: []byte(`{"id":"` + testMessageID + `","state":"idle"}`), State: "handled", AcceptedAt: time.Now()}},
	}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		scope  string
		status int
	}{
		{"workspace_slug=production", 200},
		{"workspace_id=" + workspace, 200},
		{"workspace_slug=other", 404},
		{"workspace_slug=missing", 404},
		{"workspace_id=" + workspace + "&workspace_slug=production", 400},
	} {
		request := trustedEdgeRequest("POST", "http://edge-origin.cantelop.dev"+integrationPrefix+"sessions/thread/requests?"+fixture.scope, app, "agent.cantelop.dev")
		request.Body = io.NopCloser(strings.NewReader(`{"message":{"id":"` + testMessageID + `","payload":` + string(payload) + `},"keep_alive_seconds":900,"timeout_ms":1000}`))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != fixture.status || control.creates != 0 {
			t.Fatalf("%s: %d %s; creates=%d", fixture.scope, response.Code, response.Body, control.creates)
		}
		if response.Code == 200 && !strings.Contains(response.Body.String(), `"state":"idle"`) {
			t.Fatal(response.Body)
		}
	}
	for _, id := range []string{"missing", "ses_direct"} {
		request := trustedEdgeRequest("POST", "http://edge-origin.cantelop.dev"+integrationPrefix+"sessions/"+id+"/requests?workspace_slug=production", app, "agent.cantelop.dev")
		request.Body = io.NopCloser(strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 404 || control.creates != 0 {
			t.Fatalf("Session %s: %d %s", id, response.Code, response.Body)
		}
	}
}
