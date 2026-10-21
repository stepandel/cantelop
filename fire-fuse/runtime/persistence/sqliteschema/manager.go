// Package sqliteschema owns Runtime's canonical SQLite schema and validates
// database compatibility before repositories access persisted state.
package sqliteschema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

const CurrentVersion = 28

var ErrIncompatible = errors.New("incompatible SQLite state schema")

var currentTableColumns = map[string][]string{
	"managed_storage":         {"workspace_id", "app_id", "bytes"},
	"managed_apps":            {"app_id", "record"},
	"managed_usage":           {"id", "app_id", "record"},
	"session_streams":         {"workspace_id", "session_id", "stream_id", "sequence", "closed", "view_revision", "view_updated_at", "view_nonce", "view_ciphertext"},
	"session_stream_entries":  {"stream_id", "sequence", "source_sandbox_id", "source_cursor", "message_id", "created_at", "projection", "nonce", "ciphertext", "fingerprint"},
	"message_executions":      {"message_id", "record", "revision", "terminal"},
	"compute_tenants":         {"id", "created_at"},
	"apps":                    {"id", "slug", "created_at", "updated_at", "deleted_at"},
	"app_tenants":             {"app_id", "tenant_id", "created_at"},
	"workspaces":              {"id", "app_id", "slug", "created_at", "updated_at", "archived_at"},
	"sandboxes":               {"id", "state", "record"},
	"host_slots":              {"id", "state", "record"},
	"request_events":          {"sequence", "event_id", "workspace_id", "session_id", "received_at", "status_code", "duration_us", "request_id"},
	"app_builds":              {"app_id", "version", "build_id", "created_at"},
	"app_build_operations":    {"id", "app_id", "status", "size_bytes", "build_id", "build_version", "error_code", "created_at", "updated_at"},
	"app_session_runtimes":    {"app_id", "active_build_id", "profile", "internal_port"},
	"app_release_admission":   {"app_id", "generation", "closed"},
	"sessions":                {"id", "workspace_id", "build_id", "sandbox_id", "created_at", "updated_at", "keep_alive_seconds"},
	"session_keys":            {"workspace_id", "key_hash", "session_id"},
	"session_work_leases":     {"session_id", "lease_id", "expires_at", "created_at", "updated_at"},
	"app_config_values":       {"app_id", "name", "kind", "value_text", "value_nonce", "value_ciphertext", "updated_at"},
	"app_config_revisions":    {"app_id", "revision"},
	"message_deliveries":      {"id", "workspace_id", "logical_session_id", "keep_alive_seconds", "status", "work_state", "payload_nonce", "payload_ciphertext", "session_id", "generation", "error_code", "attempts", "accepted_at", "started_at", "handled_at", "quiescent_at", "updated_at", "request_id", "reply_requested", "reply_nonce", "reply_ciphertext", "admitted_at"},
	"message_delivery_events": {"sequence", "message_id", "workspace_id", "logical_session_id", "session_id", "request_id", "attempt", "generation", "transition", "status", "work_state", "error_code", "occurred_at"},
}

var currentIndexes = map[string]struct{}{
	"sandboxes_managed_app_state":                {},
	"app_tenants_tenant_id":                      {},
	"apps_routable_slug":                         {},
	"request_events_workspace_sequence":          {},
	"workspaces_app_id":                          {},
	"workspaces_routable_slug":                   {},
	"sessions_workspace_id":                      {},
	"app_build_operations_pending_app":           {},
	"session_work_leases_expiry":                 {},
	"message_deliveries_dispatch":                {},
	"message_deliveries_session":                 {},
	"message_deliveries_terminal_updated":        {},
	"message_delivery_events_workspace_sequence": {},
	"message_delivery_events_message_sequence":   {},
	"sandboxes_active":                           {},
}

func Initialize(ctx context.Context, db *sql.DB) error {
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL", "PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("configure SQLite state database: %w", err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SQLite schema initialization: %w", err)
	}
	defer tx.Rollback()

	var objectCount int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&objectCount); err != nil {
		return fmt.Errorf("inspect SQLite schema: %w", err)
	}
	if objectCount == 0 {
		if _, err := tx.ExecContext(ctx, Canonical); err != nil {
			return fmt.Errorf("initialize SQLite schema: %w", err)
		}
		if err := setVersion(ctx, tx, CurrentVersion); err != nil {
			return err
		}
	} else {
		version, err := readVersion(ctx, tx)
		if err != nil {
			return err
		}
		if version == 24 {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE message_deliveries ADD COLUMN admitted_at TEXT;"); err != nil {
				return fmt.Errorf("migrate Message control: %w", err)
			}
			if _, err := tx.ExecContext(ctx, SessionStreams); err != nil {
				return err
			}
			version = 26
		}
		if version == 25 {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE message_deliveries DROP COLUMN priority; ALTER TABLE message_deliveries DROP COLUMN cancellation_requested_at; ALTER TABLE message_deliveries ADD COLUMN admitted_at TEXT;`); err != nil {
				return err
			}
			version = 26
		}
		if version == 26 {
			if _, err := tx.ExecContext(ctx, ActiveSandboxes); err != nil {
				return fmt.Errorf("migrate active Sandbox index: %w", err)
			}
			version = 27
		}
		if version == 27 {
			// Also accept pre-release managed schema 27 without discarding its ledger.
			if _, err := tx.ExecContext(ctx, ActiveSandboxes+ManagedApps); err != nil {
				return fmt.Errorf("migrate managed Apps: %w", err)
			}
			if err := setVersion(ctx, tx, CurrentVersion); err != nil {
				return err
			}
			version = CurrentVersion
		}
		if version != CurrentVersion {
			return fmt.Errorf("%w: database version %d is not the supported schema version %d", ErrIncompatible, version, CurrentVersion)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE message_deliveries SET admitted_at=updated_at WHERE admitted_at IS NULL AND status='handling' AND EXISTS (SELECT 1 FROM message_executions e WHERE e.message_id=message_deliveries.id AND json_type(e.record,'$.receipt')='object')`); err != nil {
		return err
	}
	if err := validate(ctx, tx, currentTableColumns, currentIndexes); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit SQLite schema initialization: %w", err)
	}
	return nil
}

func readVersion(ctx context.Context, tx *sql.Tx) (int, error) {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read SQLite schema version: %w", err)
	}
	if version < 0 {
		return 0, fmt.Errorf("%w: invalid database version %d", ErrIncompatible, version)
	}
	return version, nil
}

func setVersion(ctx context.Context, tx *sql.Tx, version int) error {
	if version < 1 || version > CurrentVersion {
		return fmt.Errorf("%w: invalid database version %d", ErrIncompatible, version)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return fmt.Errorf("write SQLite schema version: %w", err)
	}
	return nil
}

func validate(ctx context.Context, tx *sql.Tx, expectedTables map[string][]string, expectedIndexes map[string]struct{}) error {
	rows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return fmt.Errorf("inspect SQLite tables: %w", err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return fmt.Errorf("inspect SQLite table: %w", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("inspect SQLite tables: %w", err)
	}
	if len(tables) != len(expectedTables) {
		return fmt.Errorf("%w: unexpected table set", ErrIncompatible)
	}
	for _, table := range tables {
		expected, ok := expectedTables[table]
		if !ok {
			return fmt.Errorf("%w: unexpected table %q", ErrIncompatible, table)
		}
		columnRows, err := tx.QueryContext(ctx, "SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
		if err != nil {
			return fmt.Errorf("inspect SQLite table %q: %w", table, err)
		}
		var columns []string
		for columnRows.Next() {
			var column string
			if err := columnRows.Scan(&column); err != nil {
				columnRows.Close()
				return fmt.Errorf("inspect SQLite table %q: %w", table, err)
			}
			columns = append(columns, column)
		}
		if err := columnRows.Close(); err != nil {
			return fmt.Errorf("inspect SQLite table %q: %w", table, err)
		}
		if !slices.Equal(columns, expected) {
			return fmt.Errorf("%w: unexpected columns for table %q", ErrIncompatible, table)
		}
	}

	indexRows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type = 'index' AND sql IS NOT NULL ORDER BY name")
	if err != nil {
		return fmt.Errorf("inspect SQLite indexes: %w", err)
	}
	var indexes []string
	for indexRows.Next() {
		var index string
		if err := indexRows.Scan(&index); err != nil {
			indexRows.Close()
			return fmt.Errorf("inspect SQLite index: %w", err)
		}
		indexes = append(indexes, index)
	}
	if err := indexRows.Close(); err != nil {
		return fmt.Errorf("inspect SQLite indexes: %w", err)
	}
	if len(indexes) != len(expectedIndexes) {
		return fmt.Errorf("%w: unexpected index set", ErrIncompatible)
	}
	for _, index := range indexes {
		if _, ok := expectedIndexes[index]; !ok {
			return fmt.Errorf("%w: unexpected index %q", ErrIncompatible, index)
		}
	}
	return nil
}
