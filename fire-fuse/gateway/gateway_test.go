package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/edgeprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
	"github.com/stepandel/cantelop/fire-fuse/sessionbroker"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
	"github.com/stepandel/cantelop/internal/resourceid"
)

const testEdgeToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeControl struct {
	mu                sync.Mutex
	workspaces        []Workspace
	sessionRecords    []Session
	createFn          func(context.Context, CreateSessionSpec) (Session, error)
	creates           int
	terminates        int
	delay             time.Duration
	targetIP          string
	routes            map[string]session.Endpoint
	lastCreate        CreateSessionSpec
	getErr            error
	openWorkspaceFn   func(context.Context, string, string) (Workspace, error)
	messageDeliveries []MessageDelivery
	eventBatches      []controlclient.SessionEventBatch
	eventReadErr      error
	eventCloses       int
}

func (f *fakeControl) AppendSessionEvent(context.Context, controlclient.SessionEventAppend) (controlclient.SessionEvent, error) {
	return controlclient.SessionEvent{}, nil
}

func (f *fakeControl) ReadSessionEvents(ctx context.Context, _, _ string, _ uint64, _ bool) (controlclient.SessionEventBatch, error) {
	f.mu.Lock()
	if f.eventReadErr != nil {
		err := f.eventReadErr
		f.mu.Unlock()
		return controlclient.SessionEventBatch{}, err
	}
	if len(f.eventBatches) > 0 {
		batch := f.eventBatches[0]
		f.eventBatches = f.eventBatches[1:]
		f.mu.Unlock()
		return batch, nil
	}
	f.mu.Unlock()
	<-ctx.Done()
	return controlclient.SessionEventBatch{}, ctx.Err()
}

func (f *fakeControl) CloseSessionEvents(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eventCloses++
	return nil
}

func TestTrustedEdgeStreamsSessionEventsOverSSE(t *testing.T) {
	control := &fakeControl{
		workspaces: []Workspace{testWorkspace("app_0123456789abcdef0123456789abcdef", "wsp_0123456789abcdef0123456789abcdef", "primary", 0, 300)},
		eventBatches: []controlclient.SessionEventBatch{{StreamID: "11111111111111111111111111111111",
			Events: []controlclient.SessionEvent{{StreamID: "11111111111111111111111111111111",
				Sequence: 1, SessionID: "thread", MessageID: "msg_0123456789abcdef0123456789abcdef",
				CreatedAt: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), Data: json.RawMessage(`{"token":"hello"}`),
			}},
			Closed: true,
		}},
	}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest(
		http.MethodGet,
		"http://edge-origin.cantelop.dev/__cantelop/v1/sessions/thread/events?workspace_id=wsp_0123456789abcdef0123456789abcdef",
		"app_0123456789abcdef0123456789abcdef", "agent.cantelop.dev")

	request.Header.Set("Accept", "text/event-stream")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "id: 11111111111111111111111111111111:1\n") ||
		!strings.Contains(recorder.Body.String(), `"message_id":"msg_0123456789abcdef0123456789abcdef"`) {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.HasPrefix(recorder.Body.String(), ": connected\n\nid: 11111111111111111111111111111111:1\n") {
		t.Fatalf("initial comment changed replay ordering: %q", recorder.Body.String())
	}
}

// The broker deliberately blocks after its empty initial batch. Clients must
// still receive body bytes without waiting for an event or heartbeat.
func TestEmptySessionEventStreamFlushesInitialComment(t *testing.T) {
	control := &fakeControl{
		workspaces:   []Workspace{testWorkspace("app_0123456789abcdef0123456789abcdef", "wsp_0123456789abcdef0123456789abcdef", "primary", 0, 300)},
		eventBatches: []controlclient.SessionEventBatch{{StreamID: "11111111111111111111111111111111"}},
	}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request := trustedEdgeRequest(http.MethodGet,
		server.URL+"/__cantelop/v1/sessions/thread/events?workspace_id=wsp_0123456789abcdef0123456789abcdef",
		"app_0123456789abcdef0123456789abcdef", "agent.cantelop.dev").
		WithContext(ctx)
	request.RequestURI = ""
	request.Header.Set("Accept", "text/event-stream")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	initial := make([]byte, len(": connected\n\n"))
	if _, err := io.ReadFull(response.Body, initial); err != nil {
		t.Fatalf("empty stream did not flush an initial comment: %v", err)
	}
	if string(initial) != ": connected\n\n" {
		t.Fatalf("initial frame = %q", initial)
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.creates != 0 || len(control.messageDeliveries) != 0 {
		t.Fatal("opening an event stream started agent work")
	}
}

func TestSessionEventSubscriberDisconnectDoesNotTerminateSession(t *testing.T) {
	control := &fakeControl{
		workspaces:   []Workspace{testWorkspace("app_0123456789abcdef0123456789abcdef", "wsp_0123456789abcdef0123456789abcdef", "primary", 0, 300)},
		eventBatches: []controlclient.SessionEventBatch{{StreamID: "11111111111111111111111111111111"}},
	}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := trustedEdgeRequest(
		http.MethodGet,
		"http://edge-origin.cantelop.dev/__cantelop/v1/sessions/thread/events?workspace_id=wsp_0123456789abcdef0123456789abcdef",
		"app_0123456789abcdef0123456789abcdef", "agent.cantelop.dev").
		WithContext(ctx)
	request.Header.Set("Accept", "text/event-stream")
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), request)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("subscriber did not disconnect")
	}
	if control.terminateCount() != 0 || control.eventCloses != 0 {
		t.Fatal("subscriber disconnect terminated the Session stream")
	}
}

func TestTrustedEdgeStreamsSessionEventsOverOutputOnlyWebSocket(t *testing.T) {
	control := &fakeControl{
		workspaces: []Workspace{testWorkspace("app_0123456789abcdef0123456789abcdef", "wsp_0123456789abcdef0123456789abcdef", "primary", 0, 300)},
		eventBatches: []controlclient.SessionEventBatch{{StreamID: "11111111111111111111111111111111",
			Events: []controlclient.SessionEvent{{StreamID: "11111111111111111111111111111111",
				Sequence: 7, SessionID: "thread", MessageID: "msg_0123456789abcdef0123456789abcdef",
				CreatedAt: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), Data: json.RawMessage(`{"token":"hello"}`),
			}},
			Closed: true,
		}},
	}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	trusted := trustedEdgeRequest(
		http.MethodGet, "http://gateway/", "app_0123456789abcdef0123456789abcdef", "agent.cantelop.dev")

	headers := trusted.Header.Clone()
	headers.Set("Origin", "https://agent.cantelop.dev")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, response, err := websocket.Dial(ctx,
		"ws"+strings.TrimPrefix(server.URL, "http")+"/__cantelop/v1/sessions/thread/events?workspace_id=wsp_0123456789abcdef0123456789abcdef&after=6&stream_id=11111111111111111111111111111111",
		&websocket.DialOptions{HTTPClient: server.Client(), HTTPHeader: headers, Subprotocols: []string{sessionEventWebSocketProtocol}},
	)
	if err != nil {
		if response != nil {
			t.Fatalf("dial = %v, HTTP %d", err, response.StatusCode)
		}
		t.Fatal(err)
	}
	defer connection.CloseNow()
	messageType, document, err := connection.Read(ctx)
	if err != nil || messageType != websocket.MessageText || !strings.Contains(string(document), `"sequence":7`) {
		t.Fatalf("event = %s, %v", document, err)
	}
	if _, _, err := connection.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("close = %v", err)
	}
}

func newTestHandler(control *fakeControl, transport http.RoundTripper, logger *slog.Logger) (*Handler, error) {
	_ = transport
	sessions, _ := sessionbroker.New(control)
	return NewHandler(Dependencies{
		Workspaces:       control,
		WorkspaceCreator: control,
		SessionBroker:    sessions,
		Messages:         control,
		Events:           control,
		EdgeToken:        testEdgeToken,
	}, logger)
}

func TestNewHandlerRequiresFocusedDependencies(t *testing.T) {
	control := &fakeControl{}
	sessions, _ := sessionbroker.New(control)
	for name, dependencies := range map[string]Dependencies{
		"Workspace directory": {WorkspaceCreator: control, SessionBroker: sessions, Messages: control, Events: control, EdgeToken: testEdgeToken},
		"Workspace creator":   {Workspaces: control, SessionBroker: sessions, Messages: control, Events: control, EdgeToken: testEdgeToken},
		"Session broker":      {Workspaces: control, WorkspaceCreator: control, Messages: control, Events: control, EdgeToken: testEdgeToken},
		"Message queue":       {Workspaces: control, WorkspaceCreator: control, SessionBroker: sessions, Events: control, EdgeToken: testEdgeToken},
		"Session events":      {Workspaces: control, WorkspaceCreator: control, SessionBroker: sessions, Messages: control, EdgeToken: testEdgeToken},
		"edge token":          {Workspaces: control, WorkspaceCreator: control, SessionBroker: sessions, Messages: control, Events: control},
	} {
		t.Run(name, func(t *testing.T) {
			if handler, err := NewHandler(dependencies, nil); err == nil {
				t.Fatalf("handler = %#v", handler)
			}
		})
	}
}

func TestTrustedEdgeCreatesWorkspaceInCurrentApp(t *testing.T) {
	control := &fakeControl{}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest(
		http.MethodPost,
		"http://edge-origin.cantelop.dev/__cantelop/v1/workspaces",
		"app_0123456789abcdef0123456789abcdef",

		"agent.cantelop.dev")

	request.Body = io.NopCloser(strings.NewReader(`{"slug":"preview"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Workspace
		Hostname string `json:"hostname"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.AppID != "app_0123456789abcdef0123456789abcdef" || envelope.Slug != "preview" ||
		envelope.Hostname != "agent.cantelop.dev" {
		t.Fatalf("Workspace = %#v", envelope)
	}
}

func TestTrustedEdgeOpensWorkspaceBySlugInCurrentApp(t *testing.T) {
	workspace := testWorkspace(
		"app_0123456789abcdef0123456789abcdef",
		"wsp_0123456789abcdef0123456789abcdef",
		"production", 0, 0,
	)
	control := &fakeControl{workspaces: []Workspace{workspace}}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest(
		http.MethodPost,
		"http://edge-origin.cantelop.dev/__cantelop/v1/workspaces/open",
		workspace.AppID, "agent.cantelop.dev")

	request.Body = io.NopCloser(strings.NewReader(`{"slug":"production"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	var opened struct {
		Workspace
		Hostname string `json:"hostname"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &opened); err != nil {
		t.Fatal(err)
	}
	if opened.ID != workspace.ID || opened.Slug != workspace.Slug || opened.Hostname != "agent.cantelop.dev" {
		t.Fatalf("Workspace = %#v", opened)
	}
}

func TestTrustedEdgeOpenMaterializesMissingWorkspace(t *testing.T) {
	control := &fakeControl{}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest(
		http.MethodPost,
		"http://edge-origin.cantelop.dev/__cantelop/v1/workspaces/open",
		"app_0123456789abcdef0123456789abcdef", "agent.cantelop.dev")

	request.Body = io.NopCloser(strings.NewReader(`{"slug":"production"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || len(control.workspaces) != 1 || control.workspaces[0].Slug != "production" {
		t.Fatalf("response = %d %s, Workspaces = %#v", response.Code, response.Body.String(), control.workspaces)
	}
}

func TestTrustedEdgeOpenWaitsForLocalWorkspaceRegistration(t *testing.T) {
	workspace := testWorkspace("app_0123456789abcdef0123456789abcdef", "wsp_0123456789abcdef0123456789abcdef", "production", 0, 0)
	control := &fakeControl{openWorkspaceFn: func(context.Context, string, string) (Workspace, error) {
		return workspace, nil
	}}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest(http.MethodPost, "http://edge-origin.cantelop.dev/__cantelop/v1/workspaces/open",
		workspace.AppID, "agent.cantelop.dev")

	request.Body = io.NopCloser(strings.NewReader(`{"slug":"production"}`))
	response := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(finished)
	}()
	select {
	case <-finished:
		t.Fatalf("open returned before local registration: %d %s", response.Code, response.Body.String())
	case <-time.After(25 * time.Millisecond):
	}
	control.mu.Lock()
	control.workspaces = append(control.workspaces, workspace)
	control.mu.Unlock()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("open did not return after local registration")
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), workspace.ID) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestTrustedEdgeOpenReportsPendingRegistration(t *testing.T) {
	workspace := testWorkspace("app_0123456789abcdef0123456789abcdef", "wsp_0123456789abcdef0123456789abcdef", "production", 0, 0)
	control := &fakeControl{openWorkspaceFn: func(context.Context, string, string) (Workspace, error) {
		return workspace, nil
	}}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest(http.MethodPost, "http://edge-origin.cantelop.dev/__cantelop/v1/workspaces/open",
		workspace.AppID, "agent.cantelop.dev")

	request.Body = io.NopCloser(strings.NewReader(`{"slug":"production"}`))
	ctx, cancel := context.WithTimeout(request.Context(), 25*time.Millisecond)
	defer cancel()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request.WithContext(ctx))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" ||
		!strings.Contains(response.Body.String(), "workspace_not_ready") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestTrustedEdgeCreatesAndTerminatesSessionInCurrentApp(t *testing.T) {
	workspace := testWorkspace(
		"app_0123456789abcdef0123456789abcdef",
		"wsp_0123456789abcdef0123456789abcdef",
		"preview", 0, 0,
	)
	control := &fakeControl{workspaces: []Workspace{workspace}}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest(
		http.MethodPost,
		"http://edge-origin.cantelop.dev/__cantelop/v1/sessions",
		workspace.AppID, "agent.cantelop.dev")

	request.Body = io.NopCloser(strings.NewReader(
		`{"id":"ses_preview","workspace_id":"` + workspace.ID + `","keep_alive_seconds":300}`,
	))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("create response = %d %s", response.Code, response.Body.String())
	}
	if spec := control.createSpec(); spec.ID != "ses_preview" || spec.WorkspaceID != workspace.ID || spec.KeepAliveSeconds != 300 {
		t.Fatalf("CreateSessionSpec = %#v", spec)
	}

	request = trustedEdgeRequest(
		http.MethodDelete,
		"http://edge-origin.cantelop.dev/__cantelop/v1/sessions/ses_preview",
		workspace.AppID, "agent.cantelop.dev")

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || control.terminateCount() != 1 {
		t.Fatalf("terminate response/count = %d/%d %s", response.Code, control.terminateCount(), response.Body.String())
	}
}

type brokerBackedCloseControl struct {
	*fakeControl
	broker *sessionevents.Broker
}

func (c *brokerBackedCloseControl) CloseSessionEvents(_ context.Context, workspaceID, sessionID string) error {
	c.eventCloses++
	return c.broker.Close(sessionevents.Key{WorkspaceID: workspaceID, SessionID: sessionID})
}

func TestSessionStopPreservesEventStreamForReplacementSandbox(t *testing.T) {
	workspace := testWorkspace("app_0123456789abcdef0123456789abcdef", "wsp_0123456789abcdef0123456789abcdef", "primary", 0, 300)
	const logicalID = "thread"
	internalID := sessionbroker.LogicalID(workspace.AppID, logicalID)
	control := &fakeControl{
		workspaces:     []Workspace{workspace},
		sessionRecords: []Session{{ID: internalID, WorkspaceID: workspace.ID, State: session.StateActive}},
	}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	broker := sessionevents.NewBroker()
	events := &brokerBackedCloseControl{fakeControl: control, broker: broker}
	handler.events = events
	key := sessionevents.Key{WorkspaceID: workspace.ID, SessionID: logicalID}
	before, err := broker.Append(sessionevents.Append{
		Key: key, MessageID: "before", SourceSandboxID: "sbx-before", SourceCursor: 1, Data: json.RawMessage(`{"value":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Repeated stops must remain harmless to the logical Session and its stream.
	for range 2 {
		request := trustedEdgeRequest(http.MethodDelete,
			"http://edge-origin.cantelop.dev/__cantelop/v1/sessions/"+logicalID,
			workspace.AppID, "agent.cantelop.dev")

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("stop = %d %s", response.Code, response.Body.String())
		}
	}
	if control.terminateCount() != 2 || events.eventCloses != 0 {
		t.Fatalf("sandbox releases = %d, event closes = %d", control.terminateCount(), events.eventCloses)
	}
	retained, err := broker.ReadAvailable(key, 0)
	if err != nil || retained.Closed || len(retained.Events) != 1 || retained.StreamID != before.StreamID {
		t.Fatalf("stream after stop = %+v, %v", retained, err)
	}
	after, err := broker.Append(sessionevents.Append{
		Key: key, MessageID: "after", SourceSandboxID: "sbx-after", SourceCursor: 1, Data: json.RawMessage(`{"value":2}`),
	})
	if err != nil {
		t.Fatalf("replacement Sandbox output: %v", err)
	}
	batch, err := broker.ReadAvailable(key, before.Sequence)
	if err != nil || batch.Closed || batch.StreamID != before.StreamID || len(batch.Events) != 1 || after.Sequence != before.Sequence+1 {
		t.Fatalf("continued subscription = %+v, %v", batch, err)
	}
}

func TestTrustedEdgeOpensWorkspaceScopedSessionKey(t *testing.T) {
	workspace := testWorkspace(
		"app_0123456789abcdef0123456789abcdef",
		"wsp_0123456789abcdef0123456789abcdef",
		"preview", 0, 0,
	)
	control := &fakeControl{workspaces: []Workspace{workspace}}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest(
		http.MethodPost,
		"http://edge-origin.cantelop.dev/__cantelop/v1/sessions/open",
		workspace.AppID, "agent.cantelop.dev")

	request.Body = io.NopCloser(strings.NewReader(
		`{"key":"telegram","workspace_id":"` + workspace.ID + `","keep_alive_seconds":300}`,
	))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("open response = %d %s", response.Code, response.Body.String())
	}
	spec := control.createSpec()
	digest := sha256.Sum256([]byte(workspace.ID + "\x00telegram"))
	if !resourceid.ValidSessionID(spec.ID) || spec.ID == "ses_telegram" ||
		spec.WorkspaceID != workspace.ID || spec.KeepAliveSeconds != 300 ||
		spec.KeyHash != hex.EncodeToString(digest[:]) {
		t.Fatalf("CreateSessionSpec = %#v", spec)
	}
}

func TestTrustedEdgeTreatsDefaultAsLiteralWorkspaceSlug(t *testing.T) {
	appID := "app_0123456789abcdef0123456789abcdef"
	primary := testWorkspace(appID, "wsp_0123456789abcdef0123456789abcdef", "production", 0, 0)
	primary.CreatedAt = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	secondary := testWorkspace(appID, "wsp_1123456789abcdef0123456789abcdef", "default", 0, 0)
	secondary.CreatedAt = primary.CreatedAt.Add(time.Hour)
	control := &fakeControl{workspaces: []Workspace{secondary, primary}}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest(
		http.MethodPost,
		"http://edge-origin.cantelop.dev/__cantelop/v1/sessions/open",
		appID, "agent.cantelop.dev")

	request.Body = io.NopCloser(strings.NewReader(
		`{"key":"thread","workspace":"default","keep_alive_seconds":0}`,
	))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("open response = %d %s", response.Code, response.Body.String())
	}
	if spec := control.createSpec(); spec.WorkspaceID != secondary.ID || spec.KeepAliveSeconds != 0 {
		t.Fatalf("CreateSessionSpec = %#v", spec)
	}
}

func TestTrustedEdgeRejectsSessionOpenWithoutWorkspaceSelector(t *testing.T) {
	appID := "app_0123456789abcdef0123456789abcdef"
	workspace := testWorkspace(appID, "wsp_0123456789abcdef0123456789abcdef", "production", 0, 0)
	control := &fakeControl{workspaces: []Workspace{workspace}}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest(
		http.MethodPost,
		"http://edge-origin.cantelop.dev/__cantelop/v1/sessions/open",
		appID, "agent.cantelop.dev")

	request.Body = io.NopCloser(strings.NewReader(
		`{"key":"thread","keep_alive_seconds":0}`,
	))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_session_request") {
		t.Fatalf("open response = %d %s", response.Code, response.Body.String())
	}
	if spec := control.createSpec(); spec.WorkspaceID != "" {
		t.Fatalf("CreateSessionSpec = %#v", spec)
	}
}

func TestTrustedEdgeRejectsSessionOpenWithoutKeepAlive(t *testing.T) {
	appID := "app_0123456789abcdef0123456789abcdef"
	workspace := testWorkspace(appID, "wsp_0123456789abcdef0123456789abcdef", "production", 0, 0)
	control := &fakeControl{workspaces: []Workspace{workspace}}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := trustedEdgeRequest(
		http.MethodPost,
		"http://edge-origin.cantelop.dev/__cantelop/v1/sessions/open",
		appID, "agent.cantelop.dev")

	request.Body = io.NopCloser(strings.NewReader(`{"key":"thread"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || control.createCount() != 0 {
		t.Fatalf("open response/creates = %d/%d %s", response.Code, control.createCount(), response.Body.String())
	}
}

func TestTrustedEdgeDispatchRequiresAuthenticationAndCompleteMetadata(t *testing.T) {
	appID := "app_0123456789abcdef0123456789abcdef"
	workspaceID := "wsp_0123456789abcdef0123456789abcdef"
	hostname := "base-agent.cantelop.dev"
	workspace := testWorkspace(appID, workspaceID, "edge", 0, 600)

	tests := []struct {
		name       string
		mutate     func(*http.Request)
		wantStatus int
		wantCode   string
	}{
		{
			name: "missing authentication", wantStatus: http.StatusUnauthorized,
			wantCode: "edge_authentication_required",
			mutate:   func(r *http.Request) { r.Header.Del(edgeprotocol.AuthorizationHeader) },
		},
		{
			name: "wrong authentication", wantStatus: http.StatusUnauthorized,
			wantCode: "edge_authentication_required",
			mutate: func(r *http.Request) {
				r.Header.Set(edgeprotocol.AuthorizationHeader, "Bearer "+strings.Repeat("f", 64))
			},
		},
		{
			name: "repeated App", wantStatus: http.StatusBadRequest,
			wantCode: "invalid_edge_request",
			mutate:   func(r *http.Request) { r.Header.Add(edgeprotocol.AppIDHeader, appID) },
		},
		{
			name: "malformed App ID", wantStatus: http.StatusBadRequest,
			wantCode: "invalid_edge_request",
			mutate:   func(r *http.Request) { r.Header.Set(edgeprotocol.AppIDHeader, "app_invalid") },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := &fakeControl{workspaces: []Workspace{workspace}, targetIP: "172.16.0.2"}
			handler, err := newTestHandler(control, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			request := trustedEdgeRequest(http.MethodPost, "http://edge-origin.cantelop.dev/__cantelop/v1/sessions", appID, hostname)
			test.mutate(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			if control.createCount() != 0 {
				t.Fatalf("create count = %d, want 0", control.createCount())
			}
		})
	}
}

func TestTrustedEdgeDispatchValidatesWorkspaceOwnershipAndStatus(t *testing.T) {
	appID := "app_0123456789abcdef0123456789abcdef"
	workspaceID := "wsp_0123456789abcdef0123456789abcdef"
	hostname := "base-agent.cantelop.dev"

	tests := []struct {
		name       string
		workspace  *Workspace
		wantStatus int
		wantCode   string
	}{
		{name: "missing", wantStatus: http.StatusNotFound, wantCode: "resource_not_found"},
		{
			name: "other App", wantStatus: http.StatusNotFound, wantCode: "resource_not_found",
			workspace: func() *Workspace {
				value := testWorkspace("app_ffffffffffffffffffffffffffffffff", workspaceID, "edge", 0, 600)
				return &value
			}(),
		},
		{
			name: "inactive", wantStatus: http.StatusConflict, wantCode: "workspace_unavailable",
			workspace: func() *Workspace {
				value := testWorkspace(appID, workspaceID, "edge", 0, 600)
				archivedAt := time.Now()
				value.ArchivedAt = &archivedAt
				return &value
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			control := &fakeControl{targetIP: "172.16.0.2"}
			if test.workspace != nil {
				control.workspaces = []Workspace{*test.workspace}
			}
			handler, err := newTestHandler(control, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			request := trustedEdgeRequest(http.MethodPost, "http://edge-origin.cantelop.dev/__cantelop/v1/sessions", appID, hostname)
			request.Body = io.NopCloser(strings.NewReader(`{"id":"ses_ownership","workspace_id":"` + workspaceID + `","keep_alive_seconds":300}`))
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			if control.createCount() != 0 {
				t.Fatalf("create count = %d, want 0", control.createCount())
			}
		})
	}
}

func trustedEdgeRequest(method, target, appID, hostname string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	request.Header.Set(edgeprotocol.DispatchHeader, "1")
	request.Header.Set(edgeprotocol.AuthorizationHeader, "Bearer "+testEdgeToken)
	request.Header.Set(edgeprotocol.RequestIDHeader, "req_0123456789abcdef0123456789abcdef")
	request.Header.Set(edgeprotocol.AppIDHeader, appID)
	request.Header.Set(edgeprotocol.HostnameHeader, hostname)
	return request
}

func (f *fakeControl) EnqueueMessageDelivery(_ context.Context, value MessageDelivery) (MessageDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.messageDeliveries {
		if existing.ID == value.ID {
			if existing.WorkspaceID != value.WorkspaceID ||
				existing.LogicalSessionID != value.LogicalSessionID ||
				existing.KeepAliveSeconds != value.KeepAliveSeconds ||
				string(existing.Payload) != string(value.Payload) || existing.ReplyRequested != value.ReplyRequested {
				return MessageDelivery{}, &ControlError{StatusCode: http.StatusConflict, Code: "message_conflict"}
			}
			return existing, nil
		}
	}
	value.State, value.WorkState = "accepted", "pending"
	value.AcceptedAt = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	value.UpdatedAt = value.AcceptedAt
	f.messageDeliveries = append(f.messageDeliveries, value)
	return value, nil
}

func (f *fakeControl) GetMessageDelivery(_ context.Context, id string) (MessageDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, value := range f.messageDeliveries {
		if value.ID == id {
			return value, nil
		}
	}
	return MessageDelivery{}, &ControlError{StatusCode: http.StatusNotFound, Code: "message_not_found"}
}

func (f *fakeControl) messageDelivery(id string) MessageDelivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, value := range f.messageDeliveries {
		if value.ID == id {
			return value
		}
	}
	return MessageDelivery{}
}

func (f *fakeControl) ListWorkspaces(context.Context) ([]Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Workspace(nil), f.workspaces...), nil
}

func (f *fakeControl) ListAppWorkspaces(_ context.Context, appID string) ([]Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	workspaces := make([]Workspace, 0, len(f.workspaces))
	for _, workspace := range f.workspaces {
		if workspace.AppID == appID {
			workspaces = append(workspaces, workspace)
		}
	}
	return workspaces, nil
}

func (f *fakeControl) ResolveAppWorkspace(_ context.Context, appID, workspaceSlug string) (Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, workspace := range f.workspaces {
		if workspace.AppID == appID && workspace.Slug == workspaceSlug && workspace.ArchivedAt == nil {
			return workspace, nil
		}
	}
	return Workspace{}, &ControlError{StatusCode: http.StatusNotFound, Code: "not_found"}
}

func (f *fakeControl) GetWorkspace(_ context.Context, id string) (Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, workspace := range f.workspaces {
		if workspace.ID == id {
			return workspace, nil
		}
	}
	return Workspace{}, &ControlError{StatusCode: http.StatusNotFound, Code: "not_found"}
}

func (f *fakeControl) CreateWorkspace(_ context.Context, appID, slug string) (Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	workspace := Workspace{
		ID:        "wsp_0123456789abcdef0123456789abcdef",
		AppID:     appID,
		AppSlug:   "agent",
		Slug:      slug,
		CreatedAt: time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
	}
	f.workspaces = append(f.workspaces, workspace)
	return workspace, nil
}

func (f *fakeControl) OpenWorkspace(ctx context.Context, appID, slug string) (Workspace, error) {
	if f.openWorkspaceFn != nil {
		return f.openWorkspaceFn(ctx, appID, slug)
	}
	if workspace, err := f.ResolveAppWorkspace(ctx, appID, slug); err == nil {
		return workspace, nil
	}
	return f.CreateWorkspace(ctx, appID, slug)
}

func (f *fakeControl) ListSessions(_ context.Context, workspaceID string) ([]Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sessionRecords := make([]Session, 0)
	for _, sessionRecord := range f.sessionRecords {
		if sessionRecord.WorkspaceID == workspaceID {
			sessionRecords = append(sessionRecords, sessionRecord)
		}
	}
	return sessionRecords, nil
}

func (f *fakeControl) GetSession(_ context.Context, id string) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return Session{}, f.getErr
	}
	for _, sessionRecord := range f.sessionRecords {
		if sessionRecord.ID == id {
			return sessionRecord, nil
		}
	}
	return Session{}, &ControlError{StatusCode: http.StatusNotFound, Code: "runtime_not_found"}
}

func (f *fakeControl) GetSessionRoute(_ context.Context, id string) (session.Route, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sessionRecord := range f.sessionRecords {
		if sessionRecord.ID != id {
			continue
		}
		endpoint := f.routes[id]
		if endpoint.Scheme == "" && endpoint.Authority == "" && len(endpoint.Headers) == 0 && f.targetIP != "" {
			endpoint = session.Endpoint{Scheme: "http", Authority: net.JoinHostPort(f.targetIP, "3000")}
		}
		return session.Route{
			Session: session.Session{
				ID: sessionRecord.ID, WorkspaceID: sessionRecord.WorkspaceID, State: session.StateActive,
			},
			SandboxID: "sbx-0123456789abcdef0123456789abcdef",
			Endpoint:  endpoint,
		}, nil
	}
	return session.Route{}, &ControlError{StatusCode: http.StatusNotFound, Code: "runtime_not_found"}
}

func (f *fakeControl) CreateSession(ctx context.Context, spec CreateSessionSpec) (Session, error) {
	f.mu.Lock()
	f.creates++
	f.lastCreate = spec
	createFn := f.createFn
	f.mu.Unlock()
	if createFn != nil {
		return createFn(ctx, spec)
	}
	select {
	case <-ctx.Done():
		return Session{}, ctx.Err()
	case <-time.After(f.delay):
	}
	sessionRecord := Session{
		ID: spec.ID, WorkspaceID: spec.WorkspaceID, State: session.StateActive,
	}
	f.mu.Lock()
	f.sessionRecords = append(f.sessionRecords, sessionRecord)
	f.mu.Unlock()
	return sessionRecord, nil
}

func (f *fakeControl) createSpec() CreateSessionSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastCreate
}

func (f *fakeControl) TerminateSession(_ context.Context, id string) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminates++
	for index, sessionRecord := range f.sessionRecords {
		if sessionRecord.ID == id {
			f.sessionRecords[index].State = session.StateIdle
			return f.sessionRecords[index], nil
		}
	}
	return Session{}, errors.New("Session not found")
}

func (f *fakeControl) AcquireSessionWorkLease(_ context.Context, id, leaseID string, ttl time.Duration) (controlclient.WorkLease, error) {
	return controlclient.WorkLease{SessionID: id, LeaseID: leaseID, ExpiresAt: time.Now().Add(ttl)}, nil
}

func (f *fakeControl) RenewSessionWorkLease(_ context.Context, id, leaseID string, ttl time.Duration) (controlclient.WorkLease, error) {
	return controlclient.WorkLease{SessionID: id, LeaseID: leaseID, ExpiresAt: time.Now().Add(ttl)}, nil
}

func (f *fakeControl) ReleaseSessionWorkLease(_ context.Context, id, _ string) (Session, error) {
	return f.GetSession(context.Background(), id)
}

// WaitForMessageReply blocks until the delivery leaves active handling, like
// the control plane's bounded wait.
func (f *fakeControl) WaitForMessageReply(ctx context.Context, id string) (MessageDelivery, error) {
	for {
		value, err := f.GetMessageDelivery(ctx, id)
		if err != nil || value.State == "handled" || value.State == "failed" {
			return value, err
		}
		select {
		case <-ctx.Done():
			return value, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (f *fakeControl) SessionView(context.Context, string, string) (sessionevents.View, error) {
	return sessionevents.View{}, &ControlError{StatusCode: http.StatusNotFound, Code: "session_not_found"}
}

func (f *fakeControl) DeleteWorkspace(context.Context, string, string) (WorkspaceDeletion, error) {
	return WorkspaceDeletion{}, errors.New("Workspace deletion is not configured")
}

func testWorkspace(appID, workspaceID, slug string, _ int, _ int) Workspace {
	return Workspace{
		ID: workspaceID, AppID: appID, AppSlug: "agent", Slug: slug,
	}
}

func (f *fakeControl) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates
}

func (f *fakeControl) terminateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.terminates
}

func TestSessionIDFitsLifecycleContract(t *testing.T) {
	id, err := sessionbroker.NewID(strings.Repeat("a", 63))
	if err != nil {
		t.Fatal(err)
	}
	if len(id) > 64 || !regexp.MustCompile(`^ses_[A-Za-z0-9][A-Za-z0-9_-]*$`).MatchString(id) {
		t.Fatalf("generated Session ID = %q", id)
	}
}

func TestGatewayLocalStatusDoesNotCaptureAppHealthPath(t *testing.T) {
	control := &fakeControl{}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081"+path, nil)
		request.Host = "127.0.0.1:8081"
		request.RemoteAddr = "127.0.0.1:54321"
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Errorf("local %s status = %d", path, recorder.Code)
		}

		recorder = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodGet, "http://gateway"+path, nil)
		request.Host = "unknown-app.cantelop.dev"
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("App %s status = %d, want routed 404", path, recorder.Code)
		}
	}
}

// The front proxy also connects over loopback, so a loopback Host header alone
// cannot prove a request is local. A public caller that spoofs one must not
// reach the operator-only status endpoints.
func TestGatewayLocalStatusRejectsForwardedRequests(t *testing.T) {
	control := &fakeControl{workspaces: []Workspace{
		testWorkspace("app-scaled", "wsp_scaled", "scaled", 0, 300),
	}}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		for _, forwarded := range []struct {
			name   string
			header string
			value  string
		}{
			{name: "X-Forwarded-For", header: "X-Forwarded-For", value: "203.0.113.7"},
			{name: "X-Forwarded-Host", header: "X-Forwarded-Host", value: "127.0.0.1"},
			{name: "X-Forwarded-Proto", header: "X-Forwarded-Proto", value: "https"},
			{name: "Forwarded", header: "Forwarded", value: "for=203.0.113.7"},
		} {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081"+path, nil)
			request.Host = "127.0.0.1:8081"
			request.RemoteAddr = "127.0.0.1:54321"
			request.Header.Set(forwarded.header, forwarded.value)
			handler.ServeHTTP(recorder, request)
			if recorder.Code == http.StatusOK {
				t.Errorf("%s with spoofed Host and %s = 200, want the request routed as tenant traffic", path, forwarded.name)
			}
		}
	}
}

// A caller reaching the gateway from off-host cannot present a loopback source
// address even if it controls every header.
func TestGatewayLocalStatusRejectsRemoteCallers(t *testing.T) {
	control := &fakeControl{workspaces: []Workspace{
		testWorkspace("app-scaled", "wsp_scaled", "scaled", 0, 300),
	}}
	handler, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081"+path, nil)
		request.Host = "127.0.0.1:8081"
		request.RemoteAddr = "203.0.113.7:41234"
		handler.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusOK {
			t.Errorf("%s from a remote address = 200, want rejection", path)
		}
	}
}

func TestSessionEventCursorRequiresMatchingStreamIdentity(t *testing.T) {
	for _, cursor := range []string{"&after=1", "&after=1&stream_id=22222222222222222222222222222222"} {
		control := &fakeControl{workspaces: []Workspace{testWorkspace("app_0123456789abcdef0123456789abcdef", "wsp_0123456789abcdef0123456789abcdef", "primary", 0, 300)}, eventBatches: []controlclient.SessionEventBatch{{StreamID: "11111111111111111111111111111111", Closed: true}}}
		handler, _ := newTestHandler(control, nil, nil)
		request := trustedEdgeRequest(http.MethodGet, "http://edge-origin.cantelop.dev/__cantelop/v1/sessions/thread/events?workspace_id=wsp_0123456789abcdef0123456789abcdef"+cursor, "app_0123456789abcdef0123456789abcdef", "agent.cantelop.dev")
		request.Header.Set("Accept", "text/event-stream")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if strings.Contains(response.Body.String(), ": connected") {
			t.Fatal("invalid cursor received a successful subscription comment")
		}
		if response.Code != 400 && response.Code != 409 {
			t.Fatalf("cursor %s got %d", cursor, response.Code)
		}
	}
}
