// Package recovery reconciles durable lifecycle ownership before the API starts.
package recovery

import (
	"context"
	"fmt"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

// RuntimeRecoverer releases host resources owned by an interrupted runtime.
type RuntimeRecoverer interface {
	Recover(context.Context, microvm.Sandbox, microvm.RuntimeCheckpoint) error
}

// OrphanChecker inventories owned host resources without durable records.
type OrphanChecker interface {
	CheckForOrphans([]microvm.Sandbox) error
}

type HostSlotRecoverer interface {
	RecoverHostSlot(context.Context, microvm.HostSlot, microvm.RuntimeCheckpoint) error
}

// ReconcileHostSlots drains every slot left by an earlier daemon before orphan
// detection and before a fresh pool starts.
func ReconcileHostSlots(
	ctx context.Context,
	store microvm.HostSlotStore,
	recoverer HostSlotRecoverer,
	now func() time.Time,
) error {
	slots, err := store.ListHostSlots()
	if err != nil {
		return fmt.Errorf("list persisted host slots: %w", err)
	}
	for _, slot := range slots {
		if _, err := store.DrainHostSlot(slot.ID, now().UTC()); err != nil {
			return fmt.Errorf("mark host slot %s draining: %w", slot.ID, err)
		}
		checkpoint := func(ownership microvm.RuntimeOwnership) error {
			_, err := store.CheckpointHostSlot(slot.ID, ownership, now().UTC())
			return err
		}
		if err := recoverer.RecoverHostSlot(ctx, slot, checkpoint); err != nil {
			return fmt.Errorf("recover host slot %s: %w", slot.ID, err)
		}
		if err := store.DeleteHostSlot(slot.ID); err != nil {
			return fmt.Errorf("delete recovered host slot %s: %w", slot.ID, err)
		}
	}
	return nil
}

// CheckForOrphans verifies that every discovered host resource has a durable owner.
type SandboxLister interface {
	List() ([]microvm.Sandbox, error)
}

func CheckForOrphans(store SandboxLister, checker OrphanChecker) error {
	machines, err := store.List()
	if err != nil {
		return fmt.Errorf("list persisted microVMs for orphan detection: %w", err)
	}
	if err := checker.CheckForOrphans(machines); err != nil {
		return fmt.Errorf("check fire-fuse resource ownership: %w", err)
	}
	return nil
}

// Reconcile completes interrupted runtime cleanup before the API starts.
func Reconcile(
	ctx context.Context,
	store microvm.SandboxRecoveryStore,
	recoverer RuntimeRecoverer,
	now func() time.Time,
) error {
	machines, err := store.List()
	if err != nil {
		return fmt.Errorf("list persisted microVMs: %w", err)
	}
	for _, machine := range machines {
		if machine.RuntimeTiming.Status == microvm.RuntimeTimingRunning || machine.RuntimeTiming.Status == microvm.RuntimeTimingPending {
			timing := machine.RuntimeTiming
			timing.Status = microvm.RuntimeTimingUnknown
			if _, err := store.RecordRuntimeTiming(machine.ID, timing); err != nil {
				return fmt.Errorf("mark interrupted runtime timing %s: %w", machine.ID, err)
			}
		}
		if machine.Runtime == (microvm.RuntimeOwnership{}) {
			switch machine.State {
			case microvm.StateStarting:
				if _, err := store.Fail(
					machine.ID,
					microvm.Failure{Reason: microvm.FailureRuntimeStart},
					now().UTC(),
				); err != nil {
					return fmt.Errorf("fail interrupted microVM %s startup: %w", machine.ID, err)
				}
			case microvm.StateStopping:
				if _, err := store.Transition(machine.ID, microvm.StateTerminated, now().UTC()); err != nil {
					return fmt.Errorf("finish recovered microVM %s termination: %w", machine.ID, err)
				}
			}
			continue
		}

		if machine.State != microvm.StateStopping {
			machine, err = store.Transition(machine.ID, microvm.StateStopping, now().UTC())
			if err != nil {
				return fmt.Errorf("mark recovered microVM %s stopping: %w", machine.ID, err)
			}
		}
		checkpoint := func(ownership microvm.RuntimeOwnership) error {
			_, err := store.CheckpointRuntime(machine.ID, ownership, now().UTC())
			return err
		}
		if err := recoverer.Recover(ctx, machine, checkpoint); err != nil {
			return fmt.Errorf("recover microVM %s runtime: %w", machine.ID, err)
		}
		if _, err := store.Transition(machine.ID, microvm.StateTerminated, now().UTC()); err != nil {
			return fmt.Errorf("finish recovered microVM %s termination: %w", machine.ID, err)
		}
	}
	return nil
}
