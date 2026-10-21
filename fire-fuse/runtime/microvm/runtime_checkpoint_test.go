package microvm

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStoresCheckpointPartialRuntimeOwnership(t *testing.T) {
	for name, newStore := range checkpointStoreFactories() {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			createdAt := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
			machine := persistedStartingMachine("vm-checkpoint", createdAt)
			if err := store.Create(machine); err != nil {
				t.Fatalf("Create() error = %v", err)
			}

			jail := RuntimeOwnership{JailPrepared: true}
			checkpointed, err := store.CheckpointRuntime(machine.ID, jail, createdAt.Add(time.Second))
			if err != nil {
				t.Fatalf("CheckpointRuntime(jail) error = %v", err)
			}
			if checkpointed.State != StateStarting || checkpointed.Network != (NetworkAssignment{}) || checkpointed.Runtime != jail {
				t.Errorf("checkpointed machine = %#v, want starting with host-only jail ownership", checkpointed)
			}

			allocation := persistedRuntimeInfo().Ownership
			allocation.TAPCreated = false
			allocation.ForwardingApplied = false
			allocation.TAPName = ""
			allocation.Process = ProcessIdentity{}
			checkpointed, err = store.CheckpointRuntime(machine.ID, allocation, createdAt.Add(2*time.Second))
			if err != nil {
				t.Fatalf("CheckpointRuntime(allocation) error = %v", err)
			}
			if checkpointed.Runtime != allocation {
				t.Errorf("checkpointed Runtime = %#v, want %#v", checkpointed.Runtime, allocation)
			}

			unchanged, err := store.CheckpointRuntime(machine.ID, allocation, createdAt.Add(3*time.Second))
			if err != nil {
				t.Fatalf("idempotent CheckpointRuntime() error = %v", err)
			}
			if unchanged != checkpointed {
				t.Errorf("idempotent checkpoint = %#v, want unchanged %#v", unchanged, checkpointed)
			}

			rolledBack, err := store.CheckpointRuntime(machine.ID, jail, createdAt.Add(4*time.Second))
			if err != nil {
				t.Fatalf("CheckpointRuntime(rollback) error = %v", err)
			}
			if rolledBack.Runtime != jail {
				t.Errorf("rolled-back Runtime = %#v, want %#v", rolledBack.Runtime, jail)
			}
		})
	}
}

func TestStoresRejectInvalidRuntimeCheckpointWithoutMutation(t *testing.T) {
	for name, newStore := range checkpointStoreFactories() {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			at := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
			machine := persistedStartingMachine("vm-invalid-checkpoint", at)
			if err := store.Create(machine); err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			invalid := RuntimeOwnership{TAPCreated: true, TAPName: "ff0123456789abc"}
			if _, err := store.CheckpointRuntime(machine.ID, invalid, at.Add(time.Second)); !errors.Is(err, ErrInvalidRuntimeOwnership) {
				t.Fatalf("CheckpointRuntime() error = %v, want ErrInvalidRuntimeOwnership", err)
			}
			stored, err := store.Get(machine.ID)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if stored != machine {
				t.Errorf("stored machine = %#v, want unchanged %#v", stored, machine)
			}
		})
	}
}

func TestStoresRestrictRuntimeCheckpointStates(t *testing.T) {
	for name, newStore := range checkpointStoreFactories() {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			at := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
			machine := persistedStartingMachine("vm-checkpoint-state", at)
			if err := store.Create(machine); err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			runtime := persistedRuntimeInfo()
			if _, err := store.Activate(machine.ID, runtime, at.Add(time.Second)); err != nil {
				t.Fatalf("Activate() error = %v", err)
			}
			if _, err := store.CheckpointRuntime(machine.ID, runtime.Ownership, at.Add(2*time.Second)); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("CheckpointRuntime(running) error = %v, want ErrInvalidTransition", err)
			}

			if _, err := store.Transition(machine.ID, StateStopping, at.Add(2*time.Second)); err != nil {
				t.Fatalf("Transition(stopping) error = %v", err)
			}
			if _, err := store.CheckpointRuntime(machine.ID, RuntimeOwnership{}, at.Add(3*time.Second)); err != nil {
				t.Fatalf("CheckpointRuntime(stopping) error = %v", err)
			}
			if _, err := store.Transition(machine.ID, StateTerminated, at.Add(4*time.Second)); err != nil {
				t.Fatalf("Transition(terminated) error = %v", err)
			}
			if _, err := store.CheckpointRuntime(machine.ID, RuntimeOwnership{}, at.Add(5*time.Second)); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("CheckpointRuntime(terminated) error = %v, want ErrInvalidTransition", err)
			}
		})
	}
}

func TestStoresCheckpointRuntimeReturnsNotFound(t *testing.T) {
	for name, newStore := range checkpointStoreFactories() {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			if _, err := store.CheckpointRuntime("missing", RuntimeOwnership{}, time.Now()); !errors.Is(err, ErrNotFound) {
				t.Fatalf("CheckpointRuntime() error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestStoresCheckpointWorkspaceNFSOwnership(t *testing.T) {
	for name, newStore := range checkpointStoreFactories() {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			at := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
			machine := persistedStartingMachine("vm-workspace-checkpoint", at)
			if err := store.Create(machine); err != nil {
				t.Fatalf("Create() error = %v", err)
			}

			ownership := persistedRuntimeInfo().Ownership
			ownership.Process = ProcessIdentity{}
			ownership.WorkspaceExported = true
			ownership.WorkspaceNFSAllowed = false
			exported, err := store.CheckpointRuntime(
				machine.ID,
				ownership,
				at.Add(time.Second),
			)
			if err != nil {
				t.Fatalf("CheckpointRuntime(export) error = %v", err)
			}
			if !exported.Runtime.WorkspaceExported ||
				exported.Runtime.WorkspaceNFSAllowed {
				t.Fatalf(
					"export checkpoint runtime = %#v, want export only",
					exported.Runtime,
				)
			}

			ownership.WorkspaceNFSAllowed = true
			allowed, err := store.CheckpointRuntime(
				machine.ID,
				ownership,
				at.Add(2*time.Second),
			)
			if err != nil {
				t.Fatalf("CheckpointRuntime(access) error = %v", err)
			}
			if allowed.Runtime != ownership {
				t.Fatalf(
					"access checkpoint runtime = %#v, want %#v",
					allowed.Runtime,
					ownership,
				)
			}
		})
	}
}

func TestSQLiteStorePersistsRuntimeCheckpointAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	at := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	machine := persistedStartingMachine("vm-durable-checkpoint", at)
	if err := store.Create(machine); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	ownership := RuntimeOwnership{JailPrepared: true}
	checkpointed, err := store.CheckpointRuntime(machine.ID, ownership, at.Add(time.Second))
	if err != nil {
		t.Fatalf("CheckpointRuntime() error = %v", err)
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
	if stored != checkpointed {
		t.Errorf("reopened machine = %#v, want checkpoint %#v", stored, checkpointed)
	}
}

func checkpointStoreFactories() map[string]func(*testing.T) Store {
	return map[string]func(*testing.T) Store{
		"sqlite": func(t *testing.T) Store {
			store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatalf("OpenSQLiteStore() error = %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		},
	}
}
