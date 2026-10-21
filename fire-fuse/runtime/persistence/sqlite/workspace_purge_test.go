package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqliteschema"
	_ "modernc.org/sqlite"
)

func TestWorkspaceMetadataPurgeErasesRuntimeDataAndKeepsTombstone(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	workspaceID := "wsp_0123456789abcdef0123456789abcdef"
	messageID := "msg_0123456789abcdef0123456789abcdef"
	buildID := "sha256:" + strings.Repeat("a", 64)
	statements := []string{
		`INSERT INTO apps (id, slug, created_at, updated_at) VALUES ('app-test', 'test', '` + stamp + `', '` + stamp + `')`,
		`INSERT INTO workspaces VALUES ('` + workspaceID + `', 'app-test', 'production', '` + stamp + `', '` + stamp + `', '` + stamp + `')`,
		`INSERT INTO sandboxes VALUES ('sbx-test', 'terminated', CAST('{"id":"sbx-test","workspace_id":"` + workspaceID + `"}' AS BLOB))`,
		`INSERT INTO sessions VALUES ('ses-test', '` + workspaceID + `', '` + buildID + `', 'sbx-test', '` + stamp + `', '` + stamp + `', 0)`,
		`INSERT INTO session_keys VALUES ('` + workspaceID + `', '` + strings.Repeat("b", 64) + `', 'ses-test')`,
		`INSERT INTO session_work_leases VALUES ('ses-test', 'lease-test', '` + stamp + `', '` + stamp + `', '` + stamp + `')`,
		`INSERT INTO request_events (event_id, workspace_id, session_id, received_at, status_code, duration_us) VALUES ('evt-test', '` + workspaceID + `', 'ses-test', '` + stamp + `', 200, 1)`,
		`INSERT INTO message_deliveries (id, workspace_id, logical_session_id, keep_alive_seconds, status, work_state,
		 payload_nonce, payload_ciphertext, session_id, generation, attempts, accepted_at, started_at, handled_at, quiescent_at, updated_at)
		 VALUES ('` + messageID + `', '` + workspaceID + `', 'thread', 0, 'handled', 'quiescent', x'01', x'02', 'ses-test', 1, 1,
		 '` + stamp + `', '` + stamp + `', '` + stamp + `', '` + stamp + `', '` + stamp + `')`,
		`INSERT INTO message_executions VALUES ('` + messageID + `', x'7b7d', 1, 1)`,
		`INSERT INTO message_delivery_events (message_id, workspace_id, logical_session_id, session_id, attempt, generation, transition, status, work_state, occurred_at)
		 VALUES ('` + messageID + `', '` + workspaceID + `', 'thread', 'ses-test', 1, 1, 'quiescent', 'handled', 'quiescent', '` + stamp + `')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	repository := NewWorkspaces(db)
	if err := repository.PurgeMetadata(workspaceID); err != nil {
		t.Fatalf("PurgeMetadata() error = %v", err)
	}
	if err := repository.PurgeMetadata(workspaceID); err != nil {
		t.Fatalf("replayed PurgeMetadata() error = %v", err)
	}
	for _, table := range []string{"sandboxes", "sessions", "session_keys", "session_work_leases", "request_events", "message_deliveries", "message_executions", "message_delivery_events"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Errorf("%s count = %d, %v; want 0", table, count, err)
		}
	}
	var archivedAt string
	if err := db.QueryRow("SELECT archived_at FROM workspaces WHERE id = ?", workspaceID).Scan(&archivedAt); err != nil || archivedAt != stamp {
		t.Fatalf("Workspace tombstone = %q, %v", archivedAt, err)
	}
}
