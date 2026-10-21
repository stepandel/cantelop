package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

type ComputeTenants struct{ db *sql.DB }

func NewComputeTenants(db *sql.DB) *ComputeTenants { return &ComputeTenants{db: db} }

func (r *ComputeTenants) Ensure(tenant model.ComputeTenant) (bool, error) {
	if err := tenant.Validate(); err != nil {
		return false, err
	}
	result, err := r.db.Exec(`INSERT INTO compute_tenants (id, created_at) VALUES (?, ?) ON CONFLICT(id) DO NOTHING`, tenant.ID, tenant.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, fmt.Errorf("ensure compute tenant: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect compute tenant insert: %w", err)
	}
	return rows == 1, nil
}

func (r *ComputeTenants) Get(id string) (model.ComputeTenant, error) {
	if err := model.ValidateComputeTenantID(id); err != nil {
		return model.ComputeTenant{}, err
	}
	var tenant model.ComputeTenant
	var createdAt string
	err := r.db.QueryRow(`SELECT id, created_at FROM compute_tenants WHERE id = ?`, id).Scan(&tenant.ID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ComputeTenant{}, model.ErrComputeTenantMissing
	}
	if err != nil {
		return model.ComputeTenant{}, fmt.Errorf("read compute tenant: %w", err)
	}
	tenant.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return model.ComputeTenant{}, fmt.Errorf("parse compute tenant creation time: %w", err)
	}
	return tenant, tenant.Validate()
}
