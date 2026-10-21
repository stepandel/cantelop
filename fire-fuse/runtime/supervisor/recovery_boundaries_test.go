package supervisor

import (
	"context"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/host/network"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func TestSupervisorRecoversEveryDurableCheckpointBoundary(t *testing.T) {
	machine := testMachine()
	machine.State = microvm.StateStopping
	machine.CreatedAt = time.Date(2025, time.July, 21, 12, 0, 0, 0, time.UTC)
	machine.UpdatedAt = machine.CreatedAt
	allocation := testAllocation()
	assignment := microvm.NetworkAssignment{
		GuestIP:   allocation.GuestIP.String(),
		GatewayIP: allocation.HostIP.String(),
		Subnet:    allocation.Subnet.String(),
		GuestMAC:  allocation.GuestMAC,
	}
	tapName, err := network.TAPName(machine.ID)
	if err != nil {
		t.Fatal(err)
	}

	empty := microvm.RuntimeOwnership{}
	jail := microvm.RuntimeOwnership{JailPrepared: true}
	address := microvm.RuntimeOwnership{
		JailPrepared: true, AddressAllocated: true, Network: assignment,
	}
	tap := microvm.RuntimeOwnership{
		JailPrepared: true, AddressAllocated: true, TAPCreated: true,
		TAPName: tapName, Network: assignment,
	}
	forwarding := microvm.RuntimeOwnership{
		JailPrepared: true, AddressAllocated: true, TAPCreated: true,
		ForwardingApplied: true, TAPName: tapName, Network: assignment,
	}
	exported := forwarding
	exported.WorkspaceExported = true
	allowed := exported
	allowed.WorkspaceNFSAllowed = true
	cgroupJail := jail
	cgroupJail.CgroupCreated = true
	cgroupAddress := address
	cgroupAddress.CgroupCreated = true
	cgroupTAP := tap
	cgroupTAP.CgroupCreated = true
	cgroupForwarding := forwarding
	cgroupForwarding.CgroupCreated = true
	cgroupExported := exported
	cgroupExported.CgroupCreated = true
	cgroupOwned := allowed
	cgroupOwned.CgroupCreated = true
	process := cgroupOwned
	process.Process = testPersistedProcessIdentity()

	tests := []struct {
		name            string
		ownership       microvm.RuntimeOwnership
		wantOperations  []string
		wantCheckpoints []microvm.RuntimeOwnership
	}{
		{
			name:            "jail prepared",
			ownership:       jail,
			wantOperations:  []string{"clean jail"},
			wantCheckpoints: []microvm.RuntimeOwnership{empty},
		},
		{
			name:            "address allocated",
			ownership:       address,
			wantOperations:  []string{"release address", "clean jail"},
			wantCheckpoints: []microvm.RuntimeOwnership{jail, empty},
		},
		{
			name:            "TAP created",
			ownership:       tap,
			wantOperations:  []string{"delete TAP", "release address", "clean jail"},
			wantCheckpoints: []microvm.RuntimeOwnership{address, jail, empty},
		},
		{
			name:            "forwarding installed",
			ownership:       forwarding,
			wantOperations:  []string{"remove forwarding", "delete TAP", "release address", "clean jail"},
			wantCheckpoints: []microvm.RuntimeOwnership{tap, address, jail, empty},
		},
		{
			name:           "workspace exported",
			ownership:      exported,
			wantOperations: []string{"unexport workspace", "remove forwarding", "delete TAP", "release address", "clean jail"},
			wantCheckpoints: []microvm.RuntimeOwnership{
				forwarding, tap, address, jail, empty,
			},
		},
		{
			name:           "workspace NFS allowed",
			ownership:      allowed,
			wantOperations: []string{"remove workspace NFS access", "unexport workspace", "remove forwarding", "delete TAP", "release address", "clean jail"},
			wantCheckpoints: []microvm.RuntimeOwnership{
				exported, forwarding, tap, address, jail, empty,
			},
		},
		{
			name:           "cgroup created",
			ownership:      cgroupOwned,
			wantOperations: []string{"remove workspace NFS access", "unexport workspace", "remove forwarding", "delete TAP", "release address", "remove cgroup", "clean jail"},
			wantCheckpoints: []microvm.RuntimeOwnership{
				cgroupExported, cgroupForwarding, cgroupTAP, cgroupAddress, cgroupJail, jail, empty,
			},
		},
		{
			name:           "process launched",
			ownership:      process,
			wantOperations: []string{"terminate process", "remove workspace NFS access", "unexport workspace", "remove forwarding", "delete TAP", "release address", "remove cgroup", "clean jail"},
			wantCheckpoints: []microvm.RuntimeOwnership{
				cgroupOwned, cgroupExported, cgroupForwarding, cgroupTAP, cgroupAddress, cgroupJail, jail, empty,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "state.db")
			store, err := microvm.OpenSQLiteStore(statePath)
			if err != nil {
				t.Fatal(err)
			}
			persisted := machine
			persisted.Runtime = test.ownership
			if err := store.Create(persisted); err != nil {
				_ = store.Close()
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			store, err = microvm.OpenSQLiteStore(statePath)
			if err != nil {
				t.Fatal(err)
			}
			persisted, err = store.Get(machine.ID)
			if err != nil {
				_ = store.Close()
				t.Fatal(err)
			}

			var operations []string
			var checkpoints []microvm.RuntimeOwnership
			supervisor := newSupervisor(testConfig(), dependencies{
				newLayout: firecracker.NewJailLayout,
				cleanup: func(firecracker.JailLayout) error {
					operations = append(operations, "clean jail")
					return nil
				},
				removeCgroup: func(string) error {
					operations = append(operations, "remove cgroup")
					return nil
				},
				terminateProcess: func(
					context.Context,
					microvm.ProcessIdentity,
					time.Duration,
					time.Duration,
				) (processTerminationResult, error) {
					operations = append(operations, "terminate process")
					return processTerminationResult{Status: processIdentityMatch}, nil
				},
				network: &networkDependencies{
					removeForwarding: func(context.Context, string, network.Allocation) error {
						operations = append(operations, "remove forwarding")
						return nil
					},
					deleteTAP: func(context.Context, string) error {
						operations = append(operations, "delete TAP")
						return nil
					},
					release: func(string) error {
						operations = append(operations, "release address")
						return nil
					},
				},
				workspace: &workspaceDependencies{
					removeNFSAccess: func(
						_ context.Context,
						gotTAP string,
						gotAllocation network.Allocation,
					) error {
						operations = append(operations, "remove workspace NFS access")
						if gotTAP != tapName ||
							!reflect.DeepEqual(gotAllocation, allocation) {
							t.Errorf(
								"workspace NFS access mapping = %q/%#v",
								gotTAP,
								gotAllocation,
							)
						}
						return nil
					},
					unexport: func(
						_ context.Context,
						gotWorkspaceID string,
						gotGuestIP netip.Addr,
					) error {
						operations = append(operations, "unexport workspace")
						if gotWorkspaceID != machine.WorkspaceID ||
							gotGuestIP != allocation.GuestIP {
							t.Errorf(
								"workspace export mapping = %q/%s",
								gotWorkspaceID,
								gotGuestIP,
							)
						}
						return nil
					},
				},
			})

			checkpointAt := machine.CreatedAt
			err = supervisor.Recover(context.Background(), persisted, func(ownership microvm.RuntimeOwnership) error {
				checkpoints = append(checkpoints, ownership)
				checkpointAt = checkpointAt.Add(time.Second)
				_, checkpointErr := store.CheckpointRuntime(machine.ID, ownership, checkpointAt)
				return checkpointErr
			})
			if err != nil {
				_ = store.Close()
				t.Fatalf("Recover() error = %v", err)
			}
			if !reflect.DeepEqual(operations, test.wantOperations) {
				t.Errorf("operations = %v, want %v", operations, test.wantOperations)
			}
			if !reflect.DeepEqual(checkpoints, test.wantCheckpoints) {
				t.Errorf("checkpoints = %#v, want %#v", checkpoints, test.wantCheckpoints)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := microvm.OpenSQLiteStore(statePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			afterRecovery, err := reopened.Get(machine.ID)
			if err != nil {
				t.Fatal(err)
			}
			if afterRecovery.Runtime != empty {
				t.Errorf("durable ownership after recovery = %#v, want empty", afterRecovery.Runtime)
			}
		})
	}
}
