package devruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/boundedio"
	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/clients/cli/internal/sessionimage"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
)

const localSessionRuntimePort = 57321

type localCommandRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}

type execLocalCommandRunner struct{}

func (execLocalCommandRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	commandError := boundedio.NewLimitedBuffer(64 << 10)
	command.Stderr = commandError
	output, err := command.Output()
	if err != nil {
		return append(output, commandError.String()...), err
	}
	return output, nil
}

type dockerLocalSandboxFactory struct {
	databases     *localDatabases
	image         string
	environment   string
	workspaceRoot string
	runner        localCommandRunner
	client        *http.Client
	waitReady     func(context.Context, *http.Client, string) error
}

// localRuntimeClient speaks the local runtime protocol to one Sandbox. Both
// Sandbox kinds embed it for every operation that is only a protocol call.
type localRuntimeClient struct {
	origin string
	client *http.Client
}

type dockerLocalSandbox struct {
	localRuntimeClient
	revokeDatabase func()
	containerID    string
	runner         localCommandRunner
	closeOnce      sync.Once
	closeError     error
}

type nativeLocalSandboxFactory struct {
	databases            *localDatabases
	bun                  string
	sessionRuntimeModule string
	environment          map[string]string
	workspaceRoot        string
	output               io.Writer
	client               *http.Client
	waitReady            func(context.Context, *http.Client, string) error
}

type nativeLocalSandbox struct {
	localRuntimeClient
	revokeDatabase func()
	process        *supervisedProcess
	closeOnce      sync.Once
}

func newNativeLocalSandboxFactory(
	sessionRuntimeModule string,
	environment map[string]string,
	workspaceRoot string,
	output io.Writer,
) (*nativeLocalSandboxFactory, error) {
	if strings.TrimSpace(sessionRuntimeModule) == "" {
		return nil, errors.New("local Session runtime module is required")
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil, errors.New("local Workspace root is required")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		return nil, errors.New("native development requires Bun on PATH; use --container for Docker mode")
	}
	return &nativeLocalSandboxFactory{
		bun: bun, sessionRuntimeModule: sessionRuntimeModule, environment: environment,
		workspaceRoot: workspaceRoot, output: output, client: &http.Client{}, waitReady: waitForLocalSandbox,
	}, nil
}

func (factory *nativeLocalSandboxFactory) Start(ctx context.Context, workspaceID string) (localSandbox, error) {
	if !ids.ValidLocalWorkspaceID(workspaceID) {
		return nil, errors.New("invalid local Workspace ID")
	}
	workspacePath := filepath.Join(factory.workspaceRoot, workspaceID)
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		return nil, fmt.Errorf("create local Workspace directory: %w", err)
	}
	if info, err := os.Lstat(workspacePath); err != nil || !info.IsDir() {
		return nil, errors.New("local Workspace must be a real directory")
	}
	if err := os.Chmod(workspacePath, os.ModeSetgid|0o770); err != nil {
		return nil, fmt.Errorf("prepare native Workspace permissions: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("reserve native Sandbox listener: %w", err)
	}
	address := listener.Addr().String()
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	identity := newLocalSandboxIdentity()
	command := exec.Command(factory.bun, "--no-install", "--preload", filepath.Join(filepath.Dir(factory.sessionRuntimeModule), "local-workspace.mjs"), factory.sessionRuntimeModule)
	command.Dir = workspacePath
	reserved := map[string]string{
		"CANTELOP_INTERNAL_PORT":  strconv.Itoa(port),
		"CANTELOP_WORKSPACE_PATH": workspacePath,
		"CANTELOP_SANDBOX_ID":     identity,
	}
	var revoke func()
	if factory.databases != nil {
		values, cleanup := factory.databases.sessionEnvironment(workspaceID)
		revoke = cleanup
		for name, value := range values {
			reserved[name] = value
		}
	}
	command.Env = localProcessEnvironment(factory.environment, reserved)
	command.Stdout, command.Stderr = factory.output, factory.output
	process, err := startSupervisedProcess(command, revoke)
	if err != nil {
		if revoke != nil {
			revoke()
		}
		return nil, fmt.Errorf("start native Sandbox: %w", err)
	}
	sandbox := &nativeLocalSandbox{
		localRuntimeClient: localRuntimeClient{origin: "http://" + address, client: localIdentityClient(factory.client, identity)},
		revokeDatabase:     revoke,
		process:            process,
	}
	if err := factory.waitReady(ctx, sandbox.client, sandbox.origin); err != nil {
		_ = sandbox.Close(context.Background())
		return nil, fmt.Errorf("wait for native Sandbox: %w", err)
	}
	return sandbox, nil
}

func localProcessEnvironment(values, reserved map[string]string) []string {
	environment := make(map[string]string)
	for _, entry := range os.Environ() {
		name, value, found := strings.Cut(entry, "=")
		if found {
			environment[name] = value
		}
	}
	for name, value := range values {
		environment[name] = value
	}
	for name, value := range reserved {
		environment[name] = value
	}
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+environment[name])
	}
	return result
}

func (sandbox localRuntimeClient) Deliver(ctx context.Context, message messageprotocol.Request) (uint64, error) {
	return deliverLocalMessage(ctx, sandbox.client, sandbox.origin, message)
}

func (sandbox localRuntimeClient) ReadOutput(ctx context.Context, after uint64, wait bool) (messageprotocol.OutputBatch, error) {
	return readLocalOutput(ctx, sandbox.client, sandbox.origin, after, wait)
}

func (sandbox localRuntimeClient) AwaitQuiescence(ctx context.Context, minimumGeneration uint64) error {
	return awaitLocalQuiescence(ctx, sandbox.client, sandbox.origin, minimumGeneration)
}

// Close stops the native Sandbox. Its exit after an interrupt is expected, so
// the exit status is not an error.
func (sandbox *nativeLocalSandbox) Close(context.Context) error {
	sandbox.closeOnce.Do(func() {
		if sandbox.revokeDatabase != nil {
			sandbox.revokeDatabase()
		}
		sandbox.process.Close()
	})
	return nil
}

func newDockerLocalSandboxFactory(image, environment, workspaceRoot string) (*dockerLocalSandboxFactory, error) {
	if strings.TrimSpace(image) == "" {
		return nil, errors.New("local Docker image is required")
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil, errors.New("local Workspace root is required")
	}
	return &dockerLocalSandboxFactory{
		image: image, environment: environment, workspaceRoot: workspaceRoot,
		runner: execLocalCommandRunner{}, client: &http.Client{}, waitReady: waitForLocalSandbox,
	}, nil
}

func (factory *dockerLocalSandboxFactory) Start(ctx context.Context, workspaceID string) (localSandbox, error) {
	if !ids.ValidLocalWorkspaceID(workspaceID) {
		return nil, errors.New("invalid local Workspace ID")
	}
	workspacePath := filepath.Join(factory.workspaceRoot, workspaceID)
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		return nil, fmt.Errorf("create local Workspace directory: %w", err)
	}
	if info, err := os.Lstat(workspacePath); err != nil || !info.IsDir() {
		return nil, errors.New("local Workspace must be a real directory")
	}
	if err := prepareDockerWorkspace(ctx, factory.runner, workspacePath); err != nil {
		return nil, err
	}
	identity := newLocalSandboxIdentity()
	args := []string{
		"run", "--detach", "--rm",
		"--platform", "linux/amd64",
		"--user", "10001:10001", "--workdir", guestconfig.SessionWorkspace,
		"--group-add", strconv.Itoa(os.Getgid()),
		"--no-healthcheck", "--stop-signal", "SIGTERM",
		"--label", "dev.cantelop.local=true",
		"--publish", "127.0.0.1::" + strconv.Itoa(localSessionRuntimePort),
		"--env", "CANTELOP_INTERNAL_PORT=" + strconv.Itoa(localSessionRuntimePort),
		"--env", "CANTELOP_WORKSPACE_PATH=/workspace",
		"--mount", "type=bind,source=" + workspacePath + ",target=/workspace",
	}
	if factory.environment != "" {
		args = append(args, "--env-file", factory.environment)
	}
	for _, entry := range guestconfig.SessionEnvironment() {
		args = append(args, "--env", entry)
	}
	var revoke func()
	if factory.databases != nil {
		values, cleanup := factory.databases.sessionEnvironment(workspaceID)
		revoke = cleanup
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
		for _, name := range []string{"CANTELOP_LOCAL_DATABASE_ORIGIN", "CANTELOP_WORKSPACE_DATABASE_CREDENTIALS_URL", "CANTELOP_WORKSPACE_DATABASE_ACCESS_TOKEN"} {
			args = append(args, "--env", name+"="+values[name])
		}
	}
	started := false
	defer func() {
		if !started && revoke != nil {
			revoke()
		}
	}()
	args = append(args, "--env", "CANTELOP_SANDBOX_ID="+identity, factory.image)
	output, err := factory.runner.Output(ctx, "docker", args...)
	if err != nil {
		return nil, localCommandError("start local Sandbox", output, err)
	}
	containerID := strings.TrimSpace(string(output))
	if !ids.LocalContainerIDPattern.MatchString(containerID) {
		return nil, errors.New("Docker returned an invalid local Sandbox container ID")
	}

	portOutput, err := factory.runner.Output(ctx, "docker", "port", containerID, strconv.Itoa(localSessionRuntimePort)+"/tcp")
	if err != nil {
		_ = removeLocalContainer(context.Background(), factory.runner, containerID)
		return nil, localCommandError("resolve local Sandbox port", portOutput, err)
	}
	origin, _, err := localSandboxOrigin(string(portOutput))
	if err != nil {
		_ = removeLocalContainer(context.Background(), factory.runner, containerID)
		return nil, err
	}
	client := localIdentityClient(factory.client, identity)
	if err := factory.waitReady(ctx, client, origin); err != nil {
		_ = removeLocalContainer(context.Background(), factory.runner, containerID)
		return nil, fmt.Errorf("wait for local Sandbox: %w", err)
	}
	started = true
	return &dockerLocalSandbox{
		localRuntimeClient: localRuntimeClient{origin: origin, client: client},
		revokeDatabase:     revoke,
		containerID:        containerID, runner: factory.runner,
	}, nil
}

// Initialize only the mount root using the trusted runtime image. Retaining
// the host owner and sharing its group avoids stealing ownership from native
// development. Setgid plus the local runtime's umask preserves that sharing
// for newly created descendants; existing files are never recursively changed.
func prepareDockerWorkspace(ctx context.Context, runner localCommandRunner, workspacePath string) error {
	// Host-owned directories can be prepared directly. Docker Desktop bind mounts
	// can reject chmod after container chown, even when root has FOWNER.
	if err := os.Chmod(workspacePath, os.ModeSetgid|0o770); err == nil {
		return nil
	}
	// Recover directories created by older runners under the managed container UID.

	script := fmt.Sprintf("import { chownSync, chmodSync } from 'node:fs'; chownSync('/workspace', %d, %d); chmodSync('/workspace', 0o2770);", os.Getuid(), os.Getgid())
	output, err := runner.Output(ctx, "docker", "run", "--rm", "--platform", "linux/amd64",
		"--network", "none", "--read-only", "--user", "0:0",
		"--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "FOWNER",
		"--mount", "type=bind,source="+workspacePath+",target=/workspace",
		"--entrypoint", "/usr/local/bin/bun", sessionimage.SessionRuntimeImage, "--no-install", "-e", script)
	if err != nil {
		return localCommandError("prepare local Workspace permissions", output, err)
	}
	return nil
}

func readLocalOutput(
	ctx context.Context,
	client *http.Client,
	origin string,
	after uint64,
	wait bool,
) (messageprotocol.OutputBatch, error) {
	endpoint := origin + messageprotocol.OutputPath + "?after=" + strconv.FormatUint(after, 10)
	endpoint += "&wait=0"
	var batch messageprotocol.OutputBatch
	if _, err := requestLocalRuntime(ctx, client, http.MethodGet, endpoint, nil, &batch, localRuntimeMaxEnvelopeBytes+(64<<10)); err != nil {
		return messageprotocol.OutputBatch{}, err
	}
	if err := batch.Validate(after, true); err != nil {
		return messageprotocol.OutputBatch{}, err
	}
	if wait && len(batch.Events) == 0 {
		select {
		case <-ctx.Done():
			return batch, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return batch, nil
}

// localMessageFailure carries the same public failure code as the dispatcher.
type localMessageFailure string

func (e localMessageFailure) Error() string { return string(e) }

type localAcknowledgmentKey struct{}

func deliverLocalMessage(ctx context.Context, client *http.Client, origin string, message messageprotocol.Request) (uint64, error) {
	deadline := time.Now().UTC().Truncate(time.Millisecond).Add(5 * time.Minute)
	ctx, cancel := context.WithDeadline(ctx, deadline.Add(10*time.Second))
	defer cancel()
	snapshot, err := inspectLocalRuntime(ctx, client, origin)
	if err != nil {
		return 0, err
	}
	reportLocalRuntime(ctx, snapshot)
	if message.Reply && !snapshot.Capabilities.Replies {
		return 0, localMessageFailure("request_reply_unsupported")
	}
	body, err := json.Marshal(struct {
		messageprotocol.Request
		Deadline time.Time `json:"deadline"`
	}{message, deadline})
	if err != nil {
		return 0, err
	}
	admit := func() (messageprotocol.Receipt, error) {
		return requestLocalReceipt(ctx, client, http.MethodPost, origin+messageprotocol.MessagePath, body, message.Message.ID)
	}
	admissionDeadline := time.Now().Add(30 * time.Second)
	var backoff localPollBackoff
	receipt, err := admit()
	var original *messageprotocol.Receipt
	cancelRequested := false
	for {
		if err == nil {
			if !receipt.Deadline.Equal(deadline) {
				return 0, localMessageFailure("runtime_protocol_mismatch")
			}
			if original == nil {
				copy := receipt
				original = &copy
				if acknowledge, ok := ctx.Value(localAcknowledgmentKey{}).(func()); ok {
					acknowledge()
				}
			} else if receipt.SandboxID != original.SandboxID || receipt.Sequence != original.Sequence || receipt.Generation != original.Generation || !receipt.AcceptedAt.Equal(original.AcceptedAt) {
				return 0, localMessageFailure("runtime_protocol_mismatch")
			}
			reportLocalReceipt(ctx, receipt)
			switch receipt.State {
			case "succeeded":
				return receipt.Generation, nil
			case "failed":
				if receipt.ErrorCode != "" {
					return receipt.Generation, localMessageFailure(receipt.ErrorCode)
				}
				return receipt.Generation, localMessageFailure("message_handler_failed")
			case "timed_out":
				if receipt.ErrorCode != "" {
					return receipt.Generation, localMessageFailure(receipt.ErrorCode)
				}
				return receipt.Generation, localMessageFailure("message_timed_out")
			}

		} else {
			var protocol localMessageFailure
			if errors.As(err, &protocol) {
				return 0, err
			}
			var status *localRuntimeHTTPError
			if errors.As(err, &status) {
				if status.code == http.StatusNotFound && original != nil {
					return 0, localMessageFailure("mailbox_reservation_lost")
				}
				if status.code != http.StatusNotFound && status.code < 500 {
					return 0, err
				}
			}
			if original == nil && time.Now().After(admissionDeadline) {
				return 0, localMessageFailure("execution_outcome_unknown")
			}
		}
		if !cancelRequested && time.Now().After(deadline) {
			cancelRequested = cancelLocalWork(ctx, client, origin, messageprotocol.MessagePath+"/"+message.Message.ID+"/cancel", nil) == nil
		}
		select {
		case <-ctx.Done():
			if !time.Now().Before(deadline.Add(10 * time.Second)) {
				return 0, localMessageFailure("message_timed_out")
			}
			return 0, localMessageFailure("execution_outcome_unknown")
		case <-time.After(backoff.next()):
		}
		receipt, err = requestLocalReceipt(ctx, client, http.MethodGet, origin+messageprotocol.MessagePath+"/"+message.Message.ID, nil, message.Message.ID)
		// A lost ACK is reconciled only with this exact Sandbox. An absent
		// reservation may be retried with the original identity and fixed deadline.
		var status *localRuntimeHTTPError
		if original == nil && errors.As(err, &status) && status.code == http.StatusNotFound {
			receipt, err = admit()
		}
	}
}

// localPollBackoff spaces repeated polls of a local Sandbox: the first retry
// is quick for short waits, later ones back off to a capped interval.
type localPollBackoff struct{ delay time.Duration }

const (
	localPollInitialDelay = 25 * time.Millisecond
	localPollMaxDelay     = 250 * time.Millisecond
)

func (backoff *localPollBackoff) next() time.Duration {
	if backoff.delay == 0 {
		backoff.delay = localPollInitialDelay
	} else {
		backoff.delay = min(2*backoff.delay, localPollMaxDelay)
	}
	return backoff.delay
}

type localRuntimeHTTPError struct{ code int }

func (e *localRuntimeHTTPError) Error() string {
	return fmt.Sprintf("local runtime returned HTTP %d", e.code)
}

func requestLocalReceipt(ctx context.Context, client *http.Client, method, endpoint string, body []byte, id string) (messageprotocol.Receipt, error) {
	var receipt messageprotocol.Receipt
	header, err := requestLocalRuntime(ctx, client, method, endpoint, body, &receipt, localRuntimeMaxEnvelopeBytes)
	var decodeError localRuntimeDecodeError
	if errors.As(err, &decodeError) {
		return receipt, localMessageFailure("runtime_protocol_mismatch")
	}
	if err != nil {
		return receipt, err
	}
	if err := receipt.Validate(id, header.Get(messageprotocol.SandboxHeader)); err != nil {
		return receipt, localMessageFailure("runtime_protocol_mismatch")
	}
	return receipt, nil
}

// localRuntimeDecodeError is a local runtime response body that is not one
// JSON value of the expected shape within the size limit.
type localRuntimeDecodeError struct{ error }

// requestLocalRuntime sends one local runtime protocol request, bounded to ten
// seconds, and decodes a 2xx JSON reply into target when target is set. Other
// statuses return *localRuntimeHTTPError.
func requestLocalRuntime(ctx context.Context, client *http.Client, method, endpoint string, body []byte, target any, limit int64) (http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.Header, &localRuntimeHTTPError{response.StatusCode}
	}
	if target != nil {
		if err := decodeLocalRuntimeJSON(io.LimitReader(response.Body, limit+1), target); err != nil {
			return response.Header, localRuntimeDecodeError{err}
		}
	}
	return response.Header, nil
}

func decodeLocalRuntimeJSON(body io.Reader, target any) error {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid local runtime envelope")
	}
	return nil
}

func inspectLocalRuntime(ctx context.Context, client *http.Client, origin string) (messageprotocol.RuntimeStatus, error) {
	var snapshot messageprotocol.RuntimeStatus
	header, err := requestLocalRuntime(ctx, client, http.MethodGet, origin+messageprotocol.RuntimePath, nil, &snapshot, localRuntimeMaxEnvelopeBytes)
	if err != nil {
		return snapshot, err
	}
	return snapshot, snapshot.Validate(header.Get(messageprotocol.SandboxHeader), time.Now())
}

func cancelLocalWork(ctx context.Context, client *http.Client, origin, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = requestLocalRuntime(ctx, client, http.MethodPost, origin+path, data, nil, 0)
	return err
}

func awaitLocalQuiescence(ctx context.Context, client *http.Client, origin string, generation uint64) error {
	var outputDeadline time.Time
	var backoff localPollBackoff
	for {
		snapshot, err := inspectLocalRuntime(ctx, client, origin)
		if err != nil {
			return err
		}
		reportLocalRuntime(ctx, snapshot)
		if snapshot.Quiescent && snapshot.Generation >= generation {
			if snapshot.Events.Acknowledged == snapshot.Events.Latest {
				return nil
			}
			if outputDeadline.IsZero() {
				outputDeadline = time.Now().Add(30 * time.Second)
			}
			if time.Now().After(outputDeadline) {
				return localMessageFailure("output_handoff_unavailable")
			}
		}
		for i, work := range []*messageprotocol.ActivityStatus{snapshot.Activity, snapshot.MessageWork} {
			if work != nil && (time.Now().After(work.Deadline.Add(10*time.Second)) || (i == 0 && work.CancellationRequestedAt != nil && time.Now().After(work.CancellationRequestedAt.Add(10*time.Second)))) {
				if i == 0 {
					return localMessageFailure("activity_timed_out")
				}
				return localMessageFailure("message_timed_out")
			}
		}
		if snapshot.Activity != nil && time.Now().After(snapshot.Activity.Deadline) && snapshot.Activity.CancellationRequestedAt == nil {
			if err := cancelLocalWork(ctx, client, origin, messageprotocol.RuntimePath+"/activity/cancel", map[string]string{"activity_id": snapshot.Activity.ID}); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff.next()):
		}
	}
}

func (sandbox *dockerLocalSandbox) Close(ctx context.Context) error {
	sandbox.closeOnce.Do(func() {
		if sandbox.revokeDatabase != nil {
			sandbox.revokeDatabase()
		}
		output, err := sandbox.runner.Output(ctx, "docker", "stop", "--timeout", "5", sandbox.containerID)
		if err != nil && !strings.Contains(string(output), "No such container") {
			sandbox.closeError = errors.Join(localCommandError("stop local Sandbox", output, err), removeLocalContainer(ctx, sandbox.runner, sandbox.containerID))
		}
	})
	return sandbox.closeError
}

func removeLocalContainer(ctx context.Context, runner localCommandRunner, containerID string) error {
	output, err := runner.Output(ctx, "docker", "container", "rm", "--force", containerID)
	if err == nil || strings.Contains(string(output), "No such container") {
		return nil
	}
	return localCommandError("remove local Sandbox", output, err)
}

func localSandboxOrigin(output string) (string, string, error) {
	line := strings.TrimSpace(output)
	if strings.Contains(line, "\n") {
		return "", "", errors.New("Docker returned multiple local Sandbox ports")
	}
	host, port, err := net.SplitHostPort(line)
	if err != nil || host != "127.0.0.1" {
		return "", "", errors.New("Docker returned an invalid local Sandbox port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", "", errors.New("Docker returned an invalid local Sandbox port")
	}
	origin := (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String()
	return strings.TrimSuffix(origin, "/"), net.JoinHostPort(host, port), nil
}

// Docker can accept TCP connections before the guest HTTP listener is ready.
// Probe the authenticated runtime contract before admitting any Session work.
func waitForLocalSandbox(ctx context.Context, client *http.Client, origin string) error {
	readyContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		probeContext, cancelProbe := context.WithTimeout(readyContext, 2*time.Second)
		_, err := inspectLocalRuntime(probeContext, client, origin)
		cancelProbe()
		if err == nil {
			return nil
		}
		if readyContext.Err() != nil {
			return fmt.Errorf("runtime readiness: %w", readyContext.Err())
		}
		// Retry connection refusal, early proxy EOF, and unresponsive listeners.
		// A responding server with an invalid identity/protocol is not our runtime.
		var transportError *url.Error
		var protocolFailure localMessageFailure
		if errors.As(err, &protocolFailure) || !errors.As(err, &transportError) {
			return fmt.Errorf("runtime readiness: %w", err)
		}
		select {
		case <-readyContext.Done():
			return fmt.Errorf("runtime readiness: %w", readyContext.Err())
		case <-ticker.C:
		}
	}
}

func localCommandError(action string, output []byte, err error) error {
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("%s: %s", action, detail)
}

func newLocalSandboxIdentity() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return "sbx-" + hex.EncodeToString(id[:])
}

type localIdentityTransport struct {
	base     http.RoundTripper
	identity string
}

func localIdentityClient(base *http.Client, identity string) *http.Client {
	client := *base
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = localIdentityTransport{transport, identity}
	client.Timeout = 30 * time.Second
	return &client
}

func (t localIdentityTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set(messageprotocol.SandboxHeader, t.identity)
	// The local collector advances after only after handing the prior batch to
	// its broker. Send that checkpoint explicitly before the next read.
	if request.Method == http.MethodGet && request.URL.Path == messageprotocol.OutputPath && request.URL.Query().Get("after") != "0" {
		through, err := strconv.ParseUint(request.URL.Query().Get("after"), 10, 64)
		if err != nil {
			return nil, err
		}
		target := *request.URL
		target.Path += "/ack"
		target.RawQuery = ""
		ack, err := http.NewRequestWithContext(request.Context(), http.MethodPost, target.String(), strings.NewReader(fmt.Sprintf(`{"through":%d}`, through)))
		if err != nil {
			return nil, err
		}
		ack.Header.Set(messageprotocol.SandboxHeader, t.identity)
		ack.Header.Set("Content-Type", "application/json")
		response, err := t.base.RoundTrip(ack)
		if err != nil {
			return nil, err
		}
		response.Body.Close()
		if response.Header.Get(messageprotocol.SandboxHeader) != t.identity || response.Header.Get(messageprotocol.ProtocolHeader) != "2" || response.StatusCode != 200 {
			return nil, errors.New("local output acknowledgment rejected")
		}
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.Header.Get(messageprotocol.SandboxHeader) != t.identity || response.Header.Get(messageprotocol.ProtocolHeader) != "2" {
		response.Body.Close()
		return nil, localMessageFailure("runtime_protocol_mismatch")
	}
	return response, nil
}

func (sandbox localRuntimeClient) ReadReply(ctx context.Context, id string) (json.RawMessage, error) {
	return readLocalReply(ctx, sandbox.client, sandbox.origin, id)
}

func readLocalReply(ctx context.Context, client *http.Client, origin, id string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute+10*time.Second)
	defer cancel()
	var backoff localPollBackoff
	for {
		reply, err := readLocalReplyOnce(ctx, client, origin, id)
		if err == nil {
			return reply, nil
		}
		var status *localRuntimeHTTPError
		var network *url.Error
		var protocol localMessageFailure
		if errors.As(err, &protocol) || (!(errors.As(err, &status) && status.code >= 500) && !errors.As(err, &network)) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff.next()):
		}
	}
}

func readLocalReplyOnce(ctx context.Context, client *http.Client, origin, id string) (json.RawMessage, error) {
	var reply messageprotocol.MessageReply
	if _, err := requestLocalRuntime(ctx, client, http.MethodGet, origin+messageprotocol.MessagePath+"/"+id+"/reply", nil, &reply, localRuntimeMaxEnvelopeBytes); err != nil {
		return nil, err
	}
	if err := reply.Validate(); err != nil {
		return nil, err
	}
	return reply.Reply, nil
}

func (sandbox localRuntimeClient) Identity() string { return localClientIdentity(sandbox.client) }

func localClientIdentity(client *http.Client) string {
	if transport, ok := client.Transport.(localIdentityTransport); ok {
		return transport.identity
	}
	return ""
}

func (sandbox *nativeLocalSandbox) Lost() bool {
	select {
	case <-sandbox.process.done:
		return true
	case <-time.After(50 * time.Millisecond):
		return false
	}
}

func (sandbox *dockerLocalSandbox) Lost() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := sandbox.runner.Output(ctx, "docker", "inspect", "--format={{.State.Running}}", sandbox.containerID)
	return (err == nil && strings.TrimSpace(string(output)) == "false") || (err != nil && (strings.Contains(string(output), "No such object") || strings.Contains(string(output), "No such container")))
}

func (sandbox localRuntimeClient) Recover(ctx context.Context, request messageprotocol.RecoveryRequest) (uint64, error) {
	return recoverLocalSandbox(ctx, sandbox.client, sandbox.origin, request)
}

func recoverLocalSandbox(ctx context.Context, client *http.Client, origin string, input messageprotocol.RecoveryRequest) (uint64, error) {
	snapshot, err := inspectLocalRuntime(ctx, client, origin)
	if err != nil {
		return 0, err
	}
	reportLocalRuntime(ctx, snapshot)
	if !snapshot.Capabilities.Recovery {
		return 0, localMessageFailure("recovery_unsupported")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute+10*time.Second)
	defer cancel()
	var backoff localPollBackoff
	for {
		receipt, err := requestLocalRecovery(ctx, client, origin, data, input.RecoveryID)
		if err == nil {
			if receipt.State == "failed" {
				return 0, localMessageFailure("recovery_failed")
			}
			return receipt.Generation, nil
		}
		var status *localRuntimeHTTPError
		if errors.As(err, &status) && status.code < 500 {
			return 0, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(backoff.next()):
		}
	}
}

func requestLocalRecovery(ctx context.Context, client *http.Client, origin string, body []byte, id string) (messageprotocol.RecoveryReceipt, error) {
	var receipt messageprotocol.RecoveryReceipt
	if _, err := requestLocalRuntime(ctx, client, http.MethodPost, origin+messageprotocol.RecoveryPath, body, &receipt, localRuntimeMaxEnvelopeBytes); err != nil {
		return receipt, err
	}
	return receipt, receipt.Validate(id)
}
