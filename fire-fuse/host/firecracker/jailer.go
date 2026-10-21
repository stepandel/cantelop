package firecracker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

const (
	socketPollInterval  = 10 * time.Millisecond
	processAbortTimeout = 5 * time.Second
)

var (
	ErrInvalidLauncherConfig = errors.New("invalid Firecracker launcher configuration")
	ErrJailerExited          = errors.New("Firecracker jailer exited before readiness")
	ErrAPISocketTimeout      = errors.New("timed out waiting for Firecracker API socket")
	ErrAPIPathNotSocket      = errors.New("Firecracker API path exists but is not a Unix socket")
)

// LauncherConfig contains the trusted host settings used to invoke jailer.
type LauncherConfig struct {
	JailerBin      string
	FirecrackerBin string
	UID            int
	GID            int
	SocketTimeout  time.Duration
	ShutdownGrace  time.Duration
	ShutdownForce  time.Duration
	Output         io.Writer
	// CgroupParent is cgroup-v2-root-relative and contains one pre-created
	// child per JailLayout.VMID. Empty preserves standalone launcher behavior.
	CgroupParent string
}

type commandFactory func(string, ...string) *exec.Cmd

// Launcher starts jailed Firecracker processes and waits for API readiness.
type Launcher struct {
	config  LauncherConfig
	command commandFactory
}

// NewLauncher validates and constructs a production process launcher.
func NewLauncher(config LauncherConfig) (*Launcher, error) {
	return newLauncher(config, exec.Command)
}

func newLauncher(config LauncherConfig, command commandFactory) (*Launcher, error) {
	if err := validateLauncherConfig(config); err != nil {
		return nil, err
	}
	if command == nil {
		return nil, fmt.Errorf("%w: command factory is required", ErrInvalidLauncherConfig)
	}
	if config.Output == nil {
		config.Output = io.Discard
	}
	return &Launcher{config: config, command: command}, nil
}

// Start launches jailer and returns after the Firecracker API socket is ready.
func (l *Launcher) Start(ctx context.Context, layout JailLayout) (*Process, error) {
	return l.StartObserved(ctx, layout, nil)
}

// StartObserved reports process creation before waiting for API readiness.
// A failed observer aborts the process; callers retain its timing/exit handle.
func (l *Launcher) StartObserved(ctx context.Context, layout JailLayout, onStarted func(*Process) error) (*Process, error) {
	finishValidation := warmuptrace.Start(ctx, "firecracker.launcher.validate")
	if err := layout.validate(); err != nil {
		finishValidation()
		return nil, err
	}
	if filepath.Base(l.config.FirecrackerBin) != layout.ExecutableName {
		finishValidation()
		return nil, fmt.Errorf(
			"%w: layout executable %q does not match %q",
			ErrInvalidLauncherConfig,
			layout.ExecutableName,
			l.config.FirecrackerBin,
		)
	}
	finishValidation()

	args := []string{
		"--id", layout.VMID,
		"--exec-file", l.config.FirecrackerBin,
		"--uid", strconv.Itoa(l.config.UID),
		"--gid", strconv.Itoa(l.config.GID),
		"--chroot-base-dir", layout.ChrootBaseDir,
	}
	if l.config.CgroupParent != "" {
		args = append(args,
			"--cgroup-version", "2",
			"--parent-cgroup", path.Join(l.config.CgroupParent, layout.VMID),
		)
	}
	args = append(args,
		"--",
		"--api-sock", layout.JailedAPISocket,
		"--enable-pci",
	)
	cmd := l.command(l.config.JailerBin, args...)
	cmd.Stdout = l.config.Output
	cmd.Stderr = l.config.Output
	finishProcessStart := warmuptrace.Start(ctx, "firecracker.launcher.start_process")
	if err := cmd.Start(); err != nil {
		finishProcessStart()
		return nil, fmt.Errorf("start Firecracker jailer: %w", err)
	}
	finishProcessStart()

	process := newProcess(cmd, l.config.ShutdownGrace, l.config.ShutdownForce)
	if onStarted != nil {
		if err := onStarted(process); err != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), processAbortTimeout)
			defer cancel()
			return nil, errors.Join(err, wrapProcessAbortError(process.Stop(stopCtx)))
		}
	}
	finishSocket := warmuptrace.Start(ctx, "firecracker.launcher.wait_api_socket")
	if err := waitForAPISocket(ctx, layout.HostAPISocket, l.config.SocketTimeout, process); err != nil {
		finishSocket()
		stopCtx, cancel := context.WithTimeout(context.Background(), processAbortTimeout)
		defer cancel()
		return nil, errors.Join(err, wrapProcessAbortError(process.Stop(stopCtx)))
	}
	finishSocket()
	return process, nil
}

// Process is one supervised jailer/Firecracker operating-system process.
type Process struct {
	startedAt     time.Time
	stoppedAt     time.Time
	cmd           *exec.Cmd
	done          chan struct{}
	err           error
	shutdownGrace time.Duration
	shutdownForce time.Duration
	stopMu        sync.Mutex
}

func newProcess(cmd *exec.Cmd, shutdownGrace, shutdownForce time.Duration) *Process {
	process := &Process{
		startedAt:     time.Now().UTC(),
		cmd:           cmd,
		done:          make(chan struct{}),
		shutdownGrace: shutdownGrace,
		shutdownForce: shutdownForce,
	}
	go func() {
		process.err = cmd.Wait()
		process.stoppedAt = time.Now().UTC()
		close(process.done)
	}()
	return process
}

// StartedAt and StoppedAt are captured at process creation and wait completion,
// before asynchronous lifecycle work. The Done channel publishes StoppedAt.
func (p *Process) StartedAt() time.Time { return p.startedAt }
func (p *Process) StoppedAt() time.Time {
	select {
	case <-p.done:
		return p.stoppedAt
	default:
		return time.Time{}
	}
}

// PID returns the host process ID.
func (p *Process) PID() int {
	return p.cmd.Process.Pid
}

// Done is closed when the process exits.
func (p *Process) Done() <-chan struct{} {
	return p.done
}

// Wait blocks until the process exits and returns its wait result.
func (p *Process) Wait() error {
	<-p.done
	return p.err
}

// Kill simulates an abrupt host-side Firecracker failure. It deliberately
// skips the guest shutdown path used by normal Sandbox termination.
func (p *Process) Kill(ctx context.Context) error {
	p.stopMu.Lock()
	defer p.stopMu.Unlock()

	select {
	case <-p.done:
		return os.ErrProcessDone
	default:
	}
	if err := p.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill Firecracker process: %w", err)
	}
	forceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.shutdownForce)
	defer cancel()
	select {
	case <-p.done:
		return nil
	case <-forceCtx.Done():
		return fmt.Errorf("confirm Firecracker crash after SIGKILL: %w", forceCtx.Err())
	}
}

// Stop sends SIGTERM, waits for the configured grace window, then sends
// SIGKILL and confirms exit within the configured force window. Caller
// cancellation shortens the graceful wait but cannot interrupt confirmation
// after SIGKILL.
func (p *Process) Stop(ctx context.Context) error {
	p.stopMu.Lock()
	defer p.stopMu.Unlock()

	select {
	case <-p.done:
		return nil
	default:
	}

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal Firecracker process: %w", err)
	}
	graceCtx, cancelGrace := context.WithTimeout(ctx, p.shutdownGrace)
	select {
	case <-p.done:
		cancelGrace()
		return nil
	case <-graceCtx.Done():
	}
	cancelGrace()

	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill Firecracker process: %w", err)
	}
	forceCtx, cancelForce := context.WithTimeout(context.WithoutCancel(ctx), p.shutdownForce)
	defer cancelForce()
	select {
	case <-p.done:
		return nil
	case <-forceCtx.Done():
		return fmt.Errorf("confirm Firecracker exit after SIGKILL: %w", forceCtx.Err())
	}
}

func waitForAPISocket(ctx context.Context, socketPath string, timeout time.Duration, process *Process) error {
	ticker := time.NewTicker(socketPollInterval)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		ready, err := isUnixSocket(socketPath)
		if err != nil {
			return fmt.Errorf("inspect Firecracker API socket: %w", err)
		}
		if ready {
			select {
			case <-process.Done():
				return errors.Join(ErrJailerExited, process.Wait())
			default:
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return ErrAPISocketTimeout
		case <-process.Done():
			return errors.Join(ErrJailerExited, process.Wait())
		case <-ticker.C:
		}
	}
}

func isUnixSocket(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return false, ErrAPIPathNotSocket
	}
	return true, nil
}

func validateLauncherConfig(config LauncherConfig) error {
	for name, path := range map[string]string{
		"jailer binary":      config.JailerBin,
		"Firecracker binary": config.FirecrackerBin,
	} {
		if err := validateCleanAbsolutePath(path); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidLauncherConfig, name, err)
		}
		if path == string(filepath.Separator) {
			return fmt.Errorf("%w: %s cannot be filesystem root", ErrInvalidLauncherConfig, name)
		}
	}
	if !executableNamePattern.MatchString(filepath.Base(config.FirecrackerBin)) {
		return fmt.Errorf("%w: invalid Firecracker executable name", ErrInvalidLauncherConfig)
	}
	if config.UID < 1 || config.GID < 1 {
		return fmt.Errorf("%w: jail identity must be non-root", ErrInvalidLauncherConfig)
	}
	if config.SocketTimeout <= 0 || config.ShutdownGrace <= 0 || config.ShutdownForce <= 0 {
		return fmt.Errorf("%w: socket and shutdown timeouts must be positive", ErrInvalidLauncherConfig)
	}
	if config.CgroupParent != "" &&
		(path.IsAbs(config.CgroupParent) || path.Clean(config.CgroupParent) != config.CgroupParent || config.CgroupParent == ".") {
		return fmt.Errorf("%w: cgroup parent must be clean and relative", ErrInvalidLauncherConfig)
	}
	return nil
}

func wrapProcessAbortError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("abort Firecracker process after startup failure: %w", err)
}
