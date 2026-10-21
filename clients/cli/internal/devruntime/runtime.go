package devruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/clients/cli/internal/localhttp"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
	"github.com/stepandel/cantelop/fire-fuse/workspacesync"
)

const localRuntimeMaxEnvelopeBytes = 1 << 20

const localBridgeTokenHeader = "X-Cantelop-Dev-Bridge-Token"

var (
	localSessionPathPattern   = regexp.MustCompile(`^/__cantelop/v1/sessions/([A-Za-z0-9][A-Za-z0-9._:-]{0,127})$`)
	localSessionEventsPattern = regexp.MustCompile(`^/__cantelop/v1/sessions/([A-Za-z0-9][A-Za-z0-9._:-]{0,127})/events$`)
	localMessageStatusPattern = regexp.MustCompile(`^/__cantelop/v1/sessions/([A-Za-z0-9][A-Za-z0-9._:-]{0,127})/messages/(msg_[0-9a-f]{32})$`)
)

type localSandbox interface {
	Deliver(context.Context, messageprotocol.Request) (uint64, error)
	ReadOutput(context.Context, uint64, bool) (messageprotocol.OutputBatch, error)
	AwaitQuiescence(context.Context, uint64) error
	Close(context.Context) error
}

type localSandboxFactory interface {
	Start(context.Context, string) (localSandbox, error)
}

type localRuntime struct {
	syncFiles   *workspacesync.Store
	appID       string
	bridgeToken string
	databases   *localDatabases
	sandboxes   localSandboxFactory
	now         func() time.Time
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	openMu      sync.Mutex

	mu               sync.Mutex
	workspaces       map[string]localWorkspace
	slugs            map[string]string
	sessions         map[string]*localSession
	keys             map[string]string
	messages         map[string]*localMessageRecord
	messageLanes     map[string]*localMessageLane
	persistWorkspace func(localWorkspace) error
	events           sessionevents.Store
	closed           bool
}

type localMessageRecord struct {
	request           messageprotocol.Request
	state             string
	errorCode         string
	acceptedAt        time.Time
	startedAt         *time.Time
	handledAt         *time.Time
	reply             json.RawMessage
	execution         *localExecution
	recovery          *messageprotocol.RecoveryRequest
	recoverySupported bool
	// settled is closed once state becomes "handled" or "failed".
	settled chan struct{}
}

// settle wakes waiters after record reached a final state. Callers hold
// runtime.mu.
func (record *localMessageRecord) settle() {
	select {
	case <-record.settled:
	default:
		close(record.settled)
	}
}

type localMessageLane struct {
	records []*localMessageRecord
	running bool
}

type localWorkspace struct {
	ID        string
	Slug      string
	CreatedAt time.Time
}

type localSession struct {
	mu               sync.Mutex
	id               string
	workspaceID      string
	key              string
	keepAliveSeconds int
	sandbox          localSandbox
	timer            *time.Timer
	activeMessages   int
	sandboxFailed    bool
	outputCursor     uint64
	outputContext    context.Context
	cancelOutput     context.CancelCauseFunc
	outputReady      chan struct{}
	recoveryInput    *messageprotocol.Request
}

type localSessionCreateRequest struct {
	ID               string `json:"id"`
	WorkspaceID      string `json:"workspace_id"`
	KeepAliveSeconds int    `json:"keep_alive_seconds"`
}

type localSessionOpenRequest struct {
	Key              string `json:"key"`
	WorkspaceID      string `json:"workspace_id"`
	Workspace        string `json:"workspace"`
	KeepAliveSeconds int    `json:"keep_alive_seconds"`
}

type localSessionOpenEnvelope struct {
	Key              string `json:"key"`
	WorkspaceID      string `json:"workspace_id"`
	Workspace        string `json:"workspace"`
	KeepAliveSeconds *int   `json:"keep_alive_seconds"`
}

func newLocalRuntime(appID string, sandboxes localSandboxFactory) (*localRuntime, error) {
	if !ids.ValidAppID(appID) {
		return nil, errors.New("local runtime requires a valid App ID")
	}
	if sandboxes == nil {
		return nil, errors.New("local runtime requires a Sandbox factory")
	}
	runtimeContext, cancel := context.WithCancel(context.Background())
	runtime := &localRuntime{
		appID: appID, sandboxes: sandboxes, now: time.Now,
		ctx: runtimeContext, cancel: cancel,
		workspaces: make(map[string]localWorkspace), slugs: make(map[string]string),
		sessions: make(map[string]*localSession), keys: make(map[string]string),
		messages: make(map[string]*localMessageRecord), messageLanes: make(map[string]*localMessageLane),
		events: sessionevents.NewBroker(),
	}
	return runtime, nil
}

func (runtime *localRuntime) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if runtime.databases != nil && (strings.HasPrefix(request.URL.Path, "/databases/") || request.URL.Path == "/internal/v1/runtime/database/credentials") {
		runtime.databases.ServeHTTP(response, request)
		return
	}
	if runtime.bridgeToken != "" && subtle.ConstantTimeCompare([]byte(request.Header.Get(localBridgeTokenHeader)), []byte(runtime.bridgeToken)) != 1 {
		localhttp.WriteError(response, http.StatusUnauthorized, "runtime_unauthorized")
		return
	}
	if runtime.serveWorkspaceSync(response, request) || runtime.serveIntegration(response, request) {
		return
	}
	// Match gateway routing before dispatching to the implementation. Queries
	// are supported by event streams and Workspace-scoped Message status.
	expectedMethod := ""
	if request.URL.RawQuery == "" {
		switch request.URL.Path {
		case "/__cantelop/v1/workspaces", "/__cantelop/v1/workspaces/open", "/__cantelop/v1/workspaces/database/credentials", "/__cantelop/v1/sessions", "/__cantelop/v1/sessions/open", "/__cantelop/v1/messages", "/__cantelop/v1/requests":
			expectedMethod = http.MethodPost
		default:
			if localSessionPathPattern.MatchString(request.URL.Path) {
				expectedMethod = http.MethodDelete
			}
		}
	}
	if localMessageStatusPattern.MatchString(request.URL.Path) {
		expectedMethod = http.MethodGet
	}
	if localSessionEventsPattern.MatchString(request.URL.Path) {
		expectedMethod = http.MethodGet
	}
	if expectedMethod == "" {
		localhttp.WriteError(response, http.StatusNotFound, "runtime_route_not_found")
		return
	}
	if request.Method != expectedMethod {
		response.Header().Set("Allow", expectedMethod)
		localhttp.WriteError(response, http.StatusMethodNotAllowed, "runtime_method_not_allowed")
		return
	}
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/__cantelop/v1/workspaces":
		runtime.createWorkspace(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/__cantelop/v1/workspaces/open":
		runtime.openWorkspace(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/__cantelop/v1/workspaces/database/credentials":
		runtime.databaseCredentials(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/__cantelop/v1/sessions":
		runtime.createSession(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/__cantelop/v1/sessions/open":
		runtime.openSession(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/__cantelop/v1/messages":
		runtime.dispatchMessage(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/__cantelop/v1/requests":
		runtime.sessionRequest(response, request)
	case request.Method == http.MethodGet && localMessageStatusPattern.MatchString(request.URL.Path):
		runtime.messageStatus(response, request)
	case request.Method == http.MethodGet && localSessionEventsPattern.MatchString(request.URL.Path):
		runtime.sessionEvents(response, request)
	case request.Method == http.MethodDelete && localSessionPathPattern.MatchString(request.URL.Path):
		runtime.terminateSession(response, request)
	default:
		localhttp.WriteError(response, http.StatusNotFound, "runtime_route_not_found")
	}
}

// databaseCredentials authorizes an existing local Workspace before issuing local credentials.
func (runtime *localRuntime) databaseCredentials(response http.ResponseWriter, request *http.Request) {
	var input struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if decodeLocalRequest(request, &input) != nil || !ids.ValidLocalWorkspaceID(input.WorkspaceID) {
		localhttp.WriteError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	runtime.mu.Lock()
	_, exists := runtime.workspaces[input.WorkspaceID]
	runtime.mu.Unlock()
	if runtime.databases == nil {
		localhttp.WriteError(response, http.StatusServiceUnavailable, "database_unavailable")
		return
	}
	if !exists {
		localhttp.WriteError(response, http.StatusNotFound, "workspace_not_found")
		return
	}
	credentials, err := runtime.databases.apiCredentials(input.WorkspaceID)
	if err != nil {
		localhttp.WriteError(response, http.StatusServiceUnavailable, "database_unavailable")
		return
	}
	localhttp.WriteJSON(response, http.StatusOK, credentials)
}

func (runtime *localRuntime) createWorkspace(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Slug string `json:"slug"`
	}
	if decodeLocalRequest(request, &input) != nil || !ids.ValidRoutingSlug(input.Slug) {
		localhttp.WriteError(response, http.StatusBadRequest, "invalid_workspace_request")
		return
	}
	runtime.mu.Lock()
	_, exists := runtime.slugs[input.Slug]
	runtime.mu.Unlock()
	if exists {
		localhttp.WriteError(response, http.StatusConflict, "workspace_conflict")
		return
	}
	workspace := localWorkspace{
		ID: localWorkspaceID(runtime.appID, input.Slug), Slug: input.Slug, CreatedAt: runtime.now().UTC(),
	}
	// Persist outside the runtime lock; the derived ID makes the insert idempotent.
	if runtime.persistWorkspace != nil {
		if err := runtime.persistWorkspace(workspace); err != nil {
			localhttp.WriteError(response, 503, "workspace_storage_unavailable")
			return
		}
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if _, exists := runtime.slugs[input.Slug]; exists {
		localhttp.WriteError(response, http.StatusConflict, "workspace_conflict")
		return
	}
	runtime.workspaces[workspace.ID] = workspace
	runtime.slugs[workspace.Slug] = workspace.ID
	localhttp.WriteJSON(response, http.StatusCreated, runtime.workspaceResponse(workspace))
}

func (runtime *localRuntime) openWorkspace(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Slug string `json:"slug"`
	}
	if decodeLocalRequest(request, &input) != nil || !ids.ValidRoutingSlug(input.Slug) {
		localhttp.WriteError(response, http.StatusBadRequest, "invalid_workspace_request")
		return
	}
	runtime.mu.Lock()
	workspaceID, exists := runtime.slugs[input.Slug]
	runtime.mu.Unlock()
	if !exists {
		workspaceID = localWorkspaceID(runtime.appID, input.Slug)
		workspace := localWorkspace{ID: workspaceID, Slug: input.Slug, CreatedAt: runtime.now().UTC()}
		// Persist outside the runtime lock; the derived ID makes the insert idempotent.
		if runtime.persistWorkspace != nil {
			if err := runtime.persistWorkspace(workspace); err != nil {
				localhttp.WriteError(response, 503, "workspace_storage_unavailable")
				return
			}
		}
		runtime.mu.Lock()
		if _, exists := runtime.slugs[input.Slug]; !exists {
			runtime.workspaces[workspace.ID] = workspace
			runtime.slugs[workspace.Slug] = workspace.ID
		}
	} else {
		runtime.mu.Lock()
	}
	workspace := runtime.workspaces[workspaceID]
	runtime.mu.Unlock()
	localhttp.WriteJSON(response, http.StatusOK, runtime.workspaceResponse(workspace))
}

func (runtime *localRuntime) createSession(response http.ResponseWriter, request *http.Request) {
	var input localSessionCreateRequest
	if decodeLocalRequest(request, &input) != nil || !validLocalSessionCreate(input) {
		localhttp.WriteError(response, http.StatusBadRequest, "invalid_session_request")
		return
	}
	session := &localSession{
		id: input.ID, workspaceID: input.WorkspaceID, keepAliveSeconds: input.KeepAliveSeconds,
	}
	if err := runtime.addSession(request.Context(), session); err != nil {
		writeLocalSessionError(response, err)
		return
	}
	localhttp.WriteJSON(response, http.StatusAccepted, localSessionResponse(session))
}

func (runtime *localRuntime) openSession(response http.ResponseWriter, request *http.Request) {
	var envelope localSessionOpenEnvelope
	if decodeLocalRequest(request, &envelope) != nil || !validLocalSessionOpen(envelope) {
		localhttp.WriteError(response, http.StatusBadRequest, "invalid_session_request")
		return
	}
	input := localSessionOpenRequest{
		Key: envelope.Key, WorkspaceID: envelope.WorkspaceID, Workspace: envelope.Workspace,
		KeepAliveSeconds: *envelope.KeepAliveSeconds,
	}
	if input.Workspace != "" {
		workspaceID, ok := runtime.resolveLocalWorkspace(input.Workspace)
		if !ok {
			localhttp.WriteError(response, http.StatusNotFound, "resource_not_found")
			return
		}
		input.WorkspaceID, input.Workspace = workspaceID, ""
	}
	session, err := runtime.openLocalSession(request.Context(), input)
	if err != nil {
		writeLocalSessionError(response, err)
		return
	}
	localhttp.WriteJSON(response, http.StatusOK, localSessionResponse(session))
}

func (runtime *localRuntime) openLocalSession(ctx context.Context, input localSessionOpenRequest) (*localSession, error) {
	runtime.openMu.Lock()
	defer runtime.openMu.Unlock()
	key := input.WorkspaceID + "\x00" + input.Key
	runtime.mu.Lock()
	if existingID, exists := runtime.keys[key]; exists {
		session := runtime.sessions[existingID]
		runtime.mu.Unlock()
		if session == nil {
			return nil, errLocalRuntimeUnavailable
		}
		session.mu.Lock()
		matches := session.workspaceID == input.WorkspaceID && session.key == input.Key &&
			session.keepAliveSeconds == input.KeepAliveSeconds
		session.mu.Unlock()
		if !matches {
			return nil, errLocalSessionConflict
		}
		return session, nil
	}
	runtime.mu.Unlock()

	session := &localSession{
		id: localID("ses_"), workspaceID: input.WorkspaceID, key: input.Key,
		keepAliveSeconds: input.KeepAliveSeconds,
	}
	if err := runtime.addSession(ctx, session); err != nil {
		return nil, err
	}
	runtime.mu.Lock()
	runtime.keys[key] = session.id
	runtime.mu.Unlock()
	return session, nil
}

func (runtime *localRuntime) dispatchMessage(response http.ResponseWriter, request *http.Request) {
	var input messageprotocol.Request
	if decodeLocalRequest(request, &input) != nil || input.Validate() != nil || input.Reply {
		localhttp.WriteError(response, http.StatusBadRequest, "invalid_message_request")
		return
	}
	record, code, err := runtime.admitMessage(input)
	if err != nil {
		localhttp.WriteError(response, code, err.Error())
		return
	}
	writeLocalMessageReceipt(response, record)
}

func (runtime *localRuntime) admitMessage(input messageprotocol.Request) (*localMessageRecord, int, error) {
	runtime.mu.Lock()
	existing := runtime.messages[input.Message.ID]
	if existing != nil {
		matches := existing.request.Session == input.Session && existing.request.Reply == input.Reply &&
			existing.request.Message.ID == input.Message.ID &&
			string(existing.request.Message.Payload) == string(input.Message.Payload)
		runtime.mu.Unlock()
		if !matches {
			return nil, http.StatusConflict, errors.New("message_conflict")
		}
		return existing, 0, nil
	}
	if runtime.closed {
		runtime.mu.Unlock()
		return nil, http.StatusServiceUnavailable, errors.New("runtime_unavailable")
	}
	record := &localMessageRecord{request: input, state: "accepted", acceptedAt: runtime.now().UTC(), settled: make(chan struct{})}
	runtime.messages[input.Message.ID] = record
	lane := runtime.messageLanes[input.Session.ID]
	if lane == nil {
		lane = &localMessageLane{}
		runtime.messageLanes[input.Session.ID] = lane
	}
	lane.records = append(lane.records, record)
	startLane := !lane.running
	if startLane {
		lane.running = true
		runtime.wg.Add(1)
	}
	runtime.mu.Unlock()

	if startLane {
		go runtime.runMessageLane(lane)
	}
	return record, 0, nil
}

func (runtime *localRuntime) runMessageLane(lane *localMessageLane) {
	defer runtime.wg.Done()
	for {
		runtime.mu.Lock()
		if len(lane.records) == 0 {
			lane.running = false
			runtime.mu.Unlock()
			return
		}
		record := lane.records[0]
		lane.records = lane.records[1:]
		runtime.wg.Add(1)
		runtime.mu.Unlock()
		acknowledged, done := make(chan struct{}), make(chan struct{})
		var once sync.Once
		deliveryContext := context.WithValue(runtime.ctx, localAcknowledgmentKey{}, func() { once.Do(func() { close(acknowledged) }) })
		go func() {
			defer runtime.wg.Done()
			defer close(done)
			input := record.request
			session, err := runtime.openLogicalLocalSession(deliveryContext, input.Session)
			if err == nil {
				err = runtime.runSessionMessage(deliveryContext, session, input, record)
			}
			if err != nil {
				runtime.mu.Lock()
				if record.state != "handled" {
					now := runtime.now().UTC()
					record.state, record.errorCode, record.handledAt = "failed", localFailureCode(err), &now
					record.settle()
				}
				runtime.mu.Unlock()
			}
		}()
		// Uniform handoff: allow the next envelope after actor acknowledgment,
		// independently of the receiver's work completion.
		select {
		case <-acknowledged:
		case <-done:
		case <-runtime.ctx.Done():
			return
		}

	}
}

func writeLocalMessageReceipt(response http.ResponseWriter, record *localMessageRecord) {
	localhttp.WriteJSON(response, http.StatusAccepted, map[string]any{
		"id": record.request.Message.ID, "status": "accepted", "accepted_at": record.acceptedAt,
	})
}

func (runtime *localRuntime) messageStatus(response http.ResponseWriter, request *http.Request) {
	match := localMessageStatusPattern.FindStringSubmatch(request.URL.Path)
	if len(match) != 3 {
		localhttp.WriteError(response, http.StatusNotFound, "message_not_found")
		return
	}
	values := request.URL.Query()
	scope := values.Get("workspace_id")
	if request.URL.RawQuery != "" && (len(values) != 1 || len(values["workspace_id"]) != 1 || (messageprotocol.SessionEnvelope{ID: match[1], WorkspaceID: scope}).Validate() != nil) {
		localhttp.WriteError(response, 400, "invalid_workspace_request")
		return
	}
	runtime.mu.Lock()
	record := runtime.messages[match[2]]
	if record == nil || record.request.Session.ID != match[1] || (scope != "" && record.request.Session.WorkspaceID != scope) {
		runtime.mu.Unlock()
		localhttp.WriteJSON(response, http.StatusOK, map[string]any{"id": match[2], "state": "unknown"})
		return
	}
	state, acceptedAt, startedAt, handledAt, errorCode := record.state, record.acceptedAt, record.startedAt, record.handledAt, record.errorCode
	execution := record.execution
	runtime.mu.Unlock()
	value := map[string]any{"id": match[2], "workspace_id": record.request.Session.WorkspaceID, "state": state, "accepted_at": acceptedAt}
	if execution != nil {
		value["execution"] = execution
	}
	if startedAt != nil {
		value["started_at"] = startedAt
	}
	if handledAt != nil {
		if state == "failed" {
			value["failed_at"], value["error"] = handledAt, map[string]string{"code": errorCode}
		} else {
			value["handled_at"] = handledAt
		}
	}
	localhttp.WriteJSON(response, http.StatusOK, value)
}

func (runtime *localRuntime) runSessionMessage(
	ctx context.Context,
	session *localSession,
	input messageprotocol.Request,
	record *localMessageRecord,
) error {
	session.mu.Lock()
	if session.timer != nil {
		session.timer.Stop()
		session.timer = nil
	}
	if session.sandbox == nil {
		sandbox, err := runtime.sandboxes.Start(ctx, session.workspaceID)
		if err != nil {
			session.mu.Unlock()
			return fmt.Errorf("%w: %v", errLocalSandboxStartFailed, err)
		}
		session.sandbox = sandbox
		session.outputCursor = 0
		session.sandboxFailed = false
		session.recoveryInput = nil
	}
	sandbox := session.sandbox
	session.activeMessages++
	if session.cancelOutput == nil {
		session.outputContext, session.cancelOutput = context.WithCancelCause(ctx)
		session.outputReady = make(chan struct{})
		outputContext, cancelOutput, ready := session.outputContext, session.cancelOutput, session.outputReady
		runtime.wg.Add(1)
		go func() {
			defer runtime.wg.Done()
			var once sync.Once
			markReady := func() { once.Do(func() { close(ready) }) }
			err := runtime.collectLocalOutput(outputContext, session, sandbox, input.Session, markReady)
			if err != nil {
				cancelOutput(err)
			}
			markReady()
		}()
	}
	outputContext, ready := session.outputContext, session.outputReady
	session.mu.Unlock()
	runtime.mu.Lock()
	startedAt := runtime.now().UTC()
	record.state, record.startedAt = "handling", &startedAt
	runtime.mu.Unlock()
	<-ready
	deliveryContext := context.WithValue(runtime.observeLocalExecution(outputContext, record), localAcknowledgmentKey{}, ctx.Value(localAcknowledgmentKey{}))
	var generation uint64
	err := context.Cause(outputContext)
	if err == nil {
		if record.recovery != nil {
			if recoverer, ok := sandbox.(interface {
				Recover(context.Context, messageprotocol.RecoveryRequest) (uint64, error)
			}); ok {
				generation, err = recoverer.Recover(deliveryContext, *record.recovery)
			} else {
				err = errors.New("local recovery unsupported")
			}
		} else {
			generation, err = sandbox.Deliver(deliveryContext, input)
		}
	}
	var reply json.RawMessage
	if err == nil && input.Reply && record.recovery == nil {
		if reader, ok := sandbox.(interface {
			ReadReply(context.Context, string) (json.RawMessage, error)
		}); ok {
			replyContext := outputContext
			cancelReply := func() {}
			runtime.mu.Lock()
			if record.execution != nil {
				replyContext, cancelReply = context.WithDeadline(outputContext, record.execution.Deadline.Add(10*time.Second))
			}
			runtime.mu.Unlock()
			reply, err = reader.ReadReply(replyContext, input.Message.ID)
			cancelReply()
		} else {
			err = errors.New("local Sandbox does not support replies")
		}
	}
	// A handler failure settles this command, but does not cancel an activity
	// started by another command. Only loss of runtime evidence retires it.
	terminalFailure := err != nil && generation > 0
	if err != nil && !terminalFailure {
		runtime.failLocalExecution(record, err)
		runtime.finishSessionWork(session, sandbox, record, err)
		return err
	}
	runtime.mu.Lock()
	handledAt := runtime.now().UTC()
	record.state, record.handledAt, record.reply = "handled", &handledAt, reply
	if terminalFailure {
		record.state, record.errorCode = "failed", localFailureCode(err)
	}
	record.settle()
	runtime.mu.Unlock()
	// Handler completion advances the command lane. Activity only holds the
	// Sandbox alive; it must not block login input, cancellation, or steering.
	runtime.wg.Add(1)
	go func() {
		defer runtime.wg.Done()
		err := sandbox.AwaitQuiescence(deliveryContext, generation)
		if err != nil {
			runtime.failLocalExecution(record, err)
		}
		runtime.finishSessionWork(session, sandbox, record, err)
	}()
	return nil
}

func (runtime *localRuntime) finishSessionWork(session *localSession, sandbox localSandbox, record *localMessageRecord, err error) {
	runtime.mu.Lock()
	recoverySupported := record.recoverySupported
	runtime.mu.Unlock()
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.sandbox != sandbox {
		return
	}
	// A recovery is never itself recovered, so a crash-looping agent cannot
	// requeue recoveries forever ahead of ordinary commands.
	if recoverySupported && record.recovery == nil {
		input := record.request
		session.recoveryInput = &input
	}
	session.activeMessages--
	if err != nil {
		session.sandboxFailed = true
	}
	if session.activeMessages == 0 {
		if session.sandboxFailed {
			lost := false
			if runtime.ctx.Err() == nil && session.recoveryInput != nil {
				if process, ok := sandbox.(interface{ Lost() bool }); ok {
					lost = process.Lost()
				}
			}
			runtime.stopSessionOutput(session)
			_ = sandbox.Close(context.Background())
			session.sandbox = nil
			if lost {
				runtime.enqueueLocalRecovery(*session.recoveryInput)
			}
		} else {
			runtime.scheduleSandboxRelease(session)
		}
	}
}

// Called with session.mu held whenever the current Sandbox is retired.
func (runtime *localRuntime) stopSessionOutput(session *localSession) {
	if session.cancelOutput != nil {
		session.cancelOutput(nil)
	}
	session.cancelOutput = nil
	session.activeMessages = 0
}

func (runtime *localRuntime) collectLocalOutput(
	ctx context.Context,
	session *localSession,
	sandbox localSandbox,
	envelope messageprotocol.SessionEnvelope,
	markReady func(),
) error {
	session.mu.Lock()
	after := session.outputCursor
	session.mu.Unlock()
	sourceID := ""
	if identified, ok := sandbox.(interface{ Identity() string }); ok {
		sourceID = identified.Identity()
	}
	initial := true
	for {
		batch, err := sandbox.ReadOutput(ctx, after, !initial)
		if err != nil {
			return err
		}
		for _, event := range batch.Events {
			session.mu.Lock()
			if session.sandbox != sandbox || ctx.Err() != nil {
				session.mu.Unlock()
				return context.Canceled
			}
			if _, err := runtime.events.Append(sessionevents.Append{
				Key:  sessionevents.Key{WorkspaceID: envelope.WorkspaceID, SessionID: envelope.ID},
				View: event.View, MessageID: event.MessageID, Data: event.Data, SourceSandboxID: sourceID, SourceCursor: event.Cursor,
			}); err != nil {
				session.mu.Unlock()
				return err
			}
			after = event.Cursor
			session.outputCursor = after
			session.mu.Unlock()
		}
		if initial {
			initial = false
			markReady()
		}
	}
}

func (runtime *localRuntime) openLogicalLocalSession(
	ctx context.Context, input messageprotocol.SessionEnvelope,
) (*localSession, error) {
	runtime.openMu.Lock()
	defer runtime.openMu.Unlock()
	id := localLogicalSessionID(runtime.appID, input.ID)
	runtime.mu.Lock()
	existing := runtime.sessions[id]
	runtime.mu.Unlock()
	if existing != nil {
		existing.mu.Lock()
		matches := existing.workspaceID == input.WorkspaceID && existing.key == input.ID &&
			existing.keepAliveSeconds == input.KeepAliveSeconds
		existing.mu.Unlock()
		if !matches {
			return nil, errLocalSessionConflict
		}
		return existing, nil
	}
	session := &localSession{
		id: id, workspaceID: input.WorkspaceID, key: input.ID,
		keepAliveSeconds: input.KeepAliveSeconds,
	}
	if err := runtime.addSession(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

func (runtime *localRuntime) addSession(ctx context.Context, session *localSession) error {
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		return errLocalRuntimeUnavailable
	}
	if _, exists := runtime.sessions[session.id]; exists {
		runtime.mu.Unlock()
		return errLocalSessionConflict
	}
	// Production Workspace IDs embedded in API source remain usable locally.
	// Lazily recording one keeps the local bridge scoped to the configured App.
	if _, exists := runtime.workspaces[session.workspaceID]; !exists {
		runtime.workspaces[session.workspaceID] = localWorkspace{
			ID: session.workspaceID, Slug: "local", CreatedAt: runtime.now().UTC(),
		}
	}
	runtime.sessions[session.id] = session
	runtime.mu.Unlock()

	sandbox, err := runtime.sandboxes.Start(ctx, session.workspaceID)
	if err != nil {
		runtime.mu.Lock()
		delete(runtime.sessions, session.id)
		runtime.mu.Unlock()
		return fmt.Errorf("%w: %v", errLocalRuntimeUnavailable, err)
	}
	session.mu.Lock()
	session.sandbox = sandbox
	session.mu.Unlock()
	return nil
}

func (runtime *localRuntime) scheduleSandboxRelease(session *localSession) {
	if session.sandbox == nil {
		return
	}
	if session.keepAliveSeconds == 0 {
		runtime.stopSessionOutput(session)
		_ = session.sandbox.Close(context.Background())
		session.sandbox = nil
		return
	}
	var timer *time.Timer
	timer = time.AfterFunc(time.Duration(session.keepAliveSeconds)*time.Second, func() {
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.timer != timer {
			return
		}
		if session.sandbox != nil && session.activeMessages == 0 {
			runtime.stopSessionOutput(session)
			_ = session.sandbox.Close(context.Background())
			session.sandbox = nil
		}
		session.timer = nil
	})
	session.timer = timer
}

func (runtime *localRuntime) terminateSession(response http.ResponseWriter, request *http.Request) {
	match := localSessionPathPattern.FindStringSubmatch(request.URL.Path)
	if len(match) != 2 {
		localhttp.WriteError(response, http.StatusNotFound, "session_not_found")
		return
	}
	session := runtime.session(localLogicalSessionID(runtime.appID, match[1]))
	if session == nil && ids.ValidSessionID(match[1]) {
		session = runtime.session(match[1])
	}
	if session == nil {
		localhttp.WriteError(response, http.StatusNotFound, "session_not_found")
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.timer != nil {
		session.timer.Stop()
		session.timer = nil
	}
	if session.sandbox != nil {
		runtime.stopSessionOutput(session)
		_ = session.sandbox.Close(request.Context())
		session.sandbox = nil
	}
	localhttp.WriteJSON(response, http.StatusOK, map[string]any{"id": session.id, "workspace_id": session.workspaceID, "keep_alive_seconds": session.keepAliveSeconds, "state": "idle"})
}

func (runtime *localRuntime) Close() error {
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		return nil
	}
	runtime.closed = true
	runtime.cancel()
	sessions := make([]*localSession, 0, len(runtime.sessions))
	for _, session := range runtime.sessions {
		sessions = append(sessions, session)
	}
	runtime.mu.Unlock()
	runtime.wg.Wait()
	var closeError error
	for _, session := range sessions {
		session.mu.Lock()
		if session.timer != nil {
			session.timer.Stop()
			session.timer = nil
		}
		if session.sandbox != nil {
			runtime.stopSessionOutput(session)
			closeError = errors.Join(closeError, session.sandbox.Close(context.Background()))
			session.sandbox = nil
		}
		session.mu.Unlock()
	}
	return closeError
}

// ReleaseSandboxes retires the currently loaded Session runtime processes without
// terminating their logical Sessions. The next Message starts a fresh
// process against the rebuilt Session runtime while retaining Workspace data.
func (runtime *localRuntime) ReleaseSandboxes() error {
	runtime.mu.Lock()
	sessions := make([]*localSession, 0, len(runtime.sessions))
	for _, session := range runtime.sessions {
		sessions = append(sessions, session)
	}
	runtime.mu.Unlock()

	var closeError error
	for _, session := range sessions {
		session.mu.Lock()
		if session.timer != nil {
			session.timer.Stop()
			session.timer = nil
		}
		if session.sandbox != nil {
			runtime.stopSessionOutput(session)
			closeError = errors.Join(closeError, session.sandbox.Close(context.Background()))
			session.sandbox = nil
		}
		session.mu.Unlock()
	}
	return closeError
}

func (runtime *localRuntime) session(id string) *localSession {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.sessions[id]
}

func (runtime *localRuntime) resolveLocalWorkspace(selector string) (string, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	id, exists := runtime.slugs[selector]
	return id, exists
}

func (runtime *localRuntime) workspaceResponse(workspace localWorkspace) map[string]any {
	return map[string]any{
		"id": workspace.ID, "app_id": runtime.appID, "slug": workspace.Slug,
		"hostname": workspace.Slug + ".localhost", "created_at": workspace.CreatedAt,
		"updated_at": workspace.CreatedAt, "archived_at": nil,
	}
}

func validLocalSessionCreate(input localSessionCreateRequest) bool {
	return ids.ValidSessionID(input.ID) && ids.ValidLocalWorkspaceID(input.WorkspaceID) &&
		input.KeepAliveSeconds >= 0 && input.KeepAliveSeconds <= 604800
}

func validLocalSessionOpen(input localSessionOpenEnvelope) bool {
	workspaceIDPresent := input.WorkspaceID != ""
	workspaceSlugPresent := input.Workspace != ""
	workspaceValid := workspaceIDPresent != workspaceSlugPresent &&
		(!workspaceIDPresent || ids.ValidLocalWorkspaceID(input.WorkspaceID)) &&
		(!workspaceSlugPresent || ids.ValidRoutingSlug(input.Workspace))
	return ids.LocalSessionKeyPattern.MatchString(input.Key) && workspaceValid &&
		input.KeepAliveSeconds != nil && *input.KeepAliveSeconds >= 0 &&
		*input.KeepAliveSeconds <= 604800
}

func decodeLocalRequest(request *http.Request, target any) error {
	limit := int64(localRuntimeMaxEnvelopeBytes)
	switch request.URL.Path {
	case "/__cantelop/v1/workspaces", "/__cantelop/v1/workspaces/open", "/__cantelop/v1/workspaces/database/credentials", "/__cantelop/v1/sessions", "/__cantelop/v1/sessions/open":
		limit = 4 << 10
	}
	document, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil || int64(len(document)) > limit {
		return errors.New("request body exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func localID(prefix string) string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return prefix + hex.EncodeToString(value[:])
}

func localWorkspaceID(appID, slug string) string {
	value := sha256.Sum256([]byte(appID + "\x00" + slug))
	return "wsp_" + hex.EncodeToString(value[:16])
}

func localLogicalSessionID(appID, logicalID string) string {
	value := sha256.Sum256([]byte(appID + "\x00" + logicalID))
	return "ses_" + hex.EncodeToString(value[:16])
}

var (
	errLocalSessionConflict    = errors.New("local Session conflicts with an existing Session")
	errLocalSandboxStartFailed = errors.New("local Sandbox failed to start")
	errLocalRuntimeUnavailable = errors.New("local Sandbox runtime is unavailable")
)

func writeLocalSessionError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errLocalSessionConflict):
		localhttp.WriteError(response, http.StatusConflict, "session_conflict")
	case errors.Is(err, errLocalRuntimeUnavailable):
		localhttp.WriteError(response, http.StatusServiceUnavailable, "runtime_unavailable")
	default:
		localhttp.WriteError(response, http.StatusInternalServerError, "runtime_unavailable")
	}
}

func (runtime *localRuntime) sessionRequest(response http.ResponseWriter, request *http.Request) {
	var envelope struct {
		messageprotocol.Request
		TimeoutMS int `json:"timeout_ms"`
	}
	if decodeLocalRequest(request, &envelope) != nil || envelope.Request.Validate() != nil || envelope.Reply || envelope.TimeoutMS < 1 || envelope.TimeoutMS > 300_000 {
		localhttp.WriteError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	envelope.Reply = true
	record, status, err := runtime.admitMessage(envelope.Request)
	if err != nil {
		localhttp.WriteError(response, status, err.Error())
		return
	}
	deadline := time.NewTimer(time.Duration(envelope.TimeoutMS) * time.Millisecond)
	defer deadline.Stop()
	for {
		runtime.mu.Lock()
		state, reply, code := record.state, record.reply, record.errorCode
		runtime.mu.Unlock()
		if state == "handled" {
			if len(reply) == 0 {
				localhttp.WriteError(response, http.StatusGone, "request_reply_expired")
				return
			}
			localhttp.WriteJSON(response, http.StatusOK, map[string]any{"id": record.request.Message.ID, "reply": reply})
			return
		}
		if state == "failed" {
			localhttp.WriteError(response, http.StatusUnprocessableEntity, code)
			return
		}
		select {
		case <-request.Context().Done():
			return
		case <-runtime.ctx.Done():
			localhttp.WriteError(response, http.StatusServiceUnavailable, "runtime_unavailable")
			return
		case <-deadline.C:
			localhttp.WriteError(response, http.StatusGatewayTimeout, "request_wait_timeout")
			return
		case <-record.settled:
		}
	}
}

func localFailureCode(err error) string {
	var failure localMessageFailure
	if errors.As(err, &failure) {
		return string(failure)
	}
	return "execution_outcome_unknown"
}

// Recovery is a new operation ahead of ordinary queued commands, never a
// replay of the lost handler. Like message state, its identity is process-local.
func (runtime *localRuntime) enqueueLocalRecovery(input messageprotocol.Request) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed {
		return
	}
	recovery := messageprotocol.RecoveryRequest{RecoveryID: localID("rcv_"), MessageID: input.Message.ID, Session: input.Session}
	record := &localMessageRecord{request: input, state: "accepted", acceptedAt: runtime.now().UTC(), recovery: &recovery, settled: make(chan struct{})}
	lane := runtime.messageLanes[input.Session.ID]
	if lane == nil {
		lane = &localMessageLane{}
		runtime.messageLanes[input.Session.ID] = lane
	}
	lane.records = append([]*localMessageRecord{record}, lane.records...)
	if !lane.running {
		lane.running = true
		runtime.wg.Add(1)
		go runtime.runMessageLane(lane)
	}
}

func localSessionResponse(session *localSession) map[string]any {
	session.mu.Lock()
	defer session.mu.Unlock()
	state := "idle"
	if session.sandbox != nil {
		state = "active"
	}
	return map[string]any{"id": session.id, "workspace_id": session.workspaceID, "keep_alive_seconds": session.keepAliveSeconds, "state": state}
}
