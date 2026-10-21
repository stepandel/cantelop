package microvm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

const testImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testAppSessionRuntime() AppSessionRuntime {
	return AppSessionRuntime{
		ActiveBuildID: testImageID,
		Runtime:       AppRuntimeSpec{Profile: DefaultSandboxProfileName, InternalPort: 3000},
	}
}

func createTestUser(t *testing.T, store ComputeProjectionStore, userID string, now time.Time) {
	t.Helper()
	_, err := store.EnsureComputeTenant(ComputeTenant{ID: userID, CreatedAt: now})
	if err != nil {
		t.Fatalf("EnsureComputeTenant() error = %v", err)
	}
}

func seedTestApp(t *testing.T, store Store, appID, workspaceID string) {
	t.Helper()
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	createTestUser(t, store, "usr_test", now)
	app := App{ID: appID, Slug: strings.ReplaceAll(appID, "_", "-"), CreatedAt: now, UpdatedAt: now}
	workspace := Workspace{
		ID: workspaceID, AppID: appID,
		Slug: appID, CreatedAt: now, UpdatedAt: now,
	}
	build := AppBuild{AppID: appID, BuildID: testImageID, CreatedAt: now}
	if err := store.CreateAppForComputeTenant("usr_test", app, testAppSessionRuntime(), build); err != nil && !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("CreateAppForComputeTenant() error = %v", err)
	}
	if err := store.CreateWorkspace(workspace); err != nil && !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}
	// Fixture Apps represent an already activated release.
	if _, err := store.SetReleaseAdmissionForComputeTenant("usr_test", appID, "act_00000000000000000000000000000000", true); err != nil {
		t.Fatalf("close fixture App admission: %v", err)
	}
	if _, err := store.SetReleaseAdmissionForComputeTenant("usr_test", appID, "act_00000000000000000000000000000000", false); err != nil {
		t.Fatalf("open fixture App admission: %v", err)
	}
}

// prepareTestBuild registers buildID for appID through the prepared Build
// upload flow without changing the App's active Build selection.
func prepareTestBuild(t *testing.T, store Store, operationID, appID, buildID string, at time.Time) {
	t.Helper()
	operation := AppBuildOperation{ID: operationID, AppID: appID,
		Status: AppBuildOperationReceiving, CreatedAt: at, UpdatedAt: at}
	if err := store.BeginAppBuildUpload("usr_test", operation); err != nil {
		t.Fatalf("BeginAppBuildUpload() error = %v", err)
	}
	if _, _, err := store.CompletePreparedAppBuild(operation.ID, buildID, at); err != nil {
		t.Fatalf("CompletePreparedAppBuild() error = %v", err)
	}
}

// testSandbox completes a sparse Sandbox fixture into a record the SQLite
// store accepts: it fills identity, shape and timestamps left unset, gives a
// running record complete runtime ownership, and gives a failed record a
// failure reason. Fields set by the caller are kept.
func testSandbox(machine Sandbox) Sandbox {
	if machine.AppID == "" {
		machine.AppID = "app-test"
	}
	if machine.WorkspaceID == "" {
		machine.WorkspaceID = "wsp_project123"
	}
	if machine.Profile == "" {
		machine.Profile = DefaultSandboxProfileName
	}
	if machine.BuildID == "" {
		machine.BuildID = testImageID
	}
	if machine.VCPUs == 0 {
		machine.VCPUs = DefaultSandboxVCPUs
	}
	if machine.MemoryMiB == 0 {
		machine.MemoryMiB = DefaultSandboxMemoryMiB
	}
	if machine.CreatedAt.IsZero() {
		machine.CreatedAt = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	if machine.UpdatedAt.IsZero() {
		machine.UpdatedAt = machine.CreatedAt
	}
	if machine.State == StateRunning && machine.Network == (NetworkAssignment{}) && machine.Runtime == (RuntimeOwnership{}) {
		runtime := persistedRuntimeInfo()
		machine.Network, machine.Runtime = runtime.Network, runtime.Ownership
	}
	if machine.State == StateFailed && machine.Failure.Reason == "" {
		machine.Failure.Reason = FailureRuntimeStart
	}
	return machine
}

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store := openTestSQLiteStore(t)
	seedTestApp(t, store, "app-test", "wsp_project123")
	return store
}

func TestServiceCreate(t *testing.T) {
	store := openTestSQLiteStore(t)
	seedTestApp(t, store, "app-test", "wsp_project123")
	localTime := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.FixedZone("test", -7*60*60))
	service := newService(
		store,
		nil,
		func() time.Time { return localTime },
	)

	machine, err := service.Create("sandbox-test", CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if machine.ID != "sandbox-test" {
		t.Errorf("ID = %q, want sandbox-test", machine.ID)
	}
	if machine.State != StateStarting {
		t.Errorf("State = %q, want %q", machine.State, StateStarting)
	}
	if machine.WorkspaceID != "wsp_project123" {
		t.Errorf("WorkspaceID = %q, want wsp_project123", machine.WorkspaceID)
	}
	if machine.Profile != DefaultSandboxProfileName || machine.VCPUs != DefaultSandboxVCPUs || machine.MemoryMiB != DefaultSandboxMemoryMiB {
		t.Errorf("machine shape = %#v, want default/4/1024", machine)
	}
	if machine.AppID != "app-test" || machine.BuildID != testImageID {
		t.Errorf("assignment = %#v, want app-test/%s", machine, testImageID)
	}
	if machine.CreatedAt.Location() != time.UTC || !machine.CreatedAt.Equal(localTime) {
		t.Errorf("CreatedAt = %s, want %s in UTC", machine.CreatedAt, localTime)
	}
	if !machine.UpdatedAt.Equal(machine.CreatedAt) {
		t.Errorf("UpdatedAt = %s, want CreatedAt %s", machine.UpdatedAt, machine.CreatedAt)
	}

	stored, err := store.Get(machine.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored != machine {
		t.Errorf("stored machine = %#v, want %#v", stored, machine)
	}
}

func TestServiceCreateRejectsInvalidSpec(t *testing.T) {
	tests := []struct {
		name string
		spec CreateSpec
	}{
		{name: "missing app", spec: CreateSpec{}},
		{name: "unsafe app", spec: CreateSpec{AppID: "../app"}},
		{name: "missing workspace", spec: CreateSpec{AppID: "app-test"}},
		{name: "unsafe workspace", spec: CreateSpec{AppID: "app-test", WorkspaceID: "../wsp"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(openTestSQLiteStore(t))
			if _, err := service.Create("sandbox-test", test.spec); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("Create() error = %v, want ErrInvalidSpec", err)
			}
		})
	}
}

func TestServiceCreateRequiresAnActiveWorkspaceOfTheApp(t *testing.T) {
	store := openTestSQLiteStore(t)
	seedTestApp(t, store, "app-test", "wsp_project123")
	seedTestApp(t, store, "app-other", "wsp_other123")
	service := newService(store, nil, func() time.Time { return time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC) })

	for name, workspaceID := range map[string]string{"missing": "wsp_missing123", "foreign": "wsp_other123"} {
		if _, err := service.Create("sandbox-"+name, CreateSpec{AppID: "app-test", WorkspaceID: workspaceID}); !errors.Is(err, ErrUnknownWorkspace) {
			t.Fatalf("Create() with %s Workspace error = %v, want ErrUnknownWorkspace", name, err)
		}
	}
	if _, err := store.ArchiveWorkspace("wsp_project123", time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("ArchiveWorkspace() error = %v", err)
	}
	if _, err := service.Create("sandbox-archived", CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"}); !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("Create() with archived Workspace error = %v, want ErrWorkspaceUnavailable", err)
	}
}

func TestServiceTerminate(t *testing.T) {
	store := openTestSQLiteStore(t)
	createdAt := time.Date(2026, time.July, 20, 20, 0, 0, 0, time.UTC)
	if err := store.Create(testSandbox(Sandbox{ID: "vm-test", State: StateRunning, CreatedAt: createdAt, UpdatedAt: createdAt})); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	transitionTimes := []time.Time{createdAt.Add(time.Second), createdAt.Add(2 * time.Second)}
	clockCall := 0
	service := newService(
		store,
		nil,
		func() time.Time {
			at := transitionTimes[clockCall]
			clockCall++
			return at
		},
	)

	terminated, err := service.Terminate(context.Background(), "vm-test")
	if err != nil {
		t.Fatalf("Terminate() error = %v", err)
	}
	if terminated.State != StateTerminated {
		t.Errorf("State = %q, want %q", terminated.State, StateTerminated)
	}
	if !terminated.UpdatedAt.Equal(transitionTimes[1]) {
		t.Errorf("UpdatedAt = %s, want %s", terminated.UpdatedAt, transitionTimes[1])
	}

	again, err := service.Terminate(context.Background(), "vm-test")
	if err != nil {
		t.Fatalf("second Terminate() error = %v", err)
	}
	if again != terminated {
		t.Errorf("second Terminate() = %#v, want unchanged %#v", again, terminated)
	}
	if clockCall != 2 {
		t.Errorf("clock called %d times, want 2; idempotent termination should not update time", clockCall)
	}
}

func TestServiceTerminateResumesStoppingMachine(t *testing.T) {
	store := openTestSQLiteStore(t)
	if err := store.Create(testSandbox(Sandbox{ID: "vm-test", State: StateStopping})); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	clockCalls := 0
	service := newService(
		store,
		nil,
		func() time.Time {
			clockCalls++
			return time.Now()
		},
	)

	machine, err := service.Terminate(context.Background(), "vm-test")
	if err != nil {
		t.Fatalf("Terminate() error = %v", err)
	}
	if machine.State != StateTerminated {
		t.Errorf("State = %q, want %q", machine.State, StateTerminated)
	}
	if clockCalls != 1 {
		t.Errorf("clock called %d times, want 1", clockCalls)
	}
}

func TestServiceTerminateNotFound(t *testing.T) {
	service := NewService(openTestSQLiteStore(t))

	if _, err := service.Terminate(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Terminate() error = %v, want ErrNotFound", err)
	}
}

func TestServiceTerminateIsConcurrentAndIdempotent(t *testing.T) {
	store := openTestSQLiteStore(t)
	if err := store.Create(testSandbox(Sandbox{ID: "vm-test", State: StateRunning})); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	service := NewService(store)

	const callers = 20
	errorsByCaller := make(chan error, callers)
	var callersReady sync.WaitGroup
	callersReady.Add(callers)
	start := make(chan struct{})
	for range callers {
		go func() {
			callersReady.Done()
			<-start
			_, err := service.Terminate(context.Background(), "vm-test")
			errorsByCaller <- err
		}()
	}
	callersReady.Wait()
	close(start)

	for range callers {
		if err := <-errorsByCaller; err != nil {
			t.Errorf("concurrent Terminate() error = %v", err)
		}
	}

	machine, err := store.Get("vm-test")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if machine.State != StateTerminated {
		t.Errorf("State = %q, want %q", machine.State, StateTerminated)
	}
}
