package supervisor

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/host/network"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func TestSupervisorRecoversPersistedRuntimeOwnership(t *testing.T) {
	machine := testMachine()
	machine.State = microvm.StateStopping
	assignment := microvm.NetworkAssignment{
		GuestIP:   "172.16.0.2",
		GatewayIP: "172.16.0.1",
		Subnet:    "172.16.0.0/30",
		GuestMAC:  "02:fc:ac:10:00:02",
	}
	identity := testPersistedProcessIdentity()
	tapName, err := network.TAPName(machine.ID)
	if err != nil {
		t.Fatalf("TAPName() error = %v", err)
	}
	machine.Runtime = microvm.RuntimeOwnership{
		JailPrepared:      true,
		CgroupCreated:     true,
		AddressAllocated:  true,
		TAPCreated:        true,
		ForwardingApplied: true,
		TAPName:           tapName,
		Network:           assignment,
		Process:           identity,
	}
	var operations []string
	var checkpoints []microvm.RuntimeOwnership
	cfg := testConfig()
	cfg.Timeouts.ShutdownGrace = config.Duration(3 * time.Second)
	cfg.Timeouts.ShutdownForce = config.Duration(7 * time.Second)
	supervisor := newSupervisor(cfg, dependencies{
		newLayout: firecracker.NewJailLayout,
		cleanup: func(firecracker.JailLayout) error {
			operations = append(operations, "clean jail")
			return nil
		},
		removeCgroup: func(id string) error {
			operations = append(operations, "remove cgroup")
			if id != machine.ID {
				t.Errorf("cgroup ID = %q", id)
			}
			return nil
		},
		terminateProcess: func(
			_ context.Context,
			got microvm.ProcessIdentity,
			graceTimeout time.Duration,
			forceTimeout time.Duration,
		) (processTerminationResult, error) {
			operations = append(operations, "terminate process")
			if got != identity {
				t.Errorf("terminated identity = %#v, want %#v", got, identity)
			}
			if graceTimeout != 3*time.Second || forceTimeout != 7*time.Second {
				t.Errorf("termination timeouts = %s/%s, want 3s/7s", graceTimeout, forceTimeout)
			}
			return processTerminationResult{Status: processIdentityMatch}, nil
		},
		network: &networkDependencies{
			removeForwarding: func(_ context.Context, tapName string, allocation network.Allocation) error {
				operations = append(operations, "remove forwarding")
				if tapName != machine.Runtime.TAPName || !reflect.DeepEqual(allocation, testAllocation()) {
					t.Errorf("forwarding input = %q/%#v", tapName, allocation)
				}
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
	})

	err = supervisor.Recover(context.Background(), machine, func(ownership microvm.RuntimeOwnership) error {
		checkpoints = append(checkpoints, ownership)
		return nil
	})
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	wantOperations := []string{
		"terminate process",
		"remove forwarding",
		"delete TAP",
		"release address",
		"remove cgroup",
		"clean jail",
	}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Errorf("recovery operations = %v, want %v", operations, wantOperations)
	}
	wantCheckpoints := []microvm.RuntimeOwnership{
		{
			JailPrepared:      true,
			CgroupCreated:     true,
			AddressAllocated:  true,
			TAPCreated:        true,
			ForwardingApplied: true,
			TAPName:           machine.Runtime.TAPName,
			Network:           assignment,
		},
		{
			JailPrepared:     true,
			CgroupCreated:    true,
			AddressAllocated: true,
			TAPCreated:       true,
			TAPName:          machine.Runtime.TAPName,
			Network:          assignment,
		},
		{
			JailPrepared:     true,
			CgroupCreated:    true,
			AddressAllocated: true,
			Network:          assignment,
		},
		{JailPrepared: true, CgroupCreated: true},
		{JailPrepared: true},
		{},
	}
	if !reflect.DeepEqual(checkpoints, wantCheckpoints) {
		t.Errorf("recovery checkpoints = %#v, want %#v", checkpoints, wantCheckpoints)
	}
}

func TestSupervisorRecoveryUsesPersistedHostSlotIdentity(t *testing.T) {
	machine := testMachine()
	machine.State = microvm.StateStopping
	hostSlotID := "slot-01"
	tapName, err := network.TAPName(hostSlotID)
	if err != nil {
		t.Fatal(err)
	}
	allocation := testAllocation()
	machine.Runtime = microvm.RuntimeOwnership{
		HostSlotID:        hostSlotID,
		JailPrepared:      true,
		AddressAllocated:  true,
		TAPCreated:        true,
		ForwardingApplied: true,
		TAPName:           tapName,
		Network: microvm.NetworkAssignment{
			GuestIP:   allocation.GuestIP.String(),
			GatewayIP: allocation.HostIP.String(),
			Subnet:    allocation.Subnet.String(),
			GuestMAC:  allocation.GuestMAC,
		},
	}

	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		cleanup: func(layout firecracker.JailLayout) error {
			if layout.VMID != hostSlotID {
				t.Errorf("cleaned jail %q, want host slot %q", layout.VMID, hostSlotID)
			}
			return nil
		},
		network: &networkDependencies{
			removeForwarding: func(context.Context, string, network.Allocation) error { return nil },
			deleteTAP: func(_ context.Context, id string) error {
				if id != hostSlotID {
					t.Errorf("deleted TAP owner %q, want %q", id, hostSlotID)
				}
				return nil
			},
			release: func(id string) error {
				if id != hostSlotID {
					t.Errorf("released address owner %q, want %q", id, hostSlotID)
				}
				return nil
			},
		},
	})

	if err := supervisor.Recover(context.Background(), machine, discardRuntimeCheckpoint); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
}

func TestSupervisorRecoveryStopsBeforeCleanupWhenProcessTerminationFails(t *testing.T) {
	wantErr := errors.New("pidfd wait failed")
	machine := testMachine()
	machine.State = microvm.StateStopping
	machine.Runtime = microvm.RuntimeOwnership{
		JailPrepared: true,
		Process:      testPersistedProcessIdentity(),
	}
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		cleanup: func(firecracker.JailLayout) error {
			t.Fatal("jail cleaned while recovered process termination was unconfirmed")
			return nil
		},
		terminateProcess: func(
			context.Context,
			microvm.ProcessIdentity,
			time.Duration,
			time.Duration,
		) (processTerminationResult, error) {
			return processTerminationResult{}, wantErr
		},
	})

	if err := supervisor.Recover(context.Background(), machine, discardRuntimeCheckpoint); !errors.Is(err, wantErr) {
		t.Fatalf("Recover() error = %v, want termination error", err)
	}
}

func TestSupervisorRecoveryRejectsUntrustedOwnershipMappings(t *testing.T) {
	insideAssignment := microvm.NetworkAssignment{
		GuestIP:   "172.16.0.2",
		GatewayIP: "172.16.0.1",
		Subnet:    "172.16.0.0/30",
		GuestMAC:  "02:fc:ac:10:00:02",
	}
	outsideAssignment := microvm.NetworkAssignment{
		GuestIP:   "10.0.0.2",
		GatewayIP: "10.0.0.1",
		Subnet:    "10.0.0.0/30",
		GuestMAC:  "02:fc:0a:00:00:02",
	}
	tests := []struct {
		name    string
		state   microvm.State
		runtime microvm.RuntimeOwnership
	}{
		{
			name:    "machine is not stopping",
			state:   microvm.StateRunning,
			runtime: microvm.RuntimeOwnership{JailPrepared: true},
		},
		{
			name:  "TAP name does not belong to VM",
			state: microvm.StateStopping,
			runtime: microvm.RuntimeOwnership{
				JailPrepared:     true,
				AddressAllocated: true,
				TAPCreated:       true,
				TAPName:          "ff0000000000000",
				Network:          insideAssignment,
			},
		},
		{
			name:  "network is outside configured pool",
			state: microvm.StateStopping,
			runtime: microvm.RuntimeOwnership{
				JailPrepared:     true,
				AddressAllocated: true,
				Network:          outsideAssignment,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			machine := testMachine()
			machine.State = test.state
			machine.Runtime = test.runtime
			supervisor := newSupervisor(testConfig(), dependencies{
				newLayout: firecracker.NewJailLayout,
				cleanup: func(firecracker.JailLayout) error {
					t.Fatal("cleanup called for untrusted recovered ownership")
					return nil
				},
				network: &networkDependencies{},
			})

			if err := supervisor.Recover(context.Background(), machine, discardRuntimeCheckpoint); err == nil {
				t.Fatal("Recover() error = nil")
			}
		})
	}
}
