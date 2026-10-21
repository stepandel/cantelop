package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqliteschema"
	_ "modernc.org/sqlite"
)

func TestCompletePreparedBuildOperationWithWorkspaceSchema(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	createdAt := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	completedAt := createdAt.Add(time.Minute)
	operationID := "bop_" + strings.Repeat("a", 32)
	buildID := "sha256:" + strings.Repeat("b", 64)
	stamp := createdAt.Format(time.RFC3339Nano)
	statements := []string{
		`INSERT INTO apps (id, slug, created_at, updated_at) VALUES ('app-1', 'editor', '` + stamp + `', '` + stamp + `')`,
		`INSERT INTO app_session_runtimes VALUES ('app-1', NULL, 'default', 3000)`,
		`INSERT INTO workspaces VALUES ('wsp_primary', 'app-1', 'production', '` + stamp + `', '` + stamp + `', NULL)`,
		`INSERT INTO app_build_operations (id, app_id, status, size_bytes, created_at, updated_at) VALUES ('` + operationID + `', 'app-1', 'receiving', 0, '` + stamp + `', '` + stamp + `')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	operation, build, err := NewBuildOperations(db).CompletePrepared(operationID, buildID, completedAt)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != model.AppBuildOperationSucceeded || operation.BuildID != buildID || build.BuildID != buildID || build.Version != 1 {
		t.Fatalf("CompletePrepared() = %#v, %#v", operation, build)
	}
	var activeBuildID sql.NullString
	if err := db.QueryRow(`SELECT active_build_id FROM app_session_runtimes WHERE app_id = 'app-1'`).Scan(&activeBuildID); err != nil {
		t.Fatal(err)
	}
	if activeBuildID.Valid {
		t.Fatalf("active Build = %q, want unchanged (NULL)", activeBuildID.String)
	}
}
