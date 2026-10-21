package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqliteschema"
)

func TestRetentionProtectsReferencesAndRollsBackFailedAdmission(t *testing.T) {
	for _, scenario := range []string{"live-session", "active-build", "all-protected", "unbound-sandbox", "host-full", "host-reclaimed", "archived-host-reclaimed", "archived-history-kept", "archived-protected"} {
		t.Run(scenario, func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			if err := sqliteschema.Initialize(context.Background(), db); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
			stamp := formatTime(now)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec("INSERT INTO compute_tenants VALUES ('usr_alice', ?)", stamp)
			exec("INSERT INTO apps VALUES ('app-1','retention',?,?,NULL)", stamp, stamp)
			exec("INSERT INTO app_tenants VALUES ('app-1','usr_alice',?)", stamp)
			exec("INSERT INTO workspaces VALUES ('wsp-1','app-1','main',?,?,NULL)", stamp, stamp)
			archivedOnly := scenario == "archived-history-kept" || scenario == "archived-protected"
			appBuilds := 25
			if archivedOnly {
				appBuilds = 22
			}
			for v := 1; v <= appBuilds; v++ {
				exec("INSERT INTO app_builds VALUES ('app-1',?,?,?)", v, fmt.Sprintf("sha256:%064x", v), stamp)
				exec("INSERT INTO app_build_operations VALUES (?,'app-1','succeeded',1024,?,?,NULL,?,?)", fmt.Sprintf("bop_%032x", v), fmt.Sprintf("sha256:%064x", v), v, stamp, stamp)
			}
			active := appBuilds
			if scenario == "active-build" {
				active = 1
			}
			exec("INSERT INTO app_session_runtimes VALUES ('app-1',?,'default',3000)", fmt.Sprintf("sha256:%064x", active))
			if scenario == "live-session" || scenario == "all-protected" {
				for v := 1; v <= 5; v++ {
					state := "terminated"
					if v == 1 || scenario == "all-protected" {
						state = "running"
					}
					exec("INSERT INTO sandboxes VALUES (?,?,?)", fmt.Sprintf("vm-%d", v), state, []byte("{}"))
					exec("INSERT INTO sessions VALUES (?,'wsp-1',?,?,?,?,0)", fmt.Sprintf("ses-%d", v), fmt.Sprintf("sha256:%064x", v), fmt.Sprintf("vm-%d", v), stamp, stamp)
				}
			}
			if scenario == "unbound-sandbox" {
				exec("INSERT INTO sandboxes VALUES ('vm-unbound','running',?)", []byte("{}"))
			}
			if scenario == "host-full" || scenario == "host-reclaimed" || scenario == "archived-host-reclaimed" || archivedOnly {
				archivedAt := any(nil)
				if scenario == "archived-host-reclaimed" || archivedOnly {
					archivedAt = stamp
				}
				exec("INSERT INTO apps VALUES ('app-2','other',?,?,?)", stamp, stamp, archivedAt)
				count := 225
				if scenario == "host-full" || scenario == "archived-host-reclaimed" {
					count = 230
				}
				if archivedOnly {
					count = 229
				}
				for v := 1; v <= count; v++ {
					exec("INSERT INTO app_builds VALUES ('app-2',?,?,?)", v, fmt.Sprintf("sha256:%064x", 1000+v), stamp)
				}
			}
			if scenario == "archived-protected" {
				// v1 is the archived runtime's active Build and v2 backs a live
				// Session. An unbound Sandbox must not block archived reclaim.
				exec("INSERT INTO app_session_runtimes VALUES ('app-2',?,'default',3000)", fmt.Sprintf("sha256:%064x", 1001))
				exec("INSERT INTO workspaces VALUES ('wsp-2','app-2','main',?,?,?)", stamp, stamp, stamp)
				exec("INSERT INTO sandboxes VALUES ('vm-archived','running',?)", []byte("{}"))
				exec("INSERT INTO sessions VALUES ('ses-archived','wsp-2',?,'vm-archived',?,?,0)", fmt.Sprintf("sha256:%064x", 1002), stamp, stamp)
				exec("INSERT INTO sandboxes VALUES ('vm-unbound','running',?)", []byte("{}"))
			}
			op := model.AppBuildOperation{ID: fmt.Sprintf("bop_%032x", 26), AppID: "app-1", Status: model.AppBuildOperationReceiving, CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)}
			err = NewBuildOperations(db).Begin("usr_alice", op)
			blocked := scenario == "all-protected" || scenario == "unbound-sandbox" || scenario == "host-full"
			if blocked && !errors.Is(err, model.ErrAppBuildQuota) || !blocked && err != nil {
				t.Fatalf("admission: %v", err)
			}
			var count, retired int
			if err := db.QueryRow("SELECT count(*) FROM app_builds WHERE app_id='app-1'").Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 21
			if scenario == "host-reclaimed" || scenario == "archived-host-reclaimed" {
				want = 20
			}
			if blocked || archivedOnly {
				want = appBuilds
			}
			if count != want {
				t.Fatalf("builds=%d want=%d", count, want)
			}
			if err := db.QueryRow("SELECT count(*) FROM app_build_operations WHERE error_code='build_retired'").Scan(&retired); err != nil {
				t.Fatal(err)
			}
			if retired != appBuilds-want {
				t.Fatalf("retired receipts=%d", retired)
			}
			if scenario == "archived-host-reclaimed" || archivedOnly {
				// Archived reclaim runs first and frees exactly enough host slots.
				archived, highWater := 224, 230
				if archivedOnly {
					archived, highWater = 227, 229
				}
				if err := db.QueryRow("SELECT count(*) FROM app_builds WHERE app_id='app-2'").Scan(&count); err != nil || count != archived {
					t.Fatalf("archived Builds=%d, %v", count, err)
				}
				if err := db.QueryRow("SELECT count(*) FROM app_builds WHERE app_id='app-2' AND version=?", highWater).Scan(&count); err != nil || count != 1 {
					t.Fatalf("archived high-water mark lost: %d, %v", count, err)
				}
			}
			if scenario == "archived-protected" {
				if err := db.QueryRow("SELECT count(*) FROM app_builds WHERE app_id='app-2' AND version IN (1,2)").Scan(&count); err != nil || count != 2 {
					t.Fatalf("protected archived Builds=%d, %v", count, err)
				}
				if err := db.QueryRow("SELECT count(*) FROM app_builds WHERE app_id='app-2' AND version IN (3,4)").Scan(&count); err != nil || count != 0 {
					t.Fatalf("unprotected archived Builds kept=%d, %v", count, err)
				}
			}
			if !blocked && scenario != "host-reclaimed" && scenario != "archived-host-reclaimed" {
				if err := db.QueryRow("SELECT count(*) FROM app_builds WHERE app_id='app-1' AND version=1").Scan(&count); err != nil || count != 1 {
					t.Fatal("protected build removed", err)
				}
			}
			rows, err := db.Query("PRAGMA foreign_key_check")
			if err != nil {
				t.Fatal(err)
			}
			if rows.Next() {
				t.Fatal("foreign key violation")
			}
			rows.Close()
		})
	}
}
