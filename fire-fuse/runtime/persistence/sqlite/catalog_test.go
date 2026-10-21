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

func TestCatalogReadsOwnedAppWorkspaceAndBuilds(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	stamp, buildID := now.Format(time.RFC3339Nano), "sha256:"+strings.Repeat("a", 64)
	statements := []string{
		`INSERT INTO compute_tenants VALUES ('usr_alice', '` + stamp + `')`,
		`INSERT INTO apps (id, slug, created_at, updated_at) VALUES ('app-1', 'editor', '` + stamp + `', '` + stamp + `')`,
		`INSERT INTO app_tenants VALUES ('app-1', 'usr_alice', '` + stamp + `')`,
		`INSERT INTO workspaces VALUES ('wsp_primary', 'app-1', 'production', '` + stamp + `', '` + stamp + `', NULL)`,
		`INSERT INTO app_builds VALUES ('app-1', 1, '` + buildID + `', '` + stamp + `')`,
		`INSERT INTO app_session_runtimes VALUES ('app-1', '` + buildID + `', 'default', 3000)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	repository := NewCatalog(db)
	app, err := repository.GetOwnedApp("usr_alice", "app-1")
	if err != nil || app.ID != "app-1" {
		t.Fatalf("GetOwnedApp() = %#v, %v", app, err)
	}
	if _, err := repository.GetOwnedApp("usr_bob", "app-1"); !errors.Is(err, model.ErrAppNotFound) {
		t.Fatalf("foreign GetOwnedApp() = %v", err)
	}
	apps, err := repository.ListApps()
	if err != nil || len(apps) != 1 {
		t.Fatalf("ListApps() = %#v, %v", apps, err)
	}
	appRecord, err := repository.GetAppRecord("app-1")
	if err != nil || appRecord.ID != app.ID {
		t.Fatalf("GetAppRecord() = %#v, %v", appRecord, err)
	}
	builds, err := repository.ListBuilds()
	if err != nil || len(builds) != 1 || builds[0].BuildID != buildID {
		t.Fatalf("ListBuilds() = %#v, %v", builds, err)
	}
	count, err := repository.CountWorkspaces("app-1")
	if err != nil || count != 1 {
		t.Fatalf("CountWorkspaces() = %d, %v", count, err)
	}
	sessionRuntime, err := repository.GetSessionRuntime("app-1")
	if err != nil || sessionRuntime.ActiveBuildID != buildID {
		t.Fatalf("GetSessionRuntime() = %#v, %v", sessionRuntime, err)
	}
	appBuilds, err := repository.ListAppBuilds("app-1")
	if err != nil || len(appBuilds) != 1 {
		t.Fatalf("ListAppBuilds() = %#v, %v", appBuilds, err)
	}
	workspaces := NewWorkspaces(db)
	gotWorkspace, err := workspaces.Get("wsp_primary")
	if err != nil || gotWorkspace.AppID != "app-1" {
		t.Fatalf("Workspace Get() = %#v, %v", gotWorkspace, err)
	}
	listed, err := workspaces.List("app-1")
	if err != nil || len(listed) != 1 {
		t.Fatalf("Workspace List() = %#v, %v", listed, err)
	}
	routed, err := workspaces.GetByAppIDAndSlug("app-1", "production")
	if err != nil || routed.ID != gotWorkspace.ID {
		t.Fatalf("Workspace GetByAppIDAndSlug() = %#v, %v", routed, err)
	}
	plan, err := db.Query(`EXPLAIN QUERY PLAN SELECT w.id, w.app_id, w.slug, w.created_at, w.updated_at, w.archived_at
FROM workspaces w JOIN apps a ON a.id = w.app_id
WHERE w.app_id = ? AND w.slug = ? AND a.deleted_at IS NULL AND w.archived_at IS NULL`, "app-1", "production")
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	var details strings.Builder
	for plan.Next() {
		var id, parent, unused int
		var detail string
		if err := plan.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details.WriteString(detail)
		details.WriteByte('\n')
	}
	if err := plan.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(details.String(), "workspaces_routable_slug") {
		t.Fatalf("App Workspace query plan does not use slug index:\n%s", details.String())
	}
}
