package microvm

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteStorePersistsLifecycleMutations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	createdAt := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	machine := persistedStartingMachine("vm-lifecycle", createdAt)
	if err := store.Create(machine); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	runtime := persistedRuntimeInfo()
	runningAt := createdAt.Add(time.Second)
	running, err := store.Activate(machine.ID, runtime, runningAt)
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if running.State != StateRunning || running.Network != runtime.Network || running.Runtime != runtime.Ownership {
		t.Errorf("Activate() = %#v, want running machine with runtime ownership", running)
	}
	if !running.StartupCompletedAt.Equal(runningAt) || running.StartupDuration != time.Second {
		t.Errorf("startup measurement = %s / %s, want %s / 1s", running.StartupCompletedAt, running.StartupDuration, runningAt)
	}
	idempotent, err := store.Activate(machine.ID, runtime, runningAt.Add(time.Second))
	if err != nil {
		t.Fatalf("idempotent Activate() error = %v", err)
	}
	if idempotent != running {
		t.Errorf("idempotent Activate() = %#v, want unchanged %#v", idempotent, running)
	}

	stoppingAt := runningAt.Add(time.Second)
	stopping, err := store.Transition(machine.ID, StateStopping, stoppingAt)
	if err != nil {
		t.Fatalf("Transition(stopping) error = %v", err)
	}
	if stopping.Runtime != runtime.Ownership {
		t.Errorf("stopping Runtime = %#v, want ownership preserved", stopping.Runtime)
	}
	terminatedAt := stoppingAt.Add(time.Second)
	terminated, err := store.Transition(machine.ID, StateTerminated, terminatedAt)
	if err != nil {
		t.Fatalf("Transition(terminated) error = %v", err)
	}
	if terminated.Runtime != (RuntimeOwnership{}) {
		t.Errorf("terminated Runtime = %#v, want cleared ownership", terminated.Runtime)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store, err = OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	stored, err := store.Get(machine.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored != terminated {
		t.Errorf("reopened machine = %#v, want %#v", stored, terminated)
	}
}

func TestSQLiteStoreFailPreservesRuntimeOwnership(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	machine := persistedStartingMachine("vm-failed", at)
	if err := store.Create(machine); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	runtime := persistedRuntimeInfo()
	if _, err := store.Activate(machine.ID, runtime, at.Add(time.Second)); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	failure := Failure{
		Reason:      FailureRuntimeExit,
		RuntimeExit: RuntimeExit{PID: runtime.Ownership.Process.PID, ExitCode: 17},
	}
	failed, err := store.Fail(machine.ID, failure, at.Add(2*time.Second))
	if err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if failed.State != StateFailed || failed.Failure != failure || failed.Runtime != runtime.Ownership {
		t.Errorf("Fail() = %#v, want failure with ownership preserved", failed)
	}
	idempotent, err := store.Fail(machine.ID, failure, at.Add(3*time.Second))
	if err != nil {
		t.Fatalf("idempotent Fail() error = %v", err)
	}
	if idempotent != failed {
		t.Errorf("idempotent Fail() = %#v, want unchanged %#v", idempotent, failed)
	}
}

func TestSQLiteStoreMutationErrorsLeaveRecordUnchanged(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	machine := persistedStartingMachine("vm-unchanged", at)
	if err := store.Create(machine); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := store.Transition(machine.ID, StateRunning, at.Add(time.Second)); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("Transition(running) error = %v, want ErrInvalidTransition", err)
	}
	if _, err := store.Transition(machine.ID, StateStopping, at.Add(-time.Second)); !errors.Is(err, ErrInvalidSandboxRecord) {
		t.Errorf("Transition(invalid timestamp) error = %v, want ErrInvalidSandboxRecord", err)
	}
	stored, err := store.Get(machine.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored != machine {
		t.Errorf("stored machine = %#v, want unchanged %#v", stored, machine)
	}
}

func TestSQLiteStoreMutationsReturnNotFound(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for name, operation := range map[string]func() error{
		"activate": func() error {
			_, err := store.Activate("missing", persistedRuntimeInfo(), time.Now())
			return err
		},
		"transition": func() error {
			_, err := store.Transition("missing", StateStopping, time.Now())
			return err
		},
		"fail": func() error {
			_, err := store.Fail("missing", Failure{Reason: FailureRuntimeStart}, time.Now())
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, ErrNotFound) {
				t.Fatalf("operation error = %v, want ErrNotFound", err)
			}
		})
	}
}

func persistedRuntimeInfo() RuntimeInfo {
	network := NetworkAssignment{
		GuestIP: "172.16.0.2", GatewayIP: "172.16.0.1",
		Subnet: "172.16.0.0/30", GuestMAC: "02:fc:ac:10:00:02",
	}
	return RuntimeInfo{
		Network: network,
		Ownership: RuntimeOwnership{
			JailPrepared: true, AddressAllocated: true, TAPCreated: true,
			ForwardingApplied: true, TAPName: "ff0123456789abc", Network: network,
			Process: ProcessIdentity{
				PID: 123, BootID: "01234567-89ab-cdef-0123-456789abcdef", StartTimeTicks: 456,
			},
		},
	}
}
