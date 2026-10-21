package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqliteschema"
	_ "modernc.org/sqlite"
)

func TestRequestEventsRecordReplayAndList(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	for _, statement := range []string{
		`INSERT INTO compute_tenants (id, created_at) VALUES ('tenant-1', '` + now.Format(time.RFC3339Nano) + `')`,
		`INSERT INTO apps (id, slug, created_at, updated_at) VALUES ('app-1', 'editor', '` + now.Format(time.RFC3339Nano) + `', '` + now.Format(time.RFC3339Nano) + `')`,
		`INSERT INTO workspaces (id, app_id, slug, created_at, updated_at, archived_at) VALUES ('wsp_primary', 'app-1', 'production', '` + now.Format(time.RFC3339Nano) + `', '` + now.Format(time.RFC3339Nano) + `', NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	repository := NewRequestEvents(db)
	event := model.RequestEvent{
		EventID: "request-1", RequestID: "req_0123456789abcdef0123456789abcdef",
		WorkspaceID: "wsp_primary", ReceivedAt: now,
		StatusCode: 200, Duration: 1500 * time.Microsecond,
	}
	stored, created, err := repository.RecordRequestEvent(event)
	if err != nil || !created || stored.Sequence != 1 {
		t.Fatalf("RecordRequestEvent() = %#v, %t, %v", stored, created, err)
	}
	if _, created, err := repository.RecordRequestEvent(event); err != nil || created {
		t.Fatalf("replay = created %t, %v", created, err)
	}
	changed := event
	changed.StatusCode = 201
	if _, _, err := repository.RecordRequestEvent(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting replay = %v, want ErrConflict", err)
	}
	page, err := repository.ListRequestEvents(model.RequestEventQuery{AppID: "app-1", Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0] != stored {
		t.Fatalf("ListRequestEvents() = %#v, %v", page, err)
	}
}

func TestRequestEventsPageForwardAfterASequence(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	for _, statement := range []string{
		`INSERT INTO compute_tenants (id, created_at) VALUES ('tenant-1', '` + now.Format(time.RFC3339Nano) + `')`,
		`INSERT INTO apps (id, slug, created_at, updated_at) VALUES ('app-1', 'editor', '` + now.Format(time.RFC3339Nano) + `', '` + now.Format(time.RFC3339Nano) + `')`,
		`INSERT INTO workspaces (id, app_id, slug, created_at, updated_at, archived_at) VALUES ('wsp_primary', 'app-1', 'production', '` + now.Format(time.RFC3339Nano) + `', '` + now.Format(time.RFC3339Nano) + `', NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	repository := NewRequestEvents(db)
	for index := 1; index <= 5; index++ {
		if _, _, err := repository.RecordRequestEvent(model.RequestEvent{
			EventID: fmt.Sprintf("request-%d", index), WorkspaceID: "wsp_primary", ReceivedAt: now,
			StatusCode: 200, Duration: time.Millisecond,
		}); err != nil {
			t.Fatal(err)
		}
	}
	sequences := func(page model.RequestEventPage) []int64 {
		values := make([]int64, 0, len(page.Items))
		for _, event := range page.Items {
			values = append(values, event.Sequence)
		}
		return values
	}
	page, err := repository.ListRequestEvents(model.RequestEventQuery{AppID: "app-1", AfterSequence: 1, Limit: 2})
	if err != nil || !slices.Equal(sequences(page), []int64{3, 2}) || page.NextBeforeSequence != 0 {
		t.Fatalf("after 1 = %v, next %d, %v", sequences(page), page.NextBeforeSequence, err)
	}
	page, err = repository.ListRequestEvents(model.RequestEventQuery{WorkspaceID: "wsp_primary", AfterSequence: 3, Limit: 10})
	if err != nil || !slices.Equal(sequences(page), []int64{5, 4}) {
		t.Fatalf("after 3 = %v, %v", sequences(page), err)
	}
	page, err = repository.ListRequestEvents(model.RequestEventQuery{AppID: "app-1", Limit: 2})
	if err != nil || !slices.Equal(sequences(page), []int64{5, 4}) || page.NextBeforeSequence != 4 {
		t.Fatalf("latest = %v, next %d, %v", sequences(page), page.NextBeforeSequence, err)
	}
	if _, err := repository.ListRequestEvents(model.RequestEventQuery{AppID: "app-1", BeforeSequence: 4, AfterSequence: 1, Limit: 2}); !errors.Is(err, model.ErrInvalidRequestEvent) {
		t.Fatalf("before and after = %v", err)
	}
}
