package microvm

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestHostSlotStoresAtomicallyTransferReadyOwnership(t *testing.T) {
	factories := map[string]func(*testing.T) HostSlotStore{
		"sqlite": func(t *testing.T) HostSlotStore {
			store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			machineStore := store.(Store)
			now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
			machine := Sandbox{
				ID: "vm-test", AppID: "app-test", WorkspaceID: "wsp_workspace", Profile: "ubuntu-24",
				BuildID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				VCPUs:   1, MemoryMiB: 256, State: StateStarting, CreatedAt: now, UpdatedAt: now,
			}
			if err := machineStore.Create(machine); err != nil {
				t.Fatal(err)
			}
			slot := HostSlot{ID: "slot-01", Profile: "ubuntu-24", State: HostSlotPreparing, CreatedAt: now, UpdatedAt: now}
			if err := store.CreateHostSlot(slot); err != nil {
				t.Fatal(err)
			}
			ownership := reusableTestOwnership(slot.ID)
			if _, err := store.CheckpointHostSlot(slot.ID, ownership, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReadyHostSlot(slot.ID, now.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			claimedOwnership := ownership
			claimedOwnership.CgroupCreated = true
			claimedOwnership.WorkspaceExported = true
			claimedOwnership.WorkspaceNFSAllowed = true
			claimed, err := store.ClaimHostSlot(machine.ID, slot.ID, claimedOwnership, now.Add(3*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if claimed.Runtime != claimedOwnership {
				t.Fatalf("claimed runtime = %#v", claimed.Runtime)
			}
			slots, err := store.ListHostSlots()
			if err != nil {
				t.Fatal(err)
			}
			if len(slots) != 0 {
				t.Fatalf("slots after claim = %#v", slots)
			}
			persisted, err := machineStore.Get(machine.ID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Runtime != claimedOwnership {
				t.Fatalf("persisted runtime = %#v", persisted.Runtime)
			}
		})
	}
}

func TestHostSlotStoresDeleteOnlyAfterOwnershipCleanup(t *testing.T) {
	factories := map[string]func(*testing.T) HostSlotStore{
		"sqlite": func(t *testing.T) HostSlotStore {
			store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			now := time.Now().UTC()
			slot := HostSlot{ID: "slot-01", Profile: "ubuntu-24", State: HostSlotPreparing, CreatedAt: now, UpdatedAt: now}
			if err := store.CreateHostSlot(slot); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CheckpointHostSlot(slot.ID, RuntimeOwnership{HostSlotID: slot.ID, JailPrepared: true}, now); err != nil {
				t.Fatal(err)
			}
			if err := store.DeleteHostSlot(slot.ID); err == nil {
				t.Fatal("deleted slot that still owns resources")
			}
			if _, err := store.CheckpointHostSlot(slot.ID, RuntimeOwnership{}, now); err != nil {
				t.Fatal(err)
			}
			if err := store.DeleteHostSlot(slot.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHostSlotStoresRejectPartialWorkspaceIntentTransfer(t *testing.T) {
	factories := map[string]func(*testing.T) HostSlotStore{
		"sqlite": func(t *testing.T) HostSlotStore {
			store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			machineStore := store.(Store)
			now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
			machine := Sandbox{
				ID: "vm-test", AppID: "app-test", WorkspaceID: "wsp_workspace", Profile: "ubuntu-24",
				BuildID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				VCPUs:   1, MemoryMiB: 256, State: StateStarting, CreatedAt: now, UpdatedAt: now,
			}
			if err := machineStore.Create(machine); err != nil {
				t.Fatal(err)
			}
			slot := HostSlot{ID: "slot-01", Profile: "ubuntu-24", State: HostSlotPreparing, CreatedAt: now, UpdatedAt: now}
			if err := store.CreateHostSlot(slot); err != nil {
				t.Fatal(err)
			}
			ownership := reusableTestOwnership(slot.ID)
			if _, err := store.CheckpointHostSlot(slot.ID, ownership, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReadyHostSlot(slot.ID, now.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			claimed := ownership
			claimed.CgroupCreated = true
			claimed.WorkspaceExported = true
			if _, err := store.ClaimHostSlot(machine.ID, slot.ID, claimed, now.Add(3*time.Second)); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("ClaimHostSlot() error = %v, want %v", err, ErrInvalidTransition)
			}
		})
	}
}

func reusableTestOwnership(id string) RuntimeOwnership {
	return RuntimeOwnership{
		HostSlotID: id, JailPrepared: true, AddressAllocated: true, TAPCreated: true,
		ForwardingApplied: true, TAPName: "ff0123456789abc",
		Network: NetworkAssignment{GuestIP: "172.16.0.2", GatewayIP: "172.16.0.1", Subnet: "172.16.0.0/30", GuestMAC: "02:fc:ac:10:00:02"},
	}
}
