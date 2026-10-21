package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

// ManagedApps persists atomic policy, accounting evidence and retained storage.
type ManagedApps struct{ db *sql.DB }

func NewManagedApps(db *sql.DB) *ManagedApps { return &ManagedApps{db: db} }
func (s *ManagedApps) ListManagedSandboxes(app string) ([]SandboxRecord, error) {
	rows, err := s.db.Query(`SELECT id,state,record FROM sandboxes WHERE CASE WHEN json_valid(record) THEN json_extract(record,'$.app_id') END=? AND state IN ('starting','running','stopping','failed') ORDER BY id`, app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SandboxRecord{}
	for rows.Next() {
		var record SandboxRecord
		if err = rows.Scan(&record.ID, &record.State, &record.Record); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}
func (s *ManagedApps) ReadManaged(app string) (model.ManagedState, error) {
	var b []byte
	var state model.ManagedState
	err := s.db.QueryRow(`SELECT record FROM managed_apps WHERE app_id=?`, app).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &state)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return state, model.ErrManagedStateNotFound
	}
	return state, err
}
func (s *ManagedApps) WriteManaged(app string, state model.ManagedState, usage *model.ManagedUsage) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	result, err := tx.Exec(`UPDATE managed_apps SET record=? WHERE app_id=?`, b, app)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return model.ErrManagedAdmission
	}
	if usage != nil {
		b, err = json.Marshal(usage)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO managed_usage(id,app_id,record) VALUES(?,?,?)`, usage.ID, app, b); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *ManagedApps) ManagedUsage(app string) ([]model.ManagedUsage, error) {
	rows, err := s.db.Query(`SELECT record FROM managed_usage WHERE app_id=? AND NOT json_extract(record,'$.ack') ORDER BY id LIMIT 1000`, app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.ManagedUsage{}
	for rows.Next() {
		var b []byte
		var u model.ManagedUsage
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &u); err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, rows.Err()
}
func (s *ManagedApps) ManagedUsageExists(id string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM managed_usage WHERE id=?`, id).Scan(&n)
	return n > 0, err
}
func (s *ManagedApps) AckManagedUsage(app, id string) error {
	_, err := s.db.Exec(`UPDATE managed_usage SET record=CAST(json_set(record,'$.ack',json('true')) AS BLOB) WHERE app_id=? AND id=?`, app, id)
	return err
}
func (s *ManagedApps) ReserveManagedStorage(app, workspace string, maxWorkspaces int, bytes int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldApp string
	var oldBytes int64
	err = tx.QueryRow(`SELECT app_id,bytes FROM managed_storage WHERE workspace_id=?`, workspace).Scan(&oldApp, &oldBytes)
	if err == nil {
		if oldApp != app || oldBytes > bytes {
			return model.ErrManagedAdmission
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var count int
	err = tx.QueryRow(`SELECT count(*) FROM managed_storage WHERE app_id=?`, app).Scan(&count)
	if err != nil {
		return err
	}
	if count >= maxWorkspaces {
		return model.ErrManagedAdmission
	}
	if _, err = tx.Exec(`INSERT INTO managed_storage(workspace_id,app_id,bytes) VALUES(?,?,?)`, workspace, app, bytes); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *ManagedApps) ReleaseManagedStorage(workspace string) error {
	_, err := s.db.Exec(`DELETE FROM managed_storage WHERE workspace_id=?`, workspace)
	return err
}
