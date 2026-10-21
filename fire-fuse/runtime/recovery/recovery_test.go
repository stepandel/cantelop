package recovery

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

const recoveryTestImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeRuntimeRecoverer struct {
	machines []microvm.Sandbox
	err      error
}

type fakeHostSlotRecoverer struct {
	recover func(context.Context, microvm.HostSlot, microvm.RuntimeCheckpoint) error
}

func (f fakeHostSlotRecoverer) RecoverHostSlot(
	ctx context.Context,
	slot microvm.HostSlot,
	checkpoint microvm.RuntimeCheckpoint,
) error {
	return f.recover(ctx, slot, checkpoint)
}

func TestReconcileHostSlotsDrainsOwnershipBeforeDelete(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	ownership := microvm.RuntimeOwnership{HostSlotID: "slot-01", JailPrepared: true}
	if err := store.CreateHostSlot(microvm.HostSlot{
		ID: "slot-01", Profile: "ubuntu-24", State: microvm.HostSlotPreparing,
		Runtime: ownership, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	recoverer := fakeHostSlotRecoverer{recover: func(
		_ context.Context,
		slot microvm.HostSlot,
		checkpoint microvm.RuntimeCheckpoint,
	) error {
		if slot.Runtime != ownership {
			t.Fatalf("recovered slot = %#v", slot)
		}
		return checkpoint(microvm.RuntimeOwnership{})
	}}
	if err := ReconcileHostSlots(context.Background(), store, recoverer, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	slots, err := store.ListHostSlots()
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 0 {
		t.Fatalf("slots after recovery = %#v", slots)
	}
}

type fakeOrphanChecker struct {
	machines []microvm.Sandbox
	err      error
}

func (f *fakeOrphanChecker) CheckForOrphans(machines []microvm.Sandbox) error {
	f.machines = append([]microvm.Sandbox(nil), machines...)
	return f.err
}

func (f *fakeRuntimeRecoverer) Recover(
	_ context.Context,
	machine microvm.Sandbox,
	checkpoint microvm.RuntimeCheckpoint,
) error {
	f.machines = append(f.machines, machine)
	if f.err != nil {
		return f.err
	}
	return checkpoint(microvm.RuntimeOwnership{})
}

func TestReconcilePersistedRuntimes(t *testing.T) {
	at := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	store, err := microvm.OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	machines := []microvm.Sandbox{
		testRecoveryMachine("vm-clean", microvm.StateStarting, at),
		testRecoveryMachine("vm-finish", microvm.StateStopping, at),
		{
			ID:          "vm-owned",
			AppID:       "app-test",
			WorkspaceID: "wsp_vm_owned",
			Profile:     "test",
			BuildID:     recoveryTestImageID,
			VCPUs:       1,
			MemoryMiB:   128,
			State:       microvm.StateFailed,
			Runtime:     microvm.RuntimeOwnership{JailPrepared: true},
			Failure:     microvm.Failure{Reason: microvm.FailureRuntimeStart},
			CreatedAt:   at,
			UpdatedAt:   at,
		},
	}
	for _, machine := range machines {
		if err := store.Create(machine); err != nil {
			t.Fatalf("Create(%s) error = %v", machine.ID, err)
		}
	}
	recoverer := &fakeRuntimeRecoverer{}

	if err := Reconcile(
		context.Background(),
		store,
		recoverer,
		func() time.Time { return at },
	); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(recoverer.machines) != 1 ||
		recoverer.machines[0].ID != "vm-owned" ||
		recoverer.machines[0].State != microvm.StateStopping {
		t.Fatalf("recovered machines = %#v, want vm-owned in stopping", recoverer.machines)
	}
	for _, id := range []string{"vm-finish", "vm-owned"} {
		machine, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", id, err)
		}
		if machine.State != microvm.StateTerminated || machine.Runtime != (microvm.RuntimeOwnership{}) {
			t.Errorf("recovered %s = %#v, want terminated without ownership", id, machine)
		}
	}
	clean, err := store.Get("vm-clean")
	if err != nil {
		t.Fatalf("Get(vm-clean) error = %v", err)
	}
	if clean.State != microvm.StateFailed || clean.Failure.Reason != microvm.FailureRuntimeStart {
		t.Errorf("clean machine = %#v, want failed startup", clean)
	}
}

func TestReconcileInterruptedEmptyStartupIsIdempotent(t *testing.T) {
	createdAt := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	failedAt := createdAt.Add(time.Minute)
	store := newTestStore(t)
	machine := testRecoveryMachine("vm-interrupted", microvm.StateStarting, createdAt)
	if err := store.Create(machine); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	recoverer := &fakeRuntimeRecoverer{}

	if err := Reconcile(
		context.Background(),
		store,
		recoverer,
		func() time.Time { return failedAt },
	); err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	if err := Reconcile(
		context.Background(),
		store,
		recoverer,
		func() time.Time { return failedAt.Add(time.Minute) },
	); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}

	stored, err := store.Get(machine.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.State != microvm.StateFailed || stored.Failure.Reason != microvm.FailureRuntimeStart {
		t.Errorf("recovered machine = %#v, want failed startup", stored)
	}
	if !stored.UpdatedAt.Equal(failedAt) {
		t.Errorf("UpdatedAt = %s, want first recovery time %s", stored.UpdatedAt, failedAt)
	}
	if len(recoverer.machines) != 0 {
		t.Errorf("runtime recovery calls = %d, want 0", len(recoverer.machines))
	}
}

// newTestStore opens an empty SQLite state store that is closed when t ends.
func newTestStore(t *testing.T) *microvm.SQLiteStore {
	t.Helper()
	store, err := microvm.OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// testRunningRecoveryMachine returns a running record that owns a complete
// runtime, as the store requires for running Sandboxes.
func testRunningRecoveryMachine(id string, at time.Time) microvm.Sandbox {
	machine := testRecoveryMachine(id, microvm.StateRunning, at)
	machine.Network = microvm.NetworkAssignment{
		GuestIP: "172.16.0.2", GatewayIP: "172.16.0.1",
		Subnet: "172.16.0.0/30", GuestMAC: "02:fc:ac:10:00:02",
	}
	machine.Runtime = microvm.RuntimeOwnership{
		JailPrepared: true, AddressAllocated: true, TAPCreated: true,
		ForwardingApplied: true, TAPName: "ff0123456789abc", Network: machine.Network,
		Process: microvm.ProcessIdentity{
			PID: 123, BootID: "01234567-89ab-cdef-0123-456789abcdef", StartTimeTicks: 456,
		},
	}
	return machine
}

func testRecoveryMachine(id string, state microvm.State, at time.Time) microvm.Sandbox {
	return microvm.Sandbox{
		ID:          id,
		AppID:       "app-test",
		WorkspaceID: "wsp_" + id,
		Profile:     "test",
		BuildID:     recoveryTestImageID,
		VCPUs:       1,
		MemoryMiB:   128,
		State:       state,
		CreatedAt:   at,
		UpdatedAt:   at,
	}
}

func TestReconcileLeavesRetryableOwnershipOnFailure(t *testing.T) {
	store := newTestStore(t)
	machine := testRunningRecoveryMachine("vm-owned", time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC))
	if err := store.Create(machine); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	wantErr := errors.New("termination failed")
	recoverer := &fakeRuntimeRecoverer{err: wantErr}

	err := Reconcile(
		context.Background(),
		store,
		recoverer,
		time.Now,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Reconcile() error = %v, want recovery failure", err)
	}
	stored, err := store.Get(machine.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.State != microvm.StateStopping || stored.Runtime != machine.Runtime {
		t.Errorf("stored machine after failure = %#v, want stopping with ownership", stored)
	}
}

func TestCheckForOrphans(t *testing.T) {
	store := newTestStore(t)
	machine := testRecoveryMachine("vm-test", microvm.StateTerminated, time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC))
	if err := store.Create(machine); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	wantErr := errors.New("orphaned resources")
	checker := &fakeOrphanChecker{err: wantErr}

	err := CheckForOrphans(store, checker)
	if !errors.Is(err, wantErr) {
		t.Fatalf("CheckForOrphans() error = %v, want orphan error", err)
	}
	if len(checker.machines) != 1 || checker.machines[0].ID != machine.ID {
		t.Errorf("checked machines = %#v, want %s", checker.machines, machine.ID)
	}
}
