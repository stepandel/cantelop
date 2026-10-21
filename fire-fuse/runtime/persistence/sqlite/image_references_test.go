package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqliteschema"
)

func TestPublishedBuildReferencesCoverAttachmentsRuntimesAndLiveSessions(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	stamp := "2026-10-14T00:00:00Z"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"INSERT INTO compute_tenants VALUES ('usr_alice', ?)", []any{stamp}},
		{"INSERT INTO apps VALUES ('app-1','refs',?,?,NULL)", []any{stamp, stamp}},
		{"INSERT INTO app_tenants VALUES ('app-1','usr_alice',?)", []any{stamp}},
		{"INSERT INTO workspaces VALUES ('wsp-1','app-1','main',?,?,NULL)", []any{stamp, stamp}},
		{"INSERT INTO app_builds VALUES ('app-1',1,'sha256:attached',?)", []any{stamp}},
		{"INSERT INTO app_session_runtimes VALUES ('app-1','sha256:attached','default',3000)", nil},
		{"INSERT INTO sandboxes VALUES ('vm-live','running',?)", []any{[]byte("{}")}},
		{"INSERT INTO sandboxes VALUES ('vm-done','terminated',?)", []any{[]byte("{}")}},
		{"INSERT INTO sessions VALUES ('ses-live','wsp-1','sha256:live','vm-live',?,?,0)", []any{stamp, stamp}},
		{"INSERT INTO sessions VALUES ('ses-done','wsp-1','sha256:done','vm-done',?,?,0)", []any{stamp, stamp}},
		{"INSERT INTO app_build_operations VALUES ('bop_1','app-1','receiving',0,NULL,NULL,NULL,?,?)", []any{stamp, stamp}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("%s: %v", statement.query, err)
		}
	}
	references, pending, err := NewBuildOperations(db).PublishedBuildReferences()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sha256:attached", "sha256:live"} {
		if _, ok := references[id]; !ok {
			t.Errorf("%s not referenced", id)
		}
	}
	if _, ok := references["sha256:done"]; ok || len(references) != 2 || pending != 1 {
		t.Fatalf("references=%v pending=%d", references, pending)
	}
}

func TestPublishedBuildReferencesReleaseArchivedAppsButKeepLiveSessions(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	stamp := "2026-10-14T00:00:00Z"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"INSERT INTO compute_tenants VALUES ('usr_alice', ?)", []any{stamp}},
		{"INSERT INTO apps VALUES ('app-live','live',?,?,NULL)", []any{stamp, stamp}},
		{"INSERT INTO apps VALUES ('app-archived','archived',?,?,NULL)", []any{stamp, stamp}},
		{"INSERT INTO app_tenants VALUES ('app-live','usr_alice',?)", []any{stamp}},
		{"INSERT INTO app_tenants VALUES ('app-archived','usr_alice',?)", []any{stamp}},
		{"INSERT INTO workspaces VALUES ('wsp-archived','app-archived','main',?,?,NULL)", []any{stamp, stamp}},
		{"INSERT INTO app_builds VALUES ('app-archived',1,'sha256:attached',?)", []any{stamp}},
		{"INSERT INTO app_builds VALUES ('app-archived',2,'sha256:selected',?)", []any{stamp}},
		{"INSERT INTO app_builds VALUES ('app-archived',3,'sha256:shared',?)", []any{stamp}},
		{"INSERT INTO app_builds VALUES ('app-live',1,'sha256:shared',?)", []any{stamp}},
		{"INSERT INTO app_session_runtimes VALUES ('app-archived','sha256:selected','default',3000)", nil},
		{"INSERT INTO app_session_runtimes VALUES ('app-live','sha256:shared','default',3000)", nil},
		{"INSERT INTO sandboxes VALUES ('vm-live','running',?)", []any{[]byte("{}")}},
		{"INSERT INTO sandboxes VALUES ('vm-done','terminated',?)", []any{[]byte("{}")}},
		{"INSERT INTO sessions VALUES ('ses-live','wsp-archived','sha256:session-live','vm-live',?,?,0)", []any{stamp, stamp}},
		{"INSERT INTO sessions VALUES ('ses-done','wsp-archived','sha256:session-done','vm-done',?,?,0)", []any{stamp, stamp}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("%s: %v", statement.query, err)
		}
	}
	archived, err := NewCatalog(db).ArchiveApp("usr_alice", "app-archived", time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC))
	if err != nil || !archived {
		t.Fatalf("ArchiveApp: archived=%v err=%v", archived, err)
	}
	references, pending, err := NewBuildOperations(db).PublishedBuildReferences()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sha256:shared", "sha256:session-live"} {
		if _, ok := references[id]; !ok {
			t.Errorf("%s not referenced", id)
		}
	}
	if len(references) != 2 || pending != 0 {
		t.Fatalf("archived App still pins unused Builds: references=%v pending=%d", references, pending)
	}
}
