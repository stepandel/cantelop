package supervisor

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestassignment"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/host/network"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotbundle"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotrestore"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func TestSupervisorRestoresMatchingSnapshot(t *testing.T) {
	cfg := testConfig()
	machine := testMachine()
	machine.BuildID = supervisorTestImageID
	machine.WorkspaceID = "wsp_snapshot"
	cfg.Images = map[string]config.ContainerImage{
		machine.BuildID: {
			RootFSImage:     "/images/node-22.ext4",
			RootFSSHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			RootFSSizeBytes: 1024,
			Process: guestconfig.Process{
				Args:             []string{"/usr/bin/node", "server.js"},
				Env:              []string{"PATH=/usr/bin:/bin", "CANTELOP_SANDBOX_ID=stale-image-identity"},
				WorkingDirectory: "/app",
				UID:              1000,
				GID:              1000,
			},
		},
	}
	process := newFakeProcess()
	fakeRestore := &fakeRestoreClient{}
	var restoreState, restoreMemory, preparedContainer string
	var assignment guestassignment.Assignment
	workspaceLoads := 0
	supervisor := newSupervisor(cfg, dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare: func(
			_ context.Context,
			_ firecracker.JailLayout,
			_ string,
			_ string,
			container string,
			_ int,
			_ int,
		) error {
			preparedContainer = container
			return nil
		},
		prepareRestore: func(
			_ context.Context,
			_ firecracker.JailLayout,
			state,
			memory string,
			_,
			_ int,
		) error {
			restoreState = state
			restoreMemory = memory
			return nil
		},
		cleanup: func(firecracker.JailLayout) error { return nil },
		launch: func(context.Context, firecracker.JailLayout) (managedProcess, error) {
			return process, nil
		},
		newControlClient: func(string) (controlClient, error) {
			t.Fatal("shutdown client used during snapshot startup")
			return nil, nil
		},
		newRestoreClient: func(string) (restoreClient, error) {
			return fakeRestore, nil
		},
		resolveEnvironment: func(appID string) ([]string, error) {
			workspaceLoads++
			if appID != machine.AppID {
				t.Fatalf("workspace App ID = %q, want %q", appID, machine.AppID)
			}
			return []string{"PATH=/custom/bin", "TOKEN=secret", "CANTELOP_SANDBOX_ID=stale-app-identity"}, nil
		},
		assignGuest: func(
			_ context.Context,
			_ string,
			got guestassignment.Assignment,
			_ guestassignment.Milestone,
		) error {
			assignment = got
			return nil
		},
	})
	supervisor.snapshots = map[string]snapshotrestore.Bundle{
		machine.Profile: {
			StatePath:  "/snapshots/template/vmstate",
			MemoryPath: "/snapshots/template/memory",
			Manifest: snapshotbundle.Manifest{
				Machine: snapshotbundle.Machine{
					VCPUCount:   machine.VCPUs,
					MemoryMiB:   machine.MemoryMiB,
					InterfaceID: "eth0",
					GuestMAC:    "02:fc:00:00:00:01",
				},
			},
		},
	}

	if _, err := supervisor.Start(
		context.Background(),
		machine,
		discardRuntimeCheckpoint,
	); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if restoreState != "/snapshots/template/vmstate" ||
		restoreMemory != "/snapshots/template/memory" {
		t.Fatalf("restore inputs = %q/%q", restoreState, restoreMemory)
	}
	if preparedContainer != "/images/node-22.ext4" {
		t.Fatalf("prepared container = %q, want catalog image", preparedContainer)
	}
	if fakeRestore.loadCalls != 1 ||
		fakeRestore.patchCalls != 1 ||
		fakeRestore.resumeCalls != 1 ||
		!fakeRestore.closed {
		t.Fatalf("restore client = %#v", fakeRestore)
	}
	if fakeRestore.snapshotPath != "/snapshot/vmstate" ||
		fakeRestore.memoryPath != "/snapshot/memory" ||
		fakeRestore.drivePath != "/container-rootfs.ext4" {
		t.Fatalf("jailed restore paths = %#v", fakeRestore)
	}
	if fakeRestore.network.GuestMAC != "02:fc:00:00:00:01" {
		t.Fatalf("restored guest MAC = %q", fakeRestore.network.GuestMAC)
	}
	if assignment.AssignmentID != machine.ID ||
		assignment.ClockUnixNano <= 0 ||
		assignment.Network.Address != testAllocation().GuestIP.String() ||
		assignment.Network.Gateway != testAllocation().HostIP.String() ||
		assignment.Workload.Process.Args[0] != "/usr/bin/node" {
		t.Fatalf("assignment = %#v", assignment)
	}
	if got, want := assignment.Workload.Process.Env, []string{"PATH=/custom/bin", "CANTELOP_SANDBOX_ID=" + machine.ID, "TOKEN=secret"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("assignment workspace = %#v, want %#v", got, want)
	}
	if _, err := supervisor.Start(context.Background(), machine, discardRuntimeCheckpoint); err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	if workspaceLoads != 1 {
		t.Fatalf("Workspace resolver calls = %d, want 1 for an already-running instance", workspaceLoads)
	}
}

func TestSupervisorRejectsSnapshotShapeMismatch(t *testing.T) {
	cfg := testConfig()
	machine := testMachine()
	supervisor := newSupervisor(cfg, dependencies{})
	supervisor.snapshots = map[string]snapshotrestore.Bundle{
		machine.Profile: testSnapshotBundle(machine.VCPUs+1, machine.MemoryMiB),
	}

	if err := supervisor.Validate(microvm.RuntimeSpec{
		Profile: machine.Profile, BuildID: machine.BuildID,
		VCPUs: machine.VCPUs, MemoryMiB: machine.MemoryMiB,
	}); !errors.Is(err, microvm.ErrUnsupportedShape) {
		t.Fatalf("Validate() error = %v, want ErrUnsupportedShape", err)
	}
}

func TestSupervisorRejectsLegacy256MiBSnapshot(t *testing.T) {
	cfg := testConfig()
	machine := testMachine()
	supervisor := newSupervisor(cfg, dependencies{})
	supervisor.snapshots = map[string]snapshotrestore.Bundle{
		machine.Profile: testSnapshotBundle(machine.VCPUs, 256),
	}

	if err := supervisor.Validate(microvm.RuntimeSpec{
		Profile: machine.Profile, BuildID: machine.BuildID,
		VCPUs: machine.VCPUs, MemoryMiB: machine.MemoryMiB,
	}); !errors.Is(err, microvm.ErrUnsupportedShape) {
		t.Fatalf("Validate() error = %v, want ErrUnsupportedShape", err)
	}
}

func TestSupervisorStartRollsBackSnapshotRestoreFailure(t *testing.T) {
	wantErr := errors.New("snapshot rejected")
	process := newFakeProcess()
	client := &fakeRestoreClient{loadErr: wantErr}
	var operations []string
	process.onStop = func() { operations = append(operations, "stop process") }
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare:   func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
		cleanup: func(firecracker.JailLayout) error {
			operations = append(operations, "delete jail")
			return nil
		},
		launch: func(context.Context, firecracker.JailLayout) (managedProcess, error) { return process, nil },
		newRestoreClient: func(string) (restoreClient, error) {
			return client, nil
		},
		network: &networkDependencies{
			allocate: func(string) (network.Allocation, error) {
				return testAllocation(), nil
			},
			release: func(string) error {
				operations = append(operations, "release address")
				return nil
			},
			createTAP: func(context.Context, string, network.Allocation, int, int) (string, error) {
				return "ff0123456789abc", nil
			},
			deleteTAP: func(context.Context, string) error {
				operations = append(operations, "delete TAP")
				return nil
			},
			applyForwarding: func(context.Context, string, network.Allocation) error { return nil },
			removeForwarding: func(context.Context, string, network.Allocation) error {
				operations = append(operations, "remove forwarding")
				return nil
			},
		},
	})

	_, err := supervisor.Start(context.Background(), testMachine(), discardRuntimeCheckpoint)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v, want snapshot restore error", err)
	}
	wantOperations := []string{"stop process", "remove forwarding", "delete TAP", "release address", "delete jail"}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Errorf("rollback operations = %#v, want %#v", operations, wantOperations)
	}
	if !client.closed {
		t.Error("failed restore client was not closed")
	}
}

func TestSupervisorStartRollsBackGuestAssignmentFailure(t *testing.T) {
	wantErr := errors.New("guest assignment unavailable")
	process := newFakeProcess()
	var operations []string
	process.onStop = func() { operations = append(operations, "stop process") }
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare:   func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
		cleanup: func(firecracker.JailLayout) error {
			operations = append(operations, "delete jail")
			return nil
		},
		launch: func(context.Context, firecracker.JailLayout) (managedProcess, error) { return process, nil },
		assignGuest: func(context.Context, string, guestassignment.Assignment, guestassignment.Milestone) error {
			return wantErr
		},
		network: &networkDependencies{
			allocate: func(string) (network.Allocation, error) {
				return testAllocation(), nil
			},
			release: func(string) error {
				operations = append(operations, "release address")
				return nil
			},
			createTAP: func(context.Context, string, network.Allocation, int, int) (string, error) {
				return "ff0123456789abc", nil
			},
			deleteTAP: func(context.Context, string) error {
				operations = append(operations, "delete TAP")
				return nil
			},
			applyForwarding: func(context.Context, string, network.Allocation) error { return nil },
			removeForwarding: func(context.Context, string, network.Allocation) error {
				operations = append(operations, "remove forwarding")
				return nil
			},
		},
	})

	_, err := supervisor.Start(context.Background(), testMachine(), discardRuntimeCheckpoint)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v, want guest assignment error", err)
	}
	wantOperations := []string{"stop process", "remove forwarding", "delete TAP", "release address", "delete jail"}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Errorf("rollback operations = %#v, want %#v", operations, wantOperations)
	}
}

func TestSupervisorStartRollsBackTAPCreationFailure(t *testing.T) {
	wantErr := errors.New("TAP setup failed")
	var operations []string
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare:   func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
		cleanup: func(firecracker.JailLayout) error {
			operations = append(operations, "delete jail")
			return nil
		},
		launch: func(context.Context, firecracker.JailLayout) (managedProcess, error) {
			t.Fatal("launch called after TAP creation failed")
			return nil, nil
		},
		newControlClient: func(string) (controlClient, error) { return nil, nil },
		network: &networkDependencies{
			allocate: func(string) (network.Allocation, error) {
				return testAllocation(), nil
			},
			release: func(string) error {
				operations = append(operations, "release address")
				return nil
			},
			createTAP: func(context.Context, string, network.Allocation, int, int) (string, error) {
				return "", wantErr
			},
			deleteTAP: func(context.Context, string) error {
				operations = append(operations, "retry TAP deletion")
				return nil
			},
			applyForwarding:  func(context.Context, string, network.Allocation) error { return nil },
			removeForwarding: func(context.Context, string, network.Allocation) error { return nil },
		},
	})

	_, err := supervisor.Start(context.Background(), testMachine(), discardRuntimeCheckpoint)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v, want TAP setup error", err)
	}
	wantOperations := []string{"retry TAP deletion", "release address", "delete jail"}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Errorf("rollback operations = %#v, want %#v", operations, wantOperations)
	}
}

func TestSupervisorStartRollsBackForwardingFailure(t *testing.T) {
	wantErr := errors.New("forwarding setup failed")
	var operations []string
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare:   func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
		cleanup: func(firecracker.JailLayout) error {
			operations = append(operations, "delete jail")
			return nil
		},
		launch: func(context.Context, firecracker.JailLayout) (managedProcess, error) {
			t.Fatal("launch called after forwarding setup failed")
			return nil, nil
		},
		newControlClient: func(string) (controlClient, error) { return nil, nil },
		network: &networkDependencies{
			allocate: func(string) (network.Allocation, error) { return testAllocation(), nil },
			release: func(string) error {
				operations = append(operations, "release address")
				return nil
			},
			createTAP: func(context.Context, string, network.Allocation, int, int) (string, error) {
				return "ff0123456789abc", nil
			},
			deleteTAP: func(context.Context, string) error {
				operations = append(operations, "delete TAP")
				return nil
			},
			applyForwarding: func(context.Context, string, network.Allocation) error { return wantErr },
			removeForwarding: func(context.Context, string, network.Allocation) error {
				operations = append(operations, "retry forwarding removal")
				return nil
			},
		},
	})

	_, err := supervisor.Start(context.Background(), testMachine(), discardRuntimeCheckpoint)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v, want forwarding setup error", err)
	}
	wantOperations := []string{"retry forwarding removal", "delete TAP", "release address", "delete jail"}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Errorf("rollback operations = %#v, want %#v", operations, wantOperations)
	}
}

func TestSupervisorRetainsFailedRollbackForStopRetry(t *testing.T) {
	restoreErr := errors.New("snapshot rejected")
	cleanupErr := errors.New("filesystem busy")
	process := newFakeProcess()
	cleanupCalls := 0
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare:   func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
		cleanup: func(firecracker.JailLayout) error {
			cleanupCalls++
			if cleanupCalls == 1 {
				return cleanupErr
			}
			return nil
		},
		launch: func(context.Context, firecracker.JailLayout) (managedProcess, error) { return process, nil },
		newRestoreClient: func(string) (restoreClient, error) {
			return &fakeRestoreClient{loadErr: restoreErr}, nil
		},
	})

	_, err := supervisor.Start(context.Background(), testMachine(), discardRuntimeCheckpoint)
	if !errors.Is(err, restoreErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Start() error = %v, want restore and cleanup errors", err)
	}
	if err := supervisor.Stop(context.Background(), testMachine(), discardRuntimeCheckpoint); err != nil {
		t.Fatalf("Stop() retry error = %v", err)
	}
	if cleanupCalls != 2 {
		t.Errorf("cleanup calls = %d, want 2", cleanupCalls)
	}
}

func TestSupervisorStartDoesNotCleanupFailedPreparation(t *testing.T) {
	wantErr := errors.New("jail already exists")
	cleaned := 0
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare:   func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return wantErr },
		cleanup:   func(firecracker.JailLayout) error { cleaned++; return nil },
		launch: func(context.Context, firecracker.JailLayout) (managedProcess, error) {
			t.Fatal("launch called after preparation failure")
			return nil, nil
		},
		newControlClient: func(string) (controlClient, error) { return nil, nil },
	})

	if _, err := supervisor.Start(context.Background(), testMachine(), discardRuntimeCheckpoint); !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v, want preparation error", err)
	}
	if cleaned != 0 {
		t.Errorf("cleanup called %d times for an unowned existing jail", cleaned)
	}
}

func TestSupervisorClassifiesUnknownProfile(t *testing.T) {
	machine := testMachine()
	machine.Profile = "missing"
	supervisor := newSupervisor(testConfig(), dependencies{})

	validateErr := supervisor.Validate(microvm.RuntimeSpec{Profile: machine.Profile})
	if !errors.Is(validateErr, microvm.ErrUnknownProfile) {
		t.Fatalf("Validate() error = %v, want microvm.ErrUnknownProfile", validateErr)
	}
	_, err := supervisor.Start(context.Background(), machine, discardRuntimeCheckpoint)
	if !errors.Is(err, microvm.ErrUnknownProfile) {
		t.Fatalf("Start() error = %v, want microvm.ErrUnknownProfile", err)
	}
	if !errors.Is(err, config.ErrProfileNotFound) {
		t.Errorf("Start() error = %v, want config.ErrProfileNotFound cause", err)
	}
}

func TestSupervisorClassifiesUnknownImage(t *testing.T) {
	machine := testMachine()
	machine.BuildID = "missing"
	supervisor := newSupervisor(testConfig(), dependencies{})

	validateErr := supervisor.Validate(microvm.RuntimeSpec{
		Profile: machine.Profile,
		BuildID: machine.BuildID,
	})
	if !errors.Is(validateErr, microvm.ErrUnknownImage) {
		t.Fatalf("Validate() error = %v, want microvm.ErrUnknownImage", validateErr)
	}
	_, err := supervisor.Start(context.Background(), machine, discardRuntimeCheckpoint)
	if !errors.Is(err, microvm.ErrUnknownImage) {
		t.Fatalf("Start() error = %v, want microvm.ErrUnknownImage", err)
	}
	if !errors.Is(err, config.ErrImageNotFound) {
		t.Errorf("Start() error = %v, want config.ErrImageNotFound cause", err)
	}
}

func TestSupervisorValidateObservesNewlyPublishedImage(t *testing.T) {
	available := false
	image := testConfig().Images[supervisorTestImageID]
	supervisor := newSupervisor(testConfig(), dependencies{
		resolveImage: func(imageID string) (config.ContainerImage, error) {
			if imageID != supervisorTestImageID || !available {
				return config.ContainerImage{}, config.ErrImageNotFound
			}
			return image, nil
		},
	})
	spec := microvm.RuntimeSpec{
		Profile:   "default",
		BuildID:   supervisorTestImageID,
		VCPUs:     1,
		MemoryMiB: microvm.DefaultSandboxMemoryMiB,
	}

	if err := supervisor.Validate(spec); !errors.Is(err, microvm.ErrUnknownImage) {
		t.Fatalf("Validate() before publish error = %v, want microvm.ErrUnknownImage", err)
	}

	available = true
	if err := supervisor.Validate(spec); err != nil {
		t.Fatalf("Validate() after publish error = %v", err)
	}
}

func TestSupervisorStartRejectsProcessThatExitsDuringAssignment(t *testing.T) {
	process := newFakeProcess()
	cleaned := 0
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare:   func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
		cleanup:   func(firecracker.JailLayout) error { cleaned++; return nil },
		launch:    func(context.Context, firecracker.JailLayout) (managedProcess, error) { return process, nil },
		assignGuest: func(context.Context, string, guestassignment.Assignment, guestassignment.Milestone) error {
			close(process.done)
			return nil
		},
	})

	_, err := supervisor.Start(context.Background(), testMachine(), discardRuntimeCheckpoint)
	if !errors.Is(err, ErrProcessExitedDuringStart) {
		t.Fatalf("Start() error = %v, want ErrProcessExitedDuringStart", err)
	}
	if cleaned != 1 {
		t.Errorf("cleanup calls = %d, want 1", cleaned)
	}
}

func TestSupervisorConcurrentStartIsIdempotent(t *testing.T) {
	process := newFakeProcess()
	var mu sync.Mutex
	launches := 0
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare:   func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
		cleanup:   func(firecracker.JailLayout) error { return nil },
		launch: func(context.Context, firecracker.JailLayout) (managedProcess, error) {
			mu.Lock()
			launches++
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			return process, nil
		},
		newControlClient: func(string) (controlClient, error) { return &fakeControlClient{}, nil },
	})

	const callers = 20
	errorsByCaller := make(chan error, callers)
	for range callers {
		go func() {
			_, err := supervisor.Start(context.Background(), testMachine(), discardRuntimeCheckpoint)
			errorsByCaller <- err
		}()
	}
	for range callers {
		if err := <-errorsByCaller; err != nil {
			t.Errorf("concurrent Start() error = %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if launches != 1 {
		t.Errorf("launches = %d, want 1", launches)
	}
}

func TestSupervisorStartDeadlineCause(t *testing.T) {
	tests := []struct {
		name               string
		startupTimeout     time.Duration
		newContext         func() context.Context
		wantCause          error
		wantStartupTimeout bool
	}{
		{
			name:               "configured startup timeout",
			startupTimeout:     time.Nanosecond,
			newContext:         context.Background,
			wantCause:          context.DeadlineExceeded,
			wantStartupTimeout: true,
		},
		{
			name:           "caller cancellation",
			startupTimeout: time.Hour,
			newContext: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantCause: context.Canceled,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.Timeouts.Startup = config.Duration(test.startupTimeout)
			var cleaned int
			supervisor := newSupervisor(cfg, dependencies{
				newLayout: firecracker.NewJailLayout,
				prepare: func(context.Context, firecracker.JailLayout, string, string, string, int, int) error {
					return nil
				},
				cleanup: func(firecracker.JailLayout) error {
					cleaned++
					return nil
				},
				launch: func(ctx context.Context, _ firecracker.JailLayout) (managedProcess, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				},
			})

			_, err := supervisor.Start(test.newContext(), testMachine(), discardRuntimeCheckpoint)
			if !errors.Is(err, test.wantCause) {
				t.Errorf("Start() error = %v, want cause %v", err, test.wantCause)
			}
			if got := errors.Is(err, ErrStartupTimeout); got != test.wantStartupTimeout {
				t.Errorf("Start() ErrStartupTimeout = %v, want %v; error = %v", got, test.wantStartupTimeout, err)
			}
			if cleaned != 1 {
				t.Errorf("cleanup calls = %d, want 1", cleaned)
			}
		})
	}
}
