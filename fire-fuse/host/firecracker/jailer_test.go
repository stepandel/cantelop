package firecracker

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

func TestLauncherStartBuildsJailerCommandAndWaitsForSocket(t *testing.T) {
	layout := testLauncherLayout(t)
	var gotName string
	var gotArgs []string
	launcher := testLauncher(t, func(name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return jailerHelperCommand("socket", layout.HostAPISocket)
	})

	trace := warmuptrace.New()
	ctx := warmuptrace.WithContext(context.Background(), trace)
	process, err := launcher.Start(ctx, layout)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if process.PID() < 1 {
		t.Errorf("PID() = %d, want positive PID", process.PID())
	}
	if gotName != "/usr/bin/jailer" {
		t.Errorf("command = %q, want /usr/bin/jailer", gotName)
	}
	wantArgs := []string{
		"--id", "vm-test",
		"--exec-file", "/usr/bin/firecracker",
		"--uid", "1000",
		"--gid", "1001",
		"--chroot-base-dir", layout.ChrootBaseDir,
		"--cgroup-version", "2",
		"--parent-cgroup", "system.slice/fire-fuse.service/sandboxes/vm-test",
		"--",
		"--api-sock", "/run/firecracker.socket",
		"--enable-pci",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("arguments = %#v, want %#v", gotArgs, wantArgs)
	}
	traceSteps := make(map[string]bool)
	for _, step := range trace.Snapshot().Steps {
		traceSteps[step.Name] = true
	}
	for _, name := range []string{
		"firecracker.launcher.validate",
		"firecracker.launcher.start_process",
		"firecracker.launcher.wait_api_socket",
	} {
		if !traceSteps[name] {
			t.Errorf("warmup trace is missing %q", name)
		}
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := process.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestLauncherStartReportsEarlyExit(t *testing.T) {
	layout := testLauncherLayout(t)
	launcher := testLauncher(t, func(string, ...string) *exec.Cmd {
		return jailerHelperCommand("exit", layout.HostAPISocket)
	})

	_, err := launcher.Start(context.Background(), layout)
	if !errors.Is(err, ErrJailerExited) {
		t.Fatalf("Start() error = %v, want ErrJailerExited", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Start() error = %v, want wrapped exec.ExitError", err)
	}
}

func TestLauncherStartTimesOutAndStopsProcess(t *testing.T) {
	layout := testLauncherLayout(t)
	launcher := testLauncher(t, func(string, ...string) *exec.Cmd {
		return jailerHelperCommand("wait", layout.HostAPISocket)
	})
	launcher.config.SocketTimeout = 30 * time.Millisecond

	_, err := launcher.Start(context.Background(), layout)
	if !errors.Is(err, ErrAPISocketTimeout) {
		t.Fatalf("Start() error = %v, want ErrAPISocketTimeout", err)
	}
}

func TestLauncherStartHonorsCancellation(t *testing.T) {
	layout := testLauncherLayout(t)
	launcher := testLauncher(t, func(string, ...string) *exec.Cmd {
		return jailerHelperCommand("wait", layout.HostAPISocket)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := launcher.Start(ctx, layout)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want context.Canceled", err)
	}
}

func TestProcessStopUsesConfiguredGraceAndForceWindows(t *testing.T) {
	layout := testLauncherLayout(t)
	config := validLauncherConfig()
	config.ShutdownGrace = 30 * time.Millisecond
	config.ShutdownForce = time.Second
	launcher, err := newLauncher(config, func(string, ...string) *exec.Cmd {
		return jailerHelperCommand("socket-ignore-term", layout.HostAPISocket)
	})
	if err != nil {
		t.Fatalf("newLauncher() error = %v", err)
	}
	process, err := launcher.Start(context.Background(), layout)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	started := time.Now()
	if err := process.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed < config.ShutdownGrace/2 {
		t.Fatalf("Stop() elapsed = %s, want configured graceful wait before SIGKILL", elapsed)
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("process completion remained open after Stop returned")
	}
}

func TestProcessKillSkipsGracefulShutdown(t *testing.T) {
	command := exec.Command("sleep", "60")
	if err := command.Start(); err != nil {
		t.Fatalf("start process: %v", err)
	}
	process := newProcess(command, time.Second, time.Second)
	if err := process.Kill(context.Background()); err != nil {
		t.Fatalf("Kill() error = %v", err)
	}
	var exitErr *exec.ExitError
	if err := process.Wait(); !errors.As(err, &exitErr) {
		t.Fatalf("Wait() error = %v, want signal exit", err)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("Wait() status = %#v, want SIGKILL", exitErr.Sys())
	}
}

func TestNewLauncherRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		change func(*LauncherConfig)
	}{
		{name: "relative jailer", change: func(c *LauncherConfig) { c.JailerBin = "bin/jailer" }},
		{name: "relative Firecracker", change: func(c *LauncherConfig) { c.FirecrackerBin = "bin/firecracker" }},
		{name: "root UID", change: func(c *LauncherConfig) { c.UID = 0 }},
		{name: "root GID", change: func(c *LauncherConfig) { c.GID = 0 }},
		{name: "no timeout", change: func(c *LauncherConfig) { c.SocketTimeout = 0 }},
		{name: "no shutdown grace", change: func(c *LauncherConfig) { c.ShutdownGrace = 0 }},
		{name: "no shutdown force", change: func(c *LauncherConfig) { c.ShutdownForce = 0 }},
		{name: "absolute cgroup parent", change: func(c *LauncherConfig) { c.CgroupParent = "/system.slice" }},
		{name: "unclean cgroup parent", change: func(c *LauncherConfig) { c.CgroupParent = "system.slice/../other" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validLauncherConfig()
			test.change(&config)
			if _, err := NewLauncher(config); !errors.Is(err, ErrInvalidLauncherConfig) {
				t.Fatalf("NewLauncher() error = %v, want ErrInvalidLauncherConfig", err)
			}
		})
	}
}

func TestLauncherRejectsMismatchedLayoutExecutable(t *testing.T) {
	baseLayout := testLauncherLayout(t)
	layout, err := NewJailLayout(baseLayout.ChrootBaseDir, "/usr/bin/firecracker-v2", "vm-test")
	if err != nil {
		t.Fatalf("NewJailLayout() error = %v", err)
	}
	launcher := testLauncher(t, func(string, ...string) *exec.Cmd {
		t.Fatal("command factory called for invalid layout")
		return nil
	})

	if _, err := launcher.Start(context.Background(), layout); !errors.Is(err, ErrInvalidLauncherConfig) {
		t.Fatalf("Start() error = %v, want ErrInvalidLauncherConfig", err)
	}
}

func TestLauncherRejectsNonSocketAPIPath(t *testing.T) {
	layout := testLauncherLayout(t)
	if err := os.MkdirAll(filepath.Dir(layout.HostAPISocket), 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(layout.HostAPISocket, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	launcher := testLauncher(t, func(string, ...string) *exec.Cmd {
		return jailerHelperCommand("wait", layout.HostAPISocket)
	})

	if _, err := launcher.Start(context.Background(), layout); !errors.Is(err, ErrAPIPathNotSocket) {
		t.Fatalf("Start() error = %v, want ErrAPIPathNotSocket", err)
	}
}

func TestJailerHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_FIRE_FUSE_JAILER_HELPER") != "1" {
		return
	}

	mode := os.Getenv("FIRE_FUSE_HELPER_MODE")
	socketPath := os.Getenv("FIRE_FUSE_HELPER_SOCKET")
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	switch mode {
	case "exit":
		os.Exit(23)
	case "wait":
		<-signals
		os.Exit(0)
	case "socket":
		if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
			os.Exit(31)
		}
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			os.Exit(32)
		}
		defer listener.Close()
		<-signals
		os.Exit(0)
	case "socket-ignore-term":
		signal.Ignore(syscall.SIGTERM)
		if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
			os.Exit(31)
		}
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			os.Exit(32)
		}
		defer listener.Close()
		select {}
	default:
		os.Exit(30)
	}
}

func jailerHelperCommand(mode, socketPath string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestJailerHelperProcess$")
	cmd.Env = append(
		os.Environ(),
		"GO_WANT_FIRE_FUSE_JAILER_HELPER=1",
		"FIRE_FUSE_HELPER_MODE="+mode,
		"FIRE_FUSE_HELPER_SOCKET="+socketPath,
	)
	return cmd
}

func testLauncher(t *testing.T, command commandFactory) *Launcher {
	t.Helper()
	launcher, err := newLauncher(validLauncherConfig(), command)
	if err != nil {
		t.Fatalf("newLauncher() error = %v", err)
	}
	return launcher
}

func validLauncherConfig() LauncherConfig {
	return LauncherConfig{
		JailerBin:      "/usr/bin/jailer",
		FirecrackerBin: "/usr/bin/firecracker",
		UID:            1000,
		GID:            1001,
		SocketTimeout:  time.Second,
		ShutdownGrace:  time.Second,
		ShutdownForce:  time.Second,
		Output:         io.Discard,
		CgroupParent:   "system.slice/fire-fuse.service/sandboxes",
	}
}

func testLauncherLayout(t *testing.T) JailLayout {
	t.Helper()
	tempDir, err := os.MkdirTemp("/tmp", "fire-fuse-launcher-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	layout, err := NewJailLayout(tempDir, "/usr/bin/firecracker", "vm-test")
	if err != nil {
		t.Fatalf("NewJailLayout() error = %v", err)
	}
	return layout
}
