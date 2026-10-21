package devruntime

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// A local Workspace credential only grants access to its own SQLite file.
// ATTACH and VACUUM (which attaches its target internally) can open or create
// other files with the CLI user's rights, so stream connections get no
// attached-database slots and SQLite itself refuses them on every request path.
func localDatabaseConn(ctx context.Context, db *sql.DB) (*sql.Conn, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_ATTACHED, 0); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// These deprecated pragmas set process-wide directories for every connection.
// A plain substring match cannot be bypassed by quoting or comments.
func rejectLocalDatabaseFileSQL(text string) error {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "temp_store_directory") || strings.Contains(lower, "data_store_directory") {
		return errors.New("local database SQL cannot access other files")
	}
	return nil
}
