package microvm

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type storeFactory func(*testing.T) Store

func testStoreFactories(t *testing.T) map[string]storeFactory {
	t.Helper()
	return map[string]storeFactory{
		"sqlite": func(t *testing.T) Store { return openTestSQLiteStore(t) },
	}
}

func openTestSQLiteStore(t *testing.T, options ...SQLiteStoreOption) *SQLiteStore {
	t.Helper()
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"), options...)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestComputeProjectionStores(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
			createTestUser(t, store, "usr_alice", now)
			app := App{ID: "app_alice", Slug: "alice", CreatedAt: now, UpdatedAt: now}
			sessionRuntime := testAppSessionRuntime()
			sessionRuntime.ActiveBuildID = ""
			if err := store.CreateProvisioningAppForComputeTenant("usr_alice", app, sessionRuntime); err != nil {
				t.Fatalf("CreateProvisioningAppForComputeTenant() error = %v", err)
			}
			got, err := store.GetAppForComputeTenant("usr_alice", app.ID)
			if err != nil || got != app {
				t.Fatalf("GetAppForComputeTenant() = %#v, %v", got, err)
			}
			createTestUser(t, store, "usr_bob", now)
			if _, err := store.GetAppForComputeTenant("usr_bob", app.ID); !errors.Is(err, ErrAppNotFound) {
				t.Fatalf("foreign GetAppForComputeTenant() error = %v", err)
			}
			if _, err := store.ArchiveAppForComputeTenant("usr_alice", app.ID, now.Add(time.Second)); err != nil {
				t.Fatalf("ArchiveAppForComputeTenant() error = %v", err)
			}
			if err := store.OwnsAppForComputeTenant("usr_alice", app.ID); err != nil {
				t.Fatalf("archived App ownership error = %v", err)
			}
			if err := store.OwnsAppForComputeTenant("usr_bob", app.ID); !errors.Is(err, ErrAppNotFound) {
				t.Fatalf("foreign archived App ownership error = %v", err)
			}
		})
	}
}

func TestAppBuildUploadOperationsAreComputeTenantScoped(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
			createTestUser(t, store, "usr_alice", now)
			createTestUser(t, store, "usr_bob", now)
			app := App{ID: "app_alice", Slug: "alice", CreatedAt: now, UpdatedAt: now}
			sessionRuntime := testAppSessionRuntime()
			sessionRuntime.ActiveBuildID = ""
			if err := store.CreateProvisioningAppForComputeTenant("usr_alice", app, sessionRuntime); err != nil {
				t.Fatal(err)
			}
			operation := AppBuildOperation{ID: "bop_0123456789abcdef0123456789abcdef", AppID: app.ID, Status: AppBuildOperationReceiving, CreatedAt: now, UpdatedAt: now}
			if err := store.BeginAppBuildUpload("usr_alice", operation); err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetAppBuildOperationForComputeTenant("usr_bob", app.ID, operation.ID); !errors.Is(err, ErrAppBuildOperationNotFound) {
				t.Fatalf("foreign operation read error = %v", err)
			}
			if _, err := store.ArchiveAppForComputeTenant("usr_alice", app.ID, now.Add(2*time.Second)); !errors.Is(err, ErrAppBuildUploadPending) {
				t.Fatalf("archive with pending upload error = %v", err)
			}
		})
	}
}
