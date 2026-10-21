package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/databaseaccess"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestassignment"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/host/network"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotrestore"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

// Start prepares, launches, configures, and registers one Firecracker microVM.
func (s *Supervisor) Start(
	ctx context.Context,
	machine microvm.Sandbox,
	checkpoint microvm.RuntimeCheckpoint,
) (microvm.RuntimeInfo, error) {
	startupCtx, cancel := context.WithTimeoutCause(
		ctx,
		time.Duration(s.config.Timeouts.Startup),
		ErrStartupTimeout,
	)
	defer cancel()

	// A failed attempt before process creation is verified zero usage.
	recorder := microvm.RuntimeTimingRecorderFromContext(ctx)
	var measured atomic.Bool
	if recorder != nil {
		startupCtx = microvm.WithRuntimeTimingRecorder(startupCtx, func(timing microvm.RuntimeTiming) error {
			measured.Store(true)
			return recorder(timing)
		})
	}
	runtime, err := s.start(startupCtx, machine, checkpoint)
	if err != nil && recorder != nil && !measured.Load() && machine.RuntimeTiming.Status == microvm.RuntimeTimingPending {
		if timingErr := recorder(microvm.RuntimeTiming{Status: microvm.RuntimeTimingNotStarted}); timingErr != nil {
			err = errors.Join(err, microvm.ErrStateStore, timingErr)
		}
	}
	if err != nil && errors.Is(context.Cause(startupCtx), ErrStartupTimeout) {
		return runtime, errors.Join(ErrStartupTimeout, err)
	}
	if err == nil {
		s.startMemoryController(machine.ID)
	}
	return runtime, err
}

// startPlan carries the request-scoped inputs shared by the startup phases.
type startPlan struct {
	slot             *operationSlot
	machine          microvm.Sandbox
	checkpoint       microvm.RuntimeCheckpoint
	profile          config.Profile
	containerImage   config.ContainerImage
	snapshot         snapshotrestore.Bundle
	workspaceEnabled bool
	timings          startupTimings
}

func (s *Supervisor) start(
	ctx context.Context,
	machine microvm.Sandbox,
	checkpoint microvm.RuntimeCheckpoint,
) (microvm.RuntimeInfo, error) {
	finishSlotLock := warmuptrace.Start(ctx, "supervisor.acquire_machine_lock")
	slot, unlock := s.lockSlot(machine.ID)
	finishSlotLock()
	defer unlock()

	finishExistingRuntime := warmuptrace.Start(ctx, "supervisor.inspect_existing_runtime")
	if slot.instance != nil {
		if slot.instance.process != nil && !processExited(slot.instance.process) {
			finishExistingRuntime()
			if err := traceError(ctx, "store.checkpoint_existing_runtime", func() error {
				return checkpointOwnership(checkpoint, slot.instance, "finding an existing runtime")
			}); err != nil {
				return microvm.RuntimeInfo{}, err
			}
			return runtimeInfo(slot.instance), nil
		}
		if slot.instance.processIdentity != (microvm.ProcessIdentity{}) {
			identity := slot.instance.processIdentity
			slot.instance.processIdentity = microvm.ProcessIdentity{}
			if err := traceError(ctx, "store.checkpoint_exited_runtime", func() error {
				return checkpointOwnership(checkpoint, slot.instance, "observing an exited Firecracker process")
			}); err != nil {
				slot.instance.processIdentity = identity
				return microvm.RuntimeInfo{}, err
			}
		}
		if err := traceError(ctx, "supervisor.cleanup_exited_runtime", func() error {
			return s.cleanupInstance(context.WithoutCancel(ctx), machine.ID, slot.instance, checkpoint)
		}); err != nil {
			return microvm.RuntimeInfo{}, fmt.Errorf("clean up exited microVM before restart: %w", err)
		}
		slot.setInstance(nil)
	}
	finishExistingRuntime()
	startupStarted := time.Now()
	plan := &startPlan{
		slot:             slot,
		machine:          machine,
		checkpoint:       checkpoint,
		workspaceEnabled: s.config.Workspace != nil,
	}

	var err error
	plan.profile, err = traceValue(ctx, "supervisor.lookup_profile", func() (config.Profile, error) {
		return s.config.GetProfile(machine.Profile)
	})
	if err != nil {
		return microvm.RuntimeInfo{}, classifySelectionError(err)
	}
	plan.containerImage, err = traceValue(ctx, "supervisor.lookup_image", func() (config.ContainerImage, error) {
		return s.resolveContainerImage(machine.BuildID)
	})
	if err != nil {
		return microvm.RuntimeInfo{}, classifySelectionError(err)
	}
	workloadProcess, err := s.buildWorkloadProcess(ctx, plan)
	if err != nil {
		return microvm.RuntimeInfo{}, err
	}
	plan.snapshot, err = traceValue(ctx, "supervisor.lookup_snapshot", func() (snapshotrestore.Bundle, error) {
		return s.snapshotFor(machine.Profile, machine.VCPUs, machine.MemoryMiB)
	})
	if err != nil {
		return microvm.RuntimeInfo{}, err
	}
	finishPoolTake := warmuptrace.Start(ctx, "host_pool.take_slot")
	pool, prepared, poolHit := s.takeHostSlot(machine.Profile)
	finishPoolTake()
	plan.timings.HostPoolHit = poolHit
	if poolHit {
		traceAction(ctx, "host_pool.hit", func() {})
	} else {
		traceAction(ctx, "host_pool.miss", func() {})
	}
	if !poolHit && pool != nil {
		return microvm.RuntimeInfo{}, fmt.Errorf(
			"%w: no prepared host capacity is available for profile %s",
			microvm.ErrAdmissionLimit,
			machine.Profile,
		)
	}
	var owned *instance
	var networkInterface *firecracker.NetworkInterface
	if poolHit {
		owned, networkInterface, err = s.claimPooledSlot(ctx, plan, pool, prepared)
	} else {
		owned, networkInterface, err = s.prepareFreshSlot(ctx, plan)
	}
	if err != nil {
		return microvm.RuntimeInfo{}, err
	}
	layout := owned.layout

	var workspace *guestassignment.Workspace
	if plan.workspaceEnabled {
		workspace, err = s.setupWorkspace(ctx, plan, owned, workloadProcess)
		if err != nil {
			return microvm.RuntimeInfo{}, err
		}
	}

	phaseStarted := time.Now()
	if err := traceError(ctx, "cgroup.prepare", func() error {
		return s.deps.prepareCgroup(layout.VMID)
	}); err != nil {
		return microvm.RuntimeInfo{}, s.cleanupStartFailure(
			ctx, slot, machine.ID, owned, checkpoint, fmt.Errorf("prepare sandbox cgroup: %w", err),
		)
	}
	process, err := traceValue(ctx, "firecracker.launch", func() (managedProcess, error) {
		return s.deps.launch(ctx, layout)
	})
	// Production launchers observe at cmd.Start, before API readiness. Keep a
	// fallback for other managed-process implementations, including processes
	// returned alongside a launch error.
	if process != nil {
		tracked, measured := process.(*timedProcess)
		if !measured {
			tracked = observeProcess(process, microvm.RuntimeTimingRecorderFromContext(ctx))
		}
		owned.process = tracked
		process = tracked
		if _, timingErr := tracked.persist(); timingErr != nil {
			err = errors.Join(err, timingErr)
		}
	} else if err == nil {
		err = errors.New("launcher returned no process")
	}
	if err != nil {
		if process != nil {
			return microvm.RuntimeInfo{}, s.rollback(ctx, slot, machine.ID, owned, checkpoint, fmt.Errorf("launch Firecracker: %w", err))
		}
		return microvm.RuntimeInfo{}, s.cleanupStartFailure(ctx, slot, machine.ID, owned, checkpoint, fmt.Errorf("launch Firecracker: %w", err))
	}
	identity, err := traceValue(ctx, "firecracker.read_process_identity", func() (microvm.ProcessIdentity, error) {
		return s.deps.processIdentity(process.PID())
	})
	if err != nil {
		return microvm.RuntimeInfo{}, s.rollback(ctx, slot, machine.ID, owned, checkpoint, fmt.Errorf("capture Firecracker process identity: %w", err))
	}
	owned.processIdentity = identity
	if err := traceError(ctx, "store.checkpoint_firecracker_launched", func() error {
		return checkpointOwnership(checkpoint, owned, "launching the Firecracker process")
	}); err != nil {
		return microvm.RuntimeInfo{}, s.rollback(ctx, slot, machine.ID, owned, checkpoint, err)
	}
	plan.timings.FirecrackerLaunch = time.Since(phaseStarted)
	phaseStarted = time.Now()
	workload := guestconfig.Config{
		SchemaVersion: guestconfig.SchemaVersion,
		Process:       workloadProcess,
		Workspace: guestconfig.Workspace{
			Enabled:       plan.workspaceEnabled,
			ContainerPath: workspaceContainerPath(plan.workspaceEnabled),
		},
	}
	if s.deps.newRestoreClient == nil || s.deps.assignGuest == nil {
		return microvm.RuntimeInfo{}, s.rollback(
			ctx,
			slot,
			machine.ID,
			owned,
			checkpoint,
			errors.New("restore snapshot: dependencies are unavailable"),
		)
	}
	client, err := traceValue(
		ctx,
		"firecracker.create_restore_api_client",
		func() (restoreClient, error) {
			return s.deps.newRestoreClient(layout.HostAPISocket)
		},
	)
	if err != nil {
		return microvm.RuntimeInfo{}, s.rollback(
			ctx,
			slot,
			machine.ID,
			owned,
			checkpoint,
			fmt.Errorf("create Firecracker restore client: %w", err),
		)
	}
	restoreErr := traceError(ctx, "firecracker.load_snapshot", func() error {
		return client.LoadSnapshot(
			ctx,
			layout.JailedSnapshotState,
			layout.JailedSnapshotMemory,
			*networkInterface,
		)
	})
	if restoreErr == nil {
		restoreErr = traceError(ctx, "firecracker.patch_container_rootfs", func() error {
			return client.PatchContainerRootFS(ctx, layout.JailedContainerRootFS)
		})
	}
	if restoreErr == nil {
		restoreErr = traceError(ctx, "firecracker.resume_snapshot", func() error {
			return client.Resume(ctx)
		})
	}
	traceAction(ctx, "firecracker.close_restore_idle_connections", client.CloseIdleConnections)
	if restoreErr != nil {
		return microvm.RuntimeInfo{}, s.rollback(
			ctx,
			slot,
			machine.ID,
			owned,
			checkpoint,
			fmt.Errorf("restore Firecracker snapshot: %w", restoreErr),
		)
	}
	plan.timings.SnapshotRestore = time.Since(phaseStarted)
	assignmentStarted := time.Now()
	if err := traceError(ctx, "guest.assign_snapshot", func() error {
		return s.deps.assignGuest(
			ctx,
			layout.HostVSock,
			buildGuestAssignment(machine.ID, owned.network.allocation, workload, workspace),
			nil,
		)
	}); err != nil {
		return microvm.RuntimeInfo{}, s.rollback(
			ctx,
			slot,
			machine.ID,
			owned,
			checkpoint,
			fmt.Errorf("assign restored guest: %w", err),
		)
	}
	plan.timings.GuestAssignment = time.Since(assignmentStarted)
	processHasExited, _ := traceValue(ctx, "firecracker.verify_process_alive", func() (bool, error) {
		return processExited(process), nil
	})
	if processHasExited {
		return microvm.RuntimeInfo{}, s.rollback(ctx, slot, machine.ID, owned, checkpoint, ErrProcessExitedDuringStart)
	}

	traceAction(ctx, "supervisor.publish_runtime", func() {
		slot.setInstance(owned)
	})
	plan.timings.Total = time.Since(startupStarted)
	s.deps.recordStartup(machine.ID, workspace != nil, plan.timings)
	return runtimeInfo(owned), nil
}

// buildWorkloadProcess layers the App environment, platform database
// credentials, and the sandbox identity over the image's process defaults.
func (s *Supervisor) buildWorkloadProcess(
	ctx context.Context,
	plan *startPlan,
) (guestconfig.Process, error) {
	machine := plan.machine
	resolvedEnvironment, err := traceValue(ctx, "supervisor.load_scoped_environment", func() ([]string, error) {
		return s.deps.resolveEnvironment(machine.AppID)
	})
	if err != nil {
		return guestconfig.Process{}, fmt.Errorf("load App environment: %w", err)
	}
	if plan.workspaceEnabled {
		resolvedEnvironment = append(resolvedEnvironment, "CANTELOP_WORKSPACE_PATH="+workspaceContainerPath(true))
	}
	workloadProcess := plan.containerImage.Process
	workloadProcess.Env, err = guestconfig.MergeProcessEnvironment(workloadProcess, resolvedEnvironment)
	if err != nil {
		return guestconfig.Process{}, fmt.Errorf("merge scoped environment: %w", err)
	}
	// Platform-owned values override image defaults and App configuration.
	databaseEnvironment := []string{}
	filteredEnvironment := make([]string, 0, len(workloadProcess.Env))
	for _, value := range workloadProcess.Env {
		if !strings.HasPrefix(value, "CANTELOP_WORKSPACE_DATABASE_CREDENTIALS_URL=") && !strings.HasPrefix(value, "CANTELOP_WORKSPACE_DATABASE_ACCESS_TOKEN=") {
			filteredEnvironment = append(filteredEnvironment, value)
		}
	}
	workloadProcess.Env = filteredEnvironment
	if s.databaseAccess != nil {
		databaseEnvironment, err = s.databaseAccess.Environment(databaseaccess.Identity{SandboxID: machine.ID, AppID: machine.AppID, WorkspaceID: machine.WorkspaceID})
		if err != nil {
			return guestconfig.Process{}, err
		}
	}
	workloadProcess.Env, err = guestconfig.MergeEnvironment(workloadProcess.Env, databaseEnvironment)
	if err != nil {
		return guestconfig.Process{}, err
	}
	// Bind SDK receipts to the assigned sandbox before crossing the guest
	// boundary. Older snapshot agents do not inject this identity themselves.
	// Apply it last so neither image defaults nor App settings can replace it.
	workloadProcess.Env, err = guestconfig.MergeEnvironment(workloadProcess.Env, []string{
		"CANTELOP_SANDBOX_ID=" + machine.ID,
	})
	if err != nil {
		return guestconfig.Process{}, fmt.Errorf("bind sandbox identity: %w", err)
	}
	return workloadProcess, nil
}

// claimPooledSlot transfers a prepared host slot to the sandbox and binds the
// selected container rootfs into its jail.
func (s *Supervisor) claimPooledSlot(
	ctx context.Context,
	plan *startPlan,
	pool *hostSlotPool,
	owned *instance,
) (*instance, *firecracker.NetworkInterface, error) {
	slot, machine, checkpoint := plan.slot, plan.machine, plan.checkpoint
	layout := owned.layout
	owned.workspaceEnabled = plan.workspaceEnabled
	owned.workspaceID = machine.WorkspaceID
	if s.deps.prepareCgroup == nil || s.deps.removeCgroup == nil {
		if !pool.Return(machine.Profile, owned) {
			return nil, nil, errors.Join(
				errors.New("prepare sandbox cgroup: dependencies are unavailable"),
				pool.Discard(context.WithoutCancel(ctx), owned),
			)
		}
		return nil, nil, errors.New("prepare sandbox cgroup: dependencies are unavailable")
	}
	// Claim the deterministic cgroup in the existing host-slot checkpoint.
	// Workspace cleanup also accepts absent exports and firewall rules, so
	// request-specific workspace intent can share this durable transfer.
	// Recovery may safely remove any not-yet-created resource if startup
	// stops after the checkpoint.
	owned.cgroupCreated = true
	if plan.workspaceEnabled && owned.network != nil &&
		s.deps.workspace != nil && s.deps.workspace.export != nil &&
		s.deps.workspace.applyNFSAccess != nil {
		owned.workspaceExported = true
		owned.workspaceNFSAllowed = true
	}
	if err := traceError(ctx, "host_pool.claim_slot", func() error {
		return checkpointOwnership(checkpoint, owned, "claiming prepared host slot")
	}); err != nil {
		owned.cgroupCreated = false
		owned.workspaceExported = false
		owned.workspaceNFSAllowed = false
		if !pool.Return(machine.Profile, owned) {
			return nil, nil, errors.Join(
				err,
				pool.Discard(context.WithoutCancel(ctx), owned),
			)
		}
		return nil, nil, err
	}
	owned.hostCapacityClaimed = true
	phaseStarted := time.Now()
	if s.deps.bindContainer == nil {
		return nil, nil, s.cleanupStartFailure(
			ctx, slot, machine.ID, owned, checkpoint,
			errors.New("bind container rootfs: dependency is unavailable"),
		)
	}
	if err := traceError(ctx, "jail.bind_selected_container_rootfs", func() error {
		return s.deps.bindContainer(
			ctx, layout, plan.containerImage.RootFSImage, s.config.JailerUID, s.config.JailerGID,
		)
	}); err != nil {
		return nil, nil, s.cleanupStartFailure(
			ctx, slot, machine.ID, owned, checkpoint, fmt.Errorf("bind selected container rootfs: %w", err),
		)
	}
	owned.containerRootFSBound = true
	plan.timings.JailPrepare = time.Since(phaseStarted)
	if owned.network == nil || !owned.network.addressAllocated ||
		!owned.network.tapOwned || !owned.network.forwardingOwned {
		return nil, nil, s.cleanupStartFailure(
			ctx, slot, machine.ID, owned, checkpoint,
			errors.New("claim prepared host slot: network ownership is incomplete"),
		)
	}
	return owned, &firecracker.NetworkInterface{
		InterfaceID:    plan.snapshot.Manifest.Machine.InterfaceID,
		GuestMAC:       owned.network.allocation.GuestMAC,
		HostDeviceName: owned.network.tapName,
	}, nil
}

// prepareFreshSlot builds the jail and guest network for a sandbox when no
// host slot pool is configured, checkpointing each resource before creation.
func (s *Supervisor) prepareFreshSlot(
	ctx context.Context,
	plan *startPlan,
) (*instance, *firecracker.NetworkInterface, error) {
	slot, machine, checkpoint := plan.slot, plan.machine, plan.checkpoint
	snapshot := plan.snapshot
	layout, err := traceValue(ctx, "jail.derive_layout", func() (firecracker.JailLayout, error) {
		return s.deps.newLayout(s.config.RuntimeDir, s.config.FirecrackerBin, machine.ID)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("derive Firecracker jail layout: %w", err)
	}
	phaseStarted := time.Now()
	if err := traceError(ctx, "jail.prepare_assets", func() error {
		return s.deps.prepare(
			ctx,
			layout,
			plan.profile.KernelImage,
			plan.profile.RootFSImage,
			plan.containerImage.RootFSImage,
			s.config.JailerUID,
			s.config.JailerGID,
		)
	}); err != nil {
		return nil, nil, fmt.Errorf("prepare Firecracker jail: %w", err)
	}
	if s.deps.prepareRestore == nil {
		cleanupErr := s.deps.cleanup(layout)
		return nil, nil, errors.Join(
			errors.New("prepare snapshot restore: dependencies are unavailable"),
			cleanupErr,
		)
	}
	if err := traceError(ctx, "jail.prepare_snapshot_inputs", func() error {
		return s.deps.prepareRestore(
			ctx,
			layout,
			snapshot.StatePath,
			snapshot.MemoryPath,
			s.config.JailerUID,
			s.config.JailerGID,
		)
	}); err != nil {
		cleanupErr := s.deps.cleanup(layout)
		return nil, nil, errors.Join(
			fmt.Errorf("prepare snapshot restore inputs: %w", err),
			cleanupErr,
		)
	}
	plan.timings.JailPrepare = time.Since(phaseStarted)

	owned := &instance{
		layout:               layout,
		jailPrepared:         true,
		containerRootFSBound: true,
		workspaceEnabled:     plan.workspaceEnabled,
		workspaceID:          machine.WorkspaceID,
	}
	if s.deps.prepareCgroup == nil || s.deps.removeCgroup == nil {
		return nil, nil, s.cleanupStartFailure(
			ctx, slot, machine.ID, owned, checkpoint,
			errors.New("prepare sandbox cgroup: dependencies are unavailable"),
		)
	}
	// The cgroup uses a deterministic path and Remove accepts a missing path,
	// so its claim can share the jail checkpoint before creation.
	owned.cgroupCreated = true
	if err := traceError(ctx, "store.checkpoint_jail_prepared", func() error {
		return checkpointOwnership(checkpoint, owned, "preparing the jail")
	}); err != nil {
		return nil, nil, s.cleanupStartFailure(ctx, slot, machine.ID, owned, checkpoint, err)
	}
	if s.deps.network == nil {
		return nil, nil, s.cleanupStartFailure(
			ctx,
			slot,
			machine.ID,
			owned,
			checkpoint,
			errors.New("prepare guest network: dependencies are unavailable"),
		)
	}
	phaseStarted = time.Now()
	allocation, err := traceValue(ctx, "network.allocate_address", func() (network.Allocation, error) {
		return s.deps.network.allocate(machine.ID)
	})
	if err != nil {
		return nil, nil, s.cleanupStartFailure(ctx, slot, machine.ID, owned, checkpoint, fmt.Errorf("allocate network addresses: %w", err))
	}
	allocation.GuestMAC = snapshot.Manifest.Machine.GuestMAC
	owned.network = &instanceNetwork{
		addressAllocated: true,
		allocation:       allocation,
	}
	if err := traceError(ctx, "store.checkpoint_network_allocated", func() error {
		return checkpointOwnership(checkpoint, owned, "allocating network addresses")
	}); err != nil {
		return nil, nil, s.cleanupStartFailure(ctx, slot, machine.ID, owned, checkpoint, err)
	}
	owned.network.tapOwned = true
	tapName, err := traceValue(ctx, "network.create_tap", func() (string, error) {
		return s.deps.network.createTAP(
			ctx,
			machine.ID,
			allocation,
			s.config.JailerUID,
			s.config.JailerGID,
		)
	})
	if err != nil {
		return nil, nil, s.cleanupStartFailure(ctx, slot, machine.ID, owned, checkpoint, fmt.Errorf("create TAP device: %w", err))
	}
	owned.network.tapName = tapName
	if err := traceError(ctx, "store.checkpoint_tap_created", func() error {
		return checkpointOwnership(checkpoint, owned, "creating the TAP device")
	}); err != nil {
		return nil, nil, s.cleanupStartFailure(ctx, slot, machine.ID, owned, checkpoint, err)
	}
	owned.network.forwardingOwned = true
	if err := traceError(ctx, "network.apply_forwarding", func() error {
		return s.deps.network.applyForwarding(ctx, tapName, allocation)
	}); err != nil {
		return nil, nil, s.cleanupStartFailure(ctx, slot, machine.ID, owned, checkpoint, fmt.Errorf("apply network forwarding: %w", err))
	}
	if err := traceError(ctx, "store.checkpoint_forwarding_applied", func() error {
		return checkpointOwnership(checkpoint, owned, "applying network forwarding")
	}); err != nil {
		return nil, nil, s.cleanupStartFailure(ctx, slot, machine.ID, owned, checkpoint, err)
	}
	plan.timings.NetworkSetup = time.Since(phaseStarted)
	return owned, &firecracker.NetworkInterface{
		InterfaceID:    snapshot.Manifest.Machine.InterfaceID,
		GuestMAC:       allocation.GuestMAC,
		HostDeviceName: tapName,
	}, nil
}

// setupWorkspace exports the sandbox workspace over NFS and opens the guest's
// firewall path to it.
func (s *Supervisor) setupWorkspace(
	ctx context.Context,
	plan *startPlan,
	owned *instance,
	workloadProcess guestconfig.Process,
) (*guestassignment.Workspace, error) {
	slot, machine, checkpoint := plan.slot, plan.machine, plan.checkpoint
	if owned.network == nil || !owned.network.forwardingOwned ||
		s.deps.workspace == nil ||
		s.deps.workspace.export == nil ||
		s.deps.workspace.applyNFSAccess == nil {
		return nil, s.cleanupStartFailure(
			ctx,
			slot,
			machine.ID,
			owned,
			checkpoint,
			errors.New("prepare workspace NFS access: dependencies are unavailable"),
		)
	}

	// Claim both request-specific workspace resources before creating either
	// one. Their cleanup operations accept absent state, so one durable intent
	// checkpoint safely covers crashes before, between, or after the export and
	// firewall operations. A pooled slot claim may already have recorded it.
	if !owned.workspaceExported || !owned.workspaceNFSAllowed {
		owned.workspaceExported = true
		owned.workspaceNFSAllowed = true
		if err := traceError(ctx, "store.checkpoint_workspace_setup_claimed", func() error {
			return checkpointOwnership(
				checkpoint,
				owned,
				"claiming workspace setup",
			)
		}); err != nil {
			return nil, s.cleanupStartFailure(
				ctx,
				slot,
				machine.ID,
				owned,
				checkpoint,
				err,
			)
		}
	}
	workspacePath, err := traceValue(ctx, "workspace.export_nfs", func() (string, error) {
		return s.deps.workspace.export(
			ctx,
			machine.WorkspaceID,
			owned.network.allocation.GuestIP,
			workloadProcess.UID,
			workloadProcess.GID,
		)
	})
	if err != nil {
		return nil, s.cleanupStartFailure(
			ctx,
			slot,
			machine.ID,
			owned,
			checkpoint,
			fmt.Errorf("export workspace over NFS: %w", err),
		)
	}

	if err := traceError(ctx, "workspace.apply_nfs_firewall", func() error {
		return s.deps.workspace.applyNFSAccess(
			ctx,
			owned.network.tapName,
			owned.network.allocation,
		)
	}); err != nil {
		return nil, s.cleanupStartFailure(
			ctx,
			slot,
			machine.ID,
			owned,
			checkpoint,
			fmt.Errorf("allow workspace NFS access: %w", err),
		)
	}
	return &guestassignment.Workspace{
		Enabled:    true,
		ID:         machine.WorkspaceID,
		Server:     owned.network.allocation.HostIP.String(),
		ExportPath: workspacePath,
	}, nil
}

func workspaceContainerPath(enabled bool) string {
	if !enabled {
		return ""
	}
	return "/workspace"
}

func buildGuestAssignment(
	machineID string,
	allocation network.Allocation,
	workload guestconfig.Config,
	workspace *guestassignment.Workspace,
) guestassignment.Assignment {
	assignment := guestassignment.Assignment{
		SchemaVersion: guestassignment.SchemaVersion,
		AssignmentID:  machineID,
		ClockUnixNano: time.Now().UnixNano(),
		Network: guestassignment.Network{
			Address: allocation.GuestIP.String(),
			Gateway: allocation.HostIP.String(),
			Prefix:  allocation.Subnet.Bits(),
		},
		Workload: workload,
	}
	if workspace != nil {
		assignment.Workspace = *workspace
	}
	return assignment
}
