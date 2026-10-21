package sqliteschema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestInitializeCreatesAndReopensCanonicalSchema(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for attempt := 0; attempt < 2; attempt++ {
		if err := Initialize(context.Background(), db); err != nil {
			t.Fatalf("Initialize() attempt %d = %v", attempt+1, err)
		}
	}

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != CurrentVersion {
		t.Fatalf("user_version = %d, %v", version, err)
	}
}

func TestInitializeRejectsPreCutoverVersions(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE obsolete (id TEXT); PRAGMA user_version = 19"); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(context.Background(), db); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("Initialize() = %v, want ErrIncompatible", err)
	}
}

func TestInitializeRejectsUnsupportedVersionsOfCanonicalSchema(t *testing.T) {
	for _, version := range []int{21, 22, 23, CurrentVersion + 1} {
		t.Run(fmt.Sprintf("version %d", version), func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := Initialize(context.Background(), db); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
				t.Fatal(err)
			}
			if err := Initialize(context.Background(), db); !errors.Is(err, ErrIncompatible) {
				t.Fatalf("Initialize() = %v, want ErrIncompatible", err)
			}
			var got int
			if err := db.QueryRow("PRAGMA user_version").Scan(&got); err != nil || got != version {
				t.Fatalf("user_version = %d, %v; want unchanged %d", got, err, version)
			}
		})
	}
}

func TestInitializeMigratesVersion24WithoutDiscardingState(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	legacy := strings.Replace(baseCanonical, "    admitted_at TEXT,\n", "", 1)
	if _, err = db.Exec(legacy + `PRAGMA user_version=24; INSERT INTO apps(id,slug,created_at,updated_at)VALUES('app_0123456789abcdef0123456789abcdef','retained','2026-10-11T12:00:00Z','2026-10-11T12:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	if err = Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var slug string
	if err = db.QueryRow(`SELECT slug FROM apps`).Scan(&slug); err != nil || slug != "retained" {
		t.Fatalf("migration lost App: %s %v", slug, err)
	}
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != CurrentVersion {
		t.Fatal("migration version not committed")
	}
	if err = Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeMigratesVersion25PreservingViews(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	legacy := strings.Replace(baseCanonical+SessionStreams, "    admitted_at TEXT,\n", "    priority INTEGER NOT NULL DEFAULT 0,\n    cancellation_requested_at TEXT,\n", 1)
	if _, err = db.Exec(legacy + `PRAGMA user_version=25;
 INSERT INTO apps(id,slug,created_at,updated_at) VALUES('app_0123456789abcdef0123456789abcdef','retained','2026-10-11T12:00:00Z','2026-10-11T12:00:00Z');
 INSERT INTO session_streams(workspace_id,session_id,stream_id,sequence,view_revision,view_nonce,view_ciphertext) VALUES('workspace','session','stream',7,3,X'01',X'0203');`); err != nil {
		t.Fatal(err)
	}
	if err = Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var slug string
	if err = db.QueryRow(`SELECT slug FROM apps`).Scan(&slug); err != nil || slug != "retained" {
		t.Fatalf("lost App: %s %v", slug, err)
	}
	var sequence, revision int
	var ciphertext []byte
	if err = db.QueryRow(`SELECT sequence,view_revision,view_ciphertext FROM session_streams`).Scan(&sequence, &revision, &ciphertext); err != nil || sequence != 7 || revision != 3 || string(ciphertext) != "\x02\x03" {
		t.Fatalf("lost projection: %d %d %x %v", sequence, revision, ciphertext, err)
	}
	if err = Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeMigratesVersion26WithActiveSandboxIndex(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(baseCanonical + SessionStreams + `PRAGMA user_version=26;
 INSERT INTO sandboxes(id,state,record) VALUES('sbx-live','running',X'01'),('sbx-done','terminated',X'02');`); err != nil {
		t.Fatal(err)
	}
	if err = Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != CurrentVersion {
		t.Fatalf("user_version = %d, %v", version, err)
	}
	var plan string
	var id, parent, unused int
	if err = db.QueryRow(`EXPLAIN QUERY PLAN SELECT id, state, record FROM sandboxes WHERE state <> 'terminated' ORDER BY id`).Scan(&id, &parent, &unused, &plan); err != nil || !strings.Contains(plan, "sandboxes_active") {
		t.Fatalf("active Sandbox query plan = %q, %v", plan, err)
	}
	var live string
	if err = db.QueryRow(`SELECT id FROM sandboxes WHERE state <> 'terminated'`).Scan(&live); err != nil || live != "sbx-live" {
		t.Fatalf("migration lost Sandbox: %s %v", live, err)
	}
	if err = Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeMigratesVersion27PreservingManagedState(t *testing.T) {
	for _, prior := range []struct{ name, schema string }{
		{"released", baseCanonical + SessionStreams + ActiveSandboxes},
		{"managed-preview", baseCanonical + SessionStreams + ManagedApps},
	} {
		t.Run(prior.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(prior.schema + `PRAGMA user_version=27; INSERT INTO apps(id,slug,created_at,updated_at) VALUES('app_retained','retained','2026-10-11','2026-10-11'); INSERT INTO sandboxes(id,state,record) VALUES('sbx-retained','running',X'7b7d');`); err != nil {
				t.Fatal(err)
			}
			if prior.name == "managed-preview" {
				if _, err = db.Exec(`INSERT INTO managed_apps(app_id,record) VALUES('app_retained',X'7b7d'); INSERT INTO managed_usage(id,app_id,record) VALUES('lease-retained','app_retained',X'7b7d');`); err != nil {
					t.Fatal(err)
				}
			}
			if err = Initialize(context.Background(), db); err != nil {
				t.Fatal(err)
			}
			var state string
			var record []byte
			if err = db.QueryRow(`SELECT state,record FROM sandboxes WHERE id='sbx-retained'`).Scan(&state, &record); err != nil || state != "running" || string(record) != "{}" {
				t.Fatalf("lost native state: %s %s %v", state, record, err)
			}
			var n int
			if err = db.QueryRow(`SELECT count(*) FROM managed_usage`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if (prior.name == "managed-preview" && n != 1) || (prior.name == "released" && n != 0) {
				t.Fatal("managed accounting changed during migration", n)
			}
			if err = Initialize(context.Background(), db); err != nil {
				t.Fatal(err)
			}
		})
	}
}
