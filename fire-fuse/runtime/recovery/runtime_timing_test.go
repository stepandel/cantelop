package recovery

import (
	"context"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"testing"
	"time"
)

func TestRecoveryDoesNotTreatRestartAsRuntimeExit(t *testing.T) {
	at := time.Now().UTC()
	for _, timing := range []microvm.RuntimeTiming{
		{Status: "pending"},
		{Status: "running", StartedAt: at},
		{Status: "stopped", StartedAt: at, StoppedAt: at.Add(time.Second)},
		{},
	} {
		store := newTestStore(t)
		machine := testRecoveryMachine("vm-timing", microvm.StateStopping, at)
		machine.RuntimeTiming = timing
		if err := store.Create(machine); err != nil {
			t.Fatal(err)
		}
		if err := Reconcile(context.Background(), store, &fakeRuntimeRecoverer{}, func() time.Time { return at.Add(time.Hour) }); err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(machine.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := timing
		if timing.Status == "running" || timing.Status == "pending" {
			want.Status = "unknown"
		}
		if got.RuntimeTiming != want {
			t.Fatalf("got %+v, want %+v", got.RuntimeTiming, want)
		}
	}
}
