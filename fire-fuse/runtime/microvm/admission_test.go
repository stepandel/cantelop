package microvm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAdmissionLimitsValidation(t *testing.T) {
	tests := []AdmissionLimits{
		{},
		{MaxVMs: 1, MaxTotalMemoryMiB: 128},
		{MaxVMs: 1, MaxTotalMemoryMiB: 128, MaxConcurrentStarts: 2},
	}
	for _, limits := range tests {
		if _, err := NewServiceWithSupervisorAndLimits(newTestStore(t), nil, limits); !errors.Is(err, ErrInvalidAdmissionLimits) {
			t.Errorf("limits %#v: error = %v, want ErrInvalidAdmissionLimits", limits, err)
		}
	}
}

func TestAdmissionEnforcesResourceLimits(t *testing.T) {
	tests := []struct {
		name   string
		limits AdmissionLimits
	}{
		{name: "VMs", limits: AdmissionLimits{MaxVMs: 1, MaxTotalMemoryMiB: 1024, MaxConcurrentStarts: 1}},
		{name: "memory", limits: AdmissionLimits{MaxVMs: 2, MaxTotalMemoryMiB: 128, MaxConcurrentStarts: 1}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			if err := store.Create(testSandbox(Sandbox{ID: "existing", State: StateRunning, VCPUs: 1, MemoryMiB: 128})); err != nil {
				t.Fatal(err)
			}
			service, err := NewServiceWithSupervisorAndLimits(store, nil, test.limits)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Create("sandbox-new", CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"})
			if !errors.Is(err, ErrAdmissionLimit) {
				t.Errorf("Create() error = %v, want ErrAdmissionLimit", err)
			}
		})
	}
}

func TestAdmissionDoesNotReserveGuestVCPUs(t *testing.T) {
	store := newTestStore(t)
	if err := store.Create(testSandbox(Sandbox{ID: "existing", State: StateRunning, VCPUs: 4, MemoryMiB: 128})); err != nil {
		t.Fatal(err)
	}
	service, err := NewServiceWithSupervisorAndLimits(store, nil, AdmissionLimits{
		MaxVMs: 2, MaxTotalMemoryMiB: 2048, MaxConcurrentStarts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create("sandbox-new", CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"}); err != nil {
		t.Fatalf("elastic CPU admission rejected a second four-vCPU VM: %v", err)
	}
}

func TestAdmissionCountsOnlyRecordsThatOwnCapacity(t *testing.T) {
	tests := []struct {
		name      string
		machine   Sandbox
		wantLimit bool
	}{
		{name: "starting", machine: Sandbox{State: StateStarting}, wantLimit: true},
		{name: "running", machine: Sandbox{State: StateRunning}, wantLimit: true},
		{name: "stopping", machine: Sandbox{State: StateStopping}, wantLimit: true},
		{name: "failed with resources", machine: Sandbox{State: StateFailed, Runtime: RuntimeOwnership{JailPrepared: true}}, wantLimit: true},
		{name: "failed without resources", machine: Sandbox{State: StateFailed}},
		{name: "terminated", machine: Sandbox{State: StateTerminated}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			test.machine.ID = "existing"
			if err := store.Create(testSandbox(test.machine)); err != nil {
				t.Fatal(err)
			}
			service, err := NewServiceWithSupervisorAndLimits(store, nil, AdmissionLimits{
				MaxVMs: 1, MaxTotalMemoryMiB: DefaultSandboxMemoryMiB, MaxConcurrentStarts: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Create("sandbox-new", CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"})
			if got := errors.Is(err, ErrAdmissionLimit); got != test.wantLimit {
				t.Errorf("Create() admission error = %v, wantLimit %v", err, test.wantLimit)
			}
		})
	}
}

func TestAdmissionSerializesConcurrentCreates(t *testing.T) {
	service, err := NewServiceWithSupervisorAndLimits(newTestStore(t), nil, AdmissionLimits{
		MaxVMs: 1, MaxTotalMemoryMiB: DefaultSandboxMemoryMiB, MaxConcurrentStarts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	const callers = 20
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for index := range callers {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, createErr := service.Create(id, CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"})
			switch {
			case createErr == nil:
				admitted.Add(1)
			case errors.Is(createErr, ErrAdmissionLimit):
			default:
				t.Errorf("Create() error = %v", createErr)
			}
		}(fmt.Sprintf("sandbox-%d", index))
	}
	wg.Wait()
	if got := admitted.Load(); got != 1 {
		t.Errorf("admitted = %d, want 1", got)
	}
}

func TestWarmCapacityRejectsBeforeCreatingSandboxRecords(t *testing.T) {
	for _, test := range []struct {
		name     string
		capacity HostCapacity
		want     int32
	}{
		{"two ready slots", HostCapacity{Target: 2, Ready: 2}, 2},
		{"replacement preparing", HostCapacity{Target: 2, Ready: 1, Preparing: 1}, 1},
		{"pool starting", HostCapacity{Target: 2, Preparing: 1}, 0},
		{"synchronous preparation", HostCapacity{}, 16},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			service, err := NewServiceWithSupervisorAndLimits(store, nil, AdmissionLimits{MaxVMs: 16, MaxTotalMemoryMiB: 49152, MaxConcurrentStarts: 2})
			if err != nil {
				t.Fatal(err)
			}
			service.admission.profileCapacity = func(string) HostCapacity { return test.capacity }
			var admitted atomic.Int32
			var wg sync.WaitGroup
			for i := range 20 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := service.Create(fmt.Sprintf("sandbox-%d", i), CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"})
					if err == nil {
						admitted.Add(1)
					} else if !errors.Is(err, ErrAdmissionLimit) {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			rows, err := store.List()
			if err != nil || admitted.Load() != test.want || len(rows) != int(test.want) {
				t.Fatalf("admitted=%d records=%d want=%d err=%v", admitted.Load(), len(rows), test.want, err)
			}
		})
	}
}

func TestAdmissionAllowsIdempotentReplayAtCapacity(t *testing.T) {
	store := newTestStore(t)
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error { return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
	}
	service, err := NewServiceWithSupervisorAndLimits(store, supervisor, AdmissionLimits{
		MaxVMs: 1, MaxTotalMemoryMiB: DefaultSandboxMemoryMiB, MaxConcurrentStarts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"}
	first, err := service.CreateAndStartWithID(context.Background(), "create-123", spec)
	if err != nil {
		t.Fatalf("first CreateAndStartWithID() error = %v", err)
	}
	replayed, err := service.CreateAndStartWithID(context.Background(), "create-123", spec)
	if err != nil || replayed != first {
		t.Fatalf("replay at capacity = (%#v, %v), want %#v", replayed, err, first)
	}
}

func TestAdmissionCoalescesConcurrentIdempotentCreates(t *testing.T) {
	store := newTestStore(t)
	var starts atomic.Int32
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error {
			starts.Add(1)
			return nil
		},
		stop: func(context.Context, Sandbox) error { return nil },
	}
	service, err := NewServiceWithSupervisorAndLimits(store, supervisor, AdmissionLimits{
		MaxVMs: 1, MaxTotalMemoryMiB: DefaultSandboxMemoryMiB, MaxConcurrentStarts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"}

	const callers = 20
	results := make(chan Sandbox, callers)
	errorsSeen := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			machine, createErr := service.CreateAndStartWithID(
				context.Background(), "concurrent-create", spec,
			)
			results <- machine
			errorsSeen <- createErr
		}()
	}
	wg.Wait()
	close(results)
	close(errorsSeen)

	var machineID string
	for createErr := range errorsSeen {
		if createErr != nil {
			t.Errorf("CreateAndStartWithID() error = %v", createErr)
		}
	}
	for machine := range results {
		if machineID == "" {
			machineID = machine.ID
		}
		if machine.ID != machineID || machine.State != StateRunning {
			t.Errorf("machine = %#v, want running ID %q", machine, machineID)
		}
	}
	if got := starts.Load(); got != 1 {
		t.Errorf("runtime starts = %d, want 1", got)
	}
	machines, err := store.List()
	if err != nil || len(machines) != 1 {
		t.Fatalf("List() = %#v, %v; want one machine", machines, err)
	}
}

func TestAdmissionStartWaitIsContextAware(t *testing.T) {
	store := newTestStore(t)
	for _, id := range []string{"first", "second"} {
		if err := store.Create(testSandbox(Sandbox{ID: id, State: StateStarting, VCPUs: 1, MemoryMiB: 128})); err != nil {
			t.Fatal(err)
		}
	}

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error {
			entered <- struct{}{}
			<-release
			return nil
		},
		stop: func(context.Context, Sandbox) error { return nil },
	}
	service, err := NewServiceWithSupervisorAndLimits(store, supervisor, AdmissionLimits{
		MaxVMs: 2, MaxTotalMemoryMiB: 256, MaxConcurrentStarts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, startErr := service.Start(context.Background(), "first")
		firstDone <- startErr
	}()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	second, err := service.Start(ctx, "second")
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrRuntimeUnavailable) {
		t.Errorf("second Start() error = %v, want context.Canceled and ErrRuntimeUnavailable", err)
	}
	if second.State != StateFailed {
		t.Errorf("second state = %q, want %q", second.State, StateFailed)
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Errorf("first Start() error = %v", err)
	}
}
