package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/dispatcher"
	"github.com/stepandel/cantelop/fire-fuse/messagedelivery"
	"github.com/stepandel/cantelop/fire-fuse/observability"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/configcipher"
	"github.com/stepandel/cantelop/fire-fuse/sessionbroker"
	"github.com/stepandel/cantelop/fire-fuse/sessionoutput"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// This qualification exercises the actual adjacent SDK, HTTP control API,
// SQLite recovery, dispatcher and output broker. Unit tests do not substitute
// an old handler-completion response for the new mailbox ACK.
func TestSDKMailboxACKLossRecoveryAndOutputHandoff(t *testing.T) { qualifySDKMailbox(t, false, 1) }
func TestSDKMailboxDispatcherRestartAndSandboxReplacement(t *testing.T) {
	qualifySDKMailbox(t, true, 1)
}
func TestSDKSessionOutputStreamsWithoutReconciliationPacing(t *testing.T) {
	qualifySDKMailbox(t, false, 5)
}

func TestSDKSequentialRequestsWithLiveActivity(t *testing.T) {
	sdk := os.Getenv("CANTELOP_SDK_RUNTIME_TEST_MODULE")
	if sdk == "" {
		sdk = "../../../../cantelop-sdk/dist/runtime.js"
	}
	sdk, err := filepath.Abs(sdk)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sdk); err != nil {
		if os.Getenv("CANTELOP_SDK_RUNTIME_TEST_MODULE") != "" {
			t.Fatal(err)
		}
		t.Skip("build adjacent SDK or set CANTELOP_SDK_RUNTIME_TEST_MODULE")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node required for SDK qualification")
	}
	store, err := microvm.OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"), microvm.WithConfigEncryptionKey(bytes.Repeat([]byte{11}, configcipher.KeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := &fakeRuntime{running: make(map[string]bool)}
	server := httptest.NewServer(newLifecycleHandlerWithStore(store, runtime))
	defer server.Close()
	control, err := controlclient.New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	bridge := &sdkTestBridge{t: t, module: sdk, origins: map[string]string{}, ackLost: make(chan struct{})}
	worker, err := dispatcher.NewExecutionDispatcher(control, messagedelivery.NewMailboxClient(bridge), sessionoutput.NewCollector(bridge), nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	// A durable launch command owns the long-running Claude-like activity.
	// Subsequent replay requests settle while the activity keeps its Sandbox.
	logicalID := "agent:sequential"
	if _, err := control.EnqueueMessageDelivery(ctx, controlclient.MessageDelivery{
		ID: lifecycleMessageID, WorkspaceID: lifecycleWorkspace, LogicalSessionID: logicalID,
		KeepAliveSeconds: 600, Payload: []byte(`{"crash":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	if !waitUntil(5*time.Second, func() bool {
		m, err := control.GetMessageDelivery(ctx, lifecycleMessageID)
		return err == nil && m.State == "handled"
	}) {
		t.Fatal("long-running activity did not start")
	}
	const count = 67
	const replyBytes = 48 * 1024
	durations := make([]time.Duration, 0, count)
	started := time.Now()
	for i := range count {
		requestStarted := time.Now()
		id := fmt.Sprintf("msg_%032x", i+1)
		if _, err := control.EnqueueMessageDelivery(ctx, controlclient.MessageDelivery{
			ID: id, WorkspaceID: lifecycleWorkspace, LogicalSessionID: logicalID,
			KeepAliveSeconds: 600, Payload: []byte(fmt.Sprintf(`{"reply":true,"value":%d,"replyBytes":%d}`, i, replyBytes)), ReplyRequested: true,
		}); err != nil {
			t.Fatal(err)
		}
		for {
			result, err := control.WaitForMessageReply(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if result.State == "failed" {
				t.Fatalf("request failed: %s", result.ErrorCode)
			}
			if result.State != "handled" {
				continue
			}
			var reply struct {
				Value int
				Data  string
			}
			if err := json.Unmarshal(result.Reply, &reply); err != nil || reply.Value != i || reply.Data != strings.Repeat("x", replyBytes) {
				t.Fatalf("request %d returned an incorrect replay page: %v", i, err)
			}
			break
		}
		durations = append(durations, time.Since(requestStarted))
	}
	elapsed := time.Since(started)
	slices.Sort(durations)
	t.Logf("%d sequential %d-KiB SDK replies with a live activity: total=%s median=%s max=%s (local HTTP, SQLite and SDK; excludes public edge)", count, replyBytes/1024, elapsed, durations[count/2], durations[count-1])
	if runtime.startCount() != 1 || runtime.stopCount() != 0 {
		t.Fatal("sequential requests did not retain the activity's Sandbox")
	}
}

func TestSDKRequestsUpdateLogicalSessionKeepAlive(t *testing.T) {
	sdk := os.Getenv("CANTELOP_SDK_RUNTIME_TEST_MODULE")
	if sdk == "" {
		sdk = "../../../../cantelop-sdk/dist/runtime.js"
	}
	sdk, err := filepath.Abs(sdk)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(sdk); err != nil {
		if os.Getenv("CANTELOP_SDK_RUNTIME_TEST_MODULE") != "" {
			t.Fatalf("configured SDK module unavailable: %v", err)
		}
		t.Skip("build adjacent SDK or set CANTELOP_SDK_RUNTIME_TEST_MODULE to qualify actual runtime")
	}
	if _, err = exec.LookPath("node"); err != nil {
		t.Skip("Node required for SDK qualification")
	}
	runtime := &fakeRuntime{running: make(map[string]bool)}
	store, err := microvm.OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"), microvm.WithConfigEncryptionKey(bytes.Repeat([]byte{11}, configcipher.KeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := httptest.NewServer(newLifecycleHandlerWithStore(store, runtime))
	defer server.Close()
	control, err := controlclient.New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	bridge := &sdkTestBridge{t: t, module: sdk, origins: map[string]string{}, ackLost: make(chan struct{})}
	worker, err := dispatcher.NewExecutionDispatcher(control, messagedelivery.NewMailboxClient(bridge), sessionoutput.NewCollector(bridge), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	logicalID := "agent:keepalive"
	requests := []struct {
		id        string
		keepAlive int
		value     int
	}{
		{id: lifecycleMessageID, keepAlive: 900, value: 1},
		{id: "msg_11111111111111111111111111111111", keepAlive: 0, value: 2},
	}
	for _, request := range requests {
		if _, err := control.EnqueueMessageDelivery(context.Background(), controlclient.MessageDelivery{
			ID: request.id, WorkspaceID: lifecycleWorkspace, LogicalSessionID: logicalID,
			KeepAliveSeconds: request.keepAlive, Payload: []byte(fmt.Sprintf(`{"reply":true,"value":%d}`, request.value)),
			ReplyRequested: true,
		}); err != nil {
			t.Fatal(err)
		}
		var result controlclient.MessageDelivery
		if !waitUntil(12*time.Second, func() bool {
			result, err = control.GetMessageDelivery(context.Background(), request.id)
			return err == nil && result.WorkState == "quiescent"
		}) {
			t.Fatalf("request %d did not become quiescent: %#v %v", request.value, result, err)
		}
		wantReply := fmt.Sprintf(`{"value":%d}`, request.value)
		if result.State != "handled" || string(result.Reply) != wantReply {
			t.Fatalf("request %d result = %#v; want reply %s", request.value, result, wantReply)
		}
	}

	concreteID := sessionbroker.LogicalID("app-test", logicalID)
	if !waitUntil(2*time.Second, func() bool {
		sessionRecord, getErr := control.GetSession(context.Background(), concreteID)
		return getErr == nil && sessionRecord.KeepAliveSeconds == 0 && sessionRecord.State == "idle"
	}) {
		sessionRecord, getErr := control.GetSession(context.Background(), concreteID)
		t.Fatalf("updated Session did not release: %#v %v", sessionRecord, getErr)
	}
	if runtime.startCount() != 1 || runtime.stopCount() != 1 {
		t.Fatalf("Sandbox starts/stops = %d/%d, want reuse then release", runtime.startCount(), runtime.stopCount())
	}
}

func TestSDKSessionRecoveryRunsOnReplacementSandbox(t *testing.T) {
	sdk := os.Getenv("CANTELOP_SDK_RUNTIME_TEST_MODULE")
	if sdk == "" {
		sdk = "../../../../cantelop-sdk/dist/runtime.js"
	}
	sdk, err := filepath.Abs(sdk)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(sdk); err != nil {
		t.Skip("build adjacent SDK or set CANTELOP_SDK_RUNTIME_TEST_MODULE")
	}
	runtime := &fakeRuntime{running: make(map[string]bool)}
	store, err := microvm.OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"), microvm.WithConfigEncryptionKey(bytes.Repeat([]byte{11}, configcipher.KeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := httptest.NewServer(newLifecycleHandlerWithStore(store, runtime))
	defer server.Close()
	control, err := controlclient.New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	bridge := &sdkTestBridge{t: t, module: sdk, outputCount: 1, origins: map[string]string{}, ackLost: make(chan struct{})}
	if _, err = control.EnqueueMessageDelivery(context.Background(), controlclient.MessageDelivery{
		ID: lifecycleMessageID, WorkspaceID: lifecycleWorkspace, LogicalSessionID: "agent:recovery",
		KeepAliveSeconds: 600, Payload: []byte(`{"crash":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := dispatcher.NewExecutionDispatcher(control, messagedelivery.NewMailboxClient(bridge), sessionoutput.NewCollector(bridge), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	var oldSandbox string
	if !waitUntil(8*time.Second, func() bool {
		executions, listErr := control.ListExecutions(context.Background())
		if listErr == nil && len(executions) == 1 && executions[0].RecoverySupported && executions[0].Phase == "settled" {
			oldSandbox = executions[0].SandboxID
			return true
		}
		return false
	}) {
		t.Fatal("recoverable activity did not start")
	}
	runtime.mu.Lock()
	delete(runtime.running, oldSandbox)
	runtime.mu.Unlock()
	if _, err = store.Fail(oldSandbox, microvm.Failure{Reason: microvm.FailureRuntimeExit}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var result controlclient.MessageDelivery
	if !waitUntil(12*time.Second, func() bool {
		result, err = control.GetMessageDelivery(context.Background(), lifecycleMessageID)
		return err == nil && result.WorkState == "unavailable"
	}) {
		t.Fatalf("recovery did not settle lost execution: %#v %v", result, err)
	}
	batch, err := control.ReadSessionEvents(context.Background(), lifecycleWorkspace, "agent:recovery", 0, false)
	if err != nil || len(batch.Events) != 1 || !strings.Contains(string(batch.Events[0].Data), `"recovered":true`) || batch.Events[0].SourceSandboxID == oldSandbox {
		t.Fatalf("recovery output=%#v %v", batch, err)
	}
	bridge.mu.Lock()
	recoveries := bridge.recoveryPosts
	bridge.mu.Unlock()
	if recoveries != 1 || runtime.startCount() != 2 {
		t.Fatalf("recoveries=%d sandbox starts=%d", recoveries, runtime.startCount())
	}
}
func qualifySDKMailbox(t *testing.T, restart bool, outputCount int) {
	sdk := os.Getenv("CANTELOP_SDK_RUNTIME_TEST_MODULE")
	if sdk == "" {
		sdk = "../../../../cantelop-sdk/dist/runtime.js"
	}
	sdk, err := filepath.Abs(sdk)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(sdk); err != nil {
		if os.Getenv("CANTELOP_SDK_RUNTIME_TEST_MODULE") != "" {
			t.Fatalf("configured SDK module unavailable: %v", err)
		}
		t.Skip("build adjacent SDK or set CANTELOP_SDK_RUNTIME_TEST_MODULE to qualify actual runtime")
	}
	if _, err = exec.LookPath("node"); err != nil {
		t.Skip("Node required for SDK qualification")
	}
	runtime := &fakeRuntime{running: make(map[string]bool)}
	store, err := microvm.OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"), microvm.WithConfigEncryptionKey(bytes.Repeat([]byte{11}, configcipher.KeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := httptest.NewServer(newLifecycleHandlerWithStore(store, runtime))
	defer server.Close()
	control, err := controlclient.New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	bridge := &sdkTestBridge{t: t, module: sdk, outputCount: outputCount, origins: map[string]string{}, ackLost: make(chan struct{})}
	_, err = control.EnqueueMessageDelivery(context.Background(), controlclient.MessageDelivery{ID: lifecycleMessageID, WorkspaceID: lifecycleWorkspace, LogicalSessionID: "agent:primary", KeepAliveSeconds: 600, Payload: []byte(`{"text":"hello"}`)})
	if err != nil {
		t.Fatal(err)
	}
	traces := &sdkTraceSink{attempts: make(map[string]string)}
	worker, err := dispatcher.NewExecutionDispatcher(control, messagedelivery.NewMailboxClient(bridge), sessionoutput.NewCollector(bridge), traces.observe, traces, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	if restart {
		select {
		case <-bridge.ackLost:
		case <-time.After(6 * time.Second):
			cancel()
			<-done
			t.Fatal("admission not reached")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		pending, err := control.ListExecutions(context.Background())
		if err != nil || len(pending) != 1 || pending[0].Phase != "sending" {
			t.Fatalf("crash window not retained: %#v %v", pending, err)
		}
		worker, err = dispatcher.NewExecutionDispatcher(control, messagedelivery.NewMailboxClient(bridge), sessionoutput.NewCollector(bridge), traces.observe, traces, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel = context.WithCancel(context.Background())
		go func() { done <- worker.Run(ctx) }()
	}
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	var result controlclient.MessageDelivery
	if !waitUntil(12*time.Second, func() bool {
		result, err = control.GetMessageDelivery(context.Background(), lifecycleMessageID)
		return err == nil && result.WorkState == "quiescent"
	}) {
		t.Fatalf("not quiescent: %#v %v", result, err)
	}
	if result.State != "handled" || result.Generation != 1 {
		t.Fatalf("result=%#v", result)
	}
	bridge.mu.Lock()
	posts := bridge.posts
	bridge.mu.Unlock()
	if posts != 1 {
		t.Fatalf("lost ACK caused %d handler admissions", posts)
	}
	batch, err := control.ReadSessionEvents(context.Background(), lifecycleWorkspace, "agent:primary", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Events) != outputCount || batch.StreamID == "" || batch.Events[0].SourceCursor != 1 || !strings.Contains(string(batch.Events[0].Data), `"calls":1`) {
		t.Fatalf("output handoff=%#v", batch)
	}
	if outputCount > 1 {
		elapsed := batch.Events[len(batch.Events)-1].CreatedAt.Sub(batch.Events[0].CreatedAt)
		if elapsed >= 500*time.Millisecond {
			t.Fatalf("%d sequential output events took %s; collection is paced by reconciliation", outputCount, elapsed)
		}
		t.Logf("%d sequential SDK output events handed off in %s", outputCount, elapsed)
	}
	if restart {
		oldSandbox := batch.Events[0].SourceSandboxID
		if err := control.TerminateSandbox(context.Background(), oldSandbox); err != nil {
			t.Fatal(err)
		}
		secondID := "msg_11111111111111111111111111111111"
		if _, err := control.EnqueueMessageDelivery(context.Background(), controlclient.MessageDelivery{ID: secondID, WorkspaceID: lifecycleWorkspace, LogicalSessionID: "agent:primary", KeepAliveSeconds: 600, Payload: []byte(`{"text":"replacement"}`)}); err != nil {
			t.Fatal(err)
		}
		if !waitUntil(12*time.Second, func() bool {
			item, err := control.GetMessageDelivery(context.Background(), secondID)
			return err == nil && item.WorkState == "quiescent"
		}) {
			t.Fatal("replacement did not finish")
		}
		next, err := control.ReadSessionEvents(context.Background(), lifecycleWorkspace, "agent:primary", 1, false)
		if err != nil || len(next.Events) != 1 || next.Events[0].SourceCursor != 1 || next.Events[0].SourceSandboxID == oldSandbox {
			t.Fatalf("replacement cursor continuity=%#v %v", next, err)
		}
	}
	expected := 1
	if restart {
		expected = 2
	}
	if !waitUntil(2*time.Second, func() bool {
		traces.mu.Lock()
		defer traces.mu.Unlock()
		return traces.completed == expected && traces.spans >= expected
	}) {
		t.Fatal("completed SDK executions did not produce lifecycle and span observations")
	}
}

// Verify that the actual SDK collector sees persisted attempt prerequisites,
// including when the dispatcher restarts after losing the mailbox ACK.
type sdkTraceSink struct {
	mu        sync.Mutex
	attempts  map[string]string
	completed int
	spans     int
}

func (s *sdkTraceSink) Record(events ...observability.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range events {
		if e.Type == "attempt.started" {
			s.attempts[e.AttemptID] = e.TraceID
		}
		if e.Type == "attempt.completed" {
			s.completed++
		}
	}
	return nil
}
func (s *sdkTraceSink) observe(_ context.Context, r messagedelivery.ObservationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.MessageID != "" && (r.AttemptID == "" || s.attempts[r.AttemptID] != r.TraceID) {
		return errors.New("SDK event preceded its Message attempt")
	}
	if r.Observation.Type == "span.started" {
		s.spans++
	}
	return nil
}

type sdkTestBridge struct {
	mu            sync.Mutex
	t             *testing.T
	module        string
	outputCount   int
	origins       map[string]string
	posts         int
	recoveryPosts int
	ackLost       chan struct{}
	ackOnce       sync.Once
}

func (b *sdkTestBridge) RoundTrip(request *http.Request) (*http.Response, error) {
	b.mu.Lock()
	id := request.Header.Get("X-Cantelop-Sandbox-ID")
	origin := b.origins[id]
	if origin == "" {
		script := fmt.Sprintf(`import {createServer} from 'node:http'; import {createSessionRuntimeHandler} from %q;
 let calls=0; const server=createServer(createSessionRuntimeHandler({
  receive:async ({message,output,activity,reply})=>{calls++;if(message.payload.crash){activity.start(()=>new Promise(()=>{}));return}if(message.payload.reply)reply({value:message.payload.value,...(message.payload.replyBytes ? {data:'x'.repeat(message.payload.replyBytes)} : {})});for(let i=1;i<=%d;i++)await output.send({calls,delta:i});},
  onRecover:async ({output})=>{await output.send({recovered:true})}
 }));
 server.listen(0,'127.0.0.1',()=>console.log(server.address().port));`, (&url.URL{Scheme: "file", Path: b.module}).String(), b.outputCount)
		command := exec.Command("node", "--input-type=module", "-e", script)
		command.Env = append(os.Environ(), "CANTELOP_SANDBOX_ID="+id)
		command.Stderr = io.Discard
		stdout, err := command.StdoutPipe()
		if err != nil {
			b.mu.Unlock()
			return nil, err
		}
		if err = command.Start(); err != nil {
			b.mu.Unlock()
			return nil, err
		}
		b.t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
		scanner := bufio.NewScanner(stdout)
		if !scanner.Scan() {
			b.mu.Unlock()
			return nil, errors.New("SDK did not start")
		}
		origin = "http://127.0.0.1:" + scanner.Text()
		b.origins[id] = origin
	}
	lose := request.Method == "POST" && strings.HasSuffix(request.URL.Path, "/messages")
	if lose {
		b.posts++
	}
	if request.Method == "POST" && strings.HasSuffix(request.URL.Path, "/runtime/recoveries") {
		b.recoveryPosts++
	}
	b.mu.Unlock()
	target, _ := url.Parse(origin)
	clone := request.Clone(request.Context())
	copyURL := *request.URL
	copyURL.Scheme = target.Scheme
	copyURL.Host = target.Host
	clone.URL = &copyURL
	clone.Host = target.Host
	response, err := http.DefaultTransport.RoundTrip(clone)
	if err == nil && lose {
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		b.ackOnce.Do(func() { close(b.ackLost) })
		return nil, errors.New("injected ACK loss after reservation")
	}
	return response, err
}
