package microvm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

func TestServiceSessionDerivesSessionRuntimeAndHidesSandbox(t *testing.T) {
	store := newTestStore(t)
	workspaceID := "wsp_project123"
	now := time.Date(2026, time.August, 5, 15, 0, 0, 0, time.UTC)
	var started []Sandbox
	supervisor := fakeSupervisor{
		start: func(_ context.Context, machine Sandbox) error {
			now = now.Add(time.Second) // simulate boot time before activation
			started = append(started, machine)
			return nil
		},
		stop: func(context.Context, Sandbox) error { return nil },
	}
	service := newService(store, supervisor, func() time.Time { return now })

	trace := warmuptrace.New()
	ctx := warmuptrace.WithContext(context.Background(), trace)
	created, err := service.CreateSessionAndStart(
		ctx, "ses_primary", workspaceID, 5*60)

	if err != nil {
		t.Fatalf("CreateSessionAndStart() error = %v", err)
	}
	if created.WorkspaceID != workspaceID || created.BuildID != testImageID || created.State != StateActive {
		t.Fatalf("created Session = %#v", created)
	}
	if len(started) != 1 {
		t.Fatalf("supervisor starts = %d, want 1", len(started))
	}
	machine := started[0]
	if machine.ID == created.ID ||
		machine.AppID != "app-test" || machine.WorkspaceID != workspaceID ||
		machine.Profile != DefaultSandboxProfileName || machine.BuildID != testImageID ||
		machine.VCPUs != DefaultSandboxVCPUs || machine.MemoryMiB != DefaultSandboxMemoryMiB {
		t.Fatalf("derived internal Sandbox = %#v", machine)
	}
	if got := trace.Snapshot().MachineID; got != machine.ID {
		t.Fatalf("warm-up trace machine ID = %q, want %q", got, machine.ID)
	}

	replayed, err := service.CreateSessionAndStart(
		context.Background(), created.ID, workspaceID, 5*60)

	if err != nil || !reflect.DeepEqual(replayed, created) {
		t.Fatalf("replayed Session = %#v, %v", replayed, err)
	}
	if len(started) != 1 {
		t.Fatalf("supervisor starts after replay = %d, want 1", len(started))
	}
	if _, err := service.CreateSessionAndStart(
		context.Background(), created.ID, "wsp_different", 5*60); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}

	listed, err := service.ListSessions(workspaceID)
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], created) {
		t.Fatalf("ListSessions() = %#v, %v", listed, err)
	}
	terminated, err := service.TerminateSession(context.Background(), created.ID)
	if err != nil || terminated.State != StateIdle {
		t.Fatalf("TerminateSession() = %#v, %v", terminated, err)
	}
}

func TestServiceOpensOneSessionForWorkspaceScopedKey(t *testing.T) {
	store := newTestStore(t)
	var starts int
	service := NewServiceWithSupervisor(store, fakeSupervisor{
		start: func(context.Context, Sandbox) error { starts++; return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
	})
	keyHash := strings.Repeat("a", 64)
	first, err := service.OpenSessionAndStart(
		context.Background(), "ses_first", "wsp_project123", 300, keyHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.OpenSessionAndStart(
		context.Background(), "ses_second", "wsp_project123", 300, keyHash,
	)
	if err != nil || second.ID != first.ID || starts != 1 {
		t.Fatalf("second open = %#v, %v; starts = %d", second, err, starts)
	}
	updated, err := service.OpenSessionAndStart(
		context.Background(), "ses_third", "wsp_project123", 600, keyHash,
	)
	if err != nil || updated.ID != first.ID || updated.KeepAliveSeconds != 600 || starts != 1 {
		t.Fatalf("updated open = %#v, %v; starts = %d", updated, err, starts)
	}
	if _, err := service.TerminateSession(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	restarted, err := service.OpenSessionAndStart(
		context.Background(), "ses_fourth", "wsp_project123", 0, keyHash,
	)
	if err != nil || restarted.ID != first.ID || restarted.State != StateActive || restarted.KeepAliveSeconds != 0 || starts != 2 {
		t.Fatalf("restarted open = %#v, %v; starts = %d", restarted, err, starts)
	}
}

func TestServiceReopenedKeyedSessionGetsActivationGraceAndLatestIdlePolicy(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	service := newService(store, fakeSupervisor{
		start: func(context.Context, Sandbox) error { now = now.Add(time.Second); return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
	}, func() time.Time { return now })
	keyHash := strings.Repeat("b", 64)
	created, err := service.OpenSessionAndStart(
		context.Background(), "ses_keepalive", "wsp_project123", 900, keyHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	leaseID := "lease_0123456789abcdef0123456789abcdef"
	if _, err := service.AcquireSessionWorkLease(created.ID, leaseID, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReleaseSessionWorkLease(created.ID, leaseID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	reopened, err := service.OpenSessionAndStart(
		context.Background(), "ses_other", "wsp_project123", 0, keyHash,
	)
	if err != nil || reopened.ID != created.ID || reopened.KeepAliveSeconds != 0 {
		t.Fatalf("reopened Session = %#v, %v", reopened, err)
	}
	machine, err := store.GetSandboxForSession(created.ID)
	if err != nil || !machine.IdleDeadline.Equal(now.Add(sessionActivationTimeout)) {
		t.Fatalf("reopened activation deadline = %s, %v", machine.IdleDeadline, err)
	}
	if _, err := service.AcquireSessionWorkLease(created.ID, leaseID, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReleaseSessionWorkLease(created.ID, leaseID); err != nil {
		t.Fatal(err)
	}
	machine, err = store.GetSandboxForSession(created.ID)
	if err != nil || !machine.IdleDeadline.Equal(now) {
		t.Fatalf("latest idle deadline = %s, %v; want %s", machine.IdleDeadline, err, now)
	}
}

func TestServiceRejectedKeyedReopenKeepsKeepAlive(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	rejectRuntime := false
	service := newService(store, fakeSupervisor{
		validate: func(RuntimeSpec) error {
			if rejectRuntime {
				return errors.New("no capacity")
			}
			return nil
		},
		start: func(context.Context, Sandbox) error { now = now.Add(time.Second); return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
	}, func() time.Time { return now })
	keyHash := strings.Repeat("d", 64)
	created, err := service.OpenSessionAndStart(
		context.Background(), "ses_rejected", "wsp_project123", 900, keyHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.TerminateSession(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	rejectRuntime = true
	if _, err := service.OpenSessionAndStart(
		context.Background(), "ses_other", "wsp_project123", 0, keyHash,
	); err == nil {
		t.Fatal("rejected reopen succeeded")
	}
	stored, err := store.GetSession(created.ID)
	if err != nil || stored.KeepAliveSeconds != 900 {
		t.Fatalf("stored keepalive after rejected reopen = %d, %v; want 900", stored.KeepAliveSeconds, err)
	}
}

func TestServiceLatestKeyedKeepAliveWaitsForEveryWorkLease(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	service := newService(store, fakeSupervisor{
		start: func(context.Context, Sandbox) error { now = now.Add(time.Second); return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
	}, func() time.Time { return now })
	keyHash := strings.Repeat("c", 64)
	created, err := service.OpenSessionAndStart(
		context.Background(), "ses_concurrent", "wsp_project123", 900, keyHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstLease := "lease_0123456789abcdef0123456789abcdef"
	secondLease := "lease_fedcba9876543210fedcba9876543210"
	if _, err := service.AcquireSessionWorkLease(created.ID, firstLease, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenSessionAndStart(
		context.Background(), "ses_other", "wsp_project123", 0, keyHash,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcquireSessionWorkLease(created.ID, secondLease, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReleaseSessionWorkLease(created.ID, firstLease); err != nil {
		t.Fatal(err)
	}
	machine, err := store.GetSandboxForSession(created.ID)
	if err != nil || !machine.IdleDeadline.IsZero() {
		t.Fatalf("Sandbox deadline with remaining lease = %s, %v", machine.IdleDeadline, err)
	}
	if _, err := service.ReleaseSessionWorkLease(created.ID, secondLease); err != nil {
		t.Fatal(err)
	}
	machine, err = store.GetSandboxForSession(created.ID)
	if err != nil || !machine.IdleDeadline.Equal(now) {
		t.Fatalf("final Sandbox deadline = %s, %v; want %s", machine.IdleDeadline, err, now)
	}
}

func TestServiceSessionUsesActivationTimeoutUntilWorkLease(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	service := newService(store, fakeSupervisor{
		start: func(context.Context, Sandbox) error { now = now.Add(time.Second); return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
	}, func() time.Time { return now })
	createdAt := now
	created, err := service.CreateSessionAndStart(
		context.Background(), "ses_activation_timeout", "wsp_project123", 120)

	if err != nil {
		t.Fatal(err)
	}
	machine, err := store.GetSandboxForSession(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := createdAt.Add(sessionActivationTimeout); !machine.IdleDeadline.Equal(want) {
		t.Fatalf("activation deadline = %s, want %s", machine.IdleDeadline, want)
	}
	if _, err := service.ResolveSessionRoute(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	machine, err = store.GetSandboxForSession(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := createdAt.Add(sessionActivationTimeout); !machine.IdleDeadline.Equal(want) {
		t.Fatalf("route lookup changed activation deadline to %s, want %s", machine.IdleDeadline, want)
	}
}

func TestServiceSessionWorkLeaseControlsIdleDeadline(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	service := newService(store, fakeSupervisor{
		start: func(context.Context, Sandbox) error { now = now.Add(time.Second); return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
	}, func() time.Time { return now })
	created, err := service.CreateSessionAndStart(
		context.Background(), "ses_work_lease", "wsp_project123", 120)

	if err != nil {
		t.Fatal(err)
	}
	leaseID := "lease_0123456789abcdef0123456789abcdef"
	lease, err := service.AcquireSessionWorkLease(created.ID, leaseID, time.Minute)
	if err != nil || !lease.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("AcquireSessionWorkLease() = %#v, %v", lease, err)
	}
	machine, err := store.GetSandboxForSession(created.ID)
	if err != nil || !machine.IdleDeadline.IsZero() {
		t.Fatalf("leased Sandbox = %#v, %v", machine, err)
	}
	replayed, err := service.AcquireSessionWorkLease(created.ID, leaseID, time.Minute)
	if err != nil || replayed != lease {
		t.Fatalf("replayed AcquireSessionWorkLease() = %#v, %v", replayed, err)
	}
	now = now.Add(10 * time.Second)
	renewed, err := service.RenewSessionWorkLease(created.ID, leaseID, 2*time.Minute)
	if err != nil || !renewed.ExpiresAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("RenewSessionWorkLease() = %#v, %v", renewed, err)
	}
	if _, err := service.ReleaseSessionWorkLease(created.ID, leaseID); err != nil {
		t.Fatal(err)
	}
	machine, err = store.GetSandboxForSession(created.ID)
	if err != nil || !machine.IdleDeadline.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("unleased Sandbox deadline = %s, %v", machine.IdleDeadline, err)
	}
	if _, err := service.ReleaseSessionWorkLease(created.ID, leaseID); err != nil {
		t.Fatalf("idempotent ReleaseSessionWorkLease() = %v", err)
	}
	unchanged, err := store.GetSandboxForSession(created.ID)
	if err != nil || !unchanged.IdleDeadline.Equal(machine.IdleDeadline) {
		t.Fatalf("idempotent release changed deadline to %s, %v", unchanged.IdleDeadline, err)
	}
}

func TestServiceExpiredWorkLeaseStartsIdleDeadline(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	service := newService(store, fakeSupervisor{
		start: func(context.Context, Sandbox) error { now = now.Add(time.Second); return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
	}, func() time.Time { return now })
	created, err := service.CreateSessionAndStart(
		context.Background(), "ses_expiring_lease", "wsp_project123", 30)

	if err != nil {
		t.Fatal(err)
	}
	leaseID := "lease_fedcba9876543210fedcba9876543210"
	if _, err := service.AcquireSessionWorkLease(created.ID, leaseID, time.Minute); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := service.RestoreSessionWorkLeaseSchedules(); err != nil {
		t.Fatal(err)
	}
	machine, err := store.GetSandboxForSession(created.ID)
	if err != nil || !machine.IdleDeadline.Equal(now.Add(30*time.Second)) {
		t.Fatalf("expired lease deadline = %s, %v", machine.IdleDeadline, err)
	}
	if leases, err := store.ListSessionWorkLeases(created.ID); err != nil || len(leases) != 0 {
		t.Fatalf("expired leases = %#v, %v", leases, err)
	}
}

func TestServiceReactivatesLogicalSessionOnCurrentBuild(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	var started, stopped []string
	service := newService(store, fakeSupervisor{
		start: func(_ context.Context, machine Sandbox) error {
			now = now.Add(time.Second) // simulate boot time before activation
			started = append(started, machine.ID)
			return nil
		},
		stop: func(_ context.Context, machine Sandbox) error {
			stopped = append(stopped, machine.ID)
			return nil
		},
	}, func() time.Time { return now })
	created, err := service.CreateSessionAndStart(
		context.Background(), "ses_reusable", "wsp_project123", 120)

	if err != nil {
		t.Fatal(err)
	}
	firstSandbox, err := store.GetSandboxForSession(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.releaseSessionSandbox(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	idle, err := service.GetSession(created.ID)
	if err != nil || idle.State != StateIdle {
		t.Fatalf("Session after idle release = %#v, %v", idle, err)
	}

	const nextBuild = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	prepareTestBuild(t, store, "bop_0123456789abcdef0123456789abcdef", "app-test", nextBuild, now)
	if err := store.SelectAppBuildForComputeTenant("usr_test", "app-test", nextBuild, now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Minute)
	route, err := service.ResolveSessionRoute(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondSandbox, err := store.GetSandboxForSession(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if route.Session.ID != created.ID || route.Session.BuildID != nextBuild ||
		secondSandbox.ID == firstSandbox.ID || secondSandbox.BuildID != nextBuild {
		t.Fatalf("reactivated Session/Sandbox = %#v / %#v", route.Session, secondSandbox)
	}
	if len(started) != 2 || len(stopped) != 1 || stopped[0] != firstSandbox.ID {
		t.Fatalf("started/stopped Sandboxes = %#v / %#v", started, stopped)
	}

	terminated, err := service.TerminateSession(context.Background(), created.ID)
	if err != nil || terminated.State != StateIdle {
		t.Fatalf("TerminateSession() = %#v, %v", terminated, err)
	}
	if route, err := service.ResolveSessionRoute(context.Background(), created.ID); err != nil || route.Session.State != StateActive {
		t.Fatalf("reactivated Session route = %#v, %v", route, err)
	}
}

func TestServiceAllowsConcurrentSessionsAcrossWorkspaces(t *testing.T) {
	store := newTestStore(t)
	service := NewServiceWithSupervisor(store, fakeSupervisor{
		start: func(context.Context, Sandbox) error { return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
	})
	firstWorkspaceID := "wsp_project123"
	now := time.Now().UTC()
	secondWorkspace := Workspace{ID: "wsp_second", AppID: "app-test", Slug: "second", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateWorkspace(secondWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSessionAndStart(
		context.Background(), "ses_first", firstWorkspaceID, 5*60); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSessionAndStart(
		context.Background(), "ses_second", secondWorkspace.ID, 5*60); err != nil {
		t.Fatalf("second Workspace create error = %v", err)
	}
	sessions, err := store.ListSessions(secondWorkspace.ID)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("second Workspace Sessions = %#v, %v", sessions, err)
	}
}

func TestServiceSessionRejectsUnavailableWorkspace(t *testing.T) {
	store := newTestStore(t)
	service := NewServiceWithSupervisor(store, fakeSupervisor{
		start: func(context.Context, Sandbox) error { return nil },
		stop:  func(context.Context, Sandbox) error { return nil },
	})
	workspaceID := "wsp_project123"
	if _, err := store.ArchiveWorkspace(workspaceID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSessionAndStart(
		context.Background(), "ses_disabled", workspaceID, 5*60); !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("disabled Workspace create error = %v", err)
	}
}

func (s *Service) releaseSessionSandbox(ctx context.Context, sessionID string) error {
	unlock := s.sessionLocks.lock(sessionID)
	defer unlock()
	return s.releaseSessionSandboxLocked(ctx, sessionID)
}
