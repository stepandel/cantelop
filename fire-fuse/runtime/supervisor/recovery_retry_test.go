package supervisor

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/host/network"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func TestSupervisorRecoveryFailuresResumeFromDurableOwnership(t *testing.T) {
	machine, stages := recoveryRetryMachine(t)
	wantErr := errors.New("injected recovery failure")
	tests := []struct {
		name                  string
		failAction            string
		failCheckpointCall    int
		wantDurableAfterFirst microvm.RuntimeOwnership
		wantFirstOperations   []string
		wantRetryOperations   []string
	}{
		{
			name:                  "process termination",
			failAction:            "terminate process",
			wantDurableAfterFirst: stages.process,
			wantFirstOperations:   []string{"terminate process"},
			wantRetryOperations:   []string{"terminate process", "remove forwarding", "delete TAP", "release address", "clean jail"},
		},
		{
			name:                  "firewall removal",
			failAction:            "remove forwarding",
			wantDurableAfterFirst: stages.forwarding,
			wantFirstOperations:   []string{"terminate process", "remove forwarding"},
			wantRetryOperations:   []string{"remove forwarding", "delete TAP", "release address", "clean jail"},
		},
		{
			name:                  "TAP deletion",
			failAction:            "delete TAP",
			wantDurableAfterFirst: stages.tap,
			wantFirstOperations:   []string{"terminate process", "remove forwarding", "delete TAP"},
			wantRetryOperations:   []string{"delete TAP", "release address", "clean jail"},
		},
		{
			name:                  "address release",
			failAction:            "release address",
			wantDurableAfterFirst: stages.address,
			wantFirstOperations:   []string{"terminate process", "remove forwarding", "delete TAP", "release address"},
			wantRetryOperations:   []string{"release address", "clean jail"},
		},
		{
			name:                  "jail cleanup",
			failAction:            "clean jail",
			wantDurableAfterFirst: stages.jail,
			wantFirstOperations:   []string{"terminate process", "remove forwarding", "delete TAP", "release address", "clean jail"},
			wantRetryOperations:   []string{"clean jail"},
		},
		{
			name:                  "ownership checkpoint",
			failCheckpointCall:    2,
			wantDurableAfterFirst: stages.forwarding,
			wantFirstOperations:   []string{"terminate process", "remove forwarding"},
			wantRetryOperations:   []string{"remove forwarding", "delete TAP", "release address", "clean jail"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "state.db")
			store, err := microvm.OpenSQLiteStore(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Create(machine); err != nil {
				_ = store.Close()
				t.Fatal(err)
			}

			var operations []string
			actionFailed := false
			checkpointFailed := false
			checkpointCalls := 0
			perform := func(action string) error {
				operations = append(operations, action)
				if action == test.failAction && !actionFailed {
					actionFailed = true
					return wantErr
				}
				return nil
			}
			newRecoverer := func() *Supervisor {
				return newSupervisor(testConfig(), dependencies{
					newLayout: firecracker.NewJailLayout,
					cleanup: func(firecracker.JailLayout) error {
						return perform("clean jail")
					},
					terminateProcess: func(
						context.Context,
						microvm.ProcessIdentity,
						time.Duration,
						time.Duration,
					) (processTerminationResult, error) {
						if err := perform("terminate process"); err != nil {
							return processTerminationResult{}, err
						}
						return processTerminationResult{Status: processIdentityMatch}, nil
					},
					network: &networkDependencies{
						removeForwarding: func(context.Context, string, network.Allocation) error {
							return perform("remove forwarding")
						},
						deleteTAP: func(context.Context, string) error {
							return perform("delete TAP")
						},
						release: func(string) error {
							return perform("release address")
						},
					},
				})
			}
			checkpointAt := machine.CreatedAt
			checkpoint := func(ownership microvm.RuntimeOwnership) error {
				checkpointCalls++
				if checkpointCalls == test.failCheckpointCall && !checkpointFailed {
					checkpointFailed = true
					return wantErr
				}
				checkpointAt = checkpointAt.Add(time.Second)
				_, err := store.CheckpointRuntime(machine.ID, ownership, checkpointAt)
				return err
			}

			firstErr := newRecoverer().Recover(context.Background(), machine, checkpoint)
			if !errors.Is(firstErr, wantErr) {
				_ = store.Close()
				t.Fatalf("first Recover() error = %v, want injected failure", firstErr)
			}
			if !reflect.DeepEqual(operations, test.wantFirstOperations) {
				t.Errorf("first operations = %v, want %v", operations, test.wantFirstOperations)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			store, err = microvm.OpenSQLiteStore(statePath)
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := store.Get(machine.ID)
			if err != nil {
				_ = store.Close()
				t.Fatal(err)
			}
			if persisted.Runtime != test.wantDurableAfterFirst {
				_ = store.Close()
				t.Fatalf("ownership after failure = %#v, want %#v", persisted.Runtime, test.wantDurableAfterFirst)
			}

			operations = nil
			if err := newRecoverer().Recover(context.Background(), persisted, checkpoint); err != nil {
				_ = store.Close()
				t.Fatalf("retry Recover() error = %v", err)
			}
			if !reflect.DeepEqual(operations, test.wantRetryOperations) {
				t.Errorf("retry operations = %v, want %v", operations, test.wantRetryOperations)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			store, err = microvm.OpenSQLiteStore(statePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			persisted, err = store.Get(machine.ID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Runtime != (microvm.RuntimeOwnership{}) {
				t.Errorf("ownership after retry = %#v, want empty", persisted.Runtime)
			}
		})
	}
}

type recoveryOwnershipStages struct {
	jail       microvm.RuntimeOwnership
	address    microvm.RuntimeOwnership
	tap        microvm.RuntimeOwnership
	forwarding microvm.RuntimeOwnership
	process    microvm.RuntimeOwnership
}

func recoveryRetryMachine(t *testing.T) (microvm.Sandbox, recoveryOwnershipStages) {
	t.Helper()
	machine := testMachine()
	machine.State = microvm.StateStopping
	machine.CreatedAt = time.Date(2025, time.July, 21, 12, 0, 0, 0, time.UTC)
	machine.UpdatedAt = machine.CreatedAt
	allocation := testAllocation()
	assignment := microvm.NetworkAssignment{
		GuestIP: allocation.GuestIP.String(), GatewayIP: allocation.HostIP.String(),
		Subnet: allocation.Subnet.String(), GuestMAC: allocation.GuestMAC,
	}
	tapName, err := network.TAPName(machine.ID)
	if err != nil {
		t.Fatal(err)
	}
	stages := recoveryOwnershipStages{}
	stages.jail = microvm.RuntimeOwnership{JailPrepared: true}
	stages.address = microvm.RuntimeOwnership{
		JailPrepared: true, AddressAllocated: true, Network: assignment,
	}
	stages.tap = microvm.RuntimeOwnership{
		JailPrepared: true, AddressAllocated: true, TAPCreated: true,
		TAPName: tapName, Network: assignment,
	}
	stages.forwarding = microvm.RuntimeOwnership{
		JailPrepared: true, AddressAllocated: true, TAPCreated: true,
		ForwardingApplied: true, TAPName: tapName, Network: assignment,
	}
	stages.process = stages.forwarding
	stages.process.Process = testPersistedProcessIdentity()
	machine.Runtime = stages.process
	return machine, stages
}
