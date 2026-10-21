package observer

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSandboxRuntimeTimingRoundTrip(t *testing.T) {
	data := []byte(`{"id":"sbx-one","runtime_timing":{"status":"stopped","started_at":"2026-09-21T12:00:00Z","stopped_at":"2026-09-21T12:00:02Z"},"updated_at":"2026-09-21T13:00:00Z"}`)
	var sandbox Sandbox
	if err := json.Unmarshal(data, &sandbox); err != nil {
		t.Fatal(err)
	}
	if sandbox.RuntimeTiming == nil || sandbox.RuntimeTiming.Status != "stopped" || sandbox.RuntimeTiming.StoppedAt.Sub(*sandbox.RuntimeTiming.StartedAt) != 2*time.Second {
		t.Fatalf("timing=%+v", sandbox.RuntimeTiming)
	}
	encoded, err := json.Marshal(sandbox)
	if err != nil {
		t.Fatal(err)
	}
	var again Sandbox
	if err := json.Unmarshal(encoded, &again); err != nil {
		t.Fatal(err)
	}
	if again.RuntimeTiming == nil || *again.RuntimeTiming.StartedAt != *sandbox.RuntimeTiming.StartedAt || *again.RuntimeTiming.StoppedAt != *sandbox.RuntimeTiming.StoppedAt {
		t.Fatal("observer changed interval")
	}
}
