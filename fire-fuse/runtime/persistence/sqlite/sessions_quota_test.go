package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqliteschema"
	_ "modernc.org/sqlite"
)

func TestSessionStorageQuotaPreservesExistingIdentity(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	for _, statement := range []string{
		`INSERT INTO compute_tenants VALUES ('tenant-1', '` + at.Format(time.RFC3339Nano) + `')`,
		`INSERT INTO apps (id, slug, created_at, updated_at) VALUES ('app_0123456789abcdef0123456789abcdef', 'agent', '` + at.Format(time.RFC3339Nano) + `', '` + at.Format(time.RFC3339Nano) + `')`,
		`INSERT INTO workspaces (id, app_id, slug, created_at, updated_at) VALUES ('wsp_0123456789abcdef0123456789abcdef', 'app_0123456789abcdef0123456789abcdef', 'production', '` + at.Format(time.RFC3339Nano) + `', '` + at.Format(time.RFC3339Nano) + `')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE app_release_admission SET generation='act_00000000000000000000000000000000', closed=0 WHERE app_id='app_0123456789abcdef0123456789abcdef'`); err != nil {
		t.Fatal(err)
	}
	repository := NewSessions(db)
	repository.maxAppRows = 1
	first := model.Session{ID: "ses_first", WorkspaceID: "wsp_0123456789abcdef0123456789abcdef", BuildID: "sha256:" + strings.Repeat("a", 64), KeepAliveSeconds: 300, CreatedAt: at, UpdatedAt: at}
	binding := RuntimeBinding{ID: "sbx-first", State: model.StateStarting, Record: []byte(`{"app_id":"app_0123456789abcdef0123456789abcdef"}`)}
	key := strings.Repeat("b", 64)
	if _, created, err := repository.Create(first, binding, key); err != nil || !created {
		t.Fatalf("first Session created=%t err=%v", created, err)
	}
	second := first
	second.ID = "ses_second"
	if _, _, err := repository.Create(second, RuntimeBinding{ID: "sbx-second", State: model.StateStarting, Record: binding.Record}, ""); !errors.Is(err, model.ErrSessionStorageQuota) {
		t.Fatalf("new Session at capacity = %v", err)
	}
	if _, created, err := repository.Create(first, binding, key); err != nil || created {
		t.Fatalf("identity replay created=%t err=%v", created, err)
	}
	if existing, created, err := repository.Create(second, RuntimeBinding{ID: "sbx-second", State: model.StateStarting, Record: binding.Record}, key); err != nil || created || existing.ID != first.ID {
		t.Fatalf("keyed replay = %#v, %t, %v", existing, created, err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sandboxes").Scan(&count); err != nil || count != 1 {
		t.Fatalf("private bindings count=%d err=%v", count, err)
	}
	repository.maxAppSandboxRows = 1
	if _, err := repository.ReplaceBinding(first.ID, first.BuildID, RuntimeBinding{ID: "sbx-replaced", State: model.StateStarting, Record: binding.Record}, at.Add(time.Minute)); err != nil {
		t.Fatalf("replacement of the only bound Sandbox = %v", err)
	}
	repository.maxAppSandboxBytes = int64(len(binding.Record)) - 1
	if _, err := repository.ReplaceBinding(first.ID, first.BuildID, RuntimeBinding{ID: "sbx-replacement", State: model.StateStarting, Record: binding.Record}, at.Add(2*time.Minute)); !errors.Is(err, model.ErrSessionStorageQuota) {
		t.Fatalf("replacement at Sandbox capacity = %v", err)
	}
	if err := db.QueryRow("SELECT count(*) FROM sandboxes").Scan(&count); err != nil || count != 2 {
		t.Fatalf("rejected replacement changed Sandbox count=%d err=%v", count, err)
	}
}
