package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

// RuntimeBinding is the opaque private implementation record associated with
// a public Session. The adapter persists it atomically without
// interpreting its encoded payload.
type RuntimeBinding struct {
	ID     string
	State  model.State
	Record []byte
}

type Sessions struct {
	db                  *sql.DB
	maxAppRows          int64
	maxHostRows         int64
	maxAppSandboxRows   int64
	maxHostSandboxRows  int64
	maxAppSandboxBytes  int64
	maxHostSandboxBytes int64
}

func NewSessions(db *sql.DB) *Sessions {
	return &Sessions{db: db, maxAppRows: 2_000, maxHostRows: 20_000,
		maxAppSandboxRows: 10_000, maxHostSandboxRows: 100_000,
		maxAppSandboxBytes: 64 << 20, maxHostSandboxBytes: 512 << 20}
}

func (r *Sessions) Create(sessionRecord model.Session, binding RuntimeBinding, keyHash string) (model.Session, bool, error) {
	if err := sessionRecord.Validate(); err != nil {
		return model.Session{}, false, err
	}
	if binding.ID == "" || len(binding.Record) == 0 {
		return model.Session{}, false, model.ErrInvalidSession
	}
	tx, err := r.db.Begin()
	if err != nil {
		return model.Session{}, false, fmt.Errorf("begin Session creation: %w", err)
	}
	defer tx.Rollback()
	existing, err := scanSession(tx.QueryRow(`SELECT id, workspace_id, build_id, keep_alive_seconds, created_at, updated_at
FROM sessions WHERE id = ?`, sessionRecord.ID), sessionRecord.ID)
	if err == nil {
		if existing.WorkspaceID != sessionRecord.WorkspaceID ||
			(keyHash == "" && existing.KeepAliveSeconds != sessionRecord.KeepAliveSeconds) {
			return existing, false, model.ErrSessionConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, model.ErrSessionNotFound) {
		return model.Session{}, false, err
	}
	if keyHash != "" {
		existing, err = scanSession(tx.QueryRow(`SELECT s.id, s.workspace_id, s.build_id, s.keep_alive_seconds, s.created_at, s.updated_at
FROM session_keys k JOIN sessions s ON s.id = k.session_id
WHERE k.workspace_id = ? AND k.key_hash = ?`, sessionRecord.WorkspaceID, keyHash), "")
		if err == nil {
			return existing, false, nil
		}
		if !errors.Is(err, model.ErrSessionNotFound) {
			return model.Session{}, false, err
		}
	}
	var appRows, hostRows int64
	if err := tx.QueryRow(`SELECT count(*) FROM sessions s JOIN workspaces w ON w.id = s.workspace_id
WHERE w.app_id = (SELECT app_id FROM workspaces WHERE id = ?)`, sessionRecord.WorkspaceID).Scan(&appRows); err != nil {
		return model.Session{}, false, fmt.Errorf("inspect App Session storage: %w", err)
	}
	if err := tx.QueryRow(`SELECT count(*) FROM sessions`).Scan(&hostRows); err != nil {
		return model.Session{}, false, fmt.Errorf("inspect host Session storage: %w", err)
	}
	if appRows >= r.maxAppRows || hostRows >= r.maxHostRows {
		return model.Session{}, false, model.ErrSessionStorageQuota
	}
	if err := r.checkSandboxStorageQuota(tx, sessionRecord.WorkspaceID, sessionRecord.ID, len(binding.Record)); err != nil {
		return model.Session{}, false, err
	}
	var admissionClosed int
	if err := tx.QueryRow(`SELECT gate.closed FROM app_release_admission gate
JOIN workspaces workspace ON workspace.app_id = gate.app_id
WHERE workspace.id = ? AND workspace.archived_at IS NULL`, sessionRecord.WorkspaceID).Scan(&admissionClosed); err != nil || admissionClosed != 0 {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return model.Session{}, false, fmt.Errorf("read App release admission: %w", err)
		}
		return model.Session{}, false, model.ErrReleaseAdmissionClosed
	}
	if _, err := tx.Exec("INSERT INTO sandboxes (id, state, record) VALUES (?, ?, ?)", binding.ID, binding.State, binding.Record); err != nil {
		return model.Session{}, false, fmt.Errorf("insert private binding for Session: %w", err)
	}
	_, err = tx.Exec(`INSERT INTO sessions
		(id, workspace_id, build_id, keep_alive_seconds, sandbox_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, sessionRecord.ID, sessionRecord.WorkspaceID, sessionRecord.BuildID, sessionRecord.KeepAliveSeconds, binding.ID,
		formatTime(sessionRecord.CreatedAt), formatTime(sessionRecord.UpdatedAt))
	if err != nil {
		return model.Session{}, false, fmt.Errorf("insert Session: %w", err)
	}
	if keyHash != "" {
		if _, err := tx.Exec(`INSERT INTO session_keys (workspace_id, key_hash, session_id) VALUES (?, ?, ?)`,
			sessionRecord.WorkspaceID, keyHash, sessionRecord.ID); err != nil {
			return model.Session{}, false, fmt.Errorf("bind Session key: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Session{}, false, fmt.Errorf("commit Session creation: %w", err)
	}
	return sessionRecord, true, nil
}

func (r *Sessions) GetByKey(workspaceID, keyHash string) (model.Session, error) {
	return scanSession(r.db.QueryRow(`SELECT s.id, s.workspace_id, s.build_id, s.keep_alive_seconds, s.created_at, s.updated_at
FROM session_keys k JOIN sessions s ON s.id = k.session_id
WHERE k.workspace_id = ? AND k.key_hash = ?`, workspaceID, keyHash), "")
}

func (r *Sessions) Get(id string) (model.Session, error) {
	return scanSession(r.db.QueryRow(`SELECT id, workspace_id, build_id, keep_alive_seconds, created_at, updated_at
FROM sessions WHERE id = ?`, id), id)
}

func (r *Sessions) List(workspaceID string) ([]model.Session, error) {
	if model.ValidateWorkspaceID(workspaceID) != nil {
		return nil, fmt.Errorf("%w: %s", model.ErrWorkspaceNotFound, workspaceID)
	}
	var exists int
	if err := r.db.QueryRow("SELECT count(*) FROM workspaces WHERE id = ?", workspaceID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("look up Session Workspace: %w", err)
	}
	if exists == 0 {
		return nil, fmt.Errorf("%w: %s", model.ErrWorkspaceNotFound, workspaceID)
	}
	return queryAll(r.db, "Sessions", func(row scanner) (model.Session, error) {
		return scanSession(row, "")
	}, `SELECT id, workspace_id, build_id, keep_alive_seconds, created_at, updated_at
FROM sessions WHERE workspace_id = ? ORDER BY id`, workspaceID)
}

// UpdateKeepAlive changes only the idle policy of an existing Session. The
// Workspace predicate prevents a caller from rebinding Session identity.
func (r *Sessions) UpdateKeepAlive(
	id, workspaceID string,
	keepAliveSeconds int,
	at time.Time,
) (model.Session, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return model.Session{}, fmt.Errorf("begin Session keepalive update: %w", err)
	}
	defer tx.Rollback()
	sessionRecord, err := scanSession(tx.QueryRow(`SELECT id, workspace_id, build_id, keep_alive_seconds, created_at, updated_at
FROM sessions WHERE id = ?`, id), id)
	if err != nil {
		return model.Session{}, err
	}
	if sessionRecord.WorkspaceID != workspaceID {
		return sessionRecord, model.ErrSessionConflict
	}
	updated := sessionRecord
	updated.KeepAliveSeconds = keepAliveSeconds
	if at.After(updated.UpdatedAt) {
		updated.UpdatedAt = at
	}
	if err := updated.Validate(); err != nil {
		return model.Session{}, err
	}
	result, err := tx.Exec(`UPDATE sessions SET keep_alive_seconds = ?, updated_at = ?
WHERE id = ? AND workspace_id = ?`, keepAliveSeconds, formatTime(updated.UpdatedAt), id, workspaceID)
	if err != nil {
		return model.Session{}, fmt.Errorf("update Session keepalive: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return model.Session{}, fmt.Errorf("inspect Session keepalive update: %w", err)
	}
	if rows != 1 {
		return model.Session{}, fmt.Errorf("Session disappeared during keepalive update: %s", id)
	}
	if err := tx.Commit(); err != nil {
		return model.Session{}, fmt.Errorf("commit Session keepalive update: %w", err)
	}
	return updated, nil
}

func (r *Sessions) GetBinding(sessionID string) (RuntimeBinding, error) {
	var binding RuntimeBinding
	err := r.db.QueryRow(`SELECT s.id, s.state, s.record
        FROM sessions r JOIN sandboxes s ON s.id = r.sandbox_id
        WHERE r.id = ?`, sessionID).Scan(&binding.ID, &binding.State, &binding.Record)
	if errors.Is(err, sql.ErrNoRows) {
		return RuntimeBinding{}, fmt.Errorf("%w: %s", model.ErrSessionNotFound, sessionID)
	}
	if err != nil {
		return RuntimeBinding{}, fmt.Errorf("read private binding for Session: %w", err)
	}
	return binding, nil
}

// ReplaceBinding atomically points an active logical Session at a newly
// allocated Sandbox. BuildID describes the current Sandbox; it is not a pin.
func (r *Sessions) ReplaceBinding(
	sessionID, buildID string,
	binding RuntimeBinding,
	at time.Time,
) (model.Session, error) {
	if binding.ID == "" || len(binding.Record) == 0 || at.IsZero() {
		return model.Session{}, model.ErrInvalidSession
	}
	tx, err := r.db.Begin()
	if err != nil {
		return model.Session{}, fmt.Errorf("begin Session Sandbox replacement: %w", err)
	}
	defer tx.Rollback()
	sessionRecord, err := scanSession(tx.QueryRow(`SELECT id, workspace_id, build_id, keep_alive_seconds, created_at, updated_at
FROM sessions WHERE id = ?`, sessionID), sessionID)
	if err != nil {
		return model.Session{}, err
	}
	if err := r.checkSandboxStorageQuota(tx, sessionRecord.WorkspaceID, sessionRecord.ID, len(binding.Record)); err != nil {
		return model.Session{}, err
	}
	if _, err := tx.Exec("INSERT INTO sandboxes (id, state, record) VALUES (?, ?, ?)", binding.ID, binding.State, binding.Record); err != nil {
		return model.Session{}, fmt.Errorf("insert replacement Sandbox for Session: %w", err)
	}
	result, err := tx.Exec(`UPDATE sessions SET build_id = ?, sandbox_id = ?, updated_at = ? WHERE id = ?`,
		buildID, binding.ID, formatTime(at), sessionID)
	if err != nil {
		return model.Session{}, fmt.Errorf("replace Session Sandbox: %w", err)
	}
	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return model.Session{}, fmt.Errorf("inspect Session Sandbox replacement: %w", rowsErr)
	}
	if rows != 1 {
		return model.Session{}, fmt.Errorf("Session disappeared during Sandbox replacement: %s", sessionID)
	}
	if err := tx.Commit(); err != nil {
		return model.Session{}, fmt.Errorf("commit Session Sandbox replacement: %w", err)
	}
	sessionRecord.BuildID = buildID
	sessionRecord.UpdatedAt = at
	return sessionRecord, sessionRecord.Validate()
}

// checkSandboxStorageQuota bounds the Sandbox records currently bound to
// Sessions. The binding of sessionID is excluded because the caller replaces
// it; unbound historical records do not consume admission capacity.
func (r *Sessions) checkSandboxStorageQuota(tx *sql.Tx, workspaceID, sessionID string, newBytes int) error {
	var appRows, hostRows, appBytes, hostBytes int64
	if err := tx.QueryRow(`SELECT count(*), coalesce(sum(length(b.record)), 0)
FROM workspaces w JOIN sessions s ON s.workspace_id = w.id JOIN sandboxes b ON b.id = s.sandbox_id
WHERE w.app_id = (SELECT app_id FROM workspaces WHERE id = ?) AND s.id <> ?`, workspaceID, sessionID).Scan(&appRows, &appBytes); err != nil {
		return fmt.Errorf("inspect App Sandbox storage: %w", err)
	}
	if err := tx.QueryRow(`SELECT count(*), coalesce(sum(length(b.record)), 0)
FROM sessions s JOIN sandboxes b ON b.id = s.sandbox_id WHERE s.id <> ?`, sessionID).Scan(&hostRows, &hostBytes); err != nil {
		return fmt.Errorf("inspect host Sandbox storage: %w", err)
	}
	if appRows >= r.maxAppSandboxRows || hostRows >= r.maxHostSandboxRows ||
		appBytes+int64(newBytes) > r.maxAppSandboxBytes || hostBytes+int64(newBytes) > r.maxHostSandboxBytes {
		return model.ErrSessionStorageQuota
	}
	return nil
}

func scanSession(row scanner, id string) (model.Session, error) {
	var sessionRecord model.Session
	var createdAt, updatedAt string
	err := row.Scan(&sessionRecord.ID, &sessionRecord.WorkspaceID, &sessionRecord.BuildID, &sessionRecord.KeepAliveSeconds, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Session{}, fmt.Errorf("%w: %s", model.ErrSessionNotFound, id)
	}
	if err != nil {
		return model.Session{}, fmt.Errorf("read Session: %w", err)
	}
	if sessionRecord.CreatedAt, err = parseTime(createdAt); err != nil {
		return model.Session{}, err
	}
	if sessionRecord.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return model.Session{}, err
	}
	return sessionRecord, sessionRecord.Validate()
}
