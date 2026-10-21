package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqliteschema"
	_ "modernc.org/sqlite"
)

func TestSessionWorkLeasesAcquireRenewReleaseAndExpire(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`
INSERT INTO apps (id, slug, created_at, updated_at) VALUES ('app_0123456789abcdef0123456789abcdef', 'agent', ?, ?);
INSERT INTO workspaces (id, app_id, slug, created_at, updated_at) VALUES ('wsp_0123456789abcdef0123456789abcdef', 'app_0123456789abcdef0123456789abcdef', 'production', ?, ?);
INSERT INTO sandboxes (id, state, record) VALUES ('sbx_lease', 'running', X'01');
INSERT INTO sessions (id, workspace_id, build_id, sandbox_id, created_at, updated_at, keep_alive_seconds)
VALUES ('ses_lease', 'wsp_0123456789abcdef0123456789abcdef', 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'sbx_lease', ?, ?, 300)`,
		formatTime(at), formatTime(at), formatTime(at), formatTime(at), formatTime(at), formatTime(at)); err != nil {
		t.Fatal(err)
	}
	repository := NewSessionWorkLeases(db)
	first := SessionWorkLease{
		SessionID: "ses_lease", LeaseID: "lease_first", ExpiresAt: at.Add(time.Minute),
		CreatedAt: at, UpdatedAt: at,
	}
	stored, created, err := repository.Acquire(first)
	if err != nil || !created || stored != first {
		t.Fatalf("Acquire() = %#v, %t, %v", stored, created, err)
	}
	if replayed, created, err := repository.Acquire(first); err != nil || created || replayed != first {
		t.Fatalf("replayed Acquire() = %#v, %t, %v", replayed, created, err)
	}
	renewed, err := repository.Renew(first.SessionID, first.LeaseID, at.Add(2*time.Minute), at.Add(time.Second))
	if err != nil || !renewed.ExpiresAt.Equal(at.Add(2*time.Minute)) {
		t.Fatalf("Renew() = %#v, %v", renewed, err)
	}
	second := SessionWorkLease{
		SessionID: "ses_lease", LeaseID: "lease_second", ExpiresAt: at.Add(30 * time.Second),
		CreatedAt: at, UpdatedAt: at,
	}
	if _, _, err := repository.Acquire(second); err != nil {
		t.Fatal(err)
	}
	if expired, err := repository.DeleteExpired(at.Add(time.Minute)); err != nil || len(expired) != 1 || expired[0] != first.SessionID {
		t.Fatalf("DeleteExpired() = %#v, %v", expired, err)
	}
	listed, err := repository.List(first.SessionID)
	if err != nil || len(listed) != 1 || listed[0].LeaseID != first.LeaseID {
		t.Fatalf("List() = %#v, %v", listed, err)
	}
	if released, err := repository.Release(first.SessionID, first.LeaseID); err != nil || !released {
		t.Fatalf("Release() = %t, %v", released, err)
	}
	if released, err := repository.Release(first.SessionID, first.LeaseID); err != nil || released {
		t.Fatalf("idempotent Release() = %t, %v", released, err)
	}
}
