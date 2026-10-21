package supervisor

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
)

func TestSupervisorStopFallsBackAfterGuestShutdownFailure(t *testing.T) {
	process := newFakeProcess()
	wantErr := errors.New("Ctrl+Alt+Del rejected")
	client := &fakeControlClient{shutdownErr: wantErr}
	var observations []workspaceLifecycleObservation
	supervisor := newSupervisor(testConfig(), dependencies{
		newControlClient: func(string) (controlClient, error) { return client, nil },
		recordWorkspace: func(observation workspaceLifecycleObservation) {
			observations = append(observations, observation)
		},
	})
	slot, unlock := supervisor.lockSlot("vm-test")
	slot.setInstance(&instance{
		workspaceEnabled: true,
		process:          process,
	})
	unlock()

	if err := supervisor.Stop(
		context.Background(),
		testMachine(),
		discardRuntimeCheckpoint,
	); err != nil {
		t.Fatalf("Stop() error = %v, want successful forced fallback", err)
	}
	if client.shutdownCalls != 1 || process.stopCalls != 1 {
		t.Errorf(
			"shutdown/forced stop calls = %d/%d, want 1/1",
			client.shutdownCalls,
			process.stopCalls,
		)
	}
	if len(observations) != 1 ||
		observations[0].Event != "shutdown" ||
		observations[0].Outcome != "forced" ||
		observations[0].Duration <= 0 ||
		!errors.Is(observations[0].Err, wantErr) {
		t.Errorf("workspace lifecycle observations = %#v, want one forced shutdown", observations)
	}
}

func TestSupervisorStopFallsBackAfterGuestShutdownTimeout(t *testing.T) {
	cfg := testConfig()
	cfg.Timeouts.GuestShutdown = config.Duration(5 * time.Millisecond)
	process := newFakeProcess()
	client := &fakeControlClient{}
	supervisor := newSupervisor(cfg, dependencies{
		newControlClient: func(string) (controlClient, error) { return client, nil },
	})
	slot, unlock := supervisor.lockSlot("vm-test")
	slot.setInstance(&instance{
		workspaceEnabled: true,
		process:          process,
	})
	unlock()

	if err := supervisor.Stop(
		context.Background(),
		testMachine(),
		discardRuntimeCheckpoint,
	); err != nil {
		t.Fatalf("Stop() error = %v, want successful forced fallback", err)
	}
	if client.shutdownCalls != 1 || process.stopCalls != 1 {
		t.Errorf(
			"shutdown/forced stop calls = %d/%d, want 1/1",
			client.shutdownCalls,
			process.stopCalls,
		)
	}
}

func TestSupervisorDetectsUnexpectedProcessExit(t *testing.T) {
	process := newFakeProcess()
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout:        firecracker.NewJailLayout,
		prepare:          func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
		cleanup:          func(firecracker.JailLayout) error { return nil },
		launch:           func(context.Context, firecracker.JailLayout) (managedProcess, error) { return process, nil },
		newControlClient: func(string) (controlClient, error) { return &fakeControlClient{}, nil },
	})

	if _, err := supervisor.Start(context.Background(), testMachine(), discardRuntimeCheckpoint); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	close(process.done)
	if supervisor.IsRunning(testMachine().ID) {
		t.Error("IsRunning() = true after unexpected process exit")
	}
	exit, exists := supervisor.RuntimeExit(testMachine().ID)
	if !exists {
		t.Fatal("RuntimeExit() did not find exited process")
	}
	if exit.PID != process.PID() || exit.ExitCode != 0 || exit.Signal != "" {
		t.Errorf("RuntimeExit() = %#v, want PID %d and exit code 0", exit, process.PID())
	}
}

func TestNewRuntimeExitClassifiesExitCodeAndSignal(t *testing.T) {
	exitCodeErr := exec.Command("sh", "-c", "exit 23").Run()
	exitCodeProcess := newFakeProcess()
	exitCodeProcess.waitErr = exitCodeErr
	close(exitCodeProcess.done)
	if exit := newRuntimeExit(exitCodeProcess); exit.ExitCode != 23 || exit.Signal != "" {
		t.Errorf("exit-code result = %#v, want exit code 23", exit)
	}

	signalErr := exec.Command("sh", "-c", "kill -TERM $$").Run()
	signalProcess := newFakeProcess()
	signalProcess.waitErr = signalErr
	close(signalProcess.done)
	if exit := newRuntimeExit(signalProcess); exit.Signal != syscall.SIGTERM.String() {
		t.Errorf("signal result = %#v, want signal %q", exit, syscall.SIGTERM.String())
	}
}

func TestSupervisorStopDoesNotCleanupLiveProcess(t *testing.T) {
	wantErr := errors.New("could not stop")
	process := newFakeProcess()
	process.stopErr = wantErr
	process.stayAlive = true
	cleaned := 0
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare:   func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
		cleanup:   func(firecracker.JailLayout) error { cleaned++; return nil },
		launch:    func(context.Context, firecracker.JailLayout) (managedProcess, error) { return process, nil },
		newControlClient: func(string) (controlClient, error) {
			return &fakeControlClient{shutdownErr: errors.New("guest shutdown unavailable")}, nil
		},
	})
	if _, err := supervisor.Start(context.Background(), testMachine(), discardRuntimeCheckpoint); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if err := supervisor.Stop(context.Background(), testMachine(), discardRuntimeCheckpoint); !errors.Is(err, wantErr) {
		t.Fatalf("Stop() error = %v, want stop error", err)
	}
	if cleaned != 0 {
		t.Errorf("cleanup called %d times while process is alive", cleaned)
	}
}

func TestSupervisorGracefullyShutsDownWorkloadWithoutWorkspace(t *testing.T) {
	process := newFakeProcess()
	client := &fakeControlClient{onShutdown: func() { close(process.done) }}
	supervisor := newSupervisor(testConfig(), dependencies{
		newControlClient: func(string) (controlClient, error) { return client, nil },
	})
	slot, unlock := supervisor.lockSlot("vm-test")
	slot.setInstance(&instance{
		process: process,
	})
	unlock()

	if err := supervisor.Stop(
		context.Background(),
		testMachine(),
		discardRuntimeCheckpoint,
	); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if client.shutdownCalls != 1 {
		t.Errorf("graceful guest shutdown calls = %d, want 1", client.shutdownCalls)
	}
	if process.stopCalls != 0 {
		t.Errorf("forced process stop calls = %d, want 0", process.stopCalls)
	}
}
