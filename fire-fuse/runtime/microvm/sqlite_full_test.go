package microvm

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestServiceClassifiesSQLiteFullWithoutPartialRecord(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedTestApp(t, store, "app-test", "wsp_project_after_full")

	var currentPages int
	if err := store.database.SQL().QueryRow("PRAGMA page_count").Scan(&currentPages); err != nil {
		t.Fatalf("read SQLite page count: %v", err)
	}
	var maximumPages int
	if err := store.database.SQL().QueryRow(
		fmt.Sprintf("PRAGMA max_page_count = %d", currentPages),
	).Scan(&maximumPages); err != nil {
		t.Fatalf("limit SQLite page count: %v", err)
	}
	if maximumPages != currentPages {
		t.Fatalf("SQLite maximum page count = %d, want %d", maximumPages, currentPages)
	}

	// Filling the existing pages forces the same SQLITE_FULL result SQLite
	// returns when the filesystem cannot extend the database, without requiring
	// a privileged or host-specific tiny filesystem.
	at := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	var fullErr error
	for index := 0; index < 10_000; index++ {
		fullErr = store.Create(Sandbox{
			ID:          fmt.Sprintf("vm-fill-%04d", index),
			AppID:       "app-test",
			WorkspaceID: fmt.Sprintf("wsp_fill_%04d", index),
			Profile:     "test",
			BuildID:     testImageID,
			VCPUs:       1,
			MemoryMiB:   128,
			// Terminated records fill the database without reserving App
			// capacity, keeping this test focused on SQLITE_FULL handling.
			State:     StateTerminated,
			CreatedAt: at,
			UpdatedAt: at,
		})
		if fullErr != nil {
			break
		}
	}
	assertSQLiteFull(t, fullErr)

	service := newService(
		store,
		nil,
		func() time.Time { return at.Add(time.Second) },
	)
	_, err = service.Create("vm-after-full", CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project_after_full"})
	if !errors.Is(err, ErrStateStore) {
		t.Fatalf("Create() error = %v, want ErrStateStore", err)
	}
	assertSQLiteFull(t, err)
	if _, err := store.Get("vm-after-full"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(vm-after-full) error = %v, want no partial lifecycle record", err)
	}
}

func assertSQLiteFull(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("operation succeeded after SQLite reached its maximum page count")
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("error type = %T, want *sqlite.Error: %v", err, err)
	}
	if sqliteErr.Code() != sqlite3.SQLITE_FULL {
		t.Fatalf("SQLite error code = %d, want SQLITE_FULL (%d)", sqliteErr.Code(), sqlite3.SQLITE_FULL)
	}
}
