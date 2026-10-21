package microvm

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceStoreLifecycle(t *testing.T) {
	for _, test := range []struct {
		name string
		new  func(*testing.T) Store
	}{
		{name: "sqlite", new: func(t *testing.T) Store {
			store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := test.new(t)
			seedTestApp(t, store, "app-test", "wsp_project123")
			now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
			workspace := Workspace{ID: "wsp_second", AppID: "app-test", Slug: "app-test-second", CreatedAt: now, UpdatedAt: now}
			if err := store.CreateWorkspace(workspace); err != nil {
				t.Fatalf("CreateWorkspace() error = %v", err)
			}
			if err := store.CreateWorkspace(workspace); !errors.Is(err, ErrAlreadyExists) {
				t.Fatalf("replay error = %v", err)
			}
			resolved, err := store.GetWorkspaceByAppIDAndSlug(workspace.AppID, workspace.Slug)
			if err != nil || resolved.ID != workspace.ID {
				t.Fatalf("GetWorkspaceByAppIDAndSlug() = %#v, %v", resolved, err)
			}
			if _, err := store.ArchiveWorkspace("wsp_project123", now.Add(time.Minute)); err != nil {
				t.Fatalf("archive first Workspace: %v", err)
			}
			if count, err := store.CountWorkspaces("app-test"); err != nil || count != 1 {
				t.Fatalf("CountWorkspaces() after one archive = %d, %v", count, err)
			}
			archived, err := store.ArchiveWorkspace(workspace.ID, now.Add(2*time.Minute))
			if err != nil || archived.ArchivedAt == nil {
				t.Fatalf("ArchiveWorkspace() = %#v, %v", archived, err)
			}
			workspaces, err := store.ListWorkspaces("app-test")
			if err != nil || len(workspaces) != 2 {
				t.Fatalf("ListWorkspaces() = %#v, %v", workspaces, err)
			}
			if _, err := store.GetWorkspaceByAppIDAndSlug(workspace.AppID, workspace.Slug); !errors.Is(err, ErrWorkspaceNotFound) {
				t.Fatalf("archived Workspace lookup error = %v", err)
			}
			if count, err := store.CountWorkspaces("app-test"); err != nil || count != 0 {
				t.Fatalf("CountWorkspaces() after archiving all = %d, %v", count, err)
			}
			if _, err := store.GetAppRecord("app-test"); err != nil {
				t.Fatalf("App without an active Workspace error = %v", err)
			}
		})
	}
}
