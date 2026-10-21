package supervisor

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/host/network"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func TestSupervisorDoesNotDeleteTAPUntilForwardingIsRemoved(t *testing.T) {
	removeErr := errors.New("firewall busy")
	process := newFakeProcess()
	removeCalls := 0
	deleteCalls := 0
	releaseCalls := 0
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare:   func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
		cleanup:   func(firecracker.JailLayout) error { return nil },
		launch:    func(context.Context, firecracker.JailLayout) (managedProcess, error) { return process, nil },
		newControlClient: func(string) (controlClient, error) {
			return &fakeControlClient{shutdownErr: errors.New("guest shutdown unavailable")}, nil
		},
		network: &networkDependencies{
			allocate: func(string) (network.Allocation, error) { return testAllocation(), nil },
			release: func(string) error {
				releaseCalls++
				return nil
			},
			createTAP: func(context.Context, string, network.Allocation, int, int) (string, error) {
				return "ff0123456789abc", nil
			},
			deleteTAP: func(context.Context, string) error {
				deleteCalls++
				return nil
			},
			applyForwarding: func(context.Context, string, network.Allocation) error { return nil },
			removeForwarding: func(context.Context, string, network.Allocation) error {
				removeCalls++
				if removeCalls == 1 {
					return removeErr
				}
				return nil
			},
		},
	})
	if _, err := supervisor.Start(context.Background(), testMachine(), discardRuntimeCheckpoint); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if err := supervisor.Stop(context.Background(), testMachine(), discardRuntimeCheckpoint); !errors.Is(err, removeErr) {
		t.Fatalf("first Stop() error = %v, want forwarding removal error", err)
	}
	if deleteCalls != 0 || releaseCalls != 0 {
		t.Fatalf("delete/release calls = %d/%d while rules remain, want 0/0", deleteCalls, releaseCalls)
	}
	if err := supervisor.Stop(context.Background(), testMachine(), discardRuntimeCheckpoint); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
	if removeCalls != 2 || deleteCalls != 1 || releaseCalls != 1 {
		t.Errorf("remove/delete/release calls = %d/%d/%d, want 2/1/1", removeCalls, deleteCalls, releaseCalls)
	}
}

func TestSupervisorDoesNotReleaseAddressUntilTAPIsDeleted(t *testing.T) {
	deleteErr := errors.New("TAP busy")
	process := newFakeProcess()
	deleteCalls := 0
	releaseCalls := 0
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare:   func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
		cleanup:   func(firecracker.JailLayout) error { return nil },
		launch:    func(context.Context, firecracker.JailLayout) (managedProcess, error) { return process, nil },
		newControlClient: func(string) (controlClient, error) {
			return &fakeControlClient{shutdownErr: errors.New("guest shutdown unavailable")}, nil
		},
		network: &networkDependencies{
			allocate: func(string) (network.Allocation, error) { return testAllocation(), nil },
			release: func(string) error {
				releaseCalls++
				return nil
			},
			createTAP: func(context.Context, string, network.Allocation, int, int) (string, error) {
				return "ff0123456789abc", nil
			},
			deleteTAP: func(context.Context, string) error {
				deleteCalls++
				if deleteCalls == 1 {
					return deleteErr
				}
				return nil
			},
			applyForwarding:  func(context.Context, string, network.Allocation) error { return nil },
			removeForwarding: func(context.Context, string, network.Allocation) error { return nil },
		},
	})
	if _, err := supervisor.Start(context.Background(), testMachine(), discardRuntimeCheckpoint); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if err := supervisor.Stop(context.Background(), testMachine(), discardRuntimeCheckpoint); !errors.Is(err, deleteErr) {
		t.Fatalf("first Stop() error = %v, want TAP deletion error", err)
	}
	if releaseCalls != 0 {
		t.Fatalf("release calls = %d while TAP still exists, want 0", releaseCalls)
	}
	if err := supervisor.Stop(context.Background(), testMachine(), discardRuntimeCheckpoint); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
	if deleteCalls != 2 || releaseCalls != 1 {
		t.Errorf("delete/release calls = %d/%d, want 2/1", deleteCalls, releaseCalls)
	}
}

func TestSupervisorCleanupUsesConfiguredTimeout(t *testing.T) {
	cfg := testConfig()
	cfg.Timeouts.Cleanup = config.Duration(2 * time.Second)
	wantErr := errors.New("stop after observing cleanup context")
	supervisor := newSupervisor(cfg, dependencies{
		network: &networkDependencies{
			removeForwarding: func(ctx context.Context, _ string, _ network.Allocation) error {
				deadline, exists := ctx.Deadline()
				if !exists {
					t.Error("cleanup context has no deadline")
					return wantErr
				}
				remaining := time.Until(deadline)
				if remaining < time.Second || remaining > 3*time.Second {
					t.Errorf("cleanup deadline remaining = %s, want approximately 2s", remaining)
				}
				return wantErr
			},
		},
	})
	owned := &instance{
		network: &instanceNetwork{
			forwardingOwned: true,
			tapName:         "ff0000000000000",
			allocation:      testAllocation(),
		},
	}

	err := supervisor.cleanupInstance(
		context.Background(),
		"vm-test",
		owned,
		discardRuntimeCheckpoint,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("cleanupInstance() error = %v, want observed cleanup error", err)
	}
}

func TestSupervisorCleanupRetriesAfterCheckpointFailure(t *testing.T) {
	checkpointErr := errors.New("state store unavailable")
	var removeCalls, deleteCalls, releaseCalls, cleanupCalls int
	supervisor := newSupervisor(testConfig(), dependencies{
		cleanup: func(firecracker.JailLayout) error {
			cleanupCalls++
			return nil
		},
		network: &networkDependencies{
			removeForwarding: func(context.Context, string, network.Allocation) error {
				removeCalls++
				return nil
			},
			deleteTAP: func(context.Context, string) error {
				deleteCalls++
				return nil
			},
			release: func(string) error {
				releaseCalls++
				return nil
			},
		},
	})
	owned := &instance{
		jailPrepared: true,
		network: &instanceNetwork{
			addressAllocated: true,
			tapOwned:         true,
			forwardingOwned:  true,
			tapName:          "ff0123456789abc",
			allocation:       testAllocation(),
		},
	}

	err := supervisor.cleanupInstance(
		context.Background(),
		testMachine().ID,
		owned,
		func(microvm.RuntimeOwnership) error { return checkpointErr },
	)
	if !errors.Is(err, checkpointErr) {
		t.Fatalf("first cleanup error = %v, want checkpoint error", err)
	}
	if removeCalls != 1 || deleteCalls != 0 || releaseCalls != 0 || cleanupCalls != 0 {
		t.Fatalf(
			"first cleanup calls remove/delete/release/jail = %d/%d/%d/%d, want 1/0/0/0",
			removeCalls, deleteCalls, releaseCalls, cleanupCalls,
		)
	}
	if !owned.network.forwardingOwned {
		t.Fatal("forwarding ownership was not restored after checkpoint failure")
	}

	if err := supervisor.cleanupInstance(
		context.Background(),
		testMachine().ID,
		owned,
		discardRuntimeCheckpoint,
	); err != nil {
		t.Fatalf("second cleanup error = %v", err)
	}
	if removeCalls != 2 || deleteCalls != 1 || releaseCalls != 1 || cleanupCalls != 1 {
		t.Errorf(
			"retry cleanup calls remove/delete/release/jail = %d/%d/%d/%d, want 2/1/1/1",
			removeCalls, deleteCalls, releaseCalls, cleanupCalls,
		)
	}
	if runtimeOwnership(owned) != (microvm.RuntimeOwnership{}) {
		t.Errorf("ownership after retry = %#v, want empty", runtimeOwnership(owned))
	}
}

func TestSupervisorWorkspaceCleanupRetriesAfterCheckpointFailure(t *testing.T) {
	checkpointErr := errors.New("state store unavailable")
	var removeAccessCalls, unexportCalls, removeForwardingCalls int
	supervisor := newSupervisor(testConfig(), dependencies{
		workspace: &workspaceDependencies{
			removeNFSAccess: func(
				context.Context,
				string,
				network.Allocation,
			) error {
				removeAccessCalls++
				return nil
			},
			unexport: func(context.Context, string, netip.Addr) error {
				unexportCalls++
				return nil
			},
		},
		network: &networkDependencies{
			removeForwarding: func(
				context.Context,
				string,
				network.Allocation,
			) error {
				removeForwardingCalls++
				return nil
			},
		},
	})
	owned := &instance{
		workspaceID:         "workspace-1",
		workspaceExported:   true,
		workspaceNFSAllowed: true,
		network: &instanceNetwork{
			forwardingOwned: true,
			tapName:         "ff0123456789abc",
			allocation:      testAllocation(),
		},
	}

	err := supervisor.cleanupInstance(
		context.Background(),
		testMachine().ID,
		owned,
		func(microvm.RuntimeOwnership) error { return checkpointErr },
	)
	if !errors.Is(err, checkpointErr) {
		t.Fatalf("first cleanup error = %v, want checkpoint error", err)
	}
	if removeAccessCalls != 1 || unexportCalls != 0 ||
		removeForwardingCalls != 0 {
		t.Fatalf(
			"first cleanup calls access/export/forwarding = %d/%d/%d, want 1/0/0",
			removeAccessCalls,
			unexportCalls,
			removeForwardingCalls,
		)
	}
	if !owned.workspaceNFSAllowed || !owned.workspaceExported {
		t.Fatalf("workspace ownership not restored after checkpoint failure: %#v", owned)
	}

	if err := supervisor.cleanupInstance(
		context.Background(),
		testMachine().ID,
		owned,
		discardRuntimeCheckpoint,
	); err != nil {
		t.Fatalf("second cleanup error = %v", err)
	}
	if removeAccessCalls != 2 || unexportCalls != 1 ||
		removeForwardingCalls != 1 {
		t.Errorf(
			"retry cleanup calls access/export/forwarding = %d/%d/%d, want 2/1/1",
			removeAccessCalls,
			unexportCalls,
			removeForwardingCalls,
		)
	}
}

func TestSupervisorWorkspaceSetupIntentCleansEveryCrashBoundary(t *testing.T) {
	tests := []struct {
		name          string
		exportPresent bool
		accessPresent bool
	}{
		{name: "before workspace actions"},
		{name: "between export and access", exportPresent: true},
		{name: "after workspace actions", exportPresent: true, accessPresent: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exportPresent := test.exportPresent
			accessPresent := test.accessPresent
			var removeAccessCalls, unexportCalls int
			supervisor := newSupervisor(testConfig(), dependencies{
				workspace: &workspaceDependencies{
					removeNFSAccess: func(
						context.Context,
						string,
						network.Allocation,
					) error {
						removeAccessCalls++
						accessPresent = false
						return nil
					},
					unexport: func(context.Context, string, netip.Addr) error {
						unexportCalls++
						exportPresent = false
						return nil
					},
				},
			})
			owned := &instance{
				workspaceID:         "workspace-1",
				workspaceExported:   true,
				workspaceNFSAllowed: true,
				network: &instanceNetwork{
					tapName:    "ff0123456789abc",
					allocation: testAllocation(),
				},
			}

			if err := supervisor.cleanupInstance(
				context.Background(),
				testMachine().ID,
				owned,
				discardRuntimeCheckpoint,
			); err != nil {
				t.Fatalf("cleanup workspace setup intent: %v", err)
			}
			if removeAccessCalls != 1 || unexportCalls != 1 {
				t.Errorf(
					"cleanup calls access/export = %d/%d, want 1/1",
					removeAccessCalls,
					unexportCalls,
				)
			}
			if accessPresent || exportPresent {
				t.Errorf(
					"workspace resources remain after cleanup: access=%t export=%t",
					accessPresent,
					exportPresent,
				)
			}
			if owned.workspaceNFSAllowed || owned.workspaceExported {
				t.Errorf("workspace ownership remains after cleanup: %#v", owned)
			}
		})
	}
}
