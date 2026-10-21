package devruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
)

const (
	localTestAppID       = "app_0123456789abcdef0123456789abcdef"
	localTestWorkspaceID = "wsp_0123456789abcdef0123456789abcdef"
	localTestSessionID   = "ses_0123456789abcdef0123456789abcdef"
	localTestMessageID   = "msg_0123456789abcdef0123456789abcdef"
)

type fakeLocalSandboxFactory struct {
	mu        sync.Mutex
	starts    int
	sandboxes []*fakeLocalSandbox
	awaitErr  error
	output    []messageprotocol.OutputBatch
}

type fakeLocalSandbox struct {
	mu       sync.Mutex
	messages int
	closes   int
	message  messageprotocol.Request
	awaitErr error
	output   []messageprotocol.OutputBatch
	reads    int
}

type blockingLocalSandboxFactory struct {
	started  chan struct{}
	release  chan struct{}
	executed chan struct{}

	mu     sync.Mutex
	starts int
}

type orderedLocalSandboxFactory struct {
	started      chan string
	releaseFirst chan struct{}
}

type orderedLocalSandbox struct {
	started      chan string
	releaseFirst chan struct{}
}

type quiescenceLocalSandboxFactory struct {
	quiescenceStarted chan struct{}
	releaseQuiescence chan struct{}
	closed            chan struct{}
}

type quiescenceLocalSandbox struct {
	quiescenceStarted chan struct{}
	releaseQuiescence chan struct{}
	closed            chan struct{}
	closeOnce         sync.Once
	quiescenceOnce    sync.Once
}

func (factory *blockingLocalSandboxFactory) Start(ctx context.Context, _ string) (localSandbox, error) {
	factory.mu.Lock()
	factory.starts++
	factory.mu.Unlock()
	factory.started <- struct{}{}
	select {
	case <-factory.release:
		return &notifyingLocalSandbox{executed: factory.executed}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (factory *orderedLocalSandboxFactory) Start(context.Context, string) (localSandbox, error) {
	return &orderedLocalSandbox{started: factory.started, releaseFirst: factory.releaseFirst}, nil
}

func (sandbox *orderedLocalSandbox) Deliver(
	ctx context.Context,
	message messageprotocol.Request,
) (uint64, error) {
	sandbox.started <- message.Message.ID
	if message.Message.ID == localTestMessageID {
		select {
		case <-sandbox.releaseFirst:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return 1, nil
}

func (*orderedLocalSandbox) AwaitQuiescence(context.Context, uint64) error { return nil }

func (*orderedLocalSandbox) ReadOutput(
	ctx context.Context,
	_ uint64,
	wait bool,
) (messageprotocol.OutputBatch, error) {
	if wait {
		<-ctx.Done()
		return messageprotocol.OutputBatch{}, ctx.Err()
	}
	return messageprotocol.OutputBatch{}, nil
}

func (*orderedLocalSandbox) Close(context.Context) error { return nil }

func (factory *quiescenceLocalSandboxFactory) Start(context.Context, string) (localSandbox, error) {
	return &quiescenceLocalSandbox{
		quiescenceStarted: factory.quiescenceStarted,
		releaseQuiescence: factory.releaseQuiescence,
		closed:            factory.closed,
	}, nil
}

func (*quiescenceLocalSandbox) Deliver(context.Context, messageprotocol.Request) (uint64, error) {
	return 7, nil
}

func (*quiescenceLocalSandbox) ReadOutput(
	ctx context.Context,
	_ uint64,
	wait bool,
) (messageprotocol.OutputBatch, error) {
	if wait {
		<-ctx.Done()
		return messageprotocol.OutputBatch{}, ctx.Err()
	}
	return messageprotocol.OutputBatch{}, nil
}

func (sandbox *quiescenceLocalSandbox) AwaitQuiescence(ctx context.Context, _ uint64) error {
	sandbox.quiescenceOnce.Do(func() { close(sandbox.quiescenceStarted) })
	select {
	case <-sandbox.releaseQuiescence:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (sandbox *quiescenceLocalSandbox) Close(context.Context) error {
	sandbox.closeOnce.Do(func() { close(sandbox.closed) })
	return nil
}

type notifyingLocalSandbox struct {
	executed chan struct{}
}

func (sandbox *notifyingLocalSandbox) Deliver(context.Context, messageprotocol.Request) (uint64, error) {
	sandbox.executed <- struct{}{}
	return 1, nil
}

func (*notifyingLocalSandbox) ReadOutput(ctx context.Context, _ uint64, wait bool) (messageprotocol.OutputBatch, error) {
	if wait {
		<-ctx.Done()
		return messageprotocol.OutputBatch{}, ctx.Err()
	}
	return messageprotocol.OutputBatch{}, nil
}

func (*notifyingLocalSandbox) AwaitQuiescence(context.Context, uint64) error { return nil }

func (*notifyingLocalSandbox) Close(context.Context) error { return nil }

func (factory *fakeLocalSandboxFactory) Start(context.Context, string) (localSandbox, error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	sandbox := &fakeLocalSandbox{}
	sandbox.awaitErr = factory.awaitErr
	sandbox.output = append([]messageprotocol.OutputBatch(nil), factory.output...)
	factory.starts++
	factory.sandboxes = append(factory.sandboxes, sandbox)
	return sandbox, nil
}

func (sandbox *fakeLocalSandbox) Deliver(_ context.Context, message messageprotocol.Request) (uint64, error) {
	sandbox.mu.Lock()
	defer sandbox.mu.Unlock()
	sandbox.messages++
	sandbox.message = message
	return 1, nil
}

func (sandbox *fakeLocalSandbox) ReadOutput(ctx context.Context, _ uint64, wait bool) (messageprotocol.OutputBatch, error) {
	sandbox.mu.Lock()
	if sandbox.reads < len(sandbox.output) {
		batch := sandbox.output[sandbox.reads]
		sandbox.reads++
		sandbox.mu.Unlock()
		return batch, nil
	}
	sandbox.mu.Unlock()
	if wait {
		<-ctx.Done()
		return messageprotocol.OutputBatch{}, ctx.Err()
	}
	return messageprotocol.OutputBatch{}, nil
}

func TestLocalRuntimeStreamsBrokeredSessionOutput(t *testing.T) {
	factory := &fakeLocalSandboxFactory{output: []messageprotocol.OutputBatch{{Events: []messageprotocol.OutputEvent{{
		Cursor: 1, MessageID: localTestMessageID, Data: json.RawMessage(`{"token":"hello"}`),
	}}}}}
	runtime := newTestLocalRuntime(t, factory)
	body := fmt.Sprintf(`{"session":{"id":"thread","workspace_id":"%s","keep_alive_seconds":300},"message":{"id":"%s","payload":{"prompt":"hello"}}}`, localTestWorkspaceID, localTestMessageID)
	accepted := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages", body)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("accept status = %d, body = %s", accepted.Code, accepted.Body.String())
	}
	waitLocalMessageHandled(t, runtime, "thread", localTestMessageID)
	_ = runtime.events.Close(sessionevents.Key{WorkspaceID: localTestWorkspaceID, SessionID: "thread"})
	request := httptest.NewRequest(http.MethodGet,
		"/__cantelop/v1/sessions/thread/events?workspace_id="+localTestWorkspaceID, nil)
	request.Header.Set("Accept", "text/event-stream")
	response := httptest.NewRecorder()
	runtime.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), ":1\n") ||
		!strings.Contains(response.Body.String(), `"token":"hello"`) {
		t.Fatalf("events = %d %s", response.Code, response.Body.String())
	}
	if !strings.HasPrefix(response.Body.String(), ": connected\n\n") {
		t.Fatalf("local stream did not open with the connected comment: %q", response.Body.String())
	}
}

func (sandbox *fakeLocalSandbox) AwaitQuiescence(context.Context, uint64) error {
	return sandbox.awaitErr
}

func (sandbox *fakeLocalSandbox) Close(context.Context) error {
	sandbox.mu.Lock()
	defer sandbox.mu.Unlock()
	sandbox.closes++
	return nil
}

func TestLocalRuntimeDispatchesMessageAndReportsHandled(t *testing.T) {
	factory := &fakeLocalSandboxFactory{}
	runtime := newTestLocalRuntime(t, factory)
	response := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages",
		localMessageBody(localTestMessageID, "agent:primary", 30, `{"type":"steer","prompt":"focus"}`))
	if response.Code != http.StatusAccepted {
		t.Fatalf("dispatch status = %d, body = %s", response.Code, response.Body.String())
	}
	waitLocalMessageHandled(t, runtime, "agent:primary", localTestMessageID)
	if len(factory.sandboxes) != 1 || factory.sandboxes[0].messages != 1 ||
		factory.sandboxes[0].message.Message.ID != localTestMessageID {
		t.Fatalf("Sandbox Messages = %#v", factory.sandboxes)
	}
}

func TestLocalRuntimeAcceptsSDKEncodedSessionRoutes(t *testing.T) {
	factory := &fakeLocalSandboxFactory{}
	runtime := newTestLocalRuntime(t, factory)
	response := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages",
		localMessageBody(localTestMessageID, "agent:primary", 30, `{}`))
	if response.Code != http.StatusAccepted {
		t.Fatalf("dispatch status = %d, body = %s", response.Code, response.Body.String())
	}

	statusPath := "/__cantelop/v1/sessions/agent%3Aprimary/messages/" + localTestMessageID + "?workspace_id=" + localTestWorkspaceID
	deadline := time.Now().Add(time.Second)
	for {
		status := localRuntimeRequest(runtime, http.MethodGet, statusPath, "")
		if status.Code == http.StatusOK && strings.Contains(status.Body.String(), `"state":"handled"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("encoded Message status did not become handled: %d %s", status.Code, status.Body.String())
		}
		time.Sleep(time.Millisecond)
	}

	terminated := localRuntimeRequest(runtime, http.MethodDelete,
		"/__cantelop/v1/sessions/agent%3Aprimary", "")
	if terminated.Code != http.StatusOK {
		t.Fatalf("encoded termination = %d %s", terminated.Code, terminated.Body.String())
	}
	malformed := localRuntimeRequest(runtime, http.MethodDelete,
		"/__cantelop/v1/sessions/agent%2Fprimary", "")
	if malformed.Code != http.StatusNotFound {
		t.Fatalf("encoded path separator = %d %s", malformed.Code, malformed.Body.String())
	}
}

func TestLocalRuntimePreservesHandledStateWhenQuiescenceFails(t *testing.T) {
	factory := &fakeLocalSandboxFactory{awaitErr: errors.New("mailbox unavailable")}
	runtime := newTestLocalRuntime(t, factory)
	response := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages",
		localMessageBody(localTestMessageID, "agent:primary", 30, `{}`))
	if response.Code != http.StatusAccepted {
		t.Fatalf("dispatch status = %d, body = %s", response.Code, response.Body.String())
	}
	waitLocalMessageHandled(t, runtime, "agent:primary", localTestMessageID)
	if len(factory.sandboxes) != 1 {
		t.Fatalf("Sandboxes = %d, want 1", len(factory.sandboxes))
	}
	deadline := time.Now().Add(time.Second)
	for {
		factory.sandboxes[0].mu.Lock()
		closes := factory.sandboxes[0].closes
		factory.sandboxes[0].mu.Unlock()
		if closes == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Sandbox was not released after quiescence failure")
		}
		time.Sleep(time.Millisecond)
	}
	status := localRuntimeRequest(runtime, http.MethodGet,
		"/__cantelop/v1/sessions/agent:primary/messages/"+localTestMessageID, "")
	if !strings.Contains(status.Body.String(), `"state":"handled"`) {
		t.Fatalf("Message state after quiescence failure = %s", status.Body.String())
	}
}

func TestLocalRuntimeMessageAcceptancePrecedesSandboxStart(t *testing.T) {
	factory := &blockingLocalSandboxFactory{
		started: make(chan struct{}, 1), release: make(chan struct{}), executed: make(chan struct{}, 1),
	}
	runtime := newTestLocalRuntime(t, factory)
	body := localMessageBody(localTestMessageID, "agent:primary", 0, `{"event":"push"}`)
	response := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages", body)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"status":"accepted"`) {
		t.Fatalf("acceptance = %d %s", response.Code, response.Body.String())
	}
	select {
	case <-factory.started:
	case <-time.After(time.Second):
		t.Fatal("background Sandbox start did not begin")
	}
	duplicate := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages", body)
	if duplicate.Code != http.StatusAccepted || duplicate.Body.String() != response.Body.String() {
		t.Fatalf("duplicate response = %d %q", duplicate.Code, duplicate.Body.String())
	}
	close(factory.release)
	select {
	case <-factory.executed:
	case <-time.After(time.Second):
		t.Fatal("accepted Message was not dispatched")
	}
}

func TestLocalRuntimeDeliversMessagesInPerSessionAcceptanceOrder(t *testing.T) {
	factory := &orderedLocalSandboxFactory{
		started: make(chan string, 2), releaseFirst: make(chan struct{}),
	}
	runtime := newTestLocalRuntime(t, factory)
	secondID := "msg_fedcba9876543210fedcba9876543210"

	first := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages",
		localMessageBody(localTestMessageID, "agent:primary", 30, `{"sequence":1}`))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first Message = %d %s", first.Code, first.Body.String())
	}
	select {
	case id := <-factory.started:
		if id != localTestMessageID {
			t.Fatalf("first delivered Message = %s", id)
		}
	case <-time.After(time.Second):
		t.Fatal("first Message was not delivered")
	}

	second := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages",
		localMessageBody(secondID, "agent:primary", 30, `{"sequence":2}`))
	if second.Code != http.StatusAccepted {
		t.Fatalf("second Message = %d %s", second.Code, second.Body.String())
	}
	select {
	case id := <-factory.started:
		t.Fatalf("Message %s overtook the in-flight Message", id)
	case <-time.After(25 * time.Millisecond):
	}

	close(factory.releaseFirst)
	select {
	case id := <-factory.started:
		if id != secondID {
			t.Fatalf("second delivered Message = %s", id)
		}
	case <-time.After(time.Second):
		t.Fatal("second Message was not delivered after the first settled")
	}
}

func TestLocalRuntimeDeliversDifferentSessionsInParallel(t *testing.T) {
	factory := &orderedLocalSandboxFactory{
		started: make(chan string, 2), releaseFirst: make(chan struct{}),
	}
	runtime := newTestLocalRuntime(t, factory)
	secondID := "msg_fedcba9876543210fedcba9876543210"

	first := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages",
		localMessageBody(localTestMessageID, "agent:primary", 30, `{}`))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first Message = %d %s", first.Code, first.Body.String())
	}
	select {
	case <-factory.started:
	case <-time.After(time.Second):
		t.Fatal("first Session did not start delivery")
	}

	second := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages",
		localMessageBody(secondID, "agent:secondary", 30, `{}`))
	if second.Code != http.StatusAccepted {
		t.Fatalf("second Message = %d %s", second.Code, second.Body.String())
	}
	select {
	case id := <-factory.started:
		if id != secondID {
			t.Fatalf("parallel delivered Message = %s", id)
		}
	case <-time.After(time.Second):
		t.Fatal("second Session was serialized behind the first Session")
	}
	close(factory.releaseFirst)
}

func TestLocalRuntimeBeginsKeepAliveOnlyAfterQuiescence(t *testing.T) {
	factory := &quiescenceLocalSandboxFactory{
		quiescenceStarted: make(chan struct{}),
		releaseQuiescence: make(chan struct{}),
		closed:            make(chan struct{}),
	}
	runtime := newTestLocalRuntime(t, factory)
	response := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages",
		localMessageBody(localTestMessageID, "agent:primary", 0, `{}`))
	if response.Code != http.StatusAccepted {
		t.Fatalf("dispatch status = %d, body = %s", response.Code, response.Body.String())
	}
	select {
	case <-factory.quiescenceStarted:
	case <-time.After(time.Second):
		t.Fatal("runtime did not begin the quiescence wait")
	}
	select {
	case <-factory.closed:
		t.Fatal("zero-keep-alive Sandbox closed before quiescence")
	default:
	}
	close(factory.releaseQuiescence)
	select {
	case <-factory.closed:
	case <-time.After(time.Second):
		t.Fatal("zero-keep-alive Sandbox remained open after quiescence")
	}
}

func TestLocalRuntimeMessageStatusIsUnknownAfterRestart(t *testing.T) {
	first := newTestLocalRuntime(t, &fakeLocalSandboxFactory{})
	response := localRuntimeRequest(first, http.MethodPost, "/__cantelop/v1/messages",
		localMessageBody(localTestMessageID, "agent:primary", 30, `{}`))
	if response.Code != http.StatusAccepted {
		t.Fatalf("dispatch status = %d, body = %s", response.Code, response.Body.String())
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := newTestLocalRuntime(t, &fakeLocalSandboxFactory{})
	status := localRuntimeRequest(second, http.MethodGet,
		"/__cantelop/v1/sessions/agent%3Aprimary/messages/"+localTestMessageID, "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"state":"unknown"`) {
		t.Fatalf("restarted Message status = %d %s", status.Code, status.Body.String())
	}
}

func TestLocalRuntimeReleasesSandboxWithoutTerminatingLogicalSession(t *testing.T) {
	factory := &fakeLocalSandboxFactory{}
	runtime := newTestLocalRuntime(t, factory)
	first := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages",
		localMessageBody(localTestMessageID, "agent:primary", 30, `{}`))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first Message = %d %s", first.Code, first.Body.String())
	}
	waitLocalMessageHandled(t, runtime, "agent:primary", localTestMessageID)
	if err := runtime.ReleaseSandboxes(); err != nil {
		t.Fatal(err)
	}
	secondID := "msg_fedcba9876543210fedcba9876543210"
	second := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages",
		localMessageBody(secondID, "agent:primary", 30, `{}`))
	if second.Code != http.StatusAccepted {
		t.Fatalf("second Message = %d %s", second.Code, second.Body.String())
	}
	waitLocalMessageHandled(t, runtime, "agent:primary", secondID)
	if factory.starts != 2 {
		t.Fatalf("Sandbox starts = %d, want 2", factory.starts)
	}
}

func localMessageBody(id, session string, keepAlive int, payload string) string {
	return `{"session":{"id":"` + session + `","workspace_id":"` + localTestWorkspaceID +
		`","keep_alive_seconds":` + fmt.Sprint(keepAlive) + `},"message":{"id":"` + id + `","payload":` + payload + `}}`
}

func waitLocalMessageHandled(t *testing.T, runtime *localRuntime, session, id string) {
	t.Helper()
	path := "/__cantelop/v1/sessions/" + session + "/messages/" + id
	deadline := time.Now().Add(time.Second)
	for {
		status := localRuntimeRequest(runtime, http.MethodGet, path, "")
		if strings.Contains(status.Body.String(), `"state":"handled"`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Message %s did not become handled: %s", id, status.Body.String())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLocalRuntimeCreatesWorkspaceWithSDKResponseShape(t *testing.T) {
	runtime := newTestLocalRuntime(t, &fakeLocalSandboxFactory{})
	response := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/workspaces", `{"slug":"preview"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var workspace map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &workspace); err != nil {
		t.Fatal(err)
	}
	if workspace["app_id"] != localTestAppID || workspace["slug"] != "preview" ||
		!ids.ValidLocalWorkspaceID(workspace["id"].(string)) {
		t.Fatalf("workspace = %#v", workspace)
	}
}

func TestLocalRuntimeReportsWorkspaceDatabaseUnavailable(t *testing.T) {
	runtime := newTestLocalRuntime(t, &fakeLocalSandboxFactory{})
	path := "/__cantelop/v1/workspaces/database/credentials"
	for _, test := range []struct {
		method, body string
		status       int
		code         string
	}{
		{http.MethodPost, `{"workspace_id":"` + localTestWorkspaceID + `"}`, http.StatusServiceUnavailable, "database_unavailable"},
		{http.MethodPost, `{"workspace_id":"wsp_invalid"}`, http.StatusBadRequest, "invalid_request"},
		{http.MethodPost, `{"workspace_id":"` + localTestWorkspaceID + `","extra":1}`, http.StatusBadRequest, "invalid_request"},
		{http.MethodGet, "", http.StatusMethodNotAllowed, "runtime_method_not_allowed"},
	} {
		response := localRuntimeRequest(runtime, test.method, path, test.body)
		var document struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		if response.Code != test.status || document.Error.Code != test.code {
			t.Fatalf("%s %s: status = %d, body = %s", test.method, test.body, response.Code, response.Body.String())
		}
	}
	if response := localRuntimeRequest(runtime, http.MethodPost, path+"?workspace_id="+localTestWorkspaceID, `{}`); response.Code != http.StatusNotFound {
		t.Fatalf("query status = %d", response.Code)
	}
}

func TestLocalRuntimeOpensWorkspaceBySlug(t *testing.T) {
	runtime := newTestLocalRuntime(t, &fakeLocalSandboxFactory{})
	response := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/workspaces/open", `{"slug":"production"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var workspace map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &workspace); err != nil {
		t.Fatal(err)
	}
	if workspace["id"] != localWorkspaceID(runtime.appID, "production") || workspace["slug"] != "production" {
		t.Fatalf("workspace = %#v", workspace)
	}
}

func TestLocalRuntimeWorkspaceIDsAreStableAcrossRuns(t *testing.T) {
	first := newTestLocalRuntime(t, &fakeLocalSandboxFactory{})
	second := newTestLocalRuntime(t, &fakeLocalSandboxFactory{})
	firstResponse := localRuntimeRequest(first, http.MethodPost, "/__cantelop/v1/workspaces", `{"slug":"preview"}`)
	secondResponse := localRuntimeRequest(second, http.MethodPost, "/__cantelop/v1/workspaces", `{"slug":"preview"}`)
	var firstWorkspace, secondWorkspace map[string]any
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &firstWorkspace); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &secondWorkspace); err != nil {
		t.Fatal(err)
	}
	if firstWorkspace["id"] != secondWorkspace["id"] {
		t.Fatalf("created Workspace IDs = %q and %q", firstWorkspace["id"], secondWorkspace["id"])
	}
}

func TestLocalRuntimeOpenIsWorkspaceScopedAndConfigurationStable(t *testing.T) {
	factory := &fakeLocalSandboxFactory{}
	runtime := newTestLocalRuntime(t, factory)
	body := `{"key":"telegram","workspace_id":"` + localTestWorkspaceID + `","keep_alive_seconds":300}`
	first := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/sessions/open", body)
	second := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/sessions/open", body)
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("open statuses = %d, %d", first.Code, second.Code)
	}
	if first.Body.String() != second.Body.String() || factory.starts != 1 {
		t.Fatalf("open responses = %q, %q; starts = %d", first.Body, second.Body, factory.starts)
	}
	conflict := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/sessions/open",
		`{"key":"telegram","workspace_id":"`+localTestWorkspaceID+`","keep_alive_seconds":301}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, body = %s", conflict.Code, conflict.Body.String())
	}
}

func TestLocalRuntimeRejectsSessionOpenWithoutWorkspaceSelector(t *testing.T) {
	factory := &fakeLocalSandboxFactory{}
	runtime := newTestLocalRuntime(t, factory)
	localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/workspaces/open", `{"slug":"production"}`)
	opened := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/sessions/open",
		`{"key":"thread","keep_alive_seconds":0}`)
	if opened.Code != http.StatusBadRequest {
		t.Fatalf("open status = %d, body = %s", opened.Code, opened.Body.String())
	}
	if factory.starts != 0 {
		t.Fatalf("starts = %d", factory.starts)
	}
}

func TestLocalRuntimeTreatsDefaultAsLiteralWorkspaceSlug(t *testing.T) {
	factory := &fakeLocalSandboxFactory{}
	runtime := newTestLocalRuntime(t, factory)
	created := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/workspaces", `{"slug":"default"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var workspace map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &workspace); err != nil {
		t.Fatal(err)
	}
	opened := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/sessions/open",
		`{"key":"thread","workspace":"default","keep_alive_seconds":0}`)
	if opened.Code != http.StatusOK {
		t.Fatalf("open status = %d, body = %s", opened.Code, opened.Body.String())
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(opened.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	session := runtime.session(response.ID)
	if session == nil || session.workspaceID != workspace["id"] {
		t.Fatalf("Session = %#v, Workspace = %#v", session, workspace)
	}
}

func TestLocalRuntimeRejectsSessionOpenWithoutKeepAlive(t *testing.T) {
	runtime := newTestLocalRuntime(t, &fakeLocalSandboxFactory{})
	opened := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/sessions/open", `{"key":"thread"}`)
	if opened.Code != http.StatusBadRequest {
		t.Fatalf("open status = %d, body = %s", opened.Code, opened.Body.String())
	}
}

func newTestLocalRuntime(t *testing.T, factory localSandboxFactory) *localRuntime {
	t.Helper()
	runtime, err := newLocalRuntime(localTestAppID, factory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func TestLocalRuntimeBridgeRequiresPerRunCredential(t *testing.T) {
	runtime := newTestLocalRuntime(t, &fakeLocalSandboxFactory{})
	runtime.bridgeToken = strings.Repeat("a", 64)
	for _, path := range []string{"/__cantelop/v1/workspaces", "/__cantelop/v1/sessions/thread/events"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"slug":"demo"}`))
		response := httptest.NewRecorder()
		runtime.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s without credential = %d", path, response.Code)
		}
		request.Header.Set(localBridgeTokenHeader, strings.Repeat("b", 64))
		response = httptest.NewRecorder()
		runtime.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s with wrong credential = %d", path, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/__cantelop/v1/workspaces", strings.NewReader(`{"slug":"demo"}`))
	request.Header.Set(localBridgeTokenHeader, runtime.bridgeToken)
	response := httptest.NewRecorder()
	runtime.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("valid bridge request = %d %s", response.Code, response.Body.String())
	}
}

func localRuntimeRequest(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestLocalRuntimeCommandsAndRepliesDuringBackgroundActivity(t *testing.T) {
	factory := &quiescenceLocalSandboxFactory{
		quiescenceStarted: make(chan struct{}), releaseQuiescence: make(chan struct{}), closed: make(chan struct{}),
	}
	runtime := newTestLocalRuntime(t, factory)
	first := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages", localMessageBody(localTestMessageID, "thread", 0, `{}`))
	if first.Code != http.StatusAccepted {
		t.Fatal(first.Body.String())
	}
	select {
	case <-factory.quiescenceStarted:
	case <-time.After(time.Second):
		t.Fatal("activity wait did not start")
	}
	secondID := "msg_fedcba9876543210fedcba9876543210"
	second := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages", localMessageBody(secondID, "thread", 0, `{"command":"cancel"}`))
	if second.Code != http.StatusAccepted {
		t.Fatal(second.Body.String())
	}
	waitLocalMessageHandled(t, runtime, "thread", secondID)
	body := localRequestBody("msg_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1000)
	reply := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/requests", body)
	if reply.Code != http.StatusOK || !strings.Contains(reply.Body.String(), `"reply":{"authenticated":true}`) {
		t.Fatalf("reply = %d %s", reply.Code, reply.Body)
	}
	select {
	case <-factory.closed:
		t.Fatal("activity Sandbox closed early")
	default:
	}
	close(factory.releaseQuiescence)
	select {
	case <-factory.closed:
	case <-time.After(time.Second):
		t.Fatal("Sandbox not released")
	}
}

func (*quiescenceLocalSandbox) ReadReply(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"authenticated":true}`), nil
}

func localRequestBody(id string, timeout int) string {
	body := localMessageBody(id, "thread", 0, `{}`)
	return strings.TrimSuffix(body, "}") + fmt.Sprintf(`,"timeout_ms":%d}`, timeout)
}

func TestLocalRuntimeRequestTimeoutDoesNotCancelDelivery(t *testing.T) {
	factory := &blockingLocalSandboxFactory{started: make(chan struct{}, 1), release: make(chan struct{}), executed: make(chan struct{}, 1)}
	runtime := newTestLocalRuntime(t, factory)
	response := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/requests", localRequestBody(localTestMessageID, 20))
	if response.Code != http.StatusGatewayTimeout || !strings.Contains(response.Body.String(), "request_wait_timeout") {
		t.Fatalf("timeout = %d %s", response.Code, response.Body)
	}
	close(factory.release)
	select {
	case <-factory.executed:
	case <-time.After(time.Second):
		t.Fatal("timeout canceled delivery")
	}
	// This fake does not support replies: a retry must return a terminal error,
	// rather than enqueueing a second execution or hanging until timeout.
	response = localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/requests", localRequestBody(localTestMessageID, 1000))
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "execution_outcome_unknown") {
		t.Fatalf("failure = %d %s", response.Code, response.Body)
	}
	select {
	case <-factory.executed:
		t.Fatal("retry executed twice")
	default:
	}
}

func TestLocalRuntimeValidatesPublicRequestsAndReplyIdentity(t *testing.T) {
	runtime := newTestLocalRuntime(t, &fakeLocalSandboxFactory{})
	for _, timeout := range []int{0, -1, 300001} {
		response := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/requests", localRequestBody(localTestMessageID, timeout))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("timeout %d: %d", timeout, response.Code)
		}
	}
	body := localMessageBody(localTestMessageID, "thread", 0, `{}`)
	response := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages", strings.TrimSuffix(body, "}")+`,"reply":true}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("public reply flag: %d", response.Code)
	}
	response = localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages", body)
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	response = localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/requests", localRequestBody(localTestMessageID, 1000))
	if response.Code != http.StatusConflict {
		t.Fatalf("reply identity conflict: %d %s", response.Code, response.Body)
	}
	response = localRuntimeRequest(runtime, http.MethodPost, messageprotocol.MessagePath, body)
	if response.Code != http.StatusNotFound {
		t.Fatalf("internal route exposed publicly: %d", response.Code)
	}
}

func TestLocalRuntimeReleaseIsIdempotentAndSessionRemainsUsable(t *testing.T) {
	factory := &fakeLocalSandboxFactory{}
	runtime := newTestLocalRuntime(t, factory)
	response := localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages", localMessageBody(localTestMessageID, "thread", 300, `{}`))
	if response.Code != 202 {
		t.Fatal(response.Body.String())
	}
	waitLocalMessageHandled(t, runtime, "thread", localTestMessageID)
	for i := 0; i < 2; i++ {
		response = localRuntimeRequest(runtime, http.MethodDelete, "/__cantelop/v1/sessions/thread", "")
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"state":"idle"`) {
			t.Fatalf("release = %d %s", response.Code, response.Body)
		}
	}
	secondID := "msg_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	response = localRuntimeRequest(runtime, http.MethodPost, "/__cantelop/v1/messages", localMessageBody(secondID, "thread", 300, `{}`))
	if response.Code != 202 {
		t.Fatal(response.Body.String())
	}
	waitLocalMessageHandled(t, runtime, "thread", secondID)
	factory.mu.Lock()
	starts := factory.starts
	factory.mu.Unlock()
	if starts != 2 {
		t.Fatalf("Sandbox starts = %d", starts)
	}
}
