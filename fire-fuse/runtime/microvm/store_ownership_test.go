package microvm

import (
	"testing"
	"time"
)

func TestStoreClearsRuntimeOwnershipWhenTerminated(t *testing.T) {
	store := openTestSQLiteStore(t)
	runtime := RuntimeOwnership{JailPrepared: true}
	machine := testSandbox(Sandbox{ID: "vm-clean", State: StateStopping, Runtime: runtime})
	if err := store.Create(machine); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	terminated, err := store.Transition(machine.ID, StateTerminated, time.Now())
	if err != nil {
		t.Fatalf("Transition() error = %v", err)
	}
	if terminated.Runtime != (RuntimeOwnership{}) {
		t.Errorf("terminated Runtime = %#v, want cleared ownership", terminated.Runtime)
	}
}
