package microvm

import (
	"errors"
	"testing"
	"time"
)

func TestStoreCreateOrGet(t *testing.T) {
	store := openTestSQLiteStore(t)
	machine := testSandbox(Sandbox{ID: "sandbox-first", Profile: "small", State: StateStarting})

	created, isNew, err := store.CreateOrGet(machine)
	if err != nil || !isNew || created != machine {
		t.Fatalf("first CreateOrGet() = (%#v, %t, %v), want created sandbox", created, isNew, err)
	}
	replayed, isNew, err := store.CreateOrGet(machine)
	if err != nil || isNew || replayed != machine {
		t.Fatalf("replayed CreateOrGet() = (%#v, %t, %v), want original sandbox", replayed, isNew, err)
	}

	conflict := machine
	conflict.Profile = "large"
	if _, _, err := store.CreateOrGet(conflict); !errors.Is(err, ErrSandboxConflict) {
		t.Fatalf("conflicting CreateOrGet() error = %v, want ErrSandboxConflict", err)
	}
}

func TestStoreLifecycle(t *testing.T) {
	store := openTestSQLiteStore(t)
	createdAt := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	machine := testSandbox(Sandbox{
		ID:        "vm-001",
		Profile:   "ubuntu-24",
		VCPUs:     1,
		MemoryMiB: 256,
		State:     StateStarting,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	})

	if err := store.Create(machine); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.Create(machine); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second Create() error = %v, want ErrAlreadyExists", err)
	}
	if _, err := store.Transition(machine.ID, StateRunning, createdAt.Add(time.Second)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition(running) error = %v, want ErrInvalidTransition", err)
	}

	runningAt := createdAt.Add(time.Second)
	runtime := persistedRuntimeInfo()
	running, err := store.Activate(machine.ID, runtime, runningAt)
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if running.State != StateRunning {
		t.Errorf("State = %q, want %q", running.State, StateRunning)
	}
	if !running.UpdatedAt.Equal(runningAt) {
		t.Errorf("UpdatedAt = %s, want %s", running.UpdatedAt, runningAt)
	}
	if !running.StartupCompletedAt.Equal(runningAt) || running.StartupDuration != time.Second {
		t.Errorf("startup measurement = %s / %s, want %s / 1s", running.StartupCompletedAt, running.StartupDuration, runningAt)
	}

	unchanged, err := store.Activate(machine.ID, runtime, runningAt.Add(time.Second))
	if err != nil {
		t.Fatalf("idempotent Activate() error = %v", err)
	}
	if !unchanged.UpdatedAt.Equal(runningAt) {
		t.Errorf("idempotent transition changed UpdatedAt to %s", unchanged.UpdatedAt)
	}
	if unchanged.StartupCompletedAt != running.StartupCompletedAt || unchanged.StartupDuration != running.StartupDuration {
		t.Errorf("idempotent transition changed startup measurement to %#v", unchanged)
	}

	if _, err := store.Transition(machine.ID, StateTerminated, runningAt); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("invalid Transition() error = %v, want ErrInvalidTransition", err)
	}

	stored, err := store.Get(machine.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.State != StateRunning {
		t.Errorf("stored State = %q, want %q", stored.State, StateRunning)
	}
	if stored.Network != runtime.Network {
		t.Errorf("stored Network = %#v, want %#v", stored.Network, runtime.Network)
	}
	if stored.Runtime != runtime.Ownership {
		t.Errorf("stored Runtime = %#v, want %#v", stored.Runtime, runtime.Ownership)
	}
}

func TestStoreListIsStable(t *testing.T) {
	store := openTestSQLiteStore(t)
	for _, id := range []string{"vm-b", "vm-a"} {
		if err := store.Create(testSandbox(Sandbox{ID: id, State: StateStarting})); err != nil {
			t.Fatalf("Create(%q) error = %v", id, err)
		}
	}

	machines, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := len(machines), 2; got != want {
		t.Fatalf("len(List()) = %d, want %d", got, want)
	}
	if machines[0].ID != "vm-a" || machines[1].ID != "vm-b" {
		t.Fatalf("List() IDs = [%q, %q], want [vm-a, vm-b]", machines[0].ID, machines[1].ID)
	}
}

func TestStoreNotFound(t *testing.T) {
	store := openTestSQLiteStore(t)

	if _, err := store.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
	if _, err := store.Activate("missing", RuntimeInfo{}, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Activate() error = %v, want ErrNotFound", err)
	}
	if _, err := store.Transition("missing", StateRunning, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Transition() error = %v, want ErrNotFound", err)
	}
	if _, err := store.Fail("missing", Failure{Reason: FailureRuntimeExit}, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Fail() error = %v, want ErrNotFound", err)
	}
}

func TestStoreRejectsInvalidRuntimeOwnership(t *testing.T) {
	store := openTestSQLiteStore(t)
	machine := testSandbox(Sandbox{ID: "vm-invalid-runtime", State: StateStarting})
	if err := store.Create(machine); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	_, err := store.Activate(machine.ID, RuntimeInfo{Ownership: RuntimeOwnership{
		TAPCreated: true,
		TAPName:    "ff0123456789abc",
	}}, time.Now())
	if !errors.Is(err, ErrInvalidRuntimeOwnership) {
		t.Fatalf("Activate() error = %v, want ErrInvalidRuntimeOwnership", err)
	}

	stored, err := store.Get(machine.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored != machine {
		t.Errorf("stored machine = %#v, want unchanged %#v", stored, machine)
	}
}

func TestStoreRecordsFailure(t *testing.T) {
	store := openTestSQLiteStore(t)
	if err := store.Create(testSandbox(Sandbox{ID: "vm-failed", State: StateRunning})); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	at := time.Now().UTC()
	failure := Failure{
		Reason:      FailureRuntimeExit,
		RuntimeExit: RuntimeExit{PID: 123, ExitCode: 17},
	}

	failed, err := store.Fail("vm-failed", failure, at)
	if err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if failed.State != StateFailed || failed.Failure != failure {
		t.Errorf("Fail() = %#v, want failed state with %#v", failed, failure)
	}
	if !failed.UpdatedAt.Equal(at) {
		t.Errorf("UpdatedAt = %s, want %s", failed.UpdatedAt, at)
	}
	unchanged, err := store.Fail("vm-failed", failure, at.Add(time.Second))
	if err != nil {
		t.Fatalf("second Fail() error = %v", err)
	}
	if unchanged != failed {
		t.Errorf("idempotent Fail() = %#v, want %#v", unchanged, failed)
	}
}
