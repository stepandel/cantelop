package microvm

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeTimingSurvivesFailureCleanupAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	machine := persistedStartingMachine("vm-timing", at)
	machine.RuntimeTiming.Status = "pending"
	if err := store.Create(machine); err != nil {
		t.Fatal(err)
	}
	timing := RuntimeTiming{Status: "running", StartedAt: at.Add(time.Second)}
	if _, err := store.RecordRuntimeTiming(machine.ID, timing); err != nil {
		t.Fatal(err)
	}
	timing.Status, timing.StoppedAt = "stopped", at.Add(3*time.Second)
	if _, err := store.RecordRuntimeTiming(machine.ID, timing); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fail(machine.ID, Failure{Reason: FailureRuntimeStart}, at.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(machine.ID, StateStopping, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(machine.ID, StateTerminated, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Late start callbacks and repeated exit callbacks cannot extend the interval.
	for _, replay := range []RuntimeTiming{{Status: "running", StartedAt: timing.StartedAt}, timing, {Status: "unknown", StartedAt: timing.StartedAt}} {
		if _, err := store.RecordRuntimeTiming(machine.ID, replay); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(machine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RuntimeTiming != timing || !got.UpdatedAt.Equal(at.Add(2*time.Hour)) {
		t.Fatalf("after cleanup: %+v", got)
	}
}

func TestRuntimeTimingRejectsInventedOrInvalidMeasurements(t *testing.T) {
	at := time.Now().UTC()
	for _, previous := range []RuntimeTiming{{}, {Status: "unknown"}} {
		got, err := mergeRuntimeTiming(previous, RuntimeTiming{Status: "running", StartedAt: at})
		if err != nil || got != previous {
			t.Fatalf("legacy/unknown rewritten: %+v, %v", got, err)
		}
	}
	for _, next := range []RuntimeTiming{
		{Status: "stopped", StartedAt: at, StoppedAt: at.Add(-time.Second)},
		{Status: "running"}, {Status: "pending", StartedAt: at},
	} {
		if _, err := mergeRuntimeTiming(RuntimeTiming{Status: "pending"}, next); err == nil {
			t.Fatalf("accepted %+v", next)
		}
	}
	if _, err := mergeRuntimeTiming(RuntimeTiming{Status: "running", StartedAt: at}, RuntimeTiming{Status: "running", StartedAt: at.Add(time.Second)}); err == nil {
		t.Fatal("changed start timestamp")
	}
}
