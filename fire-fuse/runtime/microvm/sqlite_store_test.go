package microvm

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqlitefile"
)

func TestOpenSQLiteStoreInitializesSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, table := range []string{
		"compute_tenants", "apps", "app_tenants",
		"app_builds", "app_session_runtimes", "workspaces", "sandboxes",
		"sessions", "session_keys", "host_slots", "request_events", "app_config_values",
	} {
		var tableSQL string
		if err := store.database.SQL().QueryRow(
			"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", table,
		).Scan(&tableSQL); err != nil {
			t.Fatalf("read %s schema: %v", table, err)
		}
		if tableSQL == "" {
			t.Errorf("%s schema is empty", table)
		}
	}
	for _, removed := range []string{
		"users", "user_quotas", "app_owners",
		"images", "image_builds", "app_deployments", "environments", "environment_config_values",
	} {
		var count int
		if err := store.database.SQL().QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", removed).Scan(&count); err != nil || count != 0 {
			t.Fatalf("removed table %s count = %d, %v", removed, count, err)
		}
	}
	var foreignKeys int
	if err := store.database.SQL().QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign key enforcement = %d, %v; want 1", foreignKeys, err)
	}
	var userVersion int
	if err := store.database.SQL().QueryRow("PRAGMA user_version").Scan(&userVersion); err != nil || userVersion != sqliteCurrentSchemaVersion {
		t.Fatalf("SQLite user_version = %d, %v; want %d", userVersion, err, sqliteCurrentSchemaVersion)
	}
	var journalMode string
	if err := store.database.SQL().QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil || journalMode != "wal" {
		t.Fatalf("journal mode = %q, %v; want wal", journalMode, err)
	}
	var synchronous int
	if err := store.database.SQL().QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatalf("synchronous mode = %d, %v; want FULL (2)", synchronous, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat state database: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("state database permissions = %o, want no group or other access", info.Mode().Perm())
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecarInfo, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatalf("stat SQLite %s sidecar: %v", suffix, err)
		}
		if !sidecarInfo.Mode().IsRegular() || sidecarInfo.Mode().Perm()&0o077 != 0 {
			t.Errorf(
				"SQLite %s sidecar mode = %v, want private regular file",
				suffix,
				sidecarInfo.Mode(),
			)
		}
	}
}

func TestOpenSQLiteStoreRejectsPreProjectionSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(sqliteSchema + "\nPRAGMA user_version = 3;"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenSQLiteStore(path)
	if store != nil {
		_ = store.Close()
		t.Fatal("OpenSQLiteStore() accepted a pre-projection schema version")
	}
	if !errors.Is(err, ErrIncompatibleSQLiteSchema) {
		t.Fatalf("OpenSQLiteStore() error = %v, want ErrIncompatibleSQLiteSchema", err)
	}
}

func TestOpenSQLiteStoreRejectsUnversionedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenSQLiteStore(path)
	if store != nil {
		_ = store.Close()
		t.Fatal("OpenSQLiteStore() returned a store for an unversioned database")
	}
	if !errors.Is(err, ErrIncompatibleSQLiteSchema) {
		t.Fatalf("OpenSQLiteStore() error = %v, want ErrIncompatibleSQLiteSchema", err)
	}
}

func TestOpenSQLiteStoreRejectsNewerSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenSQLiteStore(path)
	if store != nil {
		_ = store.Close()
		t.Fatal("OpenSQLiteStore() returned a store for a newer schema")
	}
	if !errors.Is(err, ErrIncompatibleSQLiteSchema) {
		t.Fatalf("OpenSQLiteStore() error = %v, want ErrIncompatibleSQLiteSchema", err)
	}
}

func TestOpenSQLiteStoreRejectsPartialDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE legacy (id TEXT PRIMARY KEY) STRICT"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenSQLiteStore(path)
	if store != nil {
		_ = store.Close()
		t.Fatal("OpenSQLiteStore() returned a store for a partial database")
	}
	if !errors.Is(err, ErrIncompatibleSQLiteSchema) {
		t.Fatalf("OpenSQLiteStore() error = %v, want ErrIncompatibleSQLiteSchema", err)
	}
}

func TestOpenSQLiteStoreReopensCurrentSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("first OpenSQLiteStore() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	second, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("second OpenSQLiteStore() error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestOpenSQLiteStoreHoldsLifetimeProcessLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("first OpenSQLiteStore() error = %v", err)
	}

	second, err := OpenSQLiteStore(path)
	if second != nil {
		_ = second.Close()
		t.Fatal("second OpenSQLiteStore() returned a store while first owns the lock")
	}
	if !errors.Is(err, ErrSQLiteStoreLocked) {
		t.Fatalf("second OpenSQLiteStore() error = %v, want ErrSQLiteStoreLocked", err)
	}

	lockInfo, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatalf("stat state lock: %v", err)
	}
	if lockInfo.Mode().Perm()&0o077 != 0 {
		t.Errorf("state lock permissions = %o, want no group or other access", lockInfo.Mode().Perm())
	}

	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	third, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() after release error = %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatalf("third Close() error = %v", err)
	}
}

func TestOpenSQLiteStoreRejectsInvalidPath(t *testing.T) {
	for _, path := range []string{"state.db", "/", t.TempDir() + "/../state.db"} {
		if _, err := OpenSQLiteStore(path); !errors.Is(err, ErrInvalidSQLitePath) {
			t.Errorf("OpenSQLiteStore(%q) error = %v, want ErrInvalidSQLitePath", path, err)
		}
	}
}

func TestOpenSQLiteStoreRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "state.db")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	store, err := OpenSQLiteStore(path)
	if store != nil {
		_ = store.Close()
		t.Fatal("OpenSQLiteStore() returned a store for a symlink")
	}
	if !errors.Is(err, ErrUnsafeSQLiteFile) {
		t.Fatalf("OpenSQLiteStore() error = %v, want ErrUnsafeSQLiteFile", err)
	}
}

func TestPrepareSQLiteStateFileSecuresPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}

	if err := sqlitefile.Prepare(path); err != nil {
		t.Fatalf("prepareSQLiteStateFile() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state database mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestValidateSQLiteStateFileRejectsNonRegularFile(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	if err := sqlitefile.Validate(reader, uint32(os.Geteuid())); !errors.Is(err, ErrUnsafeSQLiteFile) {
		t.Fatalf("validateSQLiteStateFile() error = %v, want ErrUnsafeSQLiteFile", err)
	}
}

func TestValidateSQLiteStateFileRejectsWrongOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	wrongUID := uint32(os.Geteuid()) + 1
	if err := sqlitefile.Validate(file, wrongUID); !errors.Is(err, ErrUnsafeSQLiteFile) {
		t.Fatalf("validateSQLiteStateFile() error = %v, want ErrUnsafeSQLiteFile", err)
	}
}
