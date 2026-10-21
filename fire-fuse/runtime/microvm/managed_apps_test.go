package microvm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func managedFixture(t *testing.T) (*SQLiteStore, *Service, *atomic.Int64, ManagedProjection) {
	t.Helper()
	store := openTestSQLiteStore(t)
	seedTestApp(t, store, "app-test", "wsp_project123")
	if _, err := store.database.SQL().Exec(`INSERT INTO managed_apps(app_id,record) VALUES('app-test',?)`, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	clock := &atomic.Int64{}
	clock.Store(time.Now().UTC().UnixNano())
	service := newService(store, fakeSupervisor{runtime: persistedRuntimeInfo(), start: func(context.Context, Sandbox) error { return nil }, stop: func(context.Context, Sandbox) error { return nil }}, func() time.Time { return time.Unix(0, clock.Load()).UTC() })
	service.ConfigureManagedStorage(func(string, int64) error { return nil })
	t.Cleanup(func() {
		service.managedMu.Lock()
		for _, timer := range service.managedTimers {
			timer.Stop()
		}
		service.managedMu.Unlock()
	})
	now := service.now()
	p := ManagedPolicy{Active: true, ExpiresAt: now.Add(time.Hour), MaxSandboxes: 2, MaxVcpus: 2 * DefaultSandboxVCPUs, MaxMemoryMiB: 2 * (DefaultSandboxMemoryMiB + SandboxHotplugMemoryMiB), MaxWorkspaces: 8, MaxStorageBytes: 10 << 30}
	rate := int64(p.MaxVcpus)*1024 + int64(p.MaxMemoryMiB)
	projection := ManagedProjection{Revision: 1, Policy: p, Grant: ManagedGrant{ID: "reservation-test-00000001", Epoch: "invoice-1", Credits: rate * 41, ExpiresAt: now.Add(40 * time.Second)}}
	return store, service, clock, projection
}
func TestManagedRegistrationIsDeniedBeforePolicy(t *testing.T) {
	store := openTestSQLiteStore(t)
	service := NewService(store)
	user := "usr_0123456789abcdef0123456789abcdef"
	app, _, err := service.RegisterComputeAppForUser(user, RegisterComputeAppSpec{CreateAppSpec: CreateAppSpec{Managed: true, ID: "app_0123456789abcdef0123456789abcdef", Slug: "managed", SessionRuntime: AppSessionRuntime{Runtime: AppRuntimeSpec{InternalPort: 3000, Profile: "default"}}}})
	if err != nil || !app.Managed {
		t.Fatalf("registration %#v %v", app, err)
	}
	state, err := store.ReadManaged(app.ID)
	if err != nil || state.Policy.Active {
		t.Fatalf("new managed App was not denied: %#v %v", state, err)
	}
	if err = service.checkManaged(Sandbox{AppID: app.ID}); !errors.Is(err, ErrManagedAdmission) {
		t.Fatalf("admitted without policy: %v", err)
	}
}
func TestManagedAdmissionIsAtomicAcrossConcurrentSandboxes(t *testing.T) {
	store, service, _, p := managedFixture(t)
	if err := service.ProjectManagedForUser("usr_test", "app-test", p); err != nil {
		t.Fatal(err)
	}
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := service.Create(fmt.Sprintf("vm-managed-%d", n), CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"})
			if err == nil {
				admitted.Add(1)
			} else if !errors.Is(err, ErrManagedAdmission) {
				t.Errorf("unexpected admission: %v", err)
			}
		}(n)
	}
	wg.Wait()
	if admitted.Load() != 2 {
		t.Fatalf("admitted %d, expected exactly 2", admitted.Load())
	}
	machines, _ := store.List()
	if len(machines) != 2 {
		t.Fatal("quota check was not atomic with durable creation")
	}
}
func TestManagedLeaseExpiresIndependentlyOfIdleDeadlineAndCannotReplay(t *testing.T) {
	store, service, clock, p := managedFixture(t)
	if err := service.ProjectManagedForUser("usr_test", "app-test", p); err != nil {
		t.Fatal(err)
	}
	machine, err := service.Create("vm-managed-expiry", CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"})
	if err != nil {
		t.Fatal(err)
	}
	if !machine.IdleDeadline.IsZero() {
		t.Fatal("fixture unexpectedly depends on idle timer")
	}
	clock.Add(int64(36 * time.Second))
	service.tickManaged("app-test")
	stopped, err := store.Get(machine.ID)
	if err != nil || stopped.State != StateTerminated {
		t.Fatalf("native compute survived allowance deadline: %#v %v", stopped, err)
	}
	p.Grant = ManagedGrant{}
	if err = service.ProjectManagedForUser("usr_test", "app-test", p); err != nil {
		t.Fatal(err)
	}
	usage, err := store.ManagedUsage("app-test")
	if err != nil || len(usage) != 1 || usage[0].Credits < 0 {
		t.Fatalf("final usage missing: %#v %v", usage, err)
	}
	old := usage[0].ID
	if err = store.AckManagedUsage("app-test", old); err != nil {
		t.Fatal(err)
	}
	p.Grant = ManagedGrant{ID: old, Epoch: "invoice-1", Credits: 10000000, ExpiresAt: service.now().Add(30 * time.Second)}
	if err = service.ProjectManagedForUser("usr_test", "app-test", p); !errors.Is(err, ErrManagedAdmission) {
		t.Fatalf("finalized grant replay refilled credit: %v", err)
	}
}
func TestManagedRecoveryConsumesAmbiguousGrantAndStopsCompute(t *testing.T) {
	store, service, _, p := managedFixture(t)
	if err := service.ProjectManagedForUser("usr_test", "app-test", p); err != nil {
		t.Fatal(err)
	}
	machine, err := service.Create("vm-managed-restart", CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.RestoreManagedSchedules(); err != nil {
		t.Fatal(err)
	}
	state, _ := store.ReadManaged("app-test")
	if state.Consumed != p.Grant.Credits {
		t.Fatal("restart reused ambiguous credit")
	}
	stopped, _ := store.Get(machine.ID)
	if stopped.State != StateTerminated {
		t.Fatal("restart did not stop running native resources")
	}
	if _, err = service.Start(context.Background(), machine.ID); err == nil {
		t.Fatal("restarted expired compute")
	}
}
func TestManagedDrainBlocksNewComputeAndStorageSlotsSurviveArchive(t *testing.T) {
	store, service, _, p := managedFixture(t)
	p.Draining = true
	if err := service.ProjectManagedForUser("usr_test", "app-test", p); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create("vm-managed-draining", CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"}); !errors.Is(err, ErrManagedAdmission) {
		t.Fatalf("drain admitted compute: %v", err)
	}
	if err := store.ReserveManagedStorage("app-test", "one", 1, 2<<30); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveManagedStorage("app-test", "two", 1, 2<<30); !errors.Is(err, ErrManagedAdmission) {
		t.Fatal("retained storage reused quota")
	}
	if err := store.ReleaseManagedStorage("one"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveManagedStorage("app-test", "two", 1, 2<<30); err != nil {
		t.Fatal("purged allocation was not reusable", err)
	}
}

func TestManagedMeteringSchedulesOnlyAllocatedCompute(t *testing.T) {
	_, service, _, projection := managedFixture(t)
	if err := service.ProjectManagedForUser("usr_test", "app-test", projection); err != nil {
		t.Fatal(err)
	}
	service.managedMu.Lock()
	idle := len(service.managedTimers)
	service.managedMu.Unlock()
	if idle != 0 {
		t.Fatal("idle App scheduled periodic accounting writes")
	}
	machine, err := service.Create("vm-managed-meter", CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"})
	if err != nil {
		t.Fatal(err)
	}
	service.managedMu.Lock()
	active := len(service.managedTimers)
	service.managedMu.Unlock()
	if active != 1 {
		t.Fatal("durable compute allocation did not start independent metering")
	}
	if _, err = service.Terminate(context.Background(), machine.ID); err != nil {
		t.Fatal(err)
	}
	service.tickManaged("app-test")
	service.managedMu.Lock()
	idle = len(service.managedTimers)
	service.managedMu.Unlock()
	if idle != 0 {
		t.Fatal("terminated compute kept periodic metering alive")
	}
}

func TestManagedUnfitReprojectionFinalizesInstalledGrantOnce(t *testing.T) {
	store, service, _, p := managedFixture(t)
	if err := service.ProjectManagedForUser("usr_test", "app-test", p); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"vm-managed-unfit-1", "vm-managed-unfit-2"} {
		if _, err := service.Create(id, CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"}); err != nil {
			t.Fatal(err)
		}
	}
	p.Revision = 2
	p.Policy.MaxSandboxes = 1
	if err := service.ProjectManagedForUser("usr_test", "app-test", p); err != nil {
		t.Fatalf("re-projecting the installed grant failed: %v", err)
	}
	usage, err := store.ManagedUsage("app-test")
	if err != nil || len(usage) != 1 || usage[0].ID != p.Grant.ID {
		t.Fatalf("installed grant was not finalized exactly once: %#v %v", usage, err)
	}
}

func TestManagedQuotaFailureReleasesStorageSlot(t *testing.T) {
	store, service, _, p := managedFixture(t)
	if err := service.ProjectManagedForUser("usr_test", "app-test", p); err != nil {
		t.Fatal(err)
	}
	service.ConfigureManagedStorage(func(string, int64) error { return errors.New("quota unavailable") })
	if err := service.managedWorkspaceQuota("app-test", "wsp_quotafailure", 0); err == nil {
		t.Fatal("quota failure admitted a Workspace")
	}
	var n int
	if err := store.database.SQL().QueryRow(`SELECT count(*) FROM managed_storage WHERE workspace_id='wsp_quotafailure'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed quota retained a storage slot: %d %v", n, err)
	}
}

func TestManagedConcurrentSessionReplayReturnsExistingSession(t *testing.T) {
	_, service, clock, p := managedFixture(t)
	service.supervisor = fakeSupervisor{runtime: persistedRuntimeInfo(), start: func(context.Context, Sandbox) error {
		clock.Add(int64(time.Millisecond)) // startup is measured before activation
		return nil
	}, stop: func(context.Context, Sandbox) error { return nil }}
	p.Policy.MaxSandboxes = 1
	if err := service.ProjectManagedForUser("usr_test", "app-test", p); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for n := 0; n < 10; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.CreateSessionAndStart(context.Background(), "ses_managedreplay", "wsp_project123", 5*60)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent replay rejected: %v", err)
		}
	}
}
