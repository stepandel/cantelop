package microvm

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionStoreMaintainsSandboxBoundary(t *testing.T) {
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
			workspaceID := "wsp_project123"
			now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
			instance := Session{
				ID: "ses_test01", WorkspaceID: workspaceID, BuildID: testImageID,
				KeepAliveSeconds: 5 * 60,
				CreatedAt:        now, UpdatedAt: now,
			}
			sandbox := Sandbox{
				ID: deterministicInternalID("sbx-", instance.ID), AppID: "app-test",
				WorkspaceID: "wsp_project123", Profile: DefaultSandboxProfileName,
				BuildID: testImageID, VCPUs: DefaultSandboxVCPUs, MemoryMiB: DefaultSandboxMemoryMiB,
				State: StateStarting, CreatedAt: now, UpdatedAt: now,
			}
			keyHash := strings.Repeat("a", 64)

			created, inserted, err := store.CreateSession(instance, sandbox, keyHash)
			if err != nil || !inserted || created != instance {
				t.Fatalf("CreateSession() = %#v, %t, %v", created, inserted, err)
			}
			replayed, inserted, err := store.CreateSession(instance, sandbox, keyHash)
			if err != nil || inserted || replayed != instance {
				t.Fatalf("replayed CreateSession() = %#v, %t, %v", replayed, inserted, err)
			}
			keyed, err := store.GetSessionByKey(workspaceID, keyHash)
			if err != nil || keyed != instance {
				t.Fatalf("GetSessionByKey() = %#v, %v", keyed, err)
			}
			keepAliveUpdatedAt := now.Add(30 * time.Second)
			updated, err := store.UpdateSessionKeepAlive(instance.ID, workspaceID, 0, keepAliveUpdatedAt)
			if err != nil || updated.KeepAliveSeconds != 0 || !updated.UpdatedAt.Equal(keepAliveUpdatedAt) {
				t.Fatalf("UpdateSessionKeepAlive() = %#v, %v", updated, err)
			}
			keyed, err = store.GetSessionByKey(workspaceID, keyHash)
			if err != nil || keyed != updated {
				t.Fatalf("updated GetSessionByKey() = %#v, %v", keyed, err)
			}
			if _, err := store.UpdateSessionKeepAlive(instance.ID, "wsp_different", 600, keepAliveUpdatedAt); !errors.Is(err, ErrSessionConflict) {
				t.Fatalf("cross-Workspace UpdateSessionKeepAlive() error = %v", err)
			}
			if _, err := store.UpdateSessionKeepAlive(instance.ID, workspaceID, MaxSessionKeepAliveSeconds+1, keepAliveUpdatedAt); !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("invalid UpdateSessionKeepAlive() error = %v", err)
			}
			candidate := instance
			candidate.KeepAliveSeconds = 600
			candidate.ID = "ses_candidate"
			candidateSandbox := sandbox
			candidateSandbox.ID = deterministicInternalID("sbx-", candidate.ID)
			resolved, inserted, err := store.CreateSession(candidate, candidateSandbox, keyHash)
			if err != nil || inserted || resolved != updated {
				t.Fatalf("keyed replay = %#v, %t, %v", resolved, inserted, err)
			}
			conflict := instance
			conflict.WorkspaceID = "wsp_different"
			if _, _, err := store.CreateSession(conflict, sandbox, ""); !errors.Is(err, ErrSessionConflict) {
				t.Fatalf("conflicting CreateSession() error = %v", err)
			}

			storedSandbox, err := store.GetSandboxForSession(instance.ID)
			if err != nil || storedSandbox != sandbox {
				t.Fatalf("GetSandboxForSession() = %#v, %v", storedSandbox, err)
			}
			stoppingAt := now.Add(time.Minute)
			if _, err := store.Transition(sandbox.ID, StateStopping, stoppingAt); err != nil {
				t.Fatal(err)
			}
			active, err := store.GetSession(instance.ID)
			if err != nil || active.State != "" || !active.UpdatedAt.Equal(keepAliveUpdatedAt) {
				t.Fatalf("logical Session = %#v, %v", active, err)
			}
			sessions, err := store.ListSessions(workspaceID)
			if err != nil || len(sessions) != 1 || sessions[0] != active {
				t.Fatalf("ListSessions() = %#v, %v", sessions, err)
			}
			terminatedAt := stoppingAt.Add(time.Minute)
			if _, err := store.Transition(sandbox.ID, StateTerminated, terminatedAt); err != nil {
				t.Fatal(err)
			}
			replacement := sandbox
			replacement.ID = "sbx-replacement"
			replacement.State = StateStarting
			replacement.CreatedAt = terminatedAt.Add(time.Minute)
			replacement.UpdatedAt = replacement.CreatedAt
			replaced, err := store.ReplaceSessionSandbox(instance.ID, replacement, replacement.CreatedAt)
			if err != nil || replaced.State != "" || replaced.BuildID != replacement.BuildID {
				t.Fatalf("ReplaceSessionSandbox() = %#v, %v", replaced, err)
			}
			bound, err := store.GetSandboxForSession(instance.ID)
			if err != nil || bound.ID != replacement.ID {
				t.Fatalf("replacement binding = %#v, %v", bound, err)
			}
			reopenedSandbox := sandbox
			reopenedSandbox.ID = "sbx-reopened"
			reopenedSandbox.State = StateStarting
			reopenedSandbox.CreatedAt = replacement.UpdatedAt.Add(time.Minute)
			reopenedSandbox.UpdatedAt = reopenedSandbox.CreatedAt
			reopened, err := store.ReplaceSessionSandbox(instance.ID, reopenedSandbox, reopenedSandbox.CreatedAt)
			if err != nil || reopened.State != "" {
				t.Fatalf("replace Session Sandbox = %#v, %v", reopened, err)
			}
		})
	}
}

func TestSessionStoreRejectsWorkspaceSessionRuntimeOverrides(t *testing.T) {
	store := openTestSQLiteStore(t)
	seedTestApp(t, store, "app-test", "wsp_project123")
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	instance := Session{
		ID: "ses_test01", WorkspaceID: "wsp_project123",
		BuildID: testImageID, KeepAliveSeconds: 5 * 60,
		CreatedAt: now, UpdatedAt: now,
	}
	sandbox := Sandbox{
		ID: deterministicInternalID("sbx-", instance.ID), AppID: "app-test",
		WorkspaceID: "wsp_project123", Profile: "override",
		BuildID: testImageID, VCPUs: DefaultSandboxVCPUs, MemoryMiB: DefaultSandboxMemoryMiB,
		State: StateStarting, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateSession(instance, sandbox, ""); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("CreateSession() error = %v, want ErrInvalidSession", err)
	}
}

// deterministicInternalID derives stable private identifiers from public
// resource IDs for idempotent internal Sandbox creation.
func deterministicInternalID(prefix, resourceID string) string {
	digest := sha256.Sum256([]byte(resourceID))
	return prefix + hex.EncodeToString(digest[:16])
}
