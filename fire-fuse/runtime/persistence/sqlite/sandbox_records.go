package sqlite

import (
	"database/sql"
	"errors"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

var ErrSandboxRecordNotFound = errors.New("sandbox record not found")

// SandboxRecord is the persistence representation of one private runtime
// binding.
type SandboxRecord = OpaqueRecord[model.State]

type SandboxRecordMutation func(SandboxRecord) (SandboxRecord, time.Time, error)

var sandboxRecordTable = opaqueRecordTable[model.State]{
	table: "sandboxes", noun: "sandbox record", notFound: ErrSandboxRecordNotFound,
}

type SandboxRecords struct{ db *sql.DB }

func NewSandboxRecords(db *sql.DB) *SandboxRecords { return &SandboxRecords{db: db} }

func (r *SandboxRecords) Create(record SandboxRecord) (bool, error) {
	return sandboxRecordTable.create(r.db, record)
}

func (r *SandboxRecords) Get(id string) (SandboxRecord, error) {
	return sandboxRecordTable.get(r.db, id)
}

func (r *SandboxRecords) List() ([]SandboxRecord, error) {
	return sandboxRecordTable.list(r.db, "")
}

// ListActive returns every record that has not reached the terminal state.
// Terminated records accumulate with Session reactivation, so callers that
// only reason about live or recoverable Sandboxes should not decode them.
func (r *SandboxRecords) ListActive() ([]SandboxRecord, error) {
	return sandboxRecordTable.list(r.db, "state <> '"+string(model.StateTerminated)+"'")
}

// Mutate serializes a domain-owned read/validate/write transition. Every
// write must carry the domain update time.
func (r *SandboxRecords) Mutate(id string, change SandboxRecordMutation) (SandboxRecord, error) {
	if change == nil {
		return SandboxRecord{}, errors.New("sandbox record mutation is required")
	}
	return sandboxRecordTable.mutate(r.db, id, func(current SandboxRecord) (SandboxRecord, error) {
		next, updatedAt, err := change(current)
		if err != nil {
			return SandboxRecord{}, err
		}
		if updatedAt.IsZero() {
			return SandboxRecord{}, errors.New("invalid sandbox record mutation")
		}
		return next, nil
	})
}
