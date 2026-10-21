package microvm

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/configcipher"
	sqlitepersistence "github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqlite"
)

var (
	ErrInvalidSQLitePath        = sqlitepersistence.ErrInvalidPath
	ErrUnsafeSQLiteFile         = sqlitepersistence.ErrUnsafeFile
	ErrSQLiteStoreLocked        = sqlitepersistence.ErrLocked
	ErrIncompatibleSQLiteSchema = sqlitepersistence.ErrIncompatibleSchema
)

// sqliteSchema is the canonical schema for a new state database. Existing
// databases are accepted only when they already match the current schema
// version and shape.
const sqliteSchema = sqlitepersistence.CanonicalSchema
const sqliteCurrentSchemaVersion = sqlitepersistence.CurrentSchemaVersion

// SQLiteStore adapts persistence repositories to Runtime orchestration ports.
type SQLiteStore struct {
	managedApps       *sqlitepersistence.ManagedApps
	database          *sqlitepersistence.Database
	configCipher      *configcipher.Cipher
	requestEvents     *sqlitepersistence.RequestEvents
	computeTenants    *sqlitepersistence.ComputeTenants
	catalog           *sqlitepersistence.Catalog
	workspaces        *sqlitepersistence.Workspaces
	configValues      *sqlitepersistence.ConfigValues
	buildOperations   *sqlitepersistence.BuildOperations
	messageDeliveries *sqlitepersistence.MessageDeliveries
	sessions          *sqlitepersistence.Sessions
	workLeases        *sqlitepersistence.SessionWorkLeases
	sandboxRecords    *sqlitepersistence.SandboxRecords
	hostSlotRecords   *sqlitepersistence.HostSlotRecords
	sessionEventsOnce sync.Once
	sessionEvents     *sqlitepersistence.SessionEvents
}

var _ Store = (*SQLiteStore)(nil)
var _ SandboxStore = (*SQLiteStore)(nil)
var _ HostSlotStore = (*SQLiteStore)(nil)

// OpenSQLiteStore opens or initializes the state database.
type SQLiteStoreOption func(*SQLiteStore) error

// WithConfigEncryptionKey configures authenticated encryption for customer
// secrets. The caller retains ownership of key and may clear it after opening.
func WithConfigEncryptionKey(key []byte) SQLiteStoreOption {
	return func(store *SQLiteStore) error {
		cipher, err := configcipher.New(key)
		if err != nil {
			return errors.Join(ErrInvalidConfigValue, err)
		}
		store.configCipher = cipher
		return nil
	}
}

func OpenSQLiteStore(path string, options ...SQLiteStoreOption) (*SQLiteStore, error) {
	database, err := sqlitepersistence.Open(path)
	if err != nil {
		return nil, err
	}
	db := database.SQL()
	store := &SQLiteStore{
		database:       database,
		managedApps:    sqlitepersistence.NewManagedApps(db),
		requestEvents:  sqlitepersistence.NewRequestEvents(db),
		computeTenants: sqlitepersistence.NewComputeTenants(db),
		catalog:        sqlitepersistence.NewCatalog(db),
		workspaces:     sqlitepersistence.NewWorkspaces(db),
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(store); err != nil {
			return nil, errors.Join(err, database.Close())
		}
	}
	store.configValues = sqlitepersistence.NewConfigValues(db, store.configCipher)
	store.buildOperations = sqlitepersistence.NewBuildOperations(db)
	store.messageDeliveries = sqlitepersistence.NewMessageDeliveries(db, store.configCipher)
	store.sessions = sqlitepersistence.NewSessions(db)
	store.workLeases = sqlitepersistence.NewSessionWorkLeases(db)
	store.sandboxRecords = sqlitepersistence.NewSandboxRecords(db)
	store.hostSlotRecords = sqlitepersistence.NewHostSlotRecords(db)
	return store, nil
}

// Close releases the database connection and lifetime process lock.
func (s *SQLiteStore) Close() error {
	if s == nil {
		return nil
	}
	err := s.database.Close()
	s.database = nil
	return err
}

// Create inserts one validated microVM record.
func (s *SQLiteStore) Create(machine Sandbox) error {
	record, err := encodeSandboxRecord(machine)
	if err != nil {
		return err
	}
	created, err := s.sandboxRecords.Create(sqlitepersistence.SandboxRecord{
		ID: machine.ID, State: machine.State, Record: record,
	})
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, machine.ID)
	}
	return nil
}

func (s *SQLiteStore) CreateOrGet(machine Sandbox) (Sandbox, bool, error) {
	record, err := encodeSandboxRecord(machine)
	if err != nil {
		return Sandbox{}, false, err
	}
	created, err := s.sandboxRecords.Create(sqlitepersistence.SandboxRecord{
		ID: machine.ID, State: machine.State, Record: record,
	})
	if err != nil {
		return Sandbox{}, false, err
	}
	if created {
		return machine, true, nil
	}
	existing, err := s.Get(machine.ID)
	if err != nil {
		return Sandbox{}, false, err
	}
	if !sameSandboxCreateRequest(existing, machine) {
		return existing, false, ErrSandboxConflict
	}
	return existing, false, nil
}

// Get returns one validated persisted microVM record.
func (s *SQLiteStore) Get(id string) (Sandbox, error) {
	record, err := s.sandboxRecords.Get(id)
	if errors.Is(err, sqlitepersistence.ErrSandboxRecordNotFound) {
		return Sandbox{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Sandbox{}, err
	}
	return decodeSQLiteSandbox(record.ID, record.State, record.Record)
}

// List returns all validated persisted records in stable ID order.
func (s *SQLiteStore) List() ([]Sandbox, error) {
	return decodeSQLiteSandboxes(s.sandboxRecords.List())
}

// ListActive returns validated non-terminated records in stable ID order.
func (s *SQLiteStore) ListActive() ([]Sandbox, error) {
	return decodeSQLiteSandboxes(s.sandboxRecords.ListActive())
}

func decodeSQLiteSandboxes(records []sqlitepersistence.SandboxRecord, err error) ([]Sandbox, error) {
	if err != nil {
		return nil, err
	}
	machines := make([]Sandbox, 0, len(records))
	for _, record := range records {
		machine, err := decodeSQLiteSandbox(record.ID, record.State, record.Record)
		if err != nil {
			return nil, err
		}
		machines = append(machines, machine)
	}
	return machines, nil
}

// CheckpointRuntime transactionally records partial host resource ownership
// without changing lifecycle state or guest-visible networking.
func (s *SQLiteStore) CheckpointRuntime(
	id string,
	ownership RuntimeOwnership,
	at time.Time,
) (Sandbox, error) {
	if err := ownership.Validate(); err != nil {
		return Sandbox{}, err
	}
	return s.mutate(id, func(machine Sandbox) (Sandbox, error) {
		if machine.State == StateRunning || machine.State == StateTerminated {
			return Sandbox{}, fmt.Errorf(
				"%w: cannot checkpoint runtime ownership from %s",
				ErrInvalidTransition,
				machine.State,
			)
		}
		if machine.Runtime == ownership {
			return machine, nil
		}
		machine.Runtime = ownership
		machine.UpdatedAt = at
		return machine, nil
	})
}

// Activate atomically records runtime ownership and enters the running state.
func (s *SQLiteStore) Activate(id string, runtime RuntimeInfo, at time.Time) (Sandbox, error) {
	if err := runtime.Ownership.Validate(); err != nil {
		return Sandbox{}, err
	}
	return s.mutate(id, func(machine Sandbox) (Sandbox, error) {
		if machine.State == StateRunning &&
			machine.Network == runtime.Network &&
			machine.Runtime == runtime.Ownership {
			return machine, nil
		}
		if !CanTransition(machine.State, StateRunning) {
			return Sandbox{}, fmt.Errorf(
				"%w: %s -> %s",
				ErrInvalidTransition,
				machine.State,
				StateRunning,
			)
		}
		machine.State = StateRunning
		machine.Network = runtime.Network
		machine.Runtime = runtime.Ownership
		machine.StartupCompletedAt = at
		machine.StartupDuration = at.Sub(machine.CreatedAt)
		machine.UpdatedAt = at
		return machine, nil
	})
}

// Transition atomically advances one microVM lifecycle state.
func (s *SQLiteStore) Transition(id string, next State, at time.Time) (Sandbox, error) {
	return s.mutate(id, func(machine Sandbox) (Sandbox, error) {
		if machine.State == next {
			return machine, nil
		}
		if next == StateRunning {
			return Sandbox{}, fmt.Errorf("%w: use Activate to enter %s", ErrInvalidTransition, StateRunning)
		}
		if !CanTransition(machine.State, next) {
			return Sandbox{}, fmt.Errorf(
				"%w: %s -> %s",
				ErrInvalidTransition,
				machine.State,
				next,
			)
		}
		machine.State = next
		if next == StateTerminated {
			machine.Runtime = RuntimeOwnership{}
		}
		machine.UpdatedAt = at
		return machine, nil
	})
}

// Fail atomically records a safe failure and enters the failed state.
func (s *SQLiteStore) Fail(id string, failure Failure, at time.Time) (Sandbox, error) {
	return s.mutate(id, func(machine Sandbox) (Sandbox, error) {
		if machine.State != StateFailed && !CanTransition(machine.State, StateFailed) {
			return Sandbox{}, fmt.Errorf(
				"%w: %s -> %s",
				ErrInvalidTransition,
				machine.State,
				StateFailed,
			)
		}
		if machine.State == StateFailed && machine.Failure == failure {
			return machine, nil
		}
		machine.State = StateFailed
		machine.Failure = failure
		machine.UpdatedAt = at
		return machine, nil
	})
}

func (s *SQLiteStore) SetIdleDeadline(id string, deadline, at time.Time) (Sandbox, error) {
	return s.mutate(id, func(machine Sandbox) (Sandbox, error) {
		if machine.State != StateRunning || (!deadline.IsZero() && deadline.Before(at)) || at.Before(machine.CreatedAt) {
			return Sandbox{}, ErrInvalidTransition
		}
		machine.IdleDeadline = deadline
		machine.UpdatedAt = maxTime(machine.UpdatedAt, at)
		return machine, nil
	})
}

func (s *SQLiteStore) mutate(
	id string,
	change func(Sandbox) (Sandbox, error),
) (Sandbox, error) {
	record, err := s.sandboxRecords.Mutate(id, func(current sqlitepersistence.SandboxRecord) (sqlitepersistence.SandboxRecord, time.Time, error) {
		machine, err := decodeSQLiteSandbox(current.ID, current.State, current.Record)
		if err != nil {
			return sqlitepersistence.SandboxRecord{}, time.Time{}, err
		}
		next, err := change(machine)
		if err != nil {
			return sqlitepersistence.SandboxRecord{}, time.Time{}, err
		}
		nextRecord, err := encodeSandboxRecord(next)
		if err != nil {
			return sqlitepersistence.SandboxRecord{}, time.Time{}, err
		}
		return sqlitepersistence.SandboxRecord{ID: next.ID, State: next.State, Record: nextRecord}, next.UpdatedAt, nil
	})
	if errors.Is(err, sqlitepersistence.ErrSandboxRecordNotFound) {
		return Sandbox{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Sandbox{}, err
	}
	return decodeSQLiteSandbox(record.ID, record.State, record.Record)
}

func decodeSQLiteSandbox(storedID string, storedState State, record []byte) (Sandbox, error) {
	machine, err := decodeSandboxRecord(record)
	if err != nil {
		return Sandbox{}, err
	}
	if machine.ID != storedID || machine.State != storedState {
		return Sandbox{}, fmt.Errorf(
			"%w: indexed identity does not match record",
			ErrInvalidSandboxRecord,
		)
	}
	return machine, nil
}
