package warmuptrace

import (
	"context"
	"testing"
)

func TestTraceRecordsRequestSteps(t *testing.T) {
	trace := New()
	ctx := WithContext(context.Background(), trace)

	finish := Start(ctx, "service.validate")
	finish()
	finish()
	SetMachineID(ctx, "vm-test")

	snapshot := trace.Snapshot()
	if snapshot.MachineID != "vm-test" {
		t.Fatalf("machine ID = %q, want vm-test", snapshot.MachineID)
	}
	if snapshot.TotalNS < 1 {
		t.Fatalf("total = %d, want positive", snapshot.TotalNS)
	}
	if len(snapshot.Steps) != 1 {
		t.Fatalf("steps = %#v, want one idempotently completed step", snapshot.Steps)
	}
	if snapshot.Steps[0].Name != "service.validate" {
		t.Fatalf("step name = %q, want service.validate", snapshot.Steps[0].Name)
	}
	if snapshot.Steps[0].DurationNS < 1 {
		t.Fatalf("step duration = %d, want positive", snapshot.Steps[0].DurationNS)
	}
}

func TestTraceContextHelpersAreOptional(t *testing.T) {
	ctx := context.Background()
	Start(ctx, "ignored")()
	SetMachineID(ctx, "ignored")
	if trace := FromContext(ctx); trace != nil {
		t.Fatalf("trace = %#v, want nil", trace)
	}
}
