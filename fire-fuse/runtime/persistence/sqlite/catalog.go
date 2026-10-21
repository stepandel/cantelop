package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

type Catalog struct{ db *sql.DB }

func NewCatalog(db *sql.DB) *Catalog { return &Catalog{db: db} }

func (r *Catalog) GetOwnedApp(tenantID, appID string) (model.App, error) {
	if model.ValidateComputeTenantID(tenantID) != nil || model.ValidateResourceID(appID) != nil {
		return model.App{}, fmt.Errorf("%w: %s", model.ErrAppNotFound, appID)
	}
	var owned int
	if err := r.db.QueryRow(`SELECT count(*) FROM app_tenants o JOIN apps a ON a.id = o.app_id
        WHERE o.tenant_id = ? AND o.app_id = ? AND a.deleted_at IS NULL`, tenantID, appID).Scan(&owned); err != nil {
		return model.App{}, fmt.Errorf("read App owner: %w", err)
	}
	if owned == 0 {
		return model.App{}, fmt.Errorf("%w: %s", model.ErrAppNotFound, appID)
	}
	return r.GetAppRecord(appID)
}

// RequireAppOwner verifies the immutable tenant binding even after an App is
// archived. Destructive cleanup still needs authorization after the routable
// App projection has been retired.
func (r *Catalog) RequireAppOwner(tenantID, appID string) error {
	if model.ValidateComputeTenantID(tenantID) != nil || model.ValidateResourceID(appID) != nil {
		return fmt.Errorf("%w: %s", model.ErrAppNotFound, appID)
	}
	var owned int
	if err := r.db.QueryRow(`SELECT count(*) FROM app_tenants
        WHERE tenant_id = ? AND app_id = ?`, tenantID, appID).Scan(&owned); err != nil {
		return fmt.Errorf("read App owner: %w", err)
	}
	if owned == 0 {
		return fmt.Errorf("%w: %s", model.ErrAppNotFound, appID)
	}
	return nil
}

func (r *Catalog) CountWorkspaces(appID string) (int, error) {
	if model.ValidateResourceID(appID) != nil {
		return 0, model.ErrAppNotFound
	}
	var count int
	if err := r.db.QueryRow("SELECT count(*) FROM workspaces WHERE app_id = ? AND archived_at IS NULL", appID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count App Workspaces: %w", err)
	}
	return count, nil
}

func (r *Catalog) GetSessionRuntime(appID string) (model.AppSessionRuntime, error) {
	var sessionRuntime model.AppSessionRuntime
	var activeBuildID sql.NullString
	err := r.db.QueryRow(`SELECT active_build_id, profile, internal_port
FROM app_session_runtimes h JOIN apps a ON a.id = h.app_id
WHERE h.app_id = ? AND a.deleted_at IS NULL`, appID).Scan(
		&activeBuildID, &sessionRuntime.Runtime.Profile, &sessionRuntime.Runtime.InternalPort,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return model.AppSessionRuntime{}, fmt.Errorf("%w: %s", model.ErrAppNotFound, appID)
	}
	if err != nil {
		return model.AppSessionRuntime{}, fmt.Errorf("read App Session runtime: %w", err)
	}
	if activeBuildID.Valid {
		sessionRuntime.ActiveBuildID = activeBuildID.String
	}
	return sessionRuntime, sessionRuntime.Validate()
}

func (r *Catalog) ListAppBuilds(appID string) ([]model.AppBuild, error) {
	if _, err := r.GetSessionRuntime(appID); err != nil {
		return nil, err
	}
	rows, err := r.db.Query("SELECT app_id, version, build_id, created_at FROM app_builds WHERE app_id = ? ORDER BY version", appID)
	if err != nil {
		return nil, fmt.Errorf("list App Builds: %w", err)
	}
	defer rows.Close()
	var builds []model.AppBuild
	for rows.Next() {
		var build model.AppBuild
		var createdAt string
		if err := rows.Scan(&build.AppID, &build.Version, &build.BuildID, &createdAt); err != nil {
			return nil, fmt.Errorf("scan App Build: %w", err)
		}
		if build.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		if err := build.Validate(); err != nil {
			return nil, err
		}
		builds = append(builds, build)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate App Builds: %w", err)
	}
	return builds, nil
}

func (r *Catalog) GetAppRecord(id string) (model.App, error) {
	var app model.App
	var created, updated string
	err := r.db.QueryRow(`SELECT id, slug, created_at, updated_at, EXISTS(SELECT 1 FROM managed_apps m WHERE m.app_id=apps.id)
FROM apps WHERE id = ? AND deleted_at IS NULL`, id).Scan(
		&app.ID, &app.Slug, &created, &updated, &app.Managed,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return model.App{}, fmt.Errorf("%w: %s", model.ErrAppNotFound, id)
	}
	if err != nil {
		return model.App{}, fmt.Errorf("read App: %w", err)
	}
	if app.CreatedAt, err = parseTime(created); err != nil {
		return model.App{}, err
	}
	if app.UpdatedAt, err = parseTime(updated); err != nil {
		return model.App{}, err
	}
	return app, app.Validate()
}

func (r *Catalog) ListApps() ([]model.App, error) {
	rows, err := r.db.Query(`SELECT id, slug, created_at, updated_at, EXISTS(SELECT 1 FROM managed_apps m WHERE m.app_id=apps.id) FROM apps WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list Apps: %w", err)
	}
	defer rows.Close()
	apps := make([]model.App, 0)
	for rows.Next() {
		var app model.App
		var createdAt, updatedAt string
		if err := rows.Scan(&app.ID, &app.Slug, &createdAt, &updatedAt, &app.Managed); err != nil {
			return nil, fmt.Errorf("scan App: %w", err)
		}
		if app.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		if app.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, err
		}
		if err := app.Validate(); err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Apps: %w", err)
	}
	return apps, nil
}

func (r *Catalog) ListBuilds() ([]model.AppBuild, error) {
	rows, err := r.db.Query(`SELECT b.app_id, b.version, b.build_id, b.created_at FROM app_builds b
        JOIN apps a ON a.id = b.app_id WHERE a.deleted_at IS NULL ORDER BY b.app_id, b.version`)
	if err != nil {
		return nil, fmt.Errorf("list all App Builds: %w", err)
	}
	defer rows.Close()
	var builds []model.AppBuild
	for rows.Next() {
		var build model.AppBuild
		var createdAt string
		if err := rows.Scan(&build.AppID, &build.Version, &build.BuildID, &createdAt); err != nil {
			return nil, fmt.Errorf("scan App Build: %w", err)
		}
		if build.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		if err := build.Validate(); err != nil {
			return nil, err
		}
		builds = append(builds, build)
	}
	return builds, rows.Err()
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("decode persisted timestamp: %w", err)
	}
	return parsed, nil
}
