package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"

	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqlitefile"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqliteschema"
	_ "modernc.org/sqlite"
)

var (
	ErrInvalidPath        = errors.New("invalid SQLite state path")
	ErrUnsafeFile         = sqlitefile.ErrUnsafe
	ErrLocked             = sqlitefile.ErrLocked
	ErrIncompatibleSchema = sqliteschema.ErrIncompatible
)

const CanonicalSchema = sqliteschema.Canonical
const CurrentSchemaVersion = sqliteschema.CurrentVersion

// Database owns the connection pool and process-lifetime state-file lock.
// Repository composition may borrow SQL, but closing remains centralized here.
type Database struct {
	db   *sql.DB
	lock *sqlitefile.Lock
}

func Open(path string) (*Database, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: must be a clean absolute file path", ErrInvalidPath)
	}
	lock, err := sqlitefile.AcquireLock(path + ".lock")
	if err != nil {
		return nil, err
	}
	if err := sqlitefile.Prepare(path); err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	dsn := (&url.URL{Scheme: "file", Path: path}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open SQLite state database: %w", err), lock.Close())
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := sqliteschema.Initialize(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, errors.Join(err, lock.Close())
	}
	return &Database{db: db, lock: lock}, nil
}

// SQL exposes the initialized pool only for repository composition and
// persistence-level verification. Callers must not close it directly.
func (d *Database) SQL() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}

func (d *Database) Close() error {
	if d == nil {
		return nil
	}
	var closeErrors []error
	if d.db != nil {
		closeErrors = append(closeErrors, d.db.Close())
		d.db = nil
	}
	if d.lock != nil {
		closeErrors = append(closeErrors, d.lock.Close())
		d.lock = nil
	}
	return errors.Join(closeErrors...)
}
