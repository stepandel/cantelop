package supervisor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/host/network"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

// ConfigureHostSlotPool installs a durable, per-profile prepared-resource
// pool. Call it only after interrupted slots have been reconciled.
func (s *Supervisor) ConfigureHostSlotPool(
	store microvm.HostSlotStore,
	targetPerProfile int,
	now func() time.Time,
) error {
	if store == nil || now == nil {
		return errors.New("configure host slot pool: store and clock are required")
	}
	profiles := make([]string, 0, len(s.config.Profiles))
	for profile := range s.config.Profiles {
		profiles = append(profiles, profile)
	}
	pool, err := newHostSlotPool(
		profiles,
		targetPerProfile,
		func(ctx context.Context, profile, hostID string) (owned *instance, resultErr error) {
			started := time.Now()
			defer func() {
				s.observeHostPool(profile, "prepare", started, resultErr)
			}()
			createdAt := now().UTC()
			if err := store.CreateHostSlot(microvm.HostSlot{
				ID: hostID, Profile: profile, State: microvm.HostSlotPreparing,
				CreatedAt: createdAt, UpdatedAt: createdAt,
			}); err != nil {
				return nil, err
			}
			checkpoint := func(ownership microvm.RuntimeOwnership) error {
				_, err := store.CheckpointHostSlot(hostID, ownership, now().UTC())
				return err
			}
			owned, prepareErr := s.prepareHostSlot(ctx, profile, hostID, checkpoint)
			if prepareErr != nil {
				return nil, errors.Join(prepareErr, store.DeleteHostSlot(hostID))
			}
			if _, err := store.ReadyHostSlot(hostID, now().UTC()); err != nil {
				cleanupErr := s.cleanupHostSlot(context.WithoutCancel(ctx), owned, checkpoint)
				return nil, errors.Join(err, cleanupErr, store.DeleteHostSlot(hostID))
			}
			return owned, nil
		},
		func(ctx context.Context, owned *instance) (resultErr error) {
			started := time.Now()
			defer func() {
				s.observeHostPool(owned.hostProfile, "cleanup", started, resultErr)
			}()
			hostID := owned.hostSlotID
			if _, err := store.DrainHostSlot(hostID, now().UTC()); err != nil {
				return err
			}
			checkpoint := func(ownership microvm.RuntimeOwnership) error {
				_, err := store.CheckpointHostSlot(hostID, ownership, now().UTC())
				return err
			}
			return errors.Join(
				s.cleanupHostSlot(ctx, owned, checkpoint),
				store.DeleteHostSlot(hostID),
			)
		},
		nil,
	)
	if err != nil {
		return err
	}
	s.slotsMu.Lock()
	defer s.slotsMu.Unlock()
	if s.hostPool != nil {
		return errors.New("configure host slot pool: already configured")
	}
	s.hostPool = pool
	return nil
}

func (s *Supervisor) StartHostSlotPool(ctx context.Context) error {
	s.slotsMu.Lock()
	pool := s.hostPool
	s.slotsMu.Unlock()
	if pool == nil {
		return errors.New("start host slot pool: not configured")
	}
	return pool.Start(ctx)
}

func (s *Supervisor) CloseHostSlotPool(ctx context.Context) error {
	s.slotsMu.Lock()
	pool := s.hostPool
	s.slotsMu.Unlock()
	if pool == nil {
		return nil
	}
	return pool.Close(ctx)
}

func (s *Supervisor) WaitHostSlotPoolReady(ctx context.Context) error {
	s.slotsMu.Lock()
	pool := s.hostPool
	s.slotsMu.Unlock()
	if pool == nil {
		return errors.New("wait for host slot pool: not configured")
	}
	return pool.WaitReady(ctx)
}

// HostCapacity returns a point-in-time aggregate of the fixed warm-slot pool.
func (s *Supervisor) HostCapacity() microvm.HostCapacity {
	s.slotsMu.Lock()
	pool := s.hostPool
	s.slotsMu.Unlock()
	if pool == nil {
		return microvm.HostCapacity{}
	}
	return pool.capacity()
}

func (s *Supervisor) ProfileCapacity(profile string) microvm.HostCapacity {
	s.slotsMu.Lock()
	pool := s.hostPool
	s.slotsMu.Unlock()
	if pool == nil {
		return microvm.HostCapacity{}
	}
	return pool.profileCapacity(profile)
}

func (s *Supervisor) takeHostSlot(profile string) (*hostSlotPool, *instance, bool) {
	s.slotsMu.Lock()
	pool := s.hostPool
	s.slotsMu.Unlock()
	if pool == nil {
		return nil, nil, false
	}
	slot, ok := pool.Take(profile)
	return pool, slot, ok
}

// prepareHostSlot creates only request-independent jail and network resources.
// The returned slot has no tenant image, workspace, or Firecracker process.
func (s *Supervisor) prepareHostSlot(
	ctx context.Context,
	profileName string,
	hostID string,
	checkpoint microvm.RuntimeCheckpoint,
) (*instance, error) {
	profile, err := s.config.GetProfile(profileName)
	if err != nil {
		return nil, classifySelectionError(err)
	}
	snapshot, ok := s.snapshots[profileName]
	if !ok {
		return nil, fmt.Errorf("prepare host slot: profile %s has no loaded snapshot", profileName)
	}
	if s.deps.prepareBase == nil || s.deps.prepareRestore == nil || s.deps.network == nil {
		return nil, errors.New("prepare host slot: dependencies are unavailable")
	}
	layout, err := s.deps.newLayout(s.config.RuntimeDir, s.config.FirecrackerBin, hostID)
	if err != nil {
		return nil, fmt.Errorf("derive host slot jail layout: %w", err)
	}
	owned := &instance{
		hostSlotID:   hostID,
		hostProfile:  profileName,
		layout:       layout,
		jailPrepared: true,
	}
	rollback := func(cause error) (*instance, error) {
		return nil, errors.Join(cause, s.cleanupHostSlot(context.WithoutCancel(ctx), owned, checkpoint))
	}
	if err := checkpointOwnership(checkpoint, owned, "claiming reusable jail"); err != nil {
		return nil, err
	}
	if err := s.deps.prepareBase(
		ctx,
		layout,
		profile.KernelImage,
		profile.RootFSImage,
		s.config.JailerUID,
		s.config.JailerGID,
	); err != nil {
		return rollback(fmt.Errorf("prepare host slot jail: %w", err))
	}
	if err := s.deps.prepareRestore(
		ctx,
		layout,
		snapshot.StatePath,
		snapshot.MemoryPath,
		s.config.JailerUID,
		s.config.JailerGID,
	); err != nil {
		return rollback(fmt.Errorf("prepare host slot snapshot inputs: %w", err))
	}

	allocation, err := s.deps.network.allocate(hostID)
	if err != nil {
		return rollback(fmt.Errorf("allocate host slot network: %w", err))
	}
	allocation.GuestMAC = snapshot.Manifest.Machine.GuestMAC
	owned.network = &instanceNetwork{
		addressAllocated: true,
		allocation:       allocation,
	}
	if err := checkpointOwnership(checkpoint, owned, "allocating reusable network"); err != nil {
		return rollback(err)
	}
	tapName, err := network.TAPNameInNamespace(hostID, s.config.Network.Namespace)
	if err != nil {
		return rollback(fmt.Errorf("derive host slot TAP name: %w", err))
	}
	owned.network.tapOwned = true
	owned.network.tapName = tapName
	if err := checkpointOwnership(checkpoint, owned, "claiming reusable TAP"); err != nil {
		return rollback(err)
	}
	createdTAP, err := s.deps.network.createTAP(
		ctx,
		hostID,
		allocation,
		s.config.JailerUID,
		s.config.JailerGID,
	)
	if err != nil {
		return rollback(fmt.Errorf("create host slot TAP: %w", err))
	}
	if createdTAP != tapName {
		return rollback(fmt.Errorf("create host slot TAP: got %q, want %q", createdTAP, tapName))
	}
	owned.network.forwardingOwned = true
	if err := checkpointOwnership(checkpoint, owned, "claiming reusable forwarding"); err != nil {
		return rollback(err)
	}
	if err := s.deps.network.applyForwarding(ctx, tapName, allocation); err != nil {
		return rollback(fmt.Errorf("apply host slot forwarding: %w", err))
	}
	return owned, nil
}

func (s *Supervisor) cleanupHostSlot(
	ctx context.Context,
	owned *instance,
	checkpoint microvm.RuntimeCheckpoint,
) error {
	if owned == nil || owned.hostSlotID == "" {
		return errors.New("clean up host slot: invalid ownership")
	}
	return s.cleanupInstance(ctx, owned.hostSlotID, owned, checkpoint)
}

func (s *Supervisor) releaseHostSlot(profile string) error {
	s.slotsMu.Lock()
	pool := s.hostPool
	s.slotsMu.Unlock()
	if pool == nil {
		return nil
	}
	return pool.Release(profile)
}

func (s *Supervisor) waitHostSlotCapacity(ctx context.Context, profile string) error {
	s.slotsMu.Lock()
	pool := s.hostPool
	s.slotsMu.Unlock()
	if pool == nil {
		return nil
	}
	return pool.WaitRestored(ctx, profile)
}
