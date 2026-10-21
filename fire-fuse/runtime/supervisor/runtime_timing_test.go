package supervisor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestassignment"
	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func TestExitTimingSurvivesWriteFailureAndRetry(t *testing.T) {
	raw := newFakeProcess()
	var mu sync.Mutex
	unavailable := false
	var saved microvm.RuntimeTiming
	p := observeProcess(raw, func(timing microvm.RuntimeTiming) error {
		mu.Lock()
		defer mu.Unlock()
		if unavailable {
			return errors.New("database unavailable")
		}
		saved = timing
		return nil
	})
	if _, err := p.persist(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	started := saved.StartedAt
	unavailable = true
	mu.Unlock()
	close(raw.done)
	<-p.Done()
	if _, err := p.persist(); !errors.Is(err, microvm.ErrStateStore) {
		t.Fatalf("persist = %v", err)
	}
	p.mu.Lock()
	stopped := p.timing.StoppedAt
	p.mu.Unlock()
	mu.Lock()
	unavailable = false
	mu.Unlock()
	if _, err := p.persist(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if saved.Status != "stopped" || saved.StartedAt != started || saved.StoppedAt != stopped {
		t.Fatalf("retry changed timing: %+v", saved)
	}
}

func TestSupervisorRecordsExitBeforeDelayedCleanup(t *testing.T) {
	for _, failStartup := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal shutdown", true: "startup failure"}[failStartup], func(t *testing.T) {
			process := newFakeProcess()
			store := newTestStore(t)
			machine := testMachine()
			machine.CreatedAt, machine.UpdatedAt = time.Now().UTC(), time.Now().UTC()
			machine.RuntimeTiming.Status = "pending"
			if err := store.Create(machine); err != nil {
				t.Fatal(err)
			}
			ctx := microvm.WithRuntimeTimingRecorder(context.Background(), func(timing microvm.RuntimeTiming) error {
				_, err := store.RecordRuntimeTiming(machine.ID, timing)
				return err
			})
			failCleanup := true
			supervisor := newSupervisor(testConfig(), dependencies{
				newLayout: firecracker.NewJailLayout,
				prepare:   func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
				cleanup: func(firecracker.JailLayout) error {
					got, err := store.Get(machine.ID)
					if err != nil {
						t.Fatal(err)
					}
					if got.RuntimeTiming.Status != "stopped" {
						t.Errorf("cleanup before exit saved: %+v", got.RuntimeTiming)
					}
					if failCleanup {
						return errors.New("delayed cleanup")
					}
					return nil
				},
				launch:           func(context.Context, firecracker.JailLayout) (managedProcess, error) { return process, nil },
				newControlClient: func(string) (controlClient, error) { return &fakeControlClient{}, nil },
				assignGuest: func(context.Context, string, guestassignment.Assignment, guestassignment.Milestone) error {
					if failStartup {
						return errors.New("assignment failed")
					}
					return nil
				},
			})
			_, err := supervisor.Start(ctx, machine, discardRuntimeCheckpoint)
			if failStartup && err == nil {
				t.Fatal("expected startup error")
			}
			if !failStartup {
				if err != nil {
					t.Fatal(err)
				}
				if err := supervisor.Stop(context.Background(), machine, discardRuntimeCheckpoint); err == nil {
					t.Fatal("expected cleanup error")
				}
			}
			before, err := store.Get(machine.ID)
			if err != nil {
				t.Fatal(err)
			}
			if before.RuntimeTiming.Status != "stopped" {
				t.Fatalf("timing: %+v", before.RuntimeTiming)
			}
			failCleanup = false
			if err := supervisor.Stop(context.Background(), machine, discardRuntimeCheckpoint); err != nil {
				t.Fatal(err)
			}
			after, err := store.Get(machine.ID)
			if err != nil {
				t.Fatal(err)
			}
			if before.RuntimeTiming != after.RuntimeTiming {
				t.Fatal("cleanup changed runtime interval")
			}
		})
	}
}

func TestSupervisorRecordsZeroBeforeLaunch(t *testing.T) {
	machine := testMachine()
	machine.RuntimeTiming.Status = "pending"
	machine.BuildID = "missing"
	var timing microvm.RuntimeTiming
	ctx := microvm.WithRuntimeTimingRecorder(context.Background(), func(value microvm.RuntimeTiming) error { timing = value; return nil })
	_, err := newSupervisor(testConfig(), dependencies{}).Start(ctx, machine, discardRuntimeCheckpoint)
	if err == nil || timing.Status != "not_started" || !timing.StartedAt.IsZero() {
		t.Fatalf("timing=%+v error=%v", timing, err)
	}
}

func TestExitObservationDoesNotWaitForLifecycleLock(t *testing.T) {
	raw := newFakeProcess()
	recorded := make(chan microvm.RuntimeTiming, 2)
	p := observeProcess(raw, func(timing microvm.RuntimeTiming) error { recorded <- timing; return nil })
	close(raw.done)
	select {
	case timing := <-recorded:
		if timing.Status != "stopped" {
			t.Fatalf("timing=%+v", timing)
		}
	case <-time.After(time.Second):
		t.Fatal("exit not recorded independently")
	}
	<-p.Done()
}

// The production launcher records a process even when API readiness fails.
func TestSupervisorKeepsTimingOfLaunchThatFailsAfterProcessCreation(t *testing.T) {
	store := newTestStore(t)
	machine := testMachine()
	machine.CreatedAt, machine.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	machine.RuntimeTiming.Status = "pending"
	if err := store.Create(machine); err != nil {
		t.Fatal(err)
	}
	ctx := microvm.WithRuntimeTimingRecorder(context.Background(), func(timing microvm.RuntimeTiming) error {
		_, err := store.RecordRuntimeTiming(machine.ID, timing)
		return err
	})
	supervisor := newSupervisor(testConfig(), dependencies{
		newLayout: firecracker.NewJailLayout,
		prepare:   func(context.Context, firecracker.JailLayout, string, string, string, int, int) error { return nil },
		cleanup:   func(firecracker.JailLayout) error { return nil },
		launch: func(ctx context.Context, _ firecracker.JailLayout) (managedProcess, error) {
			raw := newFakeProcess()
			tracked := observeProcess(raw, microvm.RuntimeTimingRecorderFromContext(ctx))
			if _, err := tracked.persist(); err != nil {
				return tracked, err
			}
			close(raw.done)
			return tracked, errors.New("API socket readiness failed")
		},
	})
	if _, err := supervisor.Start(ctx, machine, discardRuntimeCheckpoint); err == nil {
		t.Fatal("expected launch error")
	}
	got, err := store.Get(machine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RuntimeTiming.Status != "stopped" {
		t.Fatalf("lost failed launch interval: %+v", got.RuntimeTiming)
	}
}
