package httpapi

import (
	"encoding/json"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"testing"
	"time"
)

func TestSandboxResponsePreservesRuntimeTiming(t *testing.T) {
	at := time.Now().UTC()
	timing := microvm.RuntimeTiming{Status: "stopped", StartedAt: at, StoppedAt: at.Add(time.Second)}
	response := newSandboxResponse(microvm.Sandbox{RuntimeTiming: timing, UpdatedAt: at.Add(time.Hour)})
	if response.RuntimeTiming != timing {
		t.Fatalf("timing=%+v", response.RuntimeTiming)
	}
	if newSandboxResponse(microvm.Sandbox{}).RuntimeTiming.Status != "unknown" {
		t.Fatal("legacy duration must be unknown")
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["runtime_timing"]; !ok {
		t.Fatal("runtime timing missing from API")
	}
}

func TestUnknownRuntimeTimingOmitsMissingBoundaries(t *testing.T) {
	encoded, err := json.Marshal(newSandboxResponse(microvm.Sandbox{}))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Timing map[string]any `json:"runtime_timing"`
	}
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Timing) != 1 || body.Timing["status"] != "unknown" {
		t.Fatalf("unknown timing=%s", encoded)
	}
}
