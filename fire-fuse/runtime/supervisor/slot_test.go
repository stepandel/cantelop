package supervisor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
)

func TestSupervisorRemovesSlotAfterRuntimeIsCleanedUp(t *testing.T) {
	process := newFakeProcess()
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout:        firecracker.NewJailLayout,
		prepare:          func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
		cleanup:          func(firecracker.JailLayout) error { return nil },
		launch:           func(context.Context, firecracker.JailLayout) (managedProcess, error) { return process, nil },
		newControlClient: func(string) (controlClient, error) { return &fakeControlClient{}, nil },
	})
	machine := testMachine()

	if _, err := supervisor.Start(context.Background(), machine, discardRuntimeCheckpoint); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if supervisor.lookupSlot(machine.ID) == nil {
		t.Fatal("running sandbox has no operation slot")
	}
	if err := supervisor.Stop(context.Background(), machine, discardRuntimeCheckpoint); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if got := slotCount(supervisor); got != 0 {
		t.Fatalf("slots after Stop = %d, want 0", got)
	}
}

func TestSupervisorReadPathsDoNotCreateSlots(t *testing.T) {
	supervisor := newSupervisor(testConfig(), dependencies{})

	if supervisor.IsRunning("vm-unknown") {
		t.Error("IsRunning() = true for unknown ID")
	}
	if _, exists := supervisor.RuntimeDone("vm-unknown"); exists {
		t.Error("RuntimeDone() found unknown ID")
	}
	if _, exists := supervisor.RuntimeExit("vm-unknown"); exists {
		t.Error("RuntimeExit() found unknown ID")
	}
	if _, err := supervisor.RuntimeUsage("vm-unknown"); !errors.Is(err, ErrRuntimeUsageUnavailable) {
		t.Errorf("RuntimeUsage() error = %v, want ErrRuntimeUsageUnavailable", err)
	}
	supervisor.startMemoryController("vm-unknown")
	if err := supervisor.Stop(context.Background(), testMachine(), discardRuntimeCheckpoint); err != nil {
		t.Errorf("Stop() of untracked ID error = %v", err)
	}
	if got := slotCount(supervisor); got != 0 {
		t.Fatalf("slots = %d, want 0", got)
	}
}

func TestSupervisorKeepsSlotWhoseCleanupFailed(t *testing.T) {
	cleanupErr := errors.New("filesystem busy")
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare:   func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
		cleanup:   func(firecracker.JailLayout) error { return cleanupErr },
		launch: func(context.Context, firecracker.JailLayout) (managedProcess, error) {
			return nil, errors.New("launch failed")
		},
	})

	if _, err := supervisor.Start(context.Background(), testMachine(), discardRuntimeCheckpoint); !errors.Is(err, cleanupErr) {
		t.Fatalf("Start() error = %v, want cleanup error", err)
	}
	slot := supervisor.lookupSlot(testMachine().ID)
	if slot == nil {
		t.Fatal("slot with uncleaned resources was removed")
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.instance == nil || !slot.instance.jailPrepared {
		t.Fatalf("retained instance = %#v, want prepared jail for Stop retry", slot.instance)
	}
}

func TestSupervisorKeepsSlotForWaitingOperation(t *testing.T) {
	supervisor := newSupervisor(testConfig(), dependencies{})
	first, unlockFirst := supervisor.lockSlot("vm-test")

	acquired := make(chan *operationSlot)
	release := make(chan struct{})
	go func() {
		second, unlockSecond := supervisor.lockSlot("vm-test")
		second.setInstance(&instance{process: newFakeProcess()})
		acquired <- second
		<-release
		unlockSecond()
	}()
	waitForSlotRefs(t, supervisor, "vm-test", 2)

	// The first holder tracks no runtime, but the waiter must keep the slot.
	unlockFirst()
	second := <-acquired
	if second != first {
		t.Fatal("waiting operation received a different slot")
	}
	close(release)
	waitForSlotRefs(t, supervisor, "vm-test", 0)
	if supervisor.lookupSlot("vm-test") != first {
		t.Fatal("slot tracking a runtime was removed")
	}
	if !supervisor.IsRunning("vm-test") {
		t.Fatal("IsRunning() = false for runtime published by waiting operation")
	}
}

func TestSupervisorRuntimeReadsDoNotWaitForLifecycleOperation(t *testing.T) {
	supervisor := newSupervisor(testConfig(), dependencies{})
	process := newFakeProcess()
	slot, unlock := supervisor.lockSlot("vm-test")
	slot.setInstance(&instance{process: process})
	// Keep the slot locked as a long Stop or Start would.
	defer unlock()

	observed := make(chan struct{})
	go func() {
		defer close(observed)
		if !supervisor.IsRunning("vm-test") {
			t.Error("IsRunning() = false for live process")
		}
		if done, exists := supervisor.RuntimeDone("vm-test"); !exists || done != process.Done() {
			t.Error("RuntimeDone() did not return the tracked process signal")
		}
		if _, exists := supervisor.RuntimeExit("vm-test"); exists {
			t.Error("RuntimeExit() reported a live process")
		}
		if _, err := supervisor.SyncRuntimeTiming("vm-test"); err != nil {
			t.Errorf("SyncRuntimeTiming() error = %v", err)
		}
	}()
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("runtime reads waited for the lifecycle lock")
	}

	close(process.done)
	if supervisor.IsRunning("vm-test") {
		t.Error("IsRunning() = true after process exit")
	}
	if _, exists := supervisor.RuntimeExit("vm-test"); !exists {
		t.Error("RuntimeExit() did not report the exited process")
	}
}

func slotCount(supervisor *Supervisor) int {
	supervisor.slotsMu.Lock()
	defer supervisor.slotsMu.Unlock()
	return len(supervisor.slots)
}

func waitForSlotRefs(t *testing.T, supervisor *Supervisor, id string, refs int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		supervisor.slotsMu.Lock()
		slot := supervisor.slots[id]
		current := 0
		if slot != nil {
			current = slot.refs
		}
		supervisor.slotsMu.Unlock()
		if current == refs {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d references to slot %s", refs, id)
}
