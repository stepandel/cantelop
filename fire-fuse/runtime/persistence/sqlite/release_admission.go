package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

func (s *Catalog) GetReleaseAdmissionForComputeTenant(tenantID, appID string) (model.ReleaseAdmission, error) {
	if model.ValidateComputeTenantID(tenantID) != nil || model.ValidateResourceID(appID) != nil {
		return model.ReleaseAdmission{}, model.ErrReleaseAdmissionConflict
	}
	var admission model.ReleaseAdmission
	var closed int
	err := s.db.QueryRow(`SELECT gate.generation, gate.closed
FROM app_release_admission gate
JOIN app_tenants owner ON owner.app_id = gate.app_id
JOIN apps app ON app.id = gate.app_id
WHERE gate.app_id = ? AND owner.tenant_id = ? AND app.deleted_at IS NULL`, appID, tenantID).Scan(&admission.Generation, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ReleaseAdmission{}, model.ErrAppNotFound
	}
	if err != nil {
		return model.ReleaseAdmission{}, fmt.Errorf("read App release admission: %w", err)
	}
	admission.Closed = closed != 0
	return admission, nil
}

func (s *Catalog) SetReleaseAdmissionForComputeTenant(tenantID, appID, generation string, closed bool) (model.ReleaseAdmission, error) {
	if model.ValidateComputeTenantID(tenantID) != nil || model.ValidateResourceID(appID) != nil || !model.ValidReleaseGeneration(generation) {
		return model.ReleaseAdmission{}, model.ErrReleaseAdmissionConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.ReleaseAdmission{}, fmt.Errorf("begin App release admission update: %w", err)
	}
	defer tx.Rollback()
	var current model.ReleaseAdmission
	var currentClosed int
	err = tx.QueryRow(`SELECT gate.generation, gate.closed
FROM app_release_admission gate
JOIN app_tenants owner ON owner.app_id = gate.app_id
JOIN apps app ON app.id = gate.app_id
WHERE gate.app_id = ? AND owner.tenant_id = ? AND app.deleted_at IS NULL`, appID, tenantID).Scan(&current.Generation, &currentClosed)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ReleaseAdmission{}, model.ErrAppNotFound
	}
	if err != nil {
		return model.ReleaseAdmission{}, fmt.Errorf("read App release admission for update: %w", err)
	}
	current.Closed = currentClosed != 0
	if (!closed && current.Generation != generation) ||
		(closed && generation < current.Generation) {
		return model.ReleaseAdmission{}, model.ErrReleaseAdmissionConflict
	}
	closedValue := 0
	if closed {
		closedValue = 1
	}
	if _, err := tx.Exec(`UPDATE app_release_admission SET generation = ?, closed = ? WHERE app_id = ?`, generation, closedValue, appID); err != nil {
		return model.ReleaseAdmission{}, fmt.Errorf("write App release admission: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.ReleaseAdmission{}, fmt.Errorf("commit App release admission: %w", err)
	}
	return model.ReleaseAdmission{Generation: generation, Closed: closed}, nil
}
