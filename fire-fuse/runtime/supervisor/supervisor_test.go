package supervisor

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestassignment"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
	"github.com/stepandel/cantelop/fire-fuse/host/cgroup"
	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/host/network"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotbundle"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotrestore"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

const supervisorTestImageID = "sha256:e4c0bb5ba48f30f6cf4b9da62b990ef532a6644bda683af8243c54035331b4f8"

type fakeProcess struct {
	mu        sync.Mutex
	done      chan struct{}
	stopCalls int
	stopErr   error
	waitErr   error
	stayAlive bool
	onStop    func()
}

var discardRuntimeCheckpoint microvm.RuntimeCheckpoint = func(microvm.RuntimeOwnership) error {
	return nil
}

func newSupervisor(cfg config.Config, deps dependencies) *Supervisor {
	if deps.prepareCgroup == nil {
		deps.prepareCgroup = func(string) error { return nil }
	}
	if deps.removeCgroup == nil {
		deps.removeCgroup = func(string) error { return nil }
	}
	if deps.readUsage == nil {
		deps.readUsage = func(string) (cgroup.Usage, error) { return cgroup.Usage{}, nil }
	}
	if deps.processIdentity == nil {
		deps.processIdentity = func(pid int) (microvm.ProcessIdentity, error) {
			return microvm.ProcessIdentity{
				PID:            pid,
				BootID:         "64f53e4c-a999-4d4a-a49f-a93c148fbb03",
				StartTimeTicks: 123,
			}, nil
		}
	}
	if deps.prepareRestore == nil {
		deps.prepareRestore = func(context.Context, firecracker.JailLayout, string, string, int, int) error {
			return nil
		}
	}
	if deps.newRestoreClient == nil {
		deps.newRestoreClient = func(string) (restoreClient, error) {
			return &fakeRestoreClient{}, nil
		}
	}
	if deps.assignGuest == nil {
		deps.assignGuest = func(context.Context, string, guestassignment.Assignment, guestassignment.Milestone) error {
			return nil
		}
	}
	if deps.recordStartup == nil {
		deps.recordStartup = func(string, bool, startupTimings) {}
	}
	if deps.recordWorkspace == nil {
		deps.recordWorkspace = func(workspaceLifecycleObservation) {}
	}
	if deps.network == nil {
		deps.network = &networkDependencies{
			allocate: func(string) (network.Allocation, error) {
				return testAllocation(), nil
			},
			release: func(string) error {
				return nil
			},
			createTAP: func(context.Context, string, network.Allocation, int, int) (string, error) {
				return "ff0123456789abc", nil
			},
			deleteTAP: func(context.Context, string) error {
				return nil
			},
			applyForwarding: func(context.Context, string, network.Allocation) error {
				return nil
			},
			removeForwarding: func(context.Context, string, network.Allocation) error {
				return nil
			},
		}
	}
	if cfg.Workspace != nil && deps.workspace == nil {
		deps.workspace = &workspaceDependencies{
			export: func(
				context.Context,
				string,
				netip.Addr,
				uint32,
				uint32,
			) (string, error) {
				return filepath.Join(cfg.Workspace.Root, "workspace"), nil
			},
			applyNFSAccess: func(
				context.Context,
				string,
				network.Allocation,
			) error {
				return nil
			},
			removeNFSAccess: func(
				context.Context,
				string,
				network.Allocation,
			) error {
				return nil
			},
			unexport: func(context.Context, string, netip.Addr) error {
				return nil
			},
		}
	}
	supervisor := newSupervisorWithDependencies(cfg, deps)
	supervisor.snapshots = map[string]snapshotrestore.Bundle{
		"default": testSnapshotBundle(1, microvm.DefaultSandboxMemoryMiB),
	}
	return supervisor
}

func testSnapshotBundle(vcpus, memoryMiB int) snapshotrestore.Bundle {
	return snapshotrestore.Bundle{
		StatePath:  "/snapshots/template/vmstate",
		MemoryPath: "/snapshots/template/memory",
		Manifest: snapshotbundle.Manifest{
			Machine: snapshotbundle.Machine{
				VCPUCount:   vcpus,
				MemoryMiB:   memoryMiB,
				InterfaceID: "eth0",
				GuestMAC:    "02:fc:ac:10:00:02",
			},
		},
	}
}

func newFakeProcess() *fakeProcess {
	return &fakeProcess{done: make(chan struct{})}
}

func (p *fakeProcess) PID() int { return 123 }

func (p *fakeProcess) Done() <-chan struct{} { return p.done }

func (p *fakeProcess) StartedAt() time.Time { return time.Time{} }

func (p *fakeProcess) StoppedAt() time.Time { return time.Time{} }

func (p *fakeProcess) Wait() error {
	<-p.done
	return p.waitErr
}

func (p *fakeProcess) Stop(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopCalls++
	if p.onStop != nil {
		p.onStop()
	}
	if !p.stayAlive {
		select {
		case <-p.done:
		default:
			close(p.done)
		}
	}
	return p.stopErr
}

func (p *fakeProcess) Kill(ctx context.Context) error { return p.Stop(ctx) }

type fakeControlClient struct {
	shutdownErr   error
	shutdownCalls int
	closed        bool
	onShutdown    func()
}

func (c *fakeControlClient) SendCtrlAltDel(context.Context) error {
	c.shutdownCalls++
	if c.onShutdown != nil {
		c.onShutdown()
	}
	return c.shutdownErr
}

func (c *fakeControlClient) CloseIdleConnections() { c.closed = true }

type fakeRestoreClient struct {
	snapshotPath string
	memoryPath   string
	network      firecracker.NetworkInterface
	drivePath    string
	loadCalls    int
	patchCalls   int
	resumeCalls  int
	closed       bool
	loadErr      error
	patchErr     error
	resumeErr    error
}

func (c *fakeRestoreClient) LoadSnapshot(
	_ context.Context,
	snapshotPath,
	memoryPath string,
	networkInterface firecracker.NetworkInterface,
) error {
	c.loadCalls++
	c.snapshotPath = snapshotPath
	c.memoryPath = memoryPath
	c.network = networkInterface
	return c.loadErr
}

func (c *fakeRestoreClient) PatchContainerRootFS(
	_ context.Context,
	path string,
) error {
	c.patchCalls++
	c.drivePath = path
	return c.patchErr
}

func (c *fakeRestoreClient) Resume(context.Context) error {
	c.resumeCalls++
	return c.resumeErr
}

func (c *fakeRestoreClient) CloseIdleConnections() { c.closed = true }

func TestSupervisorStartAndStop(t *testing.T) {
	cfg := testConfig()
	image := cfg.Images[supervisorTestImageID]
	image.Process.Env = append(image.Process.Env, "CANTELOP_WORKSPACE_PATH=/stale-image-default")
	cfg.Images[supervisorTestImageID] = image
	cfg.Workspace = &config.WorkspaceConfig{
		ExportFSBin:   "/usr/sbin/exportfs",
		MountpointBin: "/usr/bin/mountpoint",
		Root:          "/var/lib/fire-fuse/workspaces",
	}
	machine := testMachine()
	process := newFakeProcess()
	client := &fakeControlClient{}
	restore := &fakeRestoreClient{}
	client.onShutdown = func() { close(process.done) }
	var prepared bool
	var cleaned int
	var launched int
	var restoreSocket string
	var allocated, tapCreated, forwardingApplied, forwardingRemoved, tapDeleted, released int
	var cgroupPrepared, cgroupRemoved int
	var workspaceExported, workspaceAllowed, workspaceAccessRemoved, workspaceUnexported int
	var identityPID int
	var assignment guestassignment.Assignment
	var recordedStartup startupTimings
	var startupRecords int
	var workspaceObservations []workspaceLifecycleObservation
	var checkpoints []microvm.RuntimeOwnership
	processIdentity := microvm.ProcessIdentity{
		PID:            process.PID(),
		BootID:         "8f648680-7a89-4d7e-8d89-9bc9f919cb2d",
		StartTimeTicks: 456,
	}
	supervisor := newSupervisor(cfg, dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare: func(_ context.Context, layout firecracker.JailLayout, kernel, rootfs, containerRootFS string, uid, gid int) error {
			prepared = true
			if kernel != "/images/vmlinux" ||
				rootfs != "/images/rootfs.ext4" ||
				containerRootFS != "/images/container-rootfs.ext4" {
				t.Errorf("profile assets = %q/%q/%q", kernel, rootfs, containerRootFS)
			}
			if uid != 1000 || gid != 1001 {
				t.Errorf("jail identity = %d:%d", uid, gid)
			}
			return nil
		},
		cleanup: func(firecracker.JailLayout) error { cleaned++; return nil },
		prepareCgroup: func(id string) error {
			cgroupPrepared++
			if id != machine.ID {
				t.Errorf("prepared cgroup ID = %q", id)
			}
			return nil
		},
		removeCgroup: func(id string) error {
			cgroupRemoved++
			if id != machine.ID {
				t.Errorf("removed cgroup ID = %q", id)
			}
			return nil
		},
		readUsage: func(id string) (cgroup.Usage, error) {
			return cgroup.Usage{CPUUsageUS: 1234, MemoryCurrentBytes: 32 << 20}, nil
		},
		launch: func(context.Context, firecracker.JailLayout) (managedProcess, error) {
			launched++
			return process, nil
		},
		processIdentity: func(pid int) (microvm.ProcessIdentity, error) {
			identityPID = pid
			return processIdentity, nil
		},
		newControlClient: func(socket string) (controlClient, error) {
			return client, nil
		},
		newRestoreClient: func(socket string) (restoreClient, error) {
			restoreSocket = socket
			return restore, nil
		},
		assignGuest: func(_ context.Context, _ string, got guestassignment.Assignment, _ guestassignment.Milestone) error {
			assignment = got
			select {
			case <-process.Done():
				t.Fatal("process exited before guest assignment")
			default:
			}
			return nil
		},
		recordStartup: func(machineID string, workspaceEnabled bool, timings startupTimings) {
			startupRecords++
			if machineID != machine.ID {
				t.Errorf("startup timing machine ID = %q, want %q", machineID, machine.ID)
			}
			if !workspaceEnabled {
				t.Error("startup timing did not mark workspace enabled")
			}
			recordedStartup = timings
		},
		recordWorkspace: func(observation workspaceLifecycleObservation) {
			workspaceObservations = append(workspaceObservations, observation)
		},
		network: &networkDependencies{
			allocate: func(vmID string) (network.Allocation, error) {
				allocated++
				if vmID != machine.ID {
					t.Errorf("allocate VM ID = %q, want %q", vmID, machine.ID)
				}
				return testAllocation(), nil
			},
			release: func(vmID string) error {
				released++
				return nil
			},
			createTAP: func(_ context.Context, vmID string, allocation network.Allocation, uid, gid int) (string, error) {
				tapCreated++
				if vmID != machine.ID || !reflect.DeepEqual(allocation, testAllocation()) {
					t.Errorf("create TAP input = %q/%#v", vmID, allocation)
				}
				if uid != cfg.JailerUID || gid != cfg.JailerGID {
					t.Errorf("TAP identity = %d:%d, want %d:%d", uid, gid, cfg.JailerUID, cfg.JailerGID)
				}
				return "ff0123456789abc", nil
			},
			deleteTAP: func(_ context.Context, vmID string) error {
				tapDeleted++
				return nil
			},
			applyForwarding: func(_ context.Context, tapName string, allocation network.Allocation) error {
				forwardingApplied++
				if tapName != "ff0123456789abc" || !reflect.DeepEqual(allocation, testAllocation()) {
					t.Errorf("apply forwarding input = %q/%#v", tapName, allocation)
				}
				return nil
			},
			removeForwarding: func(_ context.Context, tapName string, allocation network.Allocation) error {
				forwardingRemoved++
				return nil
			},
		},
		workspace: &workspaceDependencies{
			export: func(
				_ context.Context,
				workspaceID string,
				guestIP netip.Addr,
				uid, gid uint32,
			) (string, error) {
				workspaceExported++
				if workspaceID != machine.WorkspaceID ||
					guestIP != testAllocation().GuestIP ||
					uid != 1000 || gid != 1000 {
					t.Errorf("workspace export input = %q/%s/%d:%d", workspaceID, guestIP, uid, gid)
				}
				return filepath.Join(cfg.Workspace.Root, workspaceID), nil
			},
			applyNFSAccess: func(
				_ context.Context,
				tapName string,
				allocation network.Allocation,
			) error {
				workspaceAllowed++
				if tapName != "ff0123456789abc" ||
					!reflect.DeepEqual(allocation, testAllocation()) {
					t.Errorf("workspace access input = %q/%#v", tapName, allocation)
				}
				return nil
			},
			removeNFSAccess: func(
				context.Context,
				string,
				network.Allocation,
			) error {
				workspaceAccessRemoved++
				return nil
			},
			unexport: func(context.Context, string, netip.Addr) error {
				workspaceUnexported++
				return nil
			},
		},
	})

	trace := warmuptrace.New()
	startContext := warmuptrace.WithContext(context.Background(), trace)
	runtime, err := supervisor.Start(startContext, machine, func(ownership microvm.RuntimeOwnership) error {
		checkpoints = append(checkpoints, ownership)
		return nil
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got, want := assignment.Workload.Process.Env, []string{"PATH=/usr/bin:/bin", "CANTELOP_WORKSPACE_PATH=/workspace", "CANTELOP_SANDBOX_ID=" + machine.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("workload environment = %#v, want %#v", got, want)
	}
	if got := assignment.Workload.Process.WorkingDirectory; got != "/app" {
		t.Fatalf("custom image working directory = %q, want /app", got)
	}
	if !reflect.DeepEqual(cfg.Images[supervisorTestImageID].Process.Env, []string{"PATH=/usr/bin:/bin", "CANTELOP_WORKSPACE_PATH=/stale-image-default"}) {
		t.Fatal("workspace environment changed the image defaults")
	}
	networkAssignment := microvm.NetworkAssignment{
		GuestIP:   "172.16.0.2",
		GatewayIP: "172.16.0.1",
		Subnet:    "172.16.0.0/30",
		GuestMAC:  "02:fc:ac:10:00:02",
	}
	wantOwnership := microvm.RuntimeOwnership{
		JailPrepared:        true,
		CgroupCreated:       true,
		AddressAllocated:    true,
		TAPCreated:          true,
		ForwardingApplied:   true,
		WorkspaceExported:   true,
		WorkspaceNFSAllowed: true,
		TAPName:             "ff0123456789abc",
		Network:             networkAssignment,
		Process:             processIdentity,
	}
	wantRuntime := microvm.RuntimeInfo{
		Network:   networkAssignment,
		Ownership: wantOwnership,
	}
	if runtime != wantRuntime {
		t.Errorf("Start() runtime = %#v, want %#v", runtime, wantRuntime)
	}
	wantCheckpoints := []microvm.RuntimeOwnership{
		{JailPrepared: true, CgroupCreated: true},
		{
			JailPrepared:     true,
			CgroupCreated:    true,
			AddressAllocated: true,
			Network:          networkAssignment,
		},
		{
			JailPrepared:     true,
			CgroupCreated:    true,
			AddressAllocated: true,
			TAPCreated:       true,
			TAPName:          "ff0123456789abc",
			Network:          networkAssignment,
		},
		{
			JailPrepared:      true,
			CgroupCreated:     true,
			AddressAllocated:  true,
			TAPCreated:        true,
			ForwardingApplied: true,
			TAPName:           "ff0123456789abc",
			Network:           networkAssignment,
		},
		{
			JailPrepared:        true,
			CgroupCreated:       true,
			AddressAllocated:    true,
			TAPCreated:          true,
			ForwardingApplied:   true,
			WorkspaceExported:   true,
			WorkspaceNFSAllowed: true,
			TAPName:             "ff0123456789abc",
			Network:             networkAssignment,
		},
		wantOwnership,
	}
	if !reflect.DeepEqual(checkpoints, wantCheckpoints) {
		t.Errorf("runtime checkpoints = %#v, want %#v", checkpoints, wantCheckpoints)
	}
	traceSteps := make(map[string]bool)
	for _, step := range trace.Snapshot().Steps {
		traceSteps[step.Name] = true
	}
	for _, name := range []string{
		"supervisor.acquire_machine_lock",
		"supervisor.lookup_profile",
		"jail.derive_layout",
		"jail.prepare_assets",
		"store.checkpoint_jail_prepared",
		"network.allocate_address",
		"network.create_tap",
		"network.apply_forwarding",
		"store.checkpoint_workspace_setup_claimed",
		"workspace.export_nfs",
		"workspace.apply_nfs_firewall",
		"firecracker.launch",
		"firecracker.read_process_identity",
		"firecracker.load_snapshot",
		"firecracker.patch_container_rootfs",
		"firecracker.resume_snapshot",
		"guest.assign_snapshot",
		"supervisor.publish_runtime",
	} {
		if !traceSteps[name] {
			t.Errorf("warmup trace is missing %q", name)
		}
	}
	if traceSteps["store.checkpoint_cgroup_claimed"] {
		t.Error("warmup trace contains a standalone cgroup checkpoint")
	}
	for _, name := range []string{
		"store.checkpoint_workspace_export_claimed",
		"store.checkpoint_workspace_access_claimed",
	} {
		if traceSteps[name] {
			t.Errorf("warmup trace contains obsolete standalone checkpoint %q", name)
		}
	}
	if identityPID != process.PID() {
		t.Errorf("process identity PID = %d, want %d", identityPID, process.PID())
	}
	if startupRecords != 1 {
		t.Errorf("startup timing records = %d, want 1", startupRecords)
	}
	for name, duration := range map[string]time.Duration{
		"total":              recordedStartup.Total,
		"jail prepare":       recordedStartup.JailPrepare,
		"network setup":      recordedStartup.NetworkSetup,
		"Firecracker launch": recordedStartup.FirecrackerLaunch,
		"snapshot restore":   recordedStartup.SnapshotRestore,
		"guest assignment":   recordedStartup.GuestAssignment,
	} {
		if duration < 0 {
			t.Errorf("%s duration = %s, want non-negative", name, duration)
		}
	}
	if !prepared || launched != 1 {
		t.Errorf("prepared = %t, launch count = %d", prepared, launched)
	}
	if restoreSocket != "/runtime/firecracker/vm-test/root/run/firecracker.socket" {
		t.Errorf("restore client socket = %q", restoreSocket)
	}
	if restore.loadCalls != 1 || restore.patchCalls != 1 || restore.resumeCalls != 1 || !restore.closed {
		t.Errorf("restore calls = load:%d patch:%d resume:%d closed:%t", restore.loadCalls, restore.patchCalls, restore.resumeCalls, restore.closed)
	}
	if assignment.AssignmentID != machine.ID || !assignment.Workspace.Enabled || assignment.Workload.Process.Args[0] != "/bin/server" {
		t.Errorf("guest assignment = %#v", assignment)
	}
	if !supervisor.IsRunning(machine.ID) {
		t.Error("IsRunning() = false after successful start")
	}
	supervisor.recordGuestMemory(machine.ID, 400, 512, time.Now())
	usage, err := supervisor.RuntimeUsage(machine.ID)
	if err != nil || usage.SandboxID != machine.ID || usage.CPUUsageUS != 1234 || usage.MemoryCurrentBytes != 32<<20 ||
		usage.GuestMemoryUsedBytes == nil || *usage.GuestMemoryUsedBytes != 112<<20 {
		t.Fatalf("RuntimeUsage() = %#v, %v", usage, err)
	}
	done, exists := supervisor.RuntimeDone(machine.ID)
	if !exists {
		t.Fatal("RuntimeDone() did not find started process")
	}
	select {
	case <-done:
		t.Fatal("RuntimeDone() was closed while process was running")
	default:
	}

	secondRuntime, err := supervisor.Start(context.Background(), machine, discardRuntimeCheckpoint)
	if err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	if secondRuntime != runtime {
		t.Errorf("second Start() runtime = %#v, want %#v", secondRuntime, runtime)
	}
	if launched != 1 {
		t.Errorf("second Start() launched another process; count = %d", launched)
	}
	if startupRecords != 1 {
		t.Errorf("idempotent second Start recorded startup again; records = %d", startupRecords)
	}
	var releaseCheckpoints []microvm.RuntimeOwnership
	if err := supervisor.Stop(context.Background(), machine, func(ownership microvm.RuntimeOwnership) error {
		releaseCheckpoints = append(releaseCheckpoints, ownership)
		return nil
	}); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	wantReleaseCheckpoints := []microvm.RuntimeOwnership{
		{
			JailPrepared:        true,
			CgroupCreated:       true,
			AddressAllocated:    true,
			TAPCreated:          true,
			ForwardingApplied:   true,
			WorkspaceExported:   true,
			WorkspaceNFSAllowed: true,
			TAPName:             "ff0123456789abc",
			Network:             networkAssignment,
		},
		{
			JailPrepared:      true,
			CgroupCreated:     true,
			AddressAllocated:  true,
			TAPCreated:        true,
			ForwardingApplied: true,
			WorkspaceExported: true,
			TAPName:           "ff0123456789abc",
			Network:           networkAssignment,
		},
		{
			JailPrepared:      true,
			CgroupCreated:     true,
			AddressAllocated:  true,
			TAPCreated:        true,
			ForwardingApplied: true,
			TAPName:           "ff0123456789abc",
			Network:           networkAssignment,
		},
		{
			JailPrepared:     true,
			CgroupCreated:    true,
			AddressAllocated: true,
			TAPCreated:       true,
			TAPName:          "ff0123456789abc",
			Network:          networkAssignment,
		},
		{
			JailPrepared:     true,
			CgroupCreated:    true,
			AddressAllocated: true,
			Network:          networkAssignment,
		},
		{JailPrepared: true, CgroupCreated: true},
		{JailPrepared: true},
		{},
	}
	if !reflect.DeepEqual(releaseCheckpoints, wantReleaseCheckpoints) {
		t.Errorf("release checkpoints = %#v, want %#v", releaseCheckpoints, wantReleaseCheckpoints)
	}
	if client.shutdownCalls != 1 {
		t.Errorf("graceful guest shutdown calls = %d, want 1", client.shutdownCalls)
	}
	if len(workspaceObservations) != 1 ||
		workspaceObservations[0].MachineID != machine.ID ||
		workspaceObservations[0].Event != "shutdown" ||
		workspaceObservations[0].Outcome != "graceful" ||
		workspaceObservations[0].Duration <= 0 ||
		workspaceObservations[0].Err != nil {
		t.Errorf("workspace lifecycle observations = %#v, want one graceful shutdown", workspaceObservations)
	}
	if process.stopCalls != 0 || cleaned != 1 || cgroupPrepared != 1 || cgroupRemoved != 1 || allocated != 1 || tapCreated != 1 ||
		forwardingApplied != 1 || forwardingRemoved != 1 || tapDeleted != 1 || released != 1 ||
		workspaceExported != 1 || workspaceAllowed != 1 ||
		workspaceAccessRemoved != 1 || workspaceUnexported != 1 {
		t.Errorf(
			"lifecycle counts: force-stop=%d jail=%d allocate=%d createTAP=%d applyForwarding=%d removeForwarding=%d deleteTAP=%d release=%d export=%d allow=%d removeAccess=%d unexport=%d",
			process.stopCalls, cleaned, allocated, tapCreated, forwardingApplied, forwardingRemoved, tapDeleted, released,
			workspaceExported, workspaceAllowed, workspaceAccessRemoved, workspaceUnexported,
		)
	}
	if supervisor.IsRunning(machine.ID) {
		t.Error("IsRunning() = true after stop")
	}
	select {
	case <-done:
	default:
		t.Error("RuntimeDone() remained open after stop")
	}
	if err := supervisor.Stop(context.Background(), machine, discardRuntimeCheckpoint); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
	if client.shutdownCalls != 1 || process.stopCalls != 0 || cleaned != 1 ||
		forwardingRemoved != 1 || tapDeleted != 1 || released != 1 {
		t.Errorf("idempotent Stop changed lifecycle counts")
	}
}

func TestNewValidatesConfiguration(t *testing.T) {
	cfg := testConfig()
	cfg.JailerUID = 0
	if _, err := New(cfg, io.Discard); !errors.Is(err, config.ErrInvalid) {
		t.Fatalf("New() error = %v, want config.ErrInvalid", err)
	}
}

func TestLauncherConfigUsesConfiguredTimeouts(t *testing.T) {
	cfg := testConfig()
	cfg.Timeouts.APISocket = config.Duration(17 * time.Second)
	cfg.Timeouts.ShutdownGrace = config.Duration(19 * time.Second)
	cfg.Timeouts.ShutdownForce = config.Duration(23 * time.Second)

	launcher := newLauncherConfig(cfg, io.Discard, "system.slice/fire-fuse.service/sandboxes")
	if launcher.SocketTimeout != 17*time.Second {
		t.Errorf("SocketTimeout = %s, want 17s", launcher.SocketTimeout)
	}
	if launcher.ShutdownGrace != 19*time.Second {
		t.Errorf("ShutdownGrace = %s, want 19s", launcher.ShutdownGrace)
	}
	if launcher.ShutdownForce != 23*time.Second {
		t.Errorf("ShutdownForce = %s, want 23s", launcher.ShutdownForce)
	}
}

func testConfig() config.Config {
	return config.Config{
		FirecrackerBin:          "/usr/bin/firecracker",
		JailerBin:               "/usr/bin/jailer",
		JailerUID:               1000,
		JailerGID:               1001,
		RuntimeDir:              "/runtime",
		StateDB:                 "/runtime/state.db",
		ConfigEncryptionKeyFile: "/etc/fire-fuse/config-encryption.key",
		Network: config.NetworkConfig{
			IPBin:        "/usr/sbin/ip",
			IPTablesBin:  "/usr/sbin/iptables",
			IP6TablesBin: "/usr/sbin/ip6tables",
			AddressPool:  "172.16.0.0/16",
		},
		Limits: config.LimitsConfig{
			MaxVMs:              32,
			MaxTotalMemoryMiB:   32768,
			MaxConcurrentStarts: 2,
		},
		Timeouts: config.TimeoutsConfig{
			Startup:         config.Duration(30 * time.Second),
			APISocket:       config.Duration(5 * time.Second),
			GuestShutdown:   config.Duration(5 * time.Second),
			ShutdownGrace:   config.Duration(5 * time.Second),
			ShutdownForce:   config.Duration(5 * time.Second),
			Cleanup:         config.Duration(5 * time.Second),
			HTTPRequestRead: config.Duration(10 * time.Second),
			HTTPIdle:        config.Duration(30 * time.Second),
			ServerShutdown:  config.Duration(5 * time.Second),
		},
		Profiles: map[string]config.Profile{
			"default": {
				KernelImage:    "/images/vmlinux",
				RootFSImage:    "/images/rootfs.ext4",
				SnapshotBundle: "/snapshots/template",
				BootArgs:       "console=ttyS0 reboot=k panic=1",
			},
		},
		Images: map[string]config.ContainerImage{
			supervisorTestImageID: {
				RootFSImage:     "/images/container-rootfs.ext4",
				RootFSSHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				RootFSSizeBytes: 1024,
				Process: guestconfig.Process{
					Args:             []string{"/bin/server"},
					Env:              []string{"PATH=/usr/bin:/bin"},
					WorkingDirectory: "/app",
					UID:              1000,
					GID:              1000,
				},
			},
		},
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

func testMachine() microvm.Sandbox {
	return microvm.Sandbox{
		ID:          "vm-test",
		AppID:       "app-test",
		WorkspaceID: "wsp_vm_test",
		Profile:     "default",
		BuildID:     supervisorTestImageID,
		VCPUs:       1,
		MemoryMiB:   microvm.DefaultSandboxMemoryMiB,
		State:       microvm.StateStarting,
	}
}

func testAllocation() network.Allocation {
	return network.Allocation{
		Subnet:   netip.MustParsePrefix("172.16.0.0/30"),
		HostIP:   netip.MustParseAddr("172.16.0.1"),
		GuestIP:  netip.MustParseAddr("172.16.0.2"),
		GuestMAC: "02:fc:ac:10:00:02",
	}
}
