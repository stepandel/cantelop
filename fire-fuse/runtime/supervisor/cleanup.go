package supervisor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func (s *Supervisor) rollback(
	ctx context.Context,
	slot *operationSlot,
	vmID string,
	owned *instance,
	checkpoint microvm.RuntimeCheckpoint,
	cause error,
) error {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackStopTimeout)
	defer cancel()
	stopErr := owned.process.Stop(stopCtx)
	if !processExited(owned.process) {
		slot.setInstance(owned)
		return errors.Join(cause, wrapStopError(stopErr))
	}
	identity := owned.processIdentity
	owned.processIdentity = microvm.ProcessIdentity{}
	if checkpointErr := checkpointOwnership(checkpoint, owned, "stopping the Firecracker process"); checkpointErr != nil {
		owned.processIdentity = identity
		slot.setInstance(owned)
		return errors.Join(cause, wrapStopError(stopErr), checkpointErr)
	}
	cleanupErr := s.cleanupInstance(stopCtx, vmID, owned, checkpoint)
	if cleanupErr != nil {
		slot.setInstance(owned)
	}
	return errors.Join(cause, wrapStopError(stopErr), cleanupErr)
}

func (s *Supervisor) cleanupStartFailure(
	ctx context.Context,
	slot *operationSlot,
	vmID string,
	owned *instance,
	checkpoint microvm.RuntimeCheckpoint,
	cause error,
) error {
	cleanupErr := s.cleanupInstance(context.WithoutCancel(ctx), vmID, owned, checkpoint)
	if cleanupErr != nil {
		slot.setInstance(owned)
	}
	return errors.Join(cause, cleanupErr)
}

func (s *Supervisor) cleanupInstance(
	ctx context.Context,
	vmID string,
	owned *instance,
	checkpoint microvm.RuntimeCheckpoint,
) error {
	hostID := vmID
	if owned.hostSlotID != "" {
		hostID = owned.hostSlotID
	}
	cleanupCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		time.Duration(s.config.Timeouts.Cleanup),
	)
	defer cancel()

	if owned.workspaceNFSAllowed {
		if owned.network == nil || s.deps.workspace == nil ||
			s.deps.workspace.removeNFSAccess == nil {
			return errors.New("remove workspace NFS access: dependencies are unavailable")
		}
		if err := releaseOwnership(checkpoint, owned, "removing workspace NFS access", func() error {
			if err := s.deps.workspace.removeNFSAccess(
				cleanupCtx,
				owned.network.tapName,
				owned.network.allocation,
			); err != nil {
				return fmt.Errorf("remove workspace NFS access: %w", err)
			}
			return nil
		}, disownFlag(&owned.workspaceNFSAllowed)); err != nil {
			return err
		}
	}
	if owned.workspaceExported {
		if owned.network == nil || s.deps.workspace == nil ||
			s.deps.workspace.unexport == nil {
			return errors.New("unexport workspace NFS path: dependencies are unavailable")
		}
		if err := releaseOwnership(checkpoint, owned, "unexporting workspace NFS path", func() error {
			if err := s.deps.workspace.unexport(
				cleanupCtx,
				owned.workspaceID,
				owned.network.allocation.GuestIP,
			); err != nil {
				return fmt.Errorf("unexport workspace NFS path: %w", err)
			}
			return nil
		}, disownFlag(&owned.workspaceExported)); err != nil {
			return err
		}
	}

	if owned.network != nil && s.deps.network != nil {
		ownedNetwork := owned.network
		if ownedNetwork.forwardingOwned {
			if err := releaseOwnership(checkpoint, owned, "removing network forwarding", func() error {
				if err := s.deps.network.removeForwarding(
					cleanupCtx,
					ownedNetwork.tapName,
					ownedNetwork.allocation,
				); err != nil {
					return fmt.Errorf("remove network forwarding: %w", err)
				}
				return nil
			}, disownFlag(&ownedNetwork.forwardingOwned)); err != nil {
				return err
			}
		}
		if ownedNetwork.tapOwned {
			if err := releaseOwnership(checkpoint, owned, "deleting the TAP device", func() error {
				if err := s.deps.network.deleteTAP(cleanupCtx, hostID); err != nil {
					return fmt.Errorf("delete TAP device: %w", err)
				}
				return nil
			}, func() func() {
				tapName := ownedNetwork.tapName
				ownedNetwork.tapOwned = false
				ownedNetwork.tapName = ""
				return func() {
					ownedNetwork.tapOwned = true
					ownedNetwork.tapName = tapName
				}
			}); err != nil {
				return err
			}
		}
		if ownedNetwork.addressAllocated {
			if err := releaseOwnership(checkpoint, owned, "releasing network addresses", func() error {
				if err := s.deps.network.release(hostID); err != nil {
					return fmt.Errorf("release network addresses: %w", err)
				}
				return nil
			}, disownFlag(&ownedNetwork.addressAllocated)); err != nil {
				return err
			}
		}
	}
	if owned.cgroupCreated {
		if s.deps.removeCgroup == nil {
			return errors.New("remove sandbox cgroup: dependency is unavailable")
		}
		if err := releaseOwnership(checkpoint, owned, "removing the sandbox cgroup", func() error {
			if err := s.deps.removeCgroup(hostID); err != nil {
				return fmt.Errorf("remove sandbox cgroup: %w", err)
			}
			return nil
		}, disownFlag(&owned.cgroupCreated)); err != nil {
			return err
		}
	}
	if owned.jailPrepared {
		if err := releaseOwnership(checkpoint, owned, "cleaning up the Firecracker jail", func() error {
			if err := s.deps.cleanup(owned.layout); err != nil {
				return fmt.Errorf("clean up Firecracker jail: %w", err)
			}
			return nil
		}, func() func() {
			hostSlotID := owned.hostSlotID
			owned.jailPrepared = false
			owned.hostSlotID = ""
			return func() {
				owned.jailPrepared = true
				owned.hostSlotID = hostSlotID
			}
		}); err != nil {
			return err
		}
	}
	if owned.hostCapacityClaimed {
		if err := s.releaseHostSlot(owned.hostProfile); err != nil {
			return fmt.Errorf("replenish released host capacity: %w", err)
		}
		owned.hostCapacityClaimed = false
		owned.hostCapacityReleased = true
	}
	if owned.hostCapacityReleased {
		// Every resource this sandbox owned is released by now; this wait only
		// gives the pool time to warm a replacement slot. Failing cleanup on it
		// would strand the machine in stopping for as long as preparation keeps
		// failing, and could never succeed once the pool is closed, so the
		// outcome is reported through the pool observer instead.
		started := time.Now()
		err := s.waitHostSlotCapacity(cleanupCtx, owned.hostProfile)
		s.observeHostPool(owned.hostProfile, "wait_released_capacity", started, err)
		owned.hostCapacityReleased = false
	}
	return nil
}

// releaseOwnership releases one owned host resource, then durably records that
// it is gone. disown clears the instance's ownership of the resource and
// returns a function that restores it. A failed checkpoint restores ownership
// so a later cleanup retries the release, which every release tolerates.
func releaseOwnership(
	checkpoint microvm.RuntimeCheckpoint,
	owned *instance,
	action string,
	release func() error,
	disown func() (restore func()),
) error {
	if err := release(); err != nil {
		return err
	}
	restore := disown()
	if err := checkpointOwnership(checkpoint, owned, action); err != nil {
		restore()
		return err
	}
	return nil
}

// disownFlag adapts a single ownership flag to releaseOwnership.
func disownFlag(owned *bool) func() (restore func()) {
	return func() func() {
		*owned = false
		return func() { *owned = true }
	}
}

func wrapStopError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("stop Firecracker process: %w", err)
}

func wrapGuestShutdownError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("gracefully stop workspace guest: %w", err)
}
