package microvm

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	sqlitepersistence "github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqlite"
)

func TestSQLiteRequestEventsAreIdempotent(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedTestApp(t, store, "app-1", "wsp_events_data")
	workspaces, err := store.ListWorkspaces("app-1")
	if err != nil || len(workspaces) != 1 {
		t.Fatal(err)
	}
	event := RequestEvent{
		EventID: "request-1", WorkspaceID: workspaces[0].ID,
		ReceivedAt: time.Date(2026, 8, 5, 12, 0, 0, 123, time.FixedZone("offset", 3600)),
		StatusCode: 200, Duration: 1500*time.Microsecond + 999*time.Nanosecond,
	}
	created, inserted, err := store.RecordRequestEvent(event)
	if err != nil || !inserted || created.Sequence != 1 || created.Duration != 1500*time.Microsecond ||
		created.ReceivedAt.Location() != time.UTC {
		t.Fatalf("first RecordRequestEvent() = %#v, %t, %v", created, inserted, err)
	}
	replayed, inserted, err := store.RecordRequestEvent(event)
	if err != nil || inserted || replayed != created {
		t.Fatalf("replay RecordRequestEvent() = %#v, %t, %v", replayed, inserted, err)
	}
	event.StatusCode = 500
	if _, _, err := store.RecordRequestEvent(event); !errors.Is(err, ErrRequestEventConflict) {
		t.Fatalf("conflicting RecordRequestEvent() error = %v", err)
	}
}

func TestSQLiteRequestEventsUseBoundedCursorPages(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedTestApp(t, store, "app-1", "wsp_events_one")
	seedTestApp(t, store, "app-2", "wsp_events_two")
	workspaceIDs := map[string]string{}
	for _, appID := range []string{"app-1", "app-2"} {
		workspaces, listErr := store.ListWorkspaces(appID)
		if listErr != nil || len(workspaces) != 1 {
			t.Fatal(listErr)
		}
		workspaceIDs[appID] = workspaces[0].ID
	}
	for index, appID := range []string{"app-1", "app-2", "app-1", "app-1"} {
		_, _, err := store.RecordRequestEvent(RequestEvent{
			EventID: "request-" + string(rune('a'+index)), WorkspaceID: workspaceIDs[appID],
			ReceivedAt: time.Date(2026, 8, 5, 12, 0, index, 0, time.UTC),
			StatusCode: 200 + index, Duration: time.Duration(index) * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ListRequestEvents(RequestEventQuery{AppID: "app-1", Limit: 2})
	if err != nil || len(first.Items) != 2 || first.Items[0].Sequence != 4 || first.Items[1].Sequence != 3 ||
		first.NextBeforeSequence != 3 {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	second, err := store.ListRequestEvents(RequestEventQuery{
		AppID: "app-1", BeforeSequence: first.NextBeforeSequence, Limit: 2,
	})
	if err != nil || len(second.Items) != 1 || second.Items[0].Sequence != 1 || second.NextBeforeSequence != 0 {
		t.Fatalf("second page = %#v, %v", second, err)
	}
	if _, err := store.ListRequestEvents(RequestEventQuery{Limit: MaxRequestEventPageSize + 1}); !errors.Is(err, ErrInvalidRequestEvent) {
		t.Fatalf("oversized query error = %v", err)
	}
}

func TestSQLiteRequestEventsUseWorkspaceAttribution(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedTestApp(t, store, "app-events", "wsp_events_data")
	workspaces, err := store.ListWorkspaces("app-events")
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("ListWorkspaces() = %#v, %v", workspaces, err)
	}
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	instance := Session{
		ID: "ses_events", WorkspaceID: workspaces[0].ID, BuildID: testImageID,
		KeepAliveSeconds: 5 * 60,
		CreatedAt:        now, UpdatedAt: now,
	}
	machine := Sandbox{
		ID: deterministicInternalID("sbx-", instance.ID), AppID: "app-events", WorkspaceID: instance.WorkspaceID,
		Profile: DefaultSandboxProfileName, BuildID: testImageID,
		VCPUs: DefaultSandboxVCPUs, MemoryMiB: DefaultSandboxMemoryMiB, State: StateStarting, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateSession(instance, machine, ""); err != nil {
		t.Fatal(err)
	}
	want := RequestEvent{
		EventID: "workspace-request", WorkspaceID: workspaces[0].ID,
		SessionID: instance.ID, ReceivedAt: now, StatusCode: 200,
	}
	created, inserted, err := store.RecordRequestEvent(want)
	if err != nil || !inserted || created.WorkspaceID != want.WorkspaceID ||
		created.SessionID != want.SessionID {
		t.Fatalf("RecordRequestEvent() = %#v, %t, %v", created, inserted, err)
	}
	page, err := store.ListRequestEvents(RequestEventQuery{WorkspaceID: workspaces[0].ID, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0] != created {
		t.Fatalf("Workspace page = %#v, %v", page, err)
	}
}

func TestSQLiteRequestEventsPruneOldestRecords(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedTestApp(t, store, "app-events", "wsp_events_data")
	workspaces, err := store.ListWorkspaces("app-events")
	if err != nil || len(workspaces) != 1 {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for index := 0; index < 3; index++ {
		_, _, err := store.RecordRequestEvent(RequestEvent{
			EventID: fmt.Sprintf("retained-%05d", index), WorkspaceID: workspaces[0].ID,
			ReceivedAt: now.Add(time.Duration(index) * time.Microsecond), StatusCode: 200,
		})
		if err != nil {
			t.Fatalf("RecordRequestEvent(%d) error = %v", index, err)
		}
	}
	tx, err := store.database.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlitepersistence.PruneRequestEvents(tx, 3, 2); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.database.SQL().QueryRow("SELECT count(*) FROM request_events WHERE workspace_id = ?", workspaces[0].ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("retained request events = %d, want 2", count)
	}
	var oldestID string
	if err := store.database.SQL().QueryRow("SELECT event_id FROM request_events WHERE workspace_id = ? ORDER BY sequence LIMIT 1", workspaces[0].ID).Scan(&oldestID); err != nil {
		t.Fatal(err)
	}
	if oldestID != "retained-00001" {
		t.Fatalf("oldest retained event = %q", oldestID)
	}
}
