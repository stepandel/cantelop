package devruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
)

type recordedLocalCommand struct {
	name string
	args []string
}

type fakeLocalCommandRunner struct {
	outputs  [][]byte
	errors   []error
	commands []recordedLocalCommand
}

func (runner *fakeLocalCommandRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.commands = append(runner.commands, recordedLocalCommand{name: name, args: append([]string(nil), args...)})
	index := len(runner.commands) - 1
	var output []byte
	var err error
	if index < len(runner.outputs) {
		output = runner.outputs[index]
	}
	if index < len(runner.errors) {
		err = runner.errors[index]
	}
	return output, err
}

type localRoundTripFunc func(*http.Request) (*http.Response, error)

func (function localRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestExecLocalCommandRunnerKeepsSuccessfulDiagnosticsOutOfMachineOutput(t *testing.T) {
	output, err := (execLocalCommandRunner{}).Output(
		context.Background(), "/bin/sh", "-c", "printf container-id; printf warning >&2",
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "container-id" {
		t.Fatalf("output = %q", output)
	}
}

func TestDockerLocalSandboxStartsWithPrivatePortEnvironmentAndWorkspace(t *testing.T) {
	workspaceRoot := t.TempDir()
	databases, _, _ := testLocalDatabases(t, t.TempDir())
	runner := &fakeLocalCommandRunner{outputs: [][]byte{
		[]byte("0123456789abcdef0123456789abcdef\n"),
		[]byte("127.0.0.1:49152\n"),
	}}
	factory := &dockerLocalSandboxFactory{
		image: "cantelop-dev:test", environment: "/tmp/local.env", workspaceRoot: workspaceRoot, databases: databases,
		runner: runner, client: http.DefaultClient, waitReady: func(context.Context, *http.Client, string) error { return nil },
	}
	sandbox, err := factory.Start(context.Background(), localTestWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	started := runner.commands[0]
	joined := strings.Join(started.args, " ")
	for _, expected := range []string{
		"run --detach --rm", "--publish 127.0.0.1::57321", "CANTELOP_INTERNAL_PORT=57321",
		"source=" + workspaceRoot + "/" + localTestWorkspaceID + ",target=/workspace",
		"--env CANTELOP_WORKSPACE_PATH=/workspace",
		"--env-file /tmp/local.env", "cantelop-dev:test",
		"--add-host host.docker.internal:host-gateway", "--env CANTELOP_LOCAL_DATABASE_ORIGIN=", "--env CANTELOP_WORKSPACE_DATABASE_CREDENTIALS_URL=",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("docker run args %q do not contain %q", joined, expected)
		}
	}
	if sandbox.(*dockerLocalSandbox).origin != "http://127.0.0.1:49152" {
		t.Fatalf("Sandbox origin = %q", sandbox.(*dockerLocalSandbox).origin)
	}
	databases.mu.Lock()
	count := len(databases.sessions)
	databases.mu.Unlock()
	if count != 1 {
		t.Fatalf("active database capabilities = %d", count)
	}
	if err := sandbox.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	databases.mu.Lock()
	count = len(databases.sessions)
	databases.mu.Unlock()
	if count != 0 {
		t.Fatalf("retired database capabilities = %d", count)
	}
}

func TestDockerLocalSandboxDeliversMessageAndAwaitsQuiescence(t *testing.T) {
	var requests []*http.Request
	client := &http.Client{Transport: localRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request)
		header := make(http.Header)
		var body io.ReadCloser = http.NoBody
		header.Set(messageprotocol.SandboxHeader, "sbx-11111111111111111111111111111111")
		header.Set(messageprotocol.ProtocolHeader, "2")
		if request.Method == http.MethodPost {
			var envelope struct {
				Deadline time.Time `json:"deadline"`
			}
			if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			body = io.NopCloser(strings.NewReader(fmt.Sprintf(`{"sandbox_id":"sbx-11111111111111111111111111111111","message_id":"%s","sequence":1,"generation":7,"state":"succeeded","accepted_at":%q,"deadline":%q}`, localTestMessageID, envelope.Deadline.Add(-5*time.Minute).Format(time.RFC3339Nano), envelope.Deadline.Format(time.RFC3339Nano))))
		} else {
			body = io.NopCloser(strings.NewReader(`{"sandbox_id":"sbx-11111111111111111111111111111111","protocol":2,"quiescent":true,"generation":7}`))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: body, Request: request}, nil
	})}
	runner := &fakeLocalCommandRunner{outputs: [][]byte{[]byte("removed\n")}}
	sandbox := &dockerLocalSandbox{
		localRuntimeClient: localRuntimeClient{origin: "http://127.0.0.1:49152", client: client},
		containerID:        "0123456789abcdef0123456789abcdef", runner: runner,
	}
	message := messageprotocol.Request{
		Session: messageprotocol.SessionEnvelope{ID: "agent:primary", WorkspaceID: localTestWorkspaceID, KeepAliveSeconds: 30},
		Message: messageprotocol.Message{ID: localTestMessageID, Payload: json.RawMessage(`{"prompt":"hello"}`)},
	}
	generation, err := sandbox.Deliver(context.Background(), message)
	if err != nil || generation != 7 {
		t.Fatalf("Deliver() = %d, %v", generation, err)
	}
	if err := sandbox.AwaitQuiescence(context.Background(), generation); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 || requests[0].URL.Path != messageprotocol.RuntimePath || requests[1].URL.Path != messageprotocol.MessagePath ||
		requests[2].URL.Path != messageprotocol.RuntimePath {
		t.Fatalf("requests = %#v", requests)
	}
	if err := sandbox.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("close commands = %#v", runner.commands)
	}
}

func TestDockerLocalSandboxCleansUpWhenPortResolutionFails(t *testing.T) {
	runner := &fakeLocalCommandRunner{
		outputs: [][]byte{[]byte("0123456789abcdef0123456789abcdef\n"), []byte("bad port\n"), []byte("removed\n")},
		errors:  []error{nil, errors.New("docker port failed")},
	}
	factory := &dockerLocalSandboxFactory{
		image: "cantelop-dev:test", workspaceRoot: t.TempDir(), runner: runner,
		client: http.DefaultClient, waitReady: func(context.Context, *http.Client, string) error { return nil },
	}
	if _, err := factory.Start(context.Background(), localTestWorkspaceID); err == nil {
		t.Fatal("Start succeeded after docker port failed")
	}
	if len(runner.commands) != 3 || runner.commands[2].args[0] != "container" {
		t.Fatalf("cleanup commands = %#v", runner.commands)
	}
}

func TestReadLocalReplyUsesInternalRouteAndValidatesResponse(t *testing.T) {
	for _, body := range []string{`{"reply":{"ok":true}}`, `{"reply":null}`, `{}`, `{"reply":true} {}`, `{"reply":true,"extra":1}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != messageprotocol.MessagePath+"/"+localTestMessageID+"/reply" {
					t.Errorf("reply route = %s %s", r.Method, r.URL.Path)
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			_, err := readLocalReply(context.Background(), server.Client(), server.URL, localTestMessageID)
			valid := body == `{"reply":{"ok":true}}` || body == `{"reply":null}`
			if (err == nil) != valid {
				t.Fatalf("reply error = %v", err)
			}
		})
	}
}

func TestLocalRequestRejectsUnsupportedRepliesBeforeAdmission(t *testing.T) {
	posts := 0
	client := &http.Client{Transport: localRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			posts++
		}
		header := make(http.Header)
		header.Set(messageprotocol.SandboxHeader, "sbx-11111111111111111111111111111111")
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(`{"sandbox_id":"sbx-11111111111111111111111111111111","protocol":2,"generation":0,"quiescent":true,"capabilities":{"replies":false}}`))}, nil
	})}
	_, err := deliverLocalMessage(context.Background(), client, "http://localhost", messageprotocol.Request{Reply: true, Session: messageprotocol.SessionEnvelope{ID: "thread", WorkspaceID: localTestWorkspaceID}, Message: messageprotocol.Message{ID: localTestMessageID, Payload: json.RawMessage(`{}`)}})
	if err == nil || err.Error() != "request_reply_unsupported" || posts != 0 {
		t.Fatalf("unsupported reply: %v, posts=%d", err, posts)
	}
}

func TestLocalReceiptRejectsMalformedEvidence(t *testing.T) {
	for _, body := range []string{`{}`, `{"unexpected":true}`, `{} {}`} {
		client := &http.Client{Transport: localRoundTripFunc(func(*http.Request) (*http.Response, error) {
			response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
			response.Header.Set(messageprotocol.SandboxHeader, "sbx-11111111111111111111111111111111")
			return response, nil
		})}
		_, err := requestLocalReceipt(context.Background(), client, http.MethodGet, "http://localhost"+messageprotocol.MessagePath+"/"+localTestMessageID, nil, localTestMessageID)
		if err == nil || err.Error() != "runtime_protocol_mismatch" {
			t.Fatalf("invalid receipt: %v", err)
		}
	}
}

func TestLocalReadinessRetriesEarlyProxyEOF(t *testing.T) {
	const identity = "sbx-11111111111111111111111111111111"
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != messageprotocol.RuntimePath || r.Header.Get(messageprotocol.SandboxHeader) != identity {
			t.Error("readiness did not probe the authenticated runtime endpoint")
			w.WriteHeader(400)
			return
		}
		if attempts.Add(1) <= 3 {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			connection.Close()
			return
		}
		w.Header().Set(messageprotocol.SandboxHeader, identity)
		w.Header().Set(messageprotocol.ProtocolHeader, "2")
		fmt.Fprintf(w, `{"sandbox_id":%q,"protocol":2,"generation":0,"quiescent":true}`, identity)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForLocalSandbox(ctx, localIdentityClient(server.Client(), identity), server.URL); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 4 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestLocalReadinessRejectsInvalidRuntime(t *testing.T) {
	const identity = "sbx-11111111111111111111111111111111"
	for _, tc := range []struct{ name, header, body string }{
		{"foreign sandbox", "sbx-22222222222222222222222222222222", `{"sandbox_id":"sbx-22222222222222222222222222222222","protocol":2}`},
		{"wrong protocol", identity, `{"sandbox_id":"sbx-11111111111111111111111111111111","protocol":1}`},
		{"malformed response", identity, `not-json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(messageprotocol.SandboxHeader, tc.header)
				w.Header().Set(messageprotocol.ProtocolHeader, "2")
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := waitForLocalSandbox(ctx, localIdentityClient(server.Client(), identity), server.URL)
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected immediate protocol rejection, got %v", err)
			}
		})
	}
}

func TestLocalReadinessHonorsCancellationWithUnresponsiveHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := waitForLocalSandbox(ctx, server.Client(), server.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}
