package sqlite

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
)

// OpaqueRecord is a persisted runtime document indexed by ID and lifecycle
// state. Its JSON payload is deliberately opaque to the SQLite adapter;
// orchestration owns decoding and lifecycle validation.
type OpaqueRecord[S ~string] struct {
	ID     string
	State  S
	Record []byte
}

// opaqueRecordTable owns the SQL shared by every (id, state, record) table.
// Table names are compile-time constants, never caller input.
type opaqueRecordTable[S ~string] struct {
	table    string
	noun     string
	notFound error
}

func (t opaqueRecordTable[S]) validate(record OpaqueRecord[S]) error {
	if record.ID == "" || record.State == "" || len(record.Record) == 0 {
		return fmt.Errorf("invalid %s", t.noun)
	}
	return nil
}

func (t opaqueRecordTable[S]) scan(row scanner, id string) (OpaqueRecord[S], error) {
	var record OpaqueRecord[S]
	err := row.Scan(&record.ID, &record.State, &record.Record)
	if errors.Is(err, sql.ErrNoRows) {
		return OpaqueRecord[S]{}, fmt.Errorf("%w: %s", t.notFound, id)
	}
	if err != nil {
		return OpaqueRecord[S]{}, fmt.Errorf("read %s: %w", t.noun, err)
	}
	return record, t.validate(record)
}

func (t opaqueRecordTable[S]) create(db *sql.DB, record OpaqueRecord[S]) (bool, error) {
	if err := t.validate(record); err != nil {
		return false, err
	}
	result, err := db.Exec(
		"INSERT INTO "+t.table+" (id, state, record) VALUES (?, ?, ?) ON CONFLICT(id) DO NOTHING",
		record.ID, record.State, record.Record,
	)
	if err != nil {
		return false, fmt.Errorf("insert %s: %w", t.noun, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect %s insert: %w", t.noun, err)
	}
	return rows == 1, nil
}

func (t opaqueRecordTable[S]) get(q rowQuerier, id string) (OpaqueRecord[S], error) {
	return t.scan(q.QueryRow("SELECT id, state, record FROM "+t.table+" WHERE id = ?", id), id)
}

// list returns records in stable ID order. where is an optional constant
// predicate appended to the query.
func (t opaqueRecordTable[S]) list(q rowsQuerier, where string) ([]OpaqueRecord[S], error) {
	query := "SELECT id, state, record FROM " + t.table
	if where != "" {
		query += " WHERE " + where
	}
	return queryAll(q, t.noun+"s", func(row scanner) (OpaqueRecord[S], error) {
		return t.scan(row, "")
	}, query+" ORDER BY id")
}

// write replaces one existing record inside a caller-owned transaction.
func (t opaqueRecordTable[S]) write(tx *sql.Tx, record OpaqueRecord[S], action string) error {
	if err := t.validate(record); err != nil {
		return err
	}
	result, err := tx.Exec(
		"UPDATE "+t.table+" SET state = ?, record = ? WHERE id = ?",
		record.State, record.Record, record.ID,
	)
	if err != nil {
		return fmt.Errorf("write %s %s: %w", t.noun, action, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect %s %s: %w", t.noun, action, err)
	}
	if rows != 1 {
		return fmt.Errorf("%s disappeared during %s: %s", t.noun, action, record.ID)
	}
	return nil
}

// mutate serializes a domain-owned read/validate/write transition. A change
// that returns the current state and document is a no-op and writes nothing.
func (t opaqueRecordTable[S]) mutate(
	db *sql.DB,
	id string,
	change func(OpaqueRecord[S]) (OpaqueRecord[S], error),
) (OpaqueRecord[S], error) {
	if change == nil {
		return OpaqueRecord[S]{}, fmt.Errorf("%s mutation is required", t.noun)
	}
	tx, err := db.Begin()
	if err != nil {
		return OpaqueRecord[S]{}, fmt.Errorf("begin %s mutation: %w", t.noun, err)
	}
	defer tx.Rollback()
	current, err := t.get(tx, id)
	if err != nil {
		return OpaqueRecord[S]{}, err
	}
	next, err := change(current)
	if err != nil {
		return OpaqueRecord[S]{}, err
	}
	if err := t.validate(next); err != nil {
		return OpaqueRecord[S]{}, err
	}
	if next.ID != current.ID {
		return OpaqueRecord[S]{}, fmt.Errorf("invalid %s mutation", t.noun)
	}
	if next.State == current.State && bytes.Equal(next.Record, current.Record) {
		return current, nil
	}
	if err := t.write(tx, next, "mutation"); err != nil {
		return OpaqueRecord[S]{}, err
	}
	if err := tx.Commit(); err != nil {
		return OpaqueRecord[S]{}, fmt.Errorf("commit %s mutation: %w", t.noun, err)
	}
	return next, nil
}
