package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type SessionWorkLease struct {
	SessionID string
	LeaseID   string
	ExpiresAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

var ErrSessionWorkLeaseNotFound = errors.New("Session work lease not found")

type SessionWorkLeases struct{ db *sql.DB }

func NewSessionWorkLeases(db *sql.DB) *SessionWorkLeases { return &SessionWorkLeases{db: db} }

func (r *SessionWorkLeases) Acquire(value SessionWorkLease) (SessionWorkLease, bool, error) {
	result, err := r.db.Exec(`INSERT INTO session_work_leases
        (session_id, lease_id, expires_at, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(session_id, lease_id) DO NOTHING`, value.SessionID, value.LeaseID,
		formatTime(value.ExpiresAt), formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		return SessionWorkLease{}, false, fmt.Errorf("acquire Session work lease: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return SessionWorkLease{}, false, fmt.Errorf("inspect Session work lease acquisition: %w", err)
	}
	stored, err := r.Get(value.SessionID, value.LeaseID)
	return stored, rows == 1, err
}

func (r *SessionWorkLeases) Renew(sessionID, leaseID string, expiresAt, at time.Time) (SessionWorkLease, error) {
	result, err := r.db.Exec(`UPDATE session_work_leases SET expires_at = ?, updated_at = ?
        WHERE session_id = ? AND lease_id = ?`, formatTime(expiresAt), formatTime(at), sessionID, leaseID)
	if err != nil {
		return SessionWorkLease{}, fmt.Errorf("renew Session work lease: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return SessionWorkLease{}, fmt.Errorf("inspect Session work lease renewal: %w", err)
	}
	if rows != 1 {
		return SessionWorkLease{}, ErrSessionWorkLeaseNotFound
	}
	return r.Get(sessionID, leaseID)
}

func (r *SessionWorkLeases) Release(sessionID, leaseID string) (bool, error) {
	result, err := r.db.Exec("DELETE FROM session_work_leases WHERE session_id = ? AND lease_id = ?", sessionID, leaseID)
	if err != nil {
		return false, fmt.Errorf("release Session work lease: %w", err)
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (r *SessionWorkLeases) Get(sessionID, leaseID string) (SessionWorkLease, error) {
	return scanSessionWorkLease(r.db.QueryRow(`SELECT session_id, lease_id, expires_at, created_at, updated_at
        FROM session_work_leases WHERE session_id = ? AND lease_id = ?`, sessionID, leaseID))
}

func (r *SessionWorkLeases) List(sessionID string) ([]SessionWorkLease, error) {
	return queryAll(r.db, "Session work leases", scanSessionWorkLease, `SELECT session_id, lease_id, expires_at, created_at, updated_at
        FROM session_work_leases WHERE session_id = ? ORDER BY lease_id`, sessionID)
}

func (r *SessionWorkLeases) ListAll() ([]SessionWorkLease, error) {
	return queryAll(r.db, "all Session work leases", scanSessionWorkLease, `SELECT session_id, lease_id, expires_at, created_at, updated_at
        FROM session_work_leases ORDER BY session_id, lease_id`)
}

func (r *SessionWorkLeases) DeleteExpired(at time.Time) ([]string, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin expired Session work lease deletion: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT DISTINCT session_id FROM session_work_leases
        WHERE expires_at <= ? ORDER BY session_id`, formatTime(at))
	if err != nil {
		return nil, fmt.Errorf("list expired Session work leases: %w", err)
	}
	var sessionIDs []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read expired Session work lease: %w", err)
		}
		sessionIDs = append(sessionIDs, sessionID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if _, err := tx.Exec("DELETE FROM session_work_leases WHERE expires_at <= ?", formatTime(at)); err != nil {
		return nil, fmt.Errorf("delete expired Session work leases: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit expired Session work lease deletion: %w", err)
	}
	return sessionIDs, nil
}

func scanSessionWorkLease(row scanner) (SessionWorkLease, error) {
	var value SessionWorkLease
	var expiresAt, createdAt, updatedAt string
	if err := row.Scan(&value.SessionID, &value.LeaseID, &expiresAt, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionWorkLease{}, ErrSessionWorkLeaseNotFound
		}
		return SessionWorkLease{}, fmt.Errorf("read Session work lease: %w", err)
	}
	var err error
	if value.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return SessionWorkLease{}, err
	}
	if value.CreatedAt, err = parseTime(createdAt); err != nil {
		return SessionWorkLease{}, err
	}
	if value.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return SessionWorkLease{}, err
	}
	return value, nil
}
