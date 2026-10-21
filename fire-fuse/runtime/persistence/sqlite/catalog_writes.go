package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

// SelectAppBuild changes the Build used for new Sessions and for Sessions
// reactivated after this point. Running Sandboxes keep their Build until they
// stop; reactivation rebinds the Session to the selected Build.
func (r *Catalog) SelectAppBuild(tenantID, appID, buildID string, at time.Time) error {
	if model.ValidateComputeTenantID(tenantID) != nil || model.ValidateResourceID(appID) != nil ||
		(buildID != "" && model.ValidateBuildID(buildID) != nil) || at.IsZero() {
		return model.ErrInvalidAppBuild
	}
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin App Build selection: %w", err)
	}
	defer tx.Rollback()
	var owned int
	if err := tx.QueryRow(`SELECT count(*) FROM app_tenants o JOIN apps a ON a.id = o.app_id
		WHERE o.tenant_id = ? AND o.app_id = ? AND a.deleted_at IS NULL`, tenantID, appID).Scan(&owned); err != nil {
		return fmt.Errorf("read App owner for Build selection: %w", err)
	}
	if owned != 1 {
		return fmt.Errorf("%w: %s", model.ErrAppNotFound, appID)
	}
	if buildID != "" {
		var present int
		if err := tx.QueryRow(`SELECT count(*) FROM app_builds WHERE app_id = ? AND build_id = ?`, appID, buildID).Scan(&present); err != nil {
			return fmt.Errorf("read App Build selection: %w", err)
		}
		if present != 1 {
			return fmt.Errorf("%w: %s", model.ErrAppBuildNotFound, buildID)
		}
	}
	result, err := tx.Exec(`UPDATE app_session_runtimes SET active_build_id = ? WHERE app_id = ?`, nullable(buildID), appID)
	if err != nil {
		return fmt.Errorf("select App Build: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect App Build selection: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: %s", model.ErrAppNotFound, appID)
	}
	stamp := formatTime(at)
	if _, err := tx.Exec(`UPDATE apps SET updated_at = CASE WHEN updated_at < ? THEN ? ELSE updated_at END WHERE id = ?`, stamp, stamp, appID); err != nil {
		return fmt.Errorf("update App Build selection time: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit App Build selection: %w", err)
	}
	return nil
}

func (r *Catalog) CreateApp(
	tenantID string,
	app model.App,
	sessionRuntime model.AppSessionRuntime,
	build *model.AppBuild,
) error {
	sessionRuntime = sessionRuntime.WithDefaults()
	if err := app.Validate(); err != nil {
		return err
	}
	if err := model.ValidateComputeTenantID(tenantID); err != nil {
		return err
	}
	if err := sessionRuntime.Validate(); err != nil {
		return err
	}
	if build == nil {
		if sessionRuntime.ActiveBuildID != "" {
			return model.ErrInvalidApp
		}
	} else if err := build.Validate(); err != nil || build.AppID != app.ID || build.BuildID != sessionRuntime.ActiveBuildID {
		return errors.Join(model.ErrInvalidAppBuild, err)
	}
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin App creation: %w", err)
	}
	defer tx.Rollback()
	var tenantExists int
	if err := tx.QueryRow(`SELECT count(*) FROM compute_tenants WHERE id = ?`, tenantID).Scan(&tenantExists); err != nil {
		return fmt.Errorf("read compute tenant: %w", err)
	}
	if tenantExists == 0 {
		return fmt.Errorf("%w: %s", model.ErrComputeTenantMissing, tenantID)
	}
	result, err := tx.Exec(`INSERT INTO apps (id, slug, created_at, updated_at)
		VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, app.ID, app.Slug, formatTime(app.CreatedAt), formatTime(app.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert App: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect App insert: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: App %s", model.ErrAlreadyExists, app.ID)
	}
	if app.Managed {
		if _, err := tx.Exec(`INSERT INTO managed_apps(app_id,record) VALUES (?,?)`, app.ID, []byte(`{}`)); err != nil {
			return err
		}
	}
	statements := []struct {
		label string
		sql   string
		args  []any
	}{
		{"App tenant", `INSERT INTO app_tenants (app_id, tenant_id, created_at) VALUES (?, ?, ?)`, []any{app.ID, tenantID, formatTime(app.CreatedAt)}},
		{"App Session runtime", `INSERT INTO app_session_runtimes
			(app_id, active_build_id, profile, internal_port)
			 VALUES (?, ?, ?, ?)`, []any{
			app.ID, nullable(sessionRuntime.ActiveBuildID), sessionRuntime.Runtime.Profile, sessionRuntime.Runtime.InternalPort,
		}},
	}
	if build != nil {
		if _, err := tx.Exec(`INSERT INTO app_builds (app_id, version, build_id, created_at) VALUES (?, ?, ?, ?)`,
			build.AppID, build.Version, build.BuildID, formatTime(build.CreatedAt)); err != nil {
			return fmt.Errorf("insert App Build: %w", err)
		}
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement.sql, statement.args...); err != nil {
			return fmt.Errorf("insert %s: %w", statement.label, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit App creation: %w", err)
	}
	return nil
}

func (r *Catalog) ArchiveApp(tenantID, appID string, at time.Time) (bool, error) {
	if model.ValidateComputeTenantID(tenantID) != nil || model.ValidateResourceID(appID) != nil || at.IsZero() {
		return false, fmt.Errorf("%w: %s", model.ErrAppNotFound, appID)
	}
	tx, err := r.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin App archival: %w", err)
	}
	defer tx.Rollback()
	var deletedAt sql.NullString
	err = tx.QueryRow(`SELECT a.deleted_at FROM apps a
		JOIN app_tenants o ON o.app_id = a.id
		WHERE o.tenant_id = ? AND a.id = ?`, tenantID, appID).Scan(&deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("%w: %s", model.ErrAppNotFound, appID)
	}
	if err != nil {
		return false, fmt.Errorf("read App before archival: %w", err)
	}
	if deletedAt.Valid {
		return false, nil
	}
	var pendingUploads int
	if err := tx.QueryRow(`SELECT count(*) FROM app_build_operations
		WHERE app_id = ? AND status IN ('receiving', 'queued', 'processing')`, appID).Scan(&pendingUploads); err != nil {
		return false, fmt.Errorf("read pending App Build uploads: %w", err)
	}
	if pendingUploads > 0 {
		return false, model.ErrAppBuildUploadPending
	}
	stamp := formatTime(at)
	if _, err := tx.Exec("UPDATE apps SET deleted_at = ? WHERE id = ?", stamp, appID); err != nil {
		return false, fmt.Errorf("archive App: %w", err)
	}
	if _, err := tx.Exec(`UPDATE workspaces SET archived_at = ?, updated_at = ?
        WHERE app_id = ? AND archived_at IS NULL`, stamp, stamp, appID); err != nil {
		return false, fmt.Errorf("archive App Workspaces: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit App archival: %w", err)
	}
	return true, nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
