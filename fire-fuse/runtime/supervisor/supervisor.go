// Package supervisor assembles Firecracker host operations into microVM lifecycle behavior.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/databaseaccess"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestassignment"
	"github.com/stepandel/cantelop/fire-fuse/host/cgroup"
	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/host/network"
	"github.com/stepandel/cantelop/fire-fuse/host/nfs"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotrestore"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

const (
	rollbackStopTimeout = 5 * time.Second
	procSelfCgroupPath  = "/proc/self/cgroup"
	cgroupMountpoint    = "/sys/fs/cgroup"
)

var (
	ErrProcessExitedDuringStart = errors.New("Firecracker process exited during startup")
	ErrStartupTimeout           = errors.New("microVM startup timed out")
	ErrOrphanedResources        = errors.New("orphaned fire-fuse resources detected")
	ErrRuntimeUsageUnavailable  = errors.New("runtime usage is unavailable")
)

type managedProcess interface {
	PID() int
	Done() <-chan struct{}
	Wait() error
	Stop(context.Context) error
	// StartedAt is captured at process creation. StoppedAt is zero until Done
	// is closed, then holds the instant Wait completed.
	StartedAt() time.Time
	StoppedAt() time.Time
}

type crashableProcess interface {
	Kill(context.Context) error
}

type controlClient interface {
	SendCtrlAltDel(context.Context) error
	CloseIdleConnections()
}

type restoreClient interface {
	LoadSnapshot(context.Context, string, string, firecracker.NetworkInterface) error
	PatchContainerRootFS(context.Context, string) error
	Resume(context.Context) error
	CloseIdleConnections()
}

type dependencies struct {
	newLayout          func(string, string, string) (firecracker.JailLayout, error)
	prepare            func(context.Context, firecracker.JailLayout, string, string, string, int, int) error
	prepareBase        func(context.Context, firecracker.JailLayout, string, string, int, int) error
	bindContainer      func(context.Context, firecracker.JailLayout, string, int, int) error
	prepareRestore     func(context.Context, firecracker.JailLayout, string, string, int, int) error
	cleanup            func(firecracker.JailLayout) error
	prepareCgroup      func(string) error
	removeCgroup       func(string) error
	readUsage          func(string) (cgroup.Usage, error)
	launch             func(context.Context, firecracker.JailLayout) (managedProcess, error)
	processIdentity    func(int) (microvm.ProcessIdentity, error)
	terminateProcess   func(context.Context, microvm.ProcessIdentity, time.Duration, time.Duration) (processTerminationResult, error)
	readJailRoot       func(string) ([]os.DirEntry, error)
	listInterfaces     func() ([]net.Interface, error)
	readTAPAlias       func(string) (string, error)
	listFirewall       func(context.Context) ([]network.OwnedFirewallRule, error)
	newControlClient   func(string) (controlClient, error)
	newRestoreClient   func(string) (restoreClient, error)
	resolveImage       func(string) (config.ContainerImage, error)
	resolveEnvironment func(string) ([]string, error)
	assignGuest        func(context.Context, string, guestassignment.Assignment, guestassignment.Milestone) error
	recordStartup      func(string, bool, startupTimings)
	recordWorkspace    func(workspaceLifecycleObservation)
	recordHostPool     func(hostPoolObservation)
	network            *networkDependencies
	workspace          *workspaceDependencies
}

type networkDependencies struct {
	allocate         func(string) (network.Allocation, error)
	release          func(string) error
	createTAP        func(context.Context, string, network.Allocation, int, int) (string, error)
	deleteTAP        func(context.Context, string) error
	applyForwarding  func(context.Context, string, network.Allocation) error
	removeForwarding func(context.Context, string, network.Allocation) error
}

type workspaceDependencies struct {
	export          func(context.Context, string, netip.Addr, uint32, uint32) (string, error)
	purge           func(context.Context, string) error
	applyNFSAccess  func(context.Context, string, network.Allocation) error
	removeNFSAccess func(context.Context, string, network.Allocation) error
	unexport        func(context.Context, string, netip.Addr) error
}

type instanceNetwork struct {
	addressAllocated bool
	tapOwned         bool
	forwardingOwned  bool
	tapName          string
	allocation       network.Allocation
}

type instance struct {
	hostSlotID           string
	hostProfile          string
	hostCapacityClaimed  bool
	hostCapacityReleased bool
	layout               firecracker.JailLayout
	jailPrepared         bool
	cgroupCreated        bool
	containerRootFSBound bool
	workspaceEnabled     bool
	workspaceID          string
	workspaceExported    bool
	workspaceNFSAllowed  bool
	process              managedProcess
	processIdentity      microvm.ProcessIdentity
	network              *instanceNetwork
}

// operationSlot serializes lifecycle operations for one sandbox ID and holds
// the runtime they track.
type operationSlot struct {
	mu       sync.Mutex
	instance *instance
	// process mirrors instance.process so read paths observe running and
	// exited state without queueing behind a lifecycle operation that holds
	// mu. It is written only with mu held, through setInstance.
	process atomic.Pointer[slotProcess]
	// refs counts lockSlot holders and waiters; it is guarded by
	// Supervisor.slotsMu.
	refs int
}

type slotProcess struct{ managedProcess }

// setInstance replaces the tracked runtime. The caller holds mu.
func (slot *operationSlot) setInstance(owned *instance) {
	slot.instance = owned
	if owned == nil || owned.process == nil {
		slot.process.Store(nil)
		return
	}
	slot.process.Store(&slotProcess{owned.process})
}

// Supervisor owns the process and jail resources for configured microVMs.
type Supervisor struct {
	databaseAccess *databaseaccess.Access
	config         config.Config
	deps           dependencies
	snapshots      map[string]snapshotrestore.Bundle

	slotsMu         sync.Mutex
	slots           map[string]*operationSlot
	hostPool        *hostSlotPool
	memoryMu        sync.Mutex
	memoryRequested map[string]int
	memoryPlugged   map[string]int
	memoryGuestUsed map[string]guestMemorySample
}

var _ microvm.Supervisor = (*Supervisor)(nil)

var _ microvm.WorkspaceDataPurger = (*Supervisor)(nil)

// PurgeWorkspace delegates permanent data removal to the configured JuiceFS
// workspace manager. Runtime lifecycle cleanup must complete before this call.
func (s *Supervisor) PurgeWorkspace(ctx context.Context, workspaceID string) error {
	if s.deps.workspace == nil {
		// Without durable workspace storage there is no host data to remove.
		return nil
	}
	if s.deps.workspace.purge == nil {
		return errors.New("Workspace purge is unavailable")
	}
	return s.deps.workspace.purge(ctx, workspaceID)
}

// EnvironmentResolver snapshots the configuration shared by every runtime of
// an App and supplies it to a newly starting guest.
type EnvironmentResolver func(appID string) ([]string, error)

// New constructs a production Firecracker supervisor.
func New(cfg config.Config, output io.Writer, resolvers ...EnvironmentResolver) (*Supervisor, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(resolvers) > 1 || (len(resolvers) == 1 && resolvers[0] == nil) {
		return nil, errors.New("exactly zero or one non-nil Environment resolver is allowed")
	}
	var resolveEnvironment EnvironmentResolver
	if len(resolvers) == 1 {
		resolveEnvironment = resolvers[0]
	}
	resourceManager, err := cgroup.NewManager(procSelfCgroupPath, cgroupMountpoint)
	if err != nil {
		return nil, fmt.Errorf("initialize sandbox cgroups: %w", err)
	}
	launcher, err := firecracker.NewLauncher(newLauncherConfig(cfg, output, resourceManager.Parent()))
	if err != nil {
		return nil, err
	}
	allocator, err := network.NewAddressAllocator(cfg.Network.AddressPool)
	if err != nil {
		return nil, err
	}
	tapManager, err := network.NewTAPManager(cfg.Network.IPBin)
	if err != nil {
		return nil, err
	}
	egressPolicy, err := network.NewEgressPolicy(
		cfg.Network.Egress.DNSResolvers,
		cfg.Network.Egress.AllowedTCPPorts,
		cfg.Network.Egress.AllowedUDPPorts,
	)
	if err != nil {
		return nil, err
	}
	forwardingManager, err := network.NewForwardingManager(
		cfg.Network.IPTablesBin,
		cfg.Network.IP6TablesBin,
		egressPolicy,
	)
	if err != nil {
		return nil, err
	}
	var workspace *workspaceDependencies
	if cfg.Workspace != nil {
		exportManager, err := nfs.NewManager(
			cfg.Workspace.ExportFSBin,
			cfg.Workspace.JuiceFSBin,
			cfg.Workspace.MountpointBin,
			cfg.Workspace.Root,
			cfg.Workspace.AdminRoot,
		)
		if err != nil {
			return nil, err
		}
		workspace = &workspaceDependencies{
			export:          exportManager.Export,
			purge:           exportManager.Purge,
			applyNFSAccess:  forwardingManager.ApplyWorkspaceNFS,
			removeNFSAccess: forwardingManager.RemoveWorkspaceNFS,
			unexport:        exportManager.Unexport,
		}
	}
	logger := slog.New(slog.NewJSONHandler(output, nil))
	snapshots, err := loadConfiguredSnapshots(cfg)
	if err != nil {
		return nil, err
	}

	supervisor := newSupervisorWithDependencies(cfg, dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare: func(
			ctx context.Context,
			layout firecracker.JailLayout,
			kernel,
			rootfs,
			containerRootFS string,
			uid,
			gid int,
		) error {
			return layout.PrepareAssetsContext(ctx, kernel, rootfs, containerRootFS, uid, gid)
		},
		prepareBase: func(
			ctx context.Context,
			layout firecracker.JailLayout,
			kernel,
			rootfs string,
			uid,
			gid int,
		) error {
			return layout.PrepareBaseAssetsContext(ctx, kernel, rootfs, uid, gid)
		},
		bindContainer: func(
			ctx context.Context,
			layout firecracker.JailLayout,
			containerRootFS string,
			uid,
			gid int,
		) error {
			return layout.BindContainerRootFSContext(ctx, containerRootFS, uid, gid)
		},
		prepareRestore: func(
			ctx context.Context,
			layout firecracker.JailLayout,
			state,
			memory string,
			uid,
			gid int,
		) error {
			return layout.PrepareRestoreInputs(ctx, state, memory, uid, gid)
		},
		cleanup: func(layout firecracker.JailLayout) error {
			return layout.Cleanup()
		},
		prepareCgroup: resourceManager.Prepare,
		removeCgroup:  resourceManager.Remove,
		readUsage:     resourceManager.Read,
		launch: func(ctx context.Context, layout firecracker.JailLayout) (managedProcess, error) {
			var tracked *timedProcess
			_, err := launcher.StartObserved(ctx, layout, func(process *firecracker.Process) error {
				tracked = observeProcess(process, microvm.RuntimeTimingRecorderFromContext(ctx))
				_, err := tracked.persist()
				return err
			})
			if tracked == nil {
				return nil, err
			}
			return tracked, err
		},
		processIdentity:  readLinuxProcessIdentity,
		terminateProcess: terminateVerifiedLinuxProcess,
		readJailRoot:     os.ReadDir,
		listInterfaces:   net.Interfaces,
		readTAPAlias:     network.ReadTAPAlias,
		listFirewall:     forwardingManager.ListOwnedRules,
		newControlClient: func(socketPath string) (controlClient, error) {
			return firecracker.NewClient(socketPath)
		},
		newRestoreClient: func(socketPath string) (restoreClient, error) {
			return firecracker.NewClient(socketPath)
		},
		resolveImage: func(imageID string) (config.ContainerImage, error) {
			return config.ResolveTrustedImage(cfg.ImageCatalog, imageID)
		},
		resolveEnvironment: resolveEnvironment,
		assignGuest:        guestassignment.Assign,
		recordStartup:      logStartup(logger),
		recordHostPool:     logHostPool(logger),
		recordWorkspace:    logWorkspaceLifecycle(logger),
		network: &networkDependencies{
			allocate: allocator.Allocate,
			release:  allocator.Release,
			createTAP: func(ctx context.Context, vmID string, allocation network.Allocation, uid, gid int) (string, error) {
				hostAddress := netip.PrefixFrom(allocation.HostIP, allocation.Subnet.Bits())
				return tapManager.Create(ctx, vmID, hostAddress, uid, gid)
			},
			deleteTAP:        tapManager.Delete,
			applyForwarding:  forwardingManager.Apply,
			removeForwarding: forwardingManager.Remove,
		},
		workspace: workspace,
	})
	supervisor.snapshots = snapshots
	return supervisor, nil
}

func newLauncherConfig(cfg config.Config, output io.Writer, cgroupParent string) firecracker.LauncherConfig {
	return firecracker.LauncherConfig{
		JailerBin:      cfg.JailerBin,
		FirecrackerBin: cfg.FirecrackerBin,
		UID:            cfg.JailerUID,
		GID:            cfg.JailerGID,
		SocketTimeout:  time.Duration(cfg.Timeouts.APISocket),
		ShutdownGrace:  time.Duration(cfg.Timeouts.ShutdownGrace),
		ShutdownForce:  time.Duration(cfg.Timeouts.ShutdownForce),
		Output:         output,
		CgroupParent:   cgroupParent,
	}
}

func loadConfiguredSnapshots(cfg config.Config) (map[string]snapshotrestore.Bundle, error) {
	profileNames := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		profileNames = append(profileNames, name)
	}
	sort.Strings(profileNames)
	identityCtx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(cfg.Timeouts.APISocket),
	)
	defer cancel()
	version, err := snapshotrestore.FirecrackerVersion(identityCtx, cfg.FirecrackerBin)
	if err != nil {
		return nil, fmt.Errorf("inspect snapshot host Firecracker: %w", err)
	}
	cpuFingerprint, err := snapshotrestore.CPUFingerprint("/proc/cpuinfo")
	if err != nil {
		return nil, fmt.Errorf("inspect snapshot host CPU: %w", err)
	}
	bundles := make(map[string]snapshotrestore.Bundle, len(profileNames))
	for _, name := range profileNames {
		profile := cfg.Profiles[name]
		bundle, err := snapshotrestore.LoadVerified(
			profile.SnapshotBundle,
			snapshotrestore.Compatibility{
				FirecrackerVersion: version,
				CPUFingerprint:     cpuFingerprint,
				HotplugMemoryMiB:   microvm.SandboxHotplugMemoryMiB,
				KernelImage:        profile.KernelImage,
				PlatformRootFS:     profile.RootFSImage,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("validate snapshot bundle for profile %s: %w", name, err)
		}
		if !bundle.MatchesMachine(microvm.DefaultSandboxVCPUs, microvm.DefaultSandboxMemoryMiB) {
			return nil, fmt.Errorf(
				"validate snapshot bundle for profile %s: default profile must be %d vCPU and %d MiB",
				name,
				microvm.DefaultSandboxVCPUs,
				microvm.DefaultSandboxMemoryMiB,
			)
		}
		bundles[name] = bundle
	}
	return bundles, nil
}

func newSupervisorWithDependencies(cfg config.Config, deps dependencies) *Supervisor {
	if deps.resolveImage == nil {
		deps.resolveImage = cfg.GetImage
	}
	if deps.resolveEnvironment == nil {
		deps.resolveEnvironment = func(string) ([]string, error) { return nil, nil }
	}
	return &Supervisor{
		config: cfg,
		deps:   deps,
		slots:  make(map[string]*operationSlot),
	}
}

// Validate checks caller-selected settings before lifecycle state is recorded.
func (s *Supervisor) Validate(spec microvm.RuntimeSpec) error {
	_, err := s.config.GetProfile(spec.Profile)
	if err != nil {
		return classifySelectionError(err)
	}
	if err := s.ValidateBuild(spec.BuildID); err != nil {
		return err
	}
	_, err = s.snapshotFor(spec.Profile, spec.VCPUs, spec.MemoryMiB)
	return err
}

// ValidateBuild verifies that a content-addressed Build is published in the
// trusted host catalog.
func (s *Supervisor) ValidateBuild(buildID string) error {
	_, err := s.resolveContainerImage(buildID)
	return classifySelectionError(err)
}

func classifySelectionError(err error) error {
	if errors.Is(err, config.ErrProfileNotFound) {
		return errors.Join(microvm.ErrUnknownProfile, err)
	}
	if errors.Is(err, config.ErrImageNotFound) {
		return errors.Join(microvm.ErrUnknownImage, err)
	}
	return err
}

func (s *Supervisor) resolveContainerImage(imageID string) (config.ContainerImage, error) {
	return s.deps.resolveImage(imageID)
}

func (s *Supervisor) snapshotFor(
	profile string,
	vcpus int,
	memoryMiB int,
) (snapshotrestore.Bundle, error) {
	bundle, ok := s.snapshots[profile]
	if !ok {
		return snapshotrestore.Bundle{}, errors.Join(
			microvm.ErrUnsupportedShape,
			fmt.Errorf("profile %s has no loaded snapshot", profile),
		)
	}
	if !bundle.MatchesMachine(vcpus, memoryMiB) {
		return snapshotrestore.Bundle{}, errors.Join(
			microvm.ErrUnsupportedShape,
			fmt.Errorf("profile %s snapshot does not support %d vCPUs and %d MiB", profile, vcpus, memoryMiB),
		)
	}
	return bundle, nil
}

// IsRunning reports whether an ID has a tracked process that has not exited.
// It does not wait for a lifecycle operation in progress on the ID.
func (s *Supervisor) IsRunning(id string) bool {
	process, exists := s.trackedProcess(id)
	return exists && !processExited(process)
}

// RuntimeUsage combines a live host cgroup reading with a recent guest memory
// observation. It refuses readings for absent or exited processes.
func (s *Supervisor) RuntimeUsage(id string) (microvm.RuntimeUsage, error) {
	slot := s.lookupSlot(id)
	if slot == nil {
		return microvm.RuntimeUsage{}, ErrRuntimeUsageUnavailable
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.instance == nil || slot.instance.process == nil || !slot.instance.cgroupCreated ||
		processExited(slot.instance.process) || s.deps.readUsage == nil {
		return microvm.RuntimeUsage{}, ErrRuntimeUsageUnavailable
	}
	usage, err := s.deps.readUsage(slot.instance.layout.VMID)
	if err != nil {
		return microvm.RuntimeUsage{}, fmt.Errorf("%w: %v", ErrRuntimeUsageUnavailable, err)
	}
	return microvm.RuntimeUsage{
		SandboxID:            id,
		CPUUsageUS:           usage.CPUUsageUS,
		MemoryCurrentBytes:   usage.MemoryCurrentBytes,
		GuestMemoryMiB:       microvm.DefaultSandboxMemoryMiB + s.pluggedMemory(id),
		GuestMemoryUsedBytes: s.guestMemoryUsed(id, time.Now()),
	}, nil
}

// RuntimeDone returns the completion signal for a tracked process.
func (s *Supervisor) RuntimeDone(id string) (<-chan struct{}, bool) {
	process, exists := s.trackedProcess(id)
	if !exists {
		return nil, false
	}
	return process.Done(), true
}

// trackedProcess returns the process published for id without waiting for a
// lifecycle operation in progress on it.
func (s *Supervisor) trackedProcess(id string) (managedProcess, bool) {
	slot := s.lookupSlot(id)
	if slot == nil {
		return nil, false
	}
	tracked := slot.process.Load()
	if tracked == nil {
		return nil, false
	}
	return tracked.managedProcess, true
}

// lookupSlot returns the operation slot for id without creating one. A slot
// removed after it is returned no longer tracks a runtime.
func (s *Supervisor) lookupSlot(id string) *operationSlot {
	s.slotsMu.Lock()
	defer s.slotsMu.Unlock()
	return s.slots[id]
}

// lockSlot acquires exclusive lifecycle access to id, creating its slot if
// needed. The returned unlock removes a slot that no longer tracks a runtime
// once no other operation holds or waits for it, so the slot map does not grow
// with every sandbox ID the host has served.
func (s *Supervisor) lockSlot(id string) (*operationSlot, func()) {
	s.slotsMu.Lock()
	slot, exists := s.slots[id]
	if !exists {
		slot = &operationSlot{}
		s.slots[id] = slot
	}
	// Waiters hold a reference before blocking on mu, so the last holder can
	// see that no other operation will use this slot.
	slot.refs++
	s.slotsMu.Unlock()

	slot.mu.Lock()
	return slot, func() {
		// slotsMu is never held while waiting for a slot's mu, so taking it
		// here, with mu still held, cannot deadlock.
		s.slotsMu.Lock()
		slot.refs--
		if slot.refs == 0 && slot.instance == nil && s.slots[id] == slot {
			delete(s.slots, id)
		}
		s.slotsMu.Unlock()
		slot.mu.Unlock()
	}
}

func processExited(process managedProcess) bool {
	select {
	case <-process.Done():
		return true
	default:
		return false
	}
}

// ConfigureDatabaseAccess must be called before any runtime starts.
func (s *Supervisor) ConfigureDatabaseAccess(endpoint, key string) error {
	if endpoint == "" && key == "" {
		return nil
	}
	access, err := databaseaccess.New(endpoint, key)
	if err != nil {
		return err
	}
	s.databaseAccess = access
	return nil
}
