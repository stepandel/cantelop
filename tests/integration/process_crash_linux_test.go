package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/host/preflight"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/runtime/supervisor"
	"golang.org/x/sys/unix"
)

func TestFirecrackerProcessCrash(t *testing.T) {
	h := newIntegrationEnvironment(t)
	if err := preflight.Check(h.Config); err != nil {
		t.Fatalf("integration host prerequisites failed: %v", err)
	}
	if h.Config.Limits.MaxTotalMemoryMiB < h.MemoryMiB {
		t.Fatalf(
			"integration process-crash test requires limits.max_total_memory_mib >= %d",
			h.MemoryMiB,
		)
	}

	store, err := microvm.OpenSQLiteStore(h.Config.StateDB)
	if err != nil {
		t.Fatalf("open isolated lifecycle store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close isolated lifecycle store: %v", err)
		}
	})
	workspaceID := ensureIntegrationApp(t, store, "process-crash", "process-crash", h.BuildID)
	runtimeSupervisor, err := supervisor.New(h.Config, os.Stderr)
	if err != nil {
		t.Fatalf("construct production supervisor: %v", err)
	}
	if err := runtimeSupervisor.CheckForOrphans(nil); err != nil {
		t.Fatalf("integration host must start without fire-fuse orphans: %v", err)
	}
	lifecycle, err := microvm.NewServiceWithSupervisorAndLimits(
		store,
		runtimeSupervisor,
		microvm.AdmissionLimits{
			MaxVMs:              h.Config.Limits.MaxVMs,
			MaxTotalMemoryMiB:   h.Config.Limits.MaxTotalMemoryMiB,
			MaxConcurrentStarts: h.Config.Limits.MaxConcurrentStarts,
		},
	)
	if err != nil {
		t.Fatalf("construct production lifecycle service: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			time.Duration(h.Config.Timeouts.Cleanup),
		)
		defer cancel()
		if err := lifecycle.TerminateAll(ctx); err != nil {
			t.Errorf("emergency process-crash cleanup: %v", err)
		}
	})

	startCtx, cancelStart := context.WithTimeout(
		context.Background(),
		time.Duration(h.Config.Timeouts.Startup)+time.Second,
	)
	running, err := lifecycle.CreateAndStart(startCtx, "sandbox-process-crash", microvm.CreateSpec{
		AppID: "process-crash", WorkspaceID: workspaceID,
	})
	cancelStart()
	if err != nil {
		t.Fatalf("start real Firecracker microVM: %v", err)
	}
	persisted, err := store.Get(running.ID)
	if err != nil {
		t.Fatalf("read durable running record: %v", err)
	}
	assertRunningOwnership(t, h, runtimeSupervisor, persisted)

	pidfd, err := unix.PidfdOpen(persisted.Runtime.Process.PID, 0)
	if err != nil {
		t.Fatalf("open Firecracker pidfd for failure injection: %v", err)
	}
	t.Cleanup(func() {
		if err := unix.Close(pidfd); err != nil {
			t.Errorf("close failure-injection pidfd: %v", err)
		}
	})
	if err := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0); err != nil {
		t.Fatalf("kill Firecracker through pidfd: %v", err)
	}

	failed := waitForDurableFailure(t, store, running.ID, time.Duration(h.Config.Timeouts.Cleanup))
	if failed.Failure.Reason != microvm.FailureRuntimeExit {
		t.Fatalf("failure reason = %q, want %q", failed.Failure.Reason, microvm.FailureRuntimeExit)
	}
	if failed.Failure.RuntimeExit.PID != persisted.Runtime.Process.PID {
		t.Fatalf(
			"failed runtime PID = %d, want %d",
			failed.Failure.RuntimeExit.PID,
			persisted.Runtime.Process.PID,
		)
	}
	if failed.Failure.RuntimeExit.Signal != unix.SIGKILL.String() {
		t.Fatalf(
			"failed runtime signal = %q, want %q",
			failed.Failure.RuntimeExit.Signal,
			unix.SIGKILL.String(),
		)
	}
	if failed.Runtime != persisted.Runtime {
		t.Fatalf("failed record lost cleanup ownership: %#v", failed.Runtime)
	}

	stopCtx, cancelStop := context.WithTimeout(
		context.Background(),
		time.Duration(h.Config.Timeouts.Cleanup),
	)
	terminated, err := lifecycle.Terminate(stopCtx, running.ID)
	cancelStop()
	if err != nil {
		t.Fatalf("clean up failed Firecracker microVM: %v", err)
	}
	if terminated.State != microvm.StateTerminated {
		t.Fatalf("cleanup lifecycle state = %q, want %q", terminated.State, microvm.StateTerminated)
	}
	assertTerminatedCleanup(t, h, runtimeSupervisor, store, persisted)
}

func waitForDurableFailure(
	t *testing.T,
	store microvm.Store,
	vmID string,
	timeout time.Duration,
) microvm.Sandbox {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		machine, err := store.Get(vmID)
		if err != nil {
			t.Fatalf("read lifecycle record while waiting for process watcher: %v", err)
		}
		if machine.State == microvm.StateFailed {
			return machine
		}

		select {
		case <-timer.C:
			t.Fatalf("lifecycle state remained %q after process crash", machine.State)
		case <-ticker.C:
		}
	}
}
