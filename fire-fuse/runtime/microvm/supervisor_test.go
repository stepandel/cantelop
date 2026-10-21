package microvm

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceCreateAndStartWithIDReplaysOriginalMachine(t *testing.T) {
	store := newTestStore(t)
	var startCalls atomic.Int32
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error {
			startCalls.Add(1)
			return nil
		},
		stop: func(context.Context, Sandbox) error { return nil },
	}
	service := newService(store, supervisor, time.Now)
	spec := CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"}

	first, err := service.CreateAndStartWithID(context.Background(), "create-123", spec)
	if err != nil {
		t.Fatalf("first CreateAndStartWithID() error = %v", err)
	}
	replayed, err := service.CreateAndStartWithID(context.Background(), "create-123", spec)
	if err != nil {
		t.Fatalf("replayed CreateAndStartWithID() error = %v", err)
	}
	if replayed != first {
		t.Errorf("replayed machine = %#v, want %#v", replayed, first)
	}
	if got := startCalls.Load(); got != 1 {
		t.Errorf("Start calls = %d, want 1", got)
	}
	machines, err := store.List()
	if err != nil || len(machines) != 1 {
		t.Fatalf("List() = %#v, %v; want one machine", machines, err)
	}
}

func TestServiceCreateAndStartWithIDRejectsChangedSpec(t *testing.T) {
	store := newTestStore(t)
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error { return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
	}
	service := newService(store, supervisor, time.Now)
	spec := CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"}
	if _, err := service.CreateAndStartWithID(context.Background(), "create-123", spec); err != nil {
		t.Fatalf("first CreateAndStartWithID() error = %v", err)
	}
	spec.AppID = "app-other"
	if _, err := service.CreateAndStartWithID(context.Background(), "create-123", spec); !errors.Is(err, ErrSandboxConflict) {
		t.Fatalf("changed CreateAndStartWithID() error = %v, want ErrSandboxConflict", err)
	}
}

func TestServiceCreateAndStartWithIDRejectsArchivedWorkspaceReplay(t *testing.T) {
	store := newTestStore(t)
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error { return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
	}
	service := newService(store, supervisor, time.Now)
	spec := CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"}
	if _, err := service.CreateAndStartWithID(context.Background(), "create-123", spec); err != nil {
		t.Fatalf("first CreateAndStartWithID() error = %v", err)
	}
	if _, err := store.ArchiveWorkspace(spec.WorkspaceID, time.Now().UTC()); err != nil {
		t.Fatalf("ArchiveWorkspace() error = %v", err)
	}
	if _, err := service.CreateAndStartWithID(context.Background(), "create-123", spec); !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("replayed CreateAndStartWithID() error = %v, want ErrWorkspaceUnavailable", err)
	}
}

func TestSandboxPinsActiveBuildAtCreation(t *testing.T) {
	store := newTestStore(t)
	secondBuild := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	prepareTestBuild(t, store, "bop_0123456789abcdef0123456789abcdef", "app-test", secondBuild, now)
	var starts []Sandbox
	supervisor := fakeSupervisor{
		start: func(_ context.Context, sandbox Sandbox) error {
			now = now.Add(time.Second) // simulate boot time before activation
			starts = append(starts, sandbox)
			return nil
		},
		stop: func(context.Context, Sandbox) error { return nil },
	}
	service := newService(store, supervisor, func() time.Time { return now })
	spec := CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"}

	first, err := service.CreateAndStartWithID(context.Background(), "sandbox-first", spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SelectAppBuildForComputeTenant("usr_test", "app-test", secondBuild, now); err != nil {
		t.Fatal(err)
	}
	replayed, err := service.CreateAndStartWithID(context.Background(), "sandbox-first", spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateAndStartWithID(context.Background(), "sandbox-second", spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.BuildID != testImageID || replayed.BuildID != testImageID || second.BuildID != secondBuild {
		t.Fatalf("builds = %q, %q, %q", first.BuildID, replayed.BuildID, second.BuildID)
	}
	if len(starts) != 2 {
		t.Fatalf("runtime starts = %d, want 2", len(starts))
	}
}

func TestServiceCreateAndStartWithIDReplaysFailure(t *testing.T) {
	store := newTestStore(t)
	var startCalls atomic.Int32
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error {
			startCalls.Add(1)
			return errors.New("boot failed")
		},
		stop: func(context.Context, Sandbox) error { return nil },
	}
	service := newService(store, supervisor, time.Now)
	spec := CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"}
	first, err := service.CreateAndStartWithID(context.Background(), "create-failed", spec)
	if !errors.Is(err, ErrRuntimeUnavailable) || first.State != StateFailed {
		t.Fatalf("first result = (%#v, %v), want failed runtime", first, err)
	}
	replayed, err := service.CreateAndStartWithID(context.Background(), "create-failed", spec)
	if !errors.Is(err, ErrRuntimeUnavailable) || replayed != first {
		t.Fatalf("replayed result = (%#v, %v), want original failure", replayed, err)
	}
	if startCalls.Load() != 1 {
		t.Errorf("Start calls = %d; want 1", startCalls.Load())
	}
}

type fakeSupervisor struct {
	validate            func(RuntimeSpec) error
	validateBuild       func(string) error
	start               func(context.Context, Sandbox) error
	startWithCheckpoint func(context.Context, Sandbox, RuntimeCheckpoint) error
	runtime             RuntimeInfo
	stop                func(context.Context, Sandbox) error
	stopWithCheckpoint  func(context.Context, Sandbox, RuntimeCheckpoint) error
	crash               func(context.Context, string) error
	isRunning           func(string) bool
	runtimeDone         func(string) (<-chan struct{}, bool)
	runtimeExit         func(string) (RuntimeExit, bool)
	runtimeUsage        func(string) (RuntimeUsage, error)
	purgeWorkspace      func(context.Context, string) error
}

func (s fakeSupervisor) PurgeWorkspace(ctx context.Context, workspaceID string) error {
	if s.purgeWorkspace == nil {
		return ErrSupervisorUnavailable
	}
	return s.purgeWorkspace(ctx, workspaceID)
}

func (s fakeSupervisor) ValidateBuild(buildID string) error {
	if s.validateBuild == nil {
		return nil
	}
	return s.validateBuild(buildID)
}

func (s fakeSupervisor) Validate(spec RuntimeSpec) error {
	if s.validate == nil {
		return nil
	}
	return s.validate(spec)
}

type failRunningStore struct {
	*SQLiteStore
	err error
}

func (s failRunningStore) Activate(string, RuntimeInfo, time.Time) (Sandbox, error) {
	return Sandbox{}, s.err
}

func (s fakeSupervisor) Start(ctx context.Context, machine Sandbox, checkpoint RuntimeCheckpoint) (RuntimeInfo, error) {
	runtime := s.runtime
	if runtime == (RuntimeInfo{}) {
		// Running records must own a complete runtime, as a real supervisor reports.
		runtime = persistedRuntimeInfo()
	}
	if s.startWithCheckpoint != nil {
		return runtime, s.startWithCheckpoint(ctx, machine, checkpoint)
	}
	return runtime, s.start(ctx, machine)
}

func (s fakeSupervisor) Stop(ctx context.Context, machine Sandbox, checkpoint RuntimeCheckpoint) error {
	if s.stopWithCheckpoint != nil {
		return s.stopWithCheckpoint(ctx, machine, checkpoint)
	}
	return s.stop(ctx, machine)
}

func (s fakeSupervisor) Crash(ctx context.Context, id string) error {
	if s.crash == nil {
		return ErrSupervisorUnavailable
	}
	return s.crash(ctx, id)
}

func (s fakeSupervisor) IsRunning(id string) bool {
	if s.isRunning == nil {
		return true
	}
	return s.isRunning(id)
}

func (s fakeSupervisor) RuntimeDone(id string) (<-chan struct{}, bool) {
	if s.runtimeDone == nil {
		return nil, false
	}
	return s.runtimeDone(id)
}

func (s fakeSupervisor) RuntimeExit(id string) (RuntimeExit, bool) {
	if s.runtimeExit == nil {
		return RuntimeExit{}, false
	}
	return s.runtimeExit(id)
}

func (s fakeSupervisor) RuntimeUsage(id string) (RuntimeUsage, error) {
	if s.runtimeUsage == nil {
		return RuntimeUsage{}, ErrRuntimeUnavailable
	}
	return s.runtimeUsage(id)
}

func TestServiceListUsageReturnsOnlyAvailableRunningReadings(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"running-1", "running-2"} {
		if err := store.Create(testSandbox(Sandbox{ID: id, State: StateStarting, CreatedAt: now, UpdatedAt: now})); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Activate(id, persistedRuntimeInfo(), now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	supervisor := fakeSupervisor{runtimeUsage: func(id string) (RuntimeUsage, error) {
		if id == "running-2" {
			return RuntimeUsage{}, ErrRuntimeUnavailable
		}
		return RuntimeUsage{CPUUsageUS: 42, MemoryCurrentBytes: 1024}, nil
	}}
	usage, err := NewServiceWithSupervisor(store, supervisor).ListUsage()
	if err != nil || len(usage) != 1 || usage[0].SandboxID != "running-1" ||
		usage[0].CPUUsageUS != 42 || usage[0].MemoryCurrentBytes != 1024 {
		t.Fatalf("ListUsage() = %#v, %v", usage, err)
	}
}

func TestServiceStart(t *testing.T) {
	store := newTestStore(t)
	starting := testSandbox(Sandbox{ID: "vm-test", State: StateStarting})
	if err := store.Create(starting); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	var received Sandbox
	runtime := persistedRuntimeInfo()
	wantNetwork := runtime.Network
	supervisor := fakeSupervisor{
		runtime: runtime,
		start: func(_ context.Context, machine Sandbox) error {
			received = machine
			return nil
		},
		stop: func(context.Context, Sandbox) error {
			t.Fatal("Stop() called after successful state transition")
			return nil
		},
	}
	service := NewServiceWithSupervisor(store, supervisor)

	running, err := service.Start(context.Background(), starting.ID)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if received != starting {
		t.Errorf("supervisor received %#v, want %#v", received, starting)
	}
	if running.State != StateRunning {
		t.Errorf("State = %q, want %q", running.State, StateRunning)
	}
	if running.Network != wantNetwork {
		t.Errorf("Network = %#v, want %#v", running.Network, wantNetwork)
	}
}

func TestServiceCreateAndStart(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.July, 20, 20, 0, 0, 0, time.UTC)
	runtime := persistedRuntimeInfo()
	wantNetwork := runtime.Network
	supervisor := fakeSupervisor{
		runtime: runtime,
		start: func(_ context.Context, machine Sandbox) error {
			now = now.Add(time.Second) // simulate boot time before activation
			if machine.ID != "sandbox-test" {
				t.Errorf("Start() ID = %q, want sandbox-test", machine.ID)
			}
			if machine.State != StateStarting {
				t.Errorf("Start() State = %q, want %q", machine.State, StateStarting)
			}
			return nil
		},
		stop: func(context.Context, Sandbox) error {
			t.Fatal("Stop() called during successful creation")
			return nil
		},
	}
	service := newService(
		store,
		supervisor,
		func() time.Time { return now },
	)

	machine, err := service.CreateAndStart(
		context.Background(), "sandbox-test", CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"},
	)
	if err != nil {
		t.Fatalf("CreateAndStart() error = %v", err)
	}
	if machine.State != StateRunning {
		t.Errorf("State = %q, want %q", machine.State, StateRunning)
	}
	if machine.Network != wantNetwork {
		t.Errorf("Network = %#v, want %#v", machine.Network, wantNetwork)
	}
	stored, err := store.Get(machine.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored != machine {
		t.Errorf("stored machine = %#v, want %#v", stored, machine)
	}
}

func TestServiceCreateAndStartRejectsUnknownProfileBeforeRecording(t *testing.T) {
	store := newTestStore(t)
	supervisor := fakeSupervisor{
		validate: func(RuntimeSpec) error { return ErrUnknownProfile },
		start: func(context.Context, Sandbox) error {
			t.Fatal("Start() called for an unknown profile")
			return nil
		},
		stop: func(context.Context, Sandbox) error { return nil },
	}
	service := newService(
		store,
		supervisor,
		time.Now,
	)

	_, err := service.CreateAndStart(
		context.Background(), "sandbox-test", CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"},
	)
	if !errors.Is(err, ErrUnknownProfile) {
		t.Fatalf("CreateAndStart() error = %v, want ErrUnknownProfile", err)
	}
	machines, listErr := store.List()
	if listErr != nil {
		t.Fatalf("List() error = %v", listErr)
	}
	if len(machines) != 0 {
		t.Errorf("stored machines = %v, want none", machines)
	}
}

func TestServiceGetMarksExitedRuntimeFailed(t *testing.T) {
	store := newTestStore(t)
	running := testSandbox(Sandbox{ID: "vm-exited", State: StateRunning})
	if err := store.Create(running); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	supervisor := fakeSupervisor{
		start:     func(context.Context, Sandbox) error { return nil },
		stop:      func(context.Context, Sandbox) error { return nil },
		isRunning: func(string) bool { return false },
	}
	service := NewServiceWithSupervisor(store, supervisor)

	machine, err := service.Get(running.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if machine.State != StateFailed {
		t.Errorf("State = %q, want %q", machine.State, StateFailed)
	}
	if machine.Failure.Reason != FailureRuntimeExit {
		t.Errorf("Failure reason = %q, want %q", machine.Failure.Reason, FailureRuntimeExit)
	}
	stored, err := store.Get(running.ID)
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if stored.State != StateFailed {
		t.Errorf("stored State = %q, want %q", stored.State, StateFailed)
	}
	if stored.Failure.Reason != FailureRuntimeExit {
		t.Errorf("stored Failure reason = %q, want %q", stored.Failure.Reason, FailureRuntimeExit)
	}
}

func TestServiceCrashUsesRuntimeExitReconciliation(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	running := testSandbox(Sandbox{ID: "vm-crash", State: StateStarting, CreatedAt: now, UpdatedAt: now})
	if err := store.Create(running); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Activate(running.ID, persistedRuntimeInfo(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	alive := true
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error { return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
		crash: func(_ context.Context, id string) error {
			if id != running.ID {
				t.Fatalf("Crash ID = %q", id)
			}
			alive = false
			return nil
		},
		isRunning: func(string) bool { return alive },
		runtimeExit: func(string) (RuntimeExit, bool) {
			return RuntimeExit{PID: 42, Signal: "killed"}, true
		},
	}
	service := newService(store, supervisor, func() time.Time { return now.Add(time.Second) })

	failed, err := service.Crash(context.Background(), running.ID)
	if err != nil {
		t.Fatalf("Crash() error = %v", err)
	}
	if failed.State != StateFailed || failed.Failure.Reason != FailureRuntimeExit ||
		failed.Failure.RuntimeExit.PID != 42 || failed.Failure.RuntimeExit.Signal != "killed" {
		t.Fatalf("Crash() = %#v, want failed runtime exit", failed)
	}
	if failed.Runtime != persistedRuntimeInfo().Ownership {
		t.Fatalf("Crash() changed runtime ownership: %#v", failed.Runtime)
	}
}

func TestServiceObservesRuntimeExit(t *testing.T) {
	store := newTestStore(t)
	starting := testSandbox(Sandbox{ID: "vm-exited", State: StateStarting})
	if err := store.Create(starting); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	done := make(chan struct{})
	running := true
	wantExit := RuntimeExit{PID: 321, ExitCode: 9}
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error { return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
		isRunning: func(string) bool {
			return running
		},
		runtimeDone: func(string) (<-chan struct{}, bool) {
			return done, true
		},
		runtimeExit: func(string) (RuntimeExit, bool) { return wantExit, true },
	}
	service := NewServiceWithSupervisor(store, supervisor)
	if _, err := service.Start(context.Background(), starting.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	running = false
	close(done)
	waitForState(t, store, starting.ID, StateFailed)
	failed, err := store.Get(starting.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if failed.Failure.Reason != FailureRuntimeExit || failed.Failure.RuntimeExit != wantExit {
		t.Errorf("Failure = %#v, want runtime exit %#v", failed.Failure, wantExit)
	}
}

func TestServiceIgnoresRuntimeExitDuringTermination(t *testing.T) {
	store := newTestStore(t)
	starting := testSandbox(Sandbox{ID: "vm-stopped", State: StateStarting})
	if err := store.Create(starting); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	done := make(chan struct{})
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error { return nil },
		stop: func(context.Context, Sandbox) error {
			close(done)
			return nil
		},
		isRunning: func(string) bool { return false },
		runtimeDone: func(string) (<-chan struct{}, bool) {
			return done, true
		},
	}
	service := NewServiceWithSupervisor(store, supervisor)
	if _, err := service.Start(context.Background(), starting.ID); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	terminated, err := service.Terminate(context.Background(), starting.ID)
	if err != nil {
		t.Fatalf("Terminate() error = %v", err)
	}
	if terminated.State != StateTerminated {
		t.Errorf("State = %q, want %q", terminated.State, StateTerminated)
	}
}

func TestServiceListReconcilesOnlyExitedRuntimes(t *testing.T) {
	store := newTestStore(t)
	for _, machine := range []Sandbox{
		{ID: "vm-alive", State: StateRunning},
		{ID: "vm-exited", State: StateRunning},
		{ID: "vm-terminated", State: StateTerminated},
	} {
		if err := store.Create(testSandbox(machine)); err != nil {
			t.Fatalf("Create(%s) error = %v", machine.ID, err)
		}
	}
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error { return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
		isRunning: func(id string) bool {
			return id == "vm-alive"
		},
	}
	service := NewServiceWithSupervisor(store, supervisor)

	machines, err := service.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	wantStates := map[string]State{
		"vm-alive":      StateRunning,
		"vm-exited":     StateFailed,
		"vm-terminated": StateTerminated,
	}
	for _, machine := range machines {
		if machine.State != wantStates[machine.ID] {
			t.Errorf("%s State = %q, want %q", machine.ID, machine.State, wantStates[machine.ID])
		}
	}
}

func TestServiceTerminateStopsRuntimeWhileStopping(t *testing.T) {
	store := newTestStore(t)
	running := testSandbox(Sandbox{ID: "vm-test", State: StateRunning})
	if err := store.Create(running); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	stopCalls := 0
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error {
			t.Fatal("Start() called during termination")
			return nil
		},
		stop: func(_ context.Context, machine Sandbox) error {
			stopCalls++
			if machine.State != StateStopping {
				t.Errorf("Stop() machine State = %q, want %q", machine.State, StateStopping)
			}
			stored, err := store.Get(machine.ID)
			if err != nil {
				t.Fatalf("Get() during Stop() error = %v", err)
			}
			if stored.State != StateStopping {
				t.Errorf("stored State during Stop() = %q, want %q", stored.State, StateStopping)
			}
			return nil
		},
	}
	service := NewServiceWithSupervisor(store, supervisor)

	machine, err := service.Terminate(context.Background(), running.ID)
	if err != nil {
		t.Fatalf("Terminate() error = %v", err)
	}
	if machine.State != StateTerminated {
		t.Errorf("State = %q, want %q", machine.State, StateTerminated)
	}
	if stopCalls != 1 {
		t.Errorf("Stop() calls = %d, want 1", stopCalls)
	}
}

func TestServiceTerminateRetriesRuntimeCleanup(t *testing.T) {
	store := newTestStore(t)
	running := testSandbox(Sandbox{ID: "vm-test", State: StateRunning})
	if err := store.Create(running); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	wantErr := errors.New("jailer still running")
	stopCalls := 0
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error { return nil },
		stop: func(context.Context, Sandbox) error {
			stopCalls++
			if stopCalls == 1 {
				return wantErr
			}
			return nil
		},
	}
	service := NewServiceWithSupervisor(store, supervisor)

	stopping, err := service.Terminate(context.Background(), running.ID)
	if !errors.Is(err, wantErr) {
		t.Fatalf("first Terminate() error = %v, want cleanup error", err)
	}
	if !errors.Is(err, ErrRuntimeUnavailable) {
		t.Errorf("first Terminate() error = %v, want ErrRuntimeUnavailable", err)
	}
	if stopping.State != StateStopping {
		t.Errorf("first State = %q, want %q", stopping.State, StateStopping)
	}
	stored, getErr := store.Get(running.ID)
	if getErr != nil {
		t.Fatalf("Get() error = %v", getErr)
	}
	if stored.State != StateStopping {
		t.Errorf("stored State after failure = %q, want %q", stored.State, StateStopping)
	}

	terminated, err := service.Terminate(context.Background(), running.ID)
	if err != nil {
		t.Fatalf("second Terminate() error = %v", err)
	}
	if terminated.State != StateTerminated {
		t.Errorf("second State = %q, want %q", terminated.State, StateTerminated)
	}
	if stopCalls != 2 {
		t.Errorf("Stop() calls = %d, want 2", stopCalls)
	}
}

func TestServiceTerminateAll(t *testing.T) {
	store := newTestStore(t)
	for _, machine := range []Sandbox{
		{ID: "vm-running", State: StateRunning},
		{ID: "vm-stopping", State: StateStopping},
		{ID: "vm-terminated", State: StateTerminated},
	} {
		if err := store.Create(testSandbox(machine)); err != nil {
			t.Fatalf("Create(%s) error = %v", machine.ID, err)
		}
	}
	stopped := make(chan string, 3)
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error { return nil },
		stop: func(_ context.Context, machine Sandbox) error {
			stopped <- machine.ID
			return nil
		},
	}
	service := NewServiceWithSupervisor(store, supervisor)

	if err := service.TerminateAll(context.Background()); err != nil {
		t.Fatalf("TerminateAll() error = %v", err)
	}
	close(stopped)
	stoppedIDs := make(map[string]bool)
	for id := range stopped {
		stoppedIDs[id] = true
	}
	if !stoppedIDs["vm-running"] || !stoppedIDs["vm-stopping"] {
		t.Errorf("Stop() IDs = %v, want running and stopping machines", stoppedIDs)
	}
	if stoppedIDs["vm-terminated"] {
		t.Error("Stop() called for an already terminated machine")
	}
	for _, id := range []string{"vm-running", "vm-stopping", "vm-terminated"} {
		machine, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", id, err)
		}
		if machine.State != StateTerminated {
			t.Errorf("%s State = %q, want %q", id, machine.State, StateTerminated)
		}
	}
}

func TestServiceTerminateAllContinuesAfterFailure(t *testing.T) {
	store := newTestStore(t)
	for _, id := range []string{"vm-broken", "vm-healthy"} {
		if err := store.Create(testSandbox(Sandbox{ID: id, State: StateRunning})); err != nil {
			t.Fatalf("Create(%s) error = %v", id, err)
		}
	}
	wantErr := errors.New("process did not exit")
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error { return nil },
		stop: func(_ context.Context, machine Sandbox) error {
			if machine.ID == "vm-broken" {
				return wantErr
			}
			return nil
		},
	}
	service := NewServiceWithSupervisor(store, supervisor)

	err := service.TerminateAll(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("TerminateAll() error = %v, want process error", err)
	}
	broken, err := store.Get("vm-broken")
	if err != nil {
		t.Fatalf("Get(vm-broken) error = %v", err)
	}
	if broken.State != StateStopping {
		t.Errorf("vm-broken State = %q, want %q", broken.State, StateStopping)
	}
	healthy, err := store.Get("vm-healthy")
	if err != nil {
		t.Fatalf("Get(vm-healthy) error = %v", err)
	}
	if healthy.State != StateTerminated {
		t.Errorf("vm-healthy State = %q, want %q", healthy.State, StateTerminated)
	}
}

func TestServiceStartMarksRuntimeFailure(t *testing.T) {
	store := newTestStore(t)
	starting := testSandbox(Sandbox{ID: "vm-test", State: StateStarting})
	if err := store.Create(starting); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	wantErr := errors.New("firecracker exited")
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error { return wantErr },
		stop:  func(context.Context, Sandbox) error { return nil },
	}
	service := NewServiceWithSupervisor(store, supervisor)

	failed, err := service.Start(context.Background(), starting.ID)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v, want runtime error", err)
	}
	if !errors.Is(err, ErrRuntimeUnavailable) {
		t.Errorf("Start() error = %v, want ErrRuntimeUnavailable", err)
	}
	if failed.State != StateFailed {
		t.Errorf("State = %q, want %q", failed.State, StateFailed)
	}
	if failed.Failure.Reason != FailureRuntimeStart {
		t.Errorf("Failure reason = %q, want %q", failed.Failure.Reason, FailureRuntimeStart)
	}
	stored, getErr := store.Get(starting.ID)
	if getErr != nil {
		t.Fatalf("Get() error = %v", getErr)
	}
	if stored.State != StateFailed {
		t.Errorf("stored State = %q, want %q", stored.State, StateFailed)
	}
	if stored.Failure.Reason != FailureRuntimeStart {
		t.Errorf("stored Failure reason = %q, want %q", stored.Failure.Reason, FailureRuntimeStart)
	}
}

func TestServiceStartPreservesUnknownSelectionCategory(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "profile", err: ErrUnknownProfile},
		{name: "image", err: ErrUnknownImage},
		{name: "shape", err: ErrUnsupportedShape},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			starting := testSandbox(Sandbox{ID: "vm-test", State: StateStarting})
			if err := store.Create(starting); err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			supervisor := fakeSupervisor{
				start: func(context.Context, Sandbox) error { return test.err },
				stop:  func(context.Context, Sandbox) error { return nil },
			}
			service := NewServiceWithSupervisor(store, supervisor)

			failed, err := service.Start(context.Background(), starting.ID)
			if !errors.Is(err, test.err) {
				t.Fatalf("Start() error = %v, want %v", err, test.err)
			}
			if errors.Is(err, ErrRuntimeUnavailable) {
				t.Errorf("Start() error = %v, unknown selection must not be a runtime outage", err)
			}
			if failed.State != StateFailed {
				t.Errorf("State = %q, want %q", failed.State, StateFailed)
			}
		})
	}
}

func TestServiceStartCleansUpAfterStateFailure(t *testing.T) {
	baseStore := openTestSQLiteStore(t)
	starting := testSandbox(Sandbox{ID: "vm-test", State: StateStarting})
	if err := baseStore.Create(starting); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	wantErr := errors.New("state store unavailable")
	store := failRunningStore{SQLiteStore: baseStore, err: wantErr}
	stopCalled := false
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error { return nil },
		stop: func(_ context.Context, machine Sandbox) error {
			stopCalled = true
			if machine != starting {
				t.Errorf("Stop() machine = %#v, want %#v", machine, starting)
			}
			return nil
		},
	}
	service := NewServiceWithSupervisor(store, supervisor)

	_, err := service.Start(context.Background(), starting.ID)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v, want state error", err)
	}
	if !errors.Is(err, ErrStateStore) {
		t.Errorf("Start() error = %v, want ErrStateStore", err)
	}
	if !stopCalled {
		t.Error("Stop() was not called after the running transition failed")
	}
}

func TestServiceStartWithoutSupervisor(t *testing.T) {
	store := newTestStore(t)
	starting := testSandbox(Sandbox{ID: "vm-test", State: StateStarting})
	if err := store.Create(starting); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	service := NewService(store)

	_, err := service.Start(context.Background(), starting.ID)
	if !errors.Is(err, ErrSupervisorUnavailable) {
		t.Fatalf("Start() error = %v, want ErrSupervisorUnavailable", err)
	}
	stored, getErr := store.Get(starting.ID)
	if getErr != nil {
		t.Fatalf("Get() error = %v", getErr)
	}
	if stored.State != StateStarting {
		t.Errorf("stored State = %q, want unchanged %q", stored.State, StateStarting)
	}
}

func TestServiceStartIsIdempotentWhenRunning(t *testing.T) {
	store := newTestStore(t)
	running := testSandbox(Sandbox{ID: "vm-test", State: StateRunning})
	if err := store.Create(running); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	supervisor := fakeSupervisor{
		start: func(context.Context, Sandbox) error {
			t.Fatal("Start() called for an already running machine")
			return nil
		},
		stop: func(context.Context, Sandbox) error { return nil },
	}
	service := NewServiceWithSupervisor(store, supervisor)

	got, err := service.Start(context.Background(), running.ID)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got != running {
		t.Errorf("Start() = %#v, want unchanged %#v", got, running)
	}
}

func waitForState(t *testing.T, store *SQLiteStore, id string, want State) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		machine, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", id, err)
		}
		if machine.State == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("%s State = %q after timeout, want %q", id, machine.State, want)
		case <-ticker.C:
		}
	}
}
