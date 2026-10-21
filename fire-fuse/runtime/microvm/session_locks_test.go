package microvm

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionReactivationIsSerializedOnlyPerSession(t *testing.T) {
	var blocking atomic.Bool
	var starts atomic.Int32
	entered := make(chan string, 4)
	release := make(chan struct{})
	service, err := NewServiceWithSupervisorAndLimits(newTestStore(t), fakeSupervisor{
		start: func(_ context.Context, machine Sandbox) error {
			if !blocking.Load() {
				return nil
			}
			starts.Add(1)
			entered <- machine.ID
			<-release
			return nil
		},
		stop: func(context.Context, Sandbox) error { return nil },
	}, AdmissionLimits{MaxVMs: 4, MaxTotalMemoryMiB: 1 << 20, MaxConcurrentStarts: 4})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"ses_lock_first", "ses_lock_second"} {
		if _, err := service.CreateSessionAndStart(ctx, id, "wsp_project123", 120); err != nil {
			t.Fatal(err)
		}
		if _, err := service.TerminateSession(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	blocking.Store(true)

	resolve := func(id string, wg *sync.WaitGroup, errs chan<- error) {
		defer wg.Done()
		route, err := service.ResolveSessionRoute(ctx, id)
		if err == nil && route.Session.State != StateActive {
			err = ErrInvalidTransition
		}
		errs <- err
	}
	awaitStart := func(what string) {
		t.Helper()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not start while another Session was booting", what)
		}
	}
	finish := func(wg *sync.WaitGroup, errs chan error) {
		t.Helper()
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	// Different Sessions boot concurrently: neither the Session lock nor the
	// admission lock is held across another Session's Start.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go resolve("ses_lock_first", &wg, errs)
	go resolve("ses_lock_second", &wg, errs)
	awaitStart("first Session")
	awaitStart("second Session")
	release <- struct{}{}
	release <- struct{}{}
	finish(&wg, errs)

	// Requests for the same Session wait for one reactivation and then route to
	// its Sandbox instead of replacing it again.
	if _, err := service.TerminateSession(ctx, "ses_lock_first"); err != nil {
		t.Fatal(err)
	}
	starts.Store(0)
	errs = make(chan error, 2)
	wg.Add(2)
	go resolve("ses_lock_first", &wg, errs)
	go resolve("ses_lock_first", &wg, errs)
	awaitStart("same Session")
	select {
	case id := <-entered:
		t.Fatalf("second reactivation of the same Session started %s concurrently", id)
	case <-time.After(100 * time.Millisecond):
	}
	release <- struct{}{}
	finish(&wg, errs)
	if got := starts.Load(); got != 1 {
		t.Fatalf("same-Session reactivation starts = %d, want 1", got)
	}
}

func TestSessionLocksReleaseIdleSlots(t *testing.T) {
	var locks sessionLocks
	unlock := locks.lock("ses_a")
	other := locks.lock("ses_b")
	other()
	unlock()
	if len(locks.slots) != 0 {
		t.Fatalf("idle Session lock slots = %d, want 0", len(locks.slots))
	}
}
