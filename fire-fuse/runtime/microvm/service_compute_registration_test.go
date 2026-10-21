package microvm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRegisterComputeAppForUserIsIdempotent(t *testing.T) {
	store := openTestSQLiteStore(t)
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	service := newService(store, nil, func() time.Time { return now })
	desired := RegisterComputeAppSpec{CreateAppSpec: CreateAppSpec{
		ID: "app_0123456789abcdef0123456789abcdef", Slug: "agent",
		SessionRuntime: AppSessionRuntime{
			Runtime: AppRuntimeSpec{Profile: DefaultSandboxProfileName, InternalPort: 3000},
		},
	}}

	app, created, err := service.RegisterComputeAppForUser("usr_0123456789abcdef0123456789abcdef", desired)
	if err != nil || !created || app.ID != desired.ID {
		t.Fatalf("first registration = %#v %t, %v", app, created, err)
	}
	projection, err := store.GetComputeTenant("usr_0123456789abcdef0123456789abcdef")
	if err != nil || projection.ID == "" || !projection.CreatedAt.Equal(now) {
		t.Fatalf("compute tenant projection = %#v, %v", projection, err)
	}
	_, created, err = service.RegisterComputeAppForUser("usr_0123456789abcdef0123456789abcdef", desired)
	if err != nil || created {
		t.Fatalf("replayed registration created=%t, err=%v", created, err)
	}

	conflict := desired
	conflict.SessionRuntime.Runtime.InternalPort = 8080
	_, _, err = service.RegisterComputeAppForUser("usr_0123456789abcdef0123456789abcdef", conflict)
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("conflicting registration error = %v", err)
	}
}

func TestArchiveComputeWorkspaceForUserFreesCapacity(t *testing.T) {
	store := openTestSQLiteStore(t)
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	service := newService(store, nil, func() time.Time { return now })
	userID := "usr_0123456789abcdef0123456789abcdef"
	app := RegisterComputeAppSpec{CreateAppSpec: CreateAppSpec{
		ID: "app_0123456789abcdef0123456789abcdef", Slug: "agent",
		SessionRuntime: AppSessionRuntime{Runtime: AppRuntimeSpec{Profile: DefaultSandboxProfileName, InternalPort: 3000}},
	}}
	if _, _, err := service.RegisterComputeAppForUser(userID, app); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxWorkspacesPerApp; i++ {
		desired := RegisterComputeWorkspaceSpec{
			ID: fmt.Sprintf("wsp_%032x", i), AppID: app.ID, Slug: fmt.Sprintf("workspace-%d", i),
		}
		if _, created, err := service.RegisterComputeWorkspaceForUser(userID, desired); err != nil || !created {
			t.Fatalf("register Workspace %d: created=%t err=%v", i, created, err)
		}
	}
	if _, _, err := service.RegisterComputeWorkspaceForUser(userID, RegisterComputeWorkspaceSpec{
		ID: "wsp_ffffffffffffffffffffffffffffffff", AppID: app.ID, Slug: "over-capacity",
	}); !errors.Is(err, ErrProviderCapacity) {
		t.Fatalf("over-capacity registration error = %v", err)
	}
	archived, err := service.ArchiveWorkspaceForUser(context.Background(), userID, app.ID, "wsp_00000000000000000000000000000000")
	if err != nil || !archived {
		t.Fatalf("archive Workspace = %t, %v", archived, err)
	}
	archived, err = service.ArchiveWorkspaceForUser(context.Background(), userID, app.ID, "wsp_00000000000000000000000000000000")
	if err != nil || archived {
		t.Fatalf("replay archive Workspace = %t, %v", archived, err)
	}
	if _, created, err := service.RegisterComputeWorkspaceForUser(userID, RegisterComputeWorkspaceSpec{
		ID: "wsp_ffffffffffffffffffffffffffffffff", AppID: app.ID, Slug: "replacement",
	}); err != nil || !created {
		t.Fatalf("replacement registration: created=%t err=%v", created, err)
	}
}

func TestPurgeComputeWorkspaceForUserArchivesThenPurges(t *testing.T) {
	store := openTestSQLiteStore(t)
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	var purged []string
	service := newService(store, fakeSupervisor{
		purgeWorkspace: func(_ context.Context, workspaceID string) error {
			purged = append(purged, workspaceID)
			return nil
		},
	}, func() time.Time { return now })
	userID := "usr_0123456789abcdef0123456789abcdef"
	appID := "app_0123456789abcdef0123456789abcdef"
	workspaceID := "wsp_0123456789abcdef0123456789abcdef"
	if _, _, err := service.RegisterComputeAppForUser(userID, RegisterComputeAppSpec{CreateAppSpec: CreateAppSpec{
		ID: appID, Slug: "agent",
		SessionRuntime: AppSessionRuntime{Runtime: AppRuntimeSpec{Profile: DefaultSandboxProfileName, InternalPort: 3000}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RegisterComputeWorkspaceForUser(userID, RegisterComputeWorkspaceSpec{
		ID: workspaceID, AppID: appID, Slug: "production",
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.PurgeWorkspaceForUser(context.Background(), userID, appID, workspaceID); err != nil {
		t.Fatalf("PurgeWorkspaceForUser() error = %v", err)
	}
	if err := service.PurgeWorkspaceForUser(context.Background(), userID, appID, workspaceID); err != nil {
		t.Fatalf("replayed PurgeWorkspaceForUser() error = %v", err)
	}
	if len(purged) != 2 || purged[0] != workspaceID || purged[1] != workspaceID {
		t.Fatalf("purged Workspaces = %#v", purged)
	}
	workspace, err := store.GetWorkspace(workspaceID)
	if err != nil || workspace.ArchivedAt == nil {
		t.Fatalf("Workspace tombstone = %#v, %v", workspace, err)
	}
}

func TestPurgeComputeWorkspaceForUserAfterAppArchival(t *testing.T) {
	store := openTestSQLiteStore(t)
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	var purged []string
	service := newService(store, fakeSupervisor{
		purgeWorkspace: func(_ context.Context, workspaceID string) error {
			purged = append(purged, workspaceID)
			return nil
		},
	}, func() time.Time { return now })
	userID := "usr_0123456789abcdef0123456789abcdef"
	appID := "app_0123456789abcdef0123456789abcdef"
	workspaceID := "wsp_0123456789abcdef0123456789abcdef"
	if _, _, err := service.RegisterComputeAppForUser(userID, RegisterComputeAppSpec{CreateAppSpec: CreateAppSpec{
		ID: appID, Slug: "agent",
		SessionRuntime: AppSessionRuntime{Runtime: AppRuntimeSpec{Profile: DefaultSandboxProfileName, InternalPort: 3000}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RegisterComputeWorkspaceForUser(userID, RegisterComputeWorkspaceSpec{
		ID: workspaceID, AppID: appID, Slug: "production",
	}); err != nil {
		t.Fatal(err)
	}
	if archived, err := service.ArchiveAppForUser(context.Background(), userID, appID); err != nil || !archived {
		t.Fatalf("ArchiveAppForUser() = %t, %v", archived, err)
	}
	if err := service.PurgeWorkspaceForUser(context.Background(), userID, appID, workspaceID); err != nil {
		t.Fatalf("PurgeWorkspaceForUser() after App archival error = %v", err)
	}
	if len(purged) != 1 || purged[0] != workspaceID {
		t.Fatalf("purged Workspaces = %#v", purged)
	}
	if err := service.PurgeWorkspaceForUser(context.Background(), "usr_fedcba9876543210fedcba9876543210", appID, workspaceID); !errors.Is(err, ErrComputeTenantMissing) {
		t.Fatalf("foreign purge error = %v", err)
	}
}

func TestRegisterComputeAppForUserBindsAndComparesExistingBuild(t *testing.T) {
	store := openTestSQLiteStore(t)
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	service := newService(store, nil, func() time.Time { return now })
	desired := RegisterComputeAppSpec{CreateAppSpec: CreateAppSpec{
		ID: "app_0123456789abcdef0123456789abcdef", Slug: "benchmark",
		BuildID: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		SessionRuntime: AppSessionRuntime{
			Runtime: AppRuntimeSpec{Profile: DefaultSandboxProfileName, InternalPort: 22},
		},
	}}

	if _, created, err := service.RegisterComputeAppForUser("usr_0123456789abcdef0123456789abcdef", desired); err != nil || !created {
		t.Fatalf("first Build-backed registration created=%t, err=%v", created, err)
	}
	sessionRuntime, err := store.GetAppSessionRuntime(desired.ID)
	if err != nil || sessionRuntime.ActiveBuildID != desired.BuildID {
		t.Fatalf("active Build = %q, err=%v", sessionRuntime.ActiveBuildID, err)
	}
	conflict := desired
	conflict.BuildID = "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if _, _, err := service.RegisterComputeAppForUser("usr_0123456789abcdef0123456789abcdef", conflict); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("conflicting Build registration error = %v", err)
	}
}

func TestRegisterComputeWorkspaceForUserIsIdempotent(t *testing.T) {
	store := openTestSQLiteStore(t)
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	service := newService(store, nil, func() time.Time { return now })
	appDesired := RegisterComputeAppSpec{CreateAppSpec: CreateAppSpec{
		ID: "app_0123456789abcdef0123456789abcdef", Slug: "agent",
		SessionRuntime: AppSessionRuntime{
			Runtime: AppRuntimeSpec{Profile: DefaultSandboxProfileName, InternalPort: 3000},
		},
	}}
	if _, _, err := service.RegisterComputeAppForUser("usr_0123456789abcdef0123456789abcdef", appDesired); err != nil {
		t.Fatal(err)
	}
	desired := RegisterComputeWorkspaceSpec{
		ID: "wsp_abcdef0123456789abcdef0123456789", AppID: appDesired.ID,
		Slug: "staging",
	}
	workspace, created, err := service.RegisterComputeWorkspaceForUser("usr_0123456789abcdef0123456789abcdef", desired)
	if err != nil || !created {
		t.Fatalf("first registration = %#v %t, %v", workspace, created, err)
	}
	_, created, err = service.RegisterComputeWorkspaceForUser("usr_0123456789abcdef0123456789abcdef", desired)
	if err != nil || created {
		t.Fatalf("replayed registration created=%t, err=%v", created, err)
	}
	desired.Slug = "preview"
	if _, _, err := service.RegisterComputeWorkspaceForUser("usr_0123456789abcdef0123456789abcdef", desired); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("conflicting registration error = %v", err)
	}
}
