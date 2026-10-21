package microvm

import (
	"context"
	"errors"
	"testing"
	"time"
)

type failCheckpointStore struct {
	*SQLiteStore
	err error
}

func (s failCheckpointStore) CheckpointRuntime(
	string,
	RuntimeOwnership,
	time.Time,
) (Sandbox, error) {
	return Sandbox{}, s.err
}

func TestServicePersistsSupervisorStartCheckpoints(t *testing.T) {
	store := openTestSQLiteStore(t)
	at := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	machine := testSandbox(Sandbox{ID: "vm-checkpoint-start", State: StateStarting})
	if err := store.Create(machine); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	runtime := persistedRuntimeInfo()
	jail := RuntimeOwnership{JailPrepared: true}
	supervisor := fakeSupervisor{
		runtime: runtime,
		startWithCheckpoint: func(_ context.Context, received Sandbox, checkpoint RuntimeCheckpoint) error {
			if received != machine {
				t.Errorf("Start() machine = %#v, want %#v", received, machine)
			}
			if err := checkpoint(jail); err != nil {
				return err
			}
			stored, err := store.Get(machine.ID)
			if err != nil {
				return err
			}
			if stored.State != StateStarting || stored.Runtime != jail {
				t.Errorf("first checkpoint stored %#v, want starting with jail ownership", stored)
			}
			return checkpoint(runtime.Ownership)
		},
		stop: func(context.Context, Sandbox) error {
			t.Fatal("Stop() called after successful activation")
			return nil
		},
	}
	service := newService(store, supervisor, func() time.Time { return at })

	running, err := service.Start(context.Background(), machine.ID)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if running.State != StateRunning || running.Runtime != runtime.Ownership {
		t.Errorf("Start() = %#v, want running with final ownership", running)
	}
}

func TestServiceAtomicallyClaimsPreparedHostSlot(t *testing.T) {
	store := openTestSQLiteStore(t)
	at := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	machine := testSandbox(Sandbox{ID: "vm-host-slot", State: StateStarting})
	if err := store.Create(machine); err != nil {
		t.Fatal(err)
	}
	ownership := reusableTestOwnership("slot-01")
	slot := HostSlot{
		ID: "slot-01", Profile: "ubuntu-24", State: HostSlotReady,
		Runtime: ownership, CreatedAt: at, UpdatedAt: at,
	}
	if err := store.CreateHostSlot(slot); err != nil {
		t.Fatal(err)
	}
	finalOwnership := ownership
	finalOwnership.Process = persistedRuntimeInfo().Ownership.Process
	runtime := RuntimeInfo{Network: ownership.Network, Ownership: finalOwnership}
	supervisor := fakeSupervisor{
		runtime: runtime,
		startWithCheckpoint: func(_ context.Context, _ Sandbox, checkpoint RuntimeCheckpoint) error {
			if err := checkpoint(ownership); err != nil {
				return err
			}
			persisted, err := store.Get(machine.ID)
			if err != nil {
				return err
			}
			if persisted.Runtime != ownership {
				t.Errorf("claimed runtime = %#v", persisted.Runtime)
			}
			slots, err := store.ListHostSlots()
			if err != nil {
				return err
			}
			if len(slots) != 0 {
				t.Errorf("slots after claim = %#v", slots)
			}
			return checkpoint(finalOwnership)
		},
		stop: func(context.Context, Sandbox) error { return nil },
	}
	service := newService(store, supervisor, func() time.Time { return at })
	running, err := service.Start(context.Background(), machine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running.Runtime != finalOwnership || running.State != StateRunning {
		t.Fatalf("running machine = %#v", running)
	}
}

func TestServiceClassifiesStartCheckpointFailureAsStateStore(t *testing.T) {
	checkpointErr := errors.New("checkpoint unavailable")
	base := openTestSQLiteStore(t)
	store := failCheckpointStore{SQLiteStore: base, err: checkpointErr}
	machine := testSandbox(Sandbox{ID: "vm-checkpoint-failure", State: StateStarting})
	if err := store.Create(machine); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	supervisor := fakeSupervisor{
		startWithCheckpoint: func(_ context.Context, _ Sandbox, checkpoint RuntimeCheckpoint) error {
			return checkpoint(RuntimeOwnership{JailPrepared: true})
		},
		stop: func(context.Context, Sandbox) error { return nil },
	}
	service := NewServiceWithSupervisor(store, supervisor)

	failed, err := service.Start(context.Background(), machine.ID)
	if !errors.Is(err, ErrStateStore) || !errors.Is(err, checkpointErr) {
		t.Fatalf("Start() error = %v, want state-store checkpoint failure", err)
	}
	if failed.State != StateFailed || failed.Failure.Reason != FailureRuntimeStart {
		t.Errorf("Start() machine = %#v, want failed startup record", failed)
	}
}

func TestServicePersistsSupervisorStopCheckpoints(t *testing.T) {
	store := openTestSQLiteStore(t)
	at := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	machine := testSandbox(Sandbox{
		ID: "vm-checkpoint-stop", State: StateRunning,
	})
	if err := store.Create(machine); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	supervisor := fakeSupervisor{
		stopWithCheckpoint: func(_ context.Context, _ Sandbox, checkpoint RuntimeCheckpoint) error {
			if err := checkpoint(RuntimeOwnership{}); err != nil {
				return err
			}
			stored, err := store.Get(machine.ID)
			if err != nil {
				return err
			}
			if stored.State != StateStopping || stored.Runtime != (RuntimeOwnership{}) {
				t.Errorf("stop checkpoint stored %#v, want stopping without ownership", stored)
			}
			return nil
		},
	}
	service := newService(store, supervisor, func() time.Time { return at })

	terminated, err := service.Terminate(context.Background(), machine.ID)
	if err != nil {
		t.Fatalf("Terminate() error = %v", err)
	}
	if terminated.State != StateTerminated || terminated.Runtime != (RuntimeOwnership{}) {
		t.Errorf("Terminate() = %#v, want terminated without ownership", terminated)
	}
}

func TestServiceClassifiesStopCheckpointFailureAsStateStore(t *testing.T) {
	checkpointErr := errors.New("checkpoint unavailable")
	base := openTestSQLiteStore(t)
	store := failCheckpointStore{SQLiteStore: base, err: checkpointErr}
	machine := testSandbox(Sandbox{ID: "vm-stop-checkpoint-failure", State: StateRunning})
	if err := store.Create(machine); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	supervisor := fakeSupervisor{
		stopWithCheckpoint: func(_ context.Context, _ Sandbox, checkpoint RuntimeCheckpoint) error {
			return checkpoint(RuntimeOwnership{})
		},
	}
	service := NewServiceWithSupervisor(store, supervisor)

	_, err := service.Terminate(context.Background(), machine.ID)
	if !errors.Is(err, ErrStateStore) || !errors.Is(err, checkpointErr) {
		t.Fatalf("Terminate() error = %v, want state-store checkpoint failure", err)
	}
	if errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("Terminate() error = %v, must not classify persistence as runtime failure", err)
	}
	stored, getErr := store.Get(machine.ID)
	if getErr != nil {
		t.Fatalf("Get() error = %v", getErr)
	}
	if stored.State != StateStopping {
		t.Errorf("stored State = %q, want %q for retry", stored.State, StateStopping)
	}
}
