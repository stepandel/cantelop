package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
)

var ErrHostSlotRecordNotFound = errors.New("host slot record not found")

// HostSlotRecord is an opaque persisted prepared-host resource record.
type HostSlotRecord = OpaqueRecord[string]

var hostSlotRecordTable = opaqueRecordTable[string]{
	table: "host_slots", noun: "host slot record", notFound: ErrHostSlotRecordNotFound,
}

type HostSlotRecords struct{ db *sql.DB }

func NewHostSlotRecords(db *sql.DB) *HostSlotRecords { return &HostSlotRecords{db: db} }

func (r *HostSlotRecords) Create(record HostSlotRecord) (bool, error) {
	return hostSlotRecordTable.create(r.db, record)
}

func (r *HostSlotRecords) List() ([]HostSlotRecord, error) {
	return hostSlotRecordTable.list(r.db, "")
}

func (r *HostSlotRecords) Mutate(id string, change func(HostSlotRecord) (HostSlotRecord, error)) (HostSlotRecord, error) {
	return hostSlotRecordTable.mutate(r.db, id, change)
}

func (r *HostSlotRecords) Delete(id string, allow func(HostSlotRecord) error) error {
	if allow == nil {
		return errors.New("host slot record deletion guard is required")
	}
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin host slot record deletion: %w", err)
	}
	defer tx.Rollback()
	record, err := hostSlotRecordTable.get(tx, id)
	if err != nil {
		return err
	}
	if err := allow(record); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM host_slots WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete host slot record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit host slot record deletion: %w", err)
	}
	return nil
}

// Claim atomically transfers an opaque host-slot record into an existing
// Sandbox record. The runtime callback validates ownership and produces the
// replacement Sandbox document; persistence only owns the transaction.
func (r *HostSlotRecords) Claim(
	sandboxID, slotID string,
	change func(SandboxRecord, HostSlotRecord) (SandboxRecord, error),
) (SandboxRecord, error) {
	if change == nil {
		return SandboxRecord{}, errors.New("host slot claim mutation is required")
	}
	tx, err := r.db.Begin()
	if err != nil {
		return SandboxRecord{}, fmt.Errorf("begin host slot claim: %w", err)
	}
	defer tx.Rollback()
	sandbox, err := sandboxRecordTable.get(tx, sandboxID)
	if err != nil {
		return SandboxRecord{}, err
	}
	slot, err := hostSlotRecordTable.get(tx, slotID)
	if err != nil {
		return SandboxRecord{}, err
	}
	next, err := change(sandbox, slot)
	if err != nil {
		return SandboxRecord{}, err
	}
	if next.ID != sandbox.ID {
		return SandboxRecord{}, errors.New("invalid host slot claim mutation")
	}
	if err := sandboxRecordTable.write(tx, next, "claim"); err != nil {
		return SandboxRecord{}, err
	}
	result, err := tx.Exec("DELETE FROM host_slots WHERE id = ?", slotID)
	if err != nil {
		return SandboxRecord{}, fmt.Errorf("delete claimed host slot record: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return SandboxRecord{}, fmt.Errorf("inspect claimed host slot deletion: %w", err)
	} else if rows != 1 {
		return SandboxRecord{}, fmt.Errorf("host slot record disappeared during claim: %s", slotID)
	}
	if err := tx.Commit(); err != nil {
		return SandboxRecord{}, fmt.Errorf("commit host slot claim: %w", err)
	}
	return next, nil
}
