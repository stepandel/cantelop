package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

type BuildOperations struct{ db *sql.DB }

func NewBuildOperations(db *sql.DB) *BuildOperations { return &BuildOperations{db: db} }

func (r *BuildOperations) Begin(tenantID string, operation model.AppBuildOperation) error {
	if operation.Validate() != nil || operation.Status != model.AppBuildOperationReceiving || model.ValidateComputeTenantID(tenantID) != nil {
		return model.ErrInvalidAppBuildOperation
	}
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin App Build admission: %w", err)
	}
	defer tx.Rollback()
	// Authorize and reject pending/replayed operations before retention can mutate
	// anything. Admission and cleanup commit together, or neither takes effect.
	var owned, pending, replay int
	if err := tx.QueryRow(`SELECT
		(SELECT COUNT(*) FROM apps a JOIN app_tenants o ON o.app_id=a.id
		 WHERE a.id=? AND o.tenant_id=? AND a.deleted_at IS NULL),
		(SELECT COUNT(*) FROM app_build_operations WHERE app_id=? AND status IN ('receiving','queued','processing')),
		(SELECT COUNT(*) FROM app_build_operations WHERE id=?)`,
		operation.AppID, tenantID, operation.AppID, operation.ID).Scan(&owned, &pending, &replay); err != nil {
		return fmt.Errorf("check App Build admission: %w", err)
	}
	if owned != 1 {
		return model.ErrAppNotFound
	}
	if pending != 0 || replay != 0 {
		return model.ErrAppBuildUploadPending
	}
	// Reclaim archived attachments first so host pressure does not cost the
	// requesting App its rollback history.
	if err := pruneArchivedBuildsForAdmission(tx, operation.CreatedAt); err != nil {
		return err
	}
	if err := pruneAppBuildsForAdmission(tx, operation.AppID, operation.CreatedAt); err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT INTO app_build_operations
		(id, app_id, status, created_at, updated_at)
		SELECT ?, a.id, ?, ?, ? FROM apps a JOIN app_tenants o ON o.app_id = a.id
		WHERE a.id = ? AND o.tenant_id = ? AND a.deleted_at IS NULL
		AND (SELECT COUNT(*) FROM app_builds WHERE app_id = a.id) < ?
		AND (`+hostBuildCountSQL+`) < ?`,
		operation.ID, operation.Status, formatTime(operation.CreatedAt), formatTime(operation.UpdatedAt),
		operation.AppID, tenantID, model.DefaultMaxBuildsPerApp, model.DefaultMaxRetainedBuilds)
	if err != nil {
		if isConstraint(err) {
			return model.ErrAppBuildUploadPending
		}
		return fmt.Errorf("insert App Build operation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect App Build operation insert: %w", err)
	}
	if rows == 0 {
		var owned, appBuilds, hostBuilds int
		err := tx.QueryRow(`SELECT COUNT(a.id),
			(SELECT COUNT(*) FROM app_builds WHERE app_id = ?), `+hostBuildCountSQL+`
			FROM apps a JOIN app_tenants o ON o.app_id = a.id
			WHERE a.id = ? AND o.tenant_id = ? AND a.deleted_at IS NULL`,
			operation.AppID, operation.AppID, tenantID).Scan(&owned, &appBuilds, &hostBuilds)
		if err != nil {
			return fmt.Errorf("inspect App Build quota: %w", err)
		}
		if owned == 1 && (appBuilds >= model.DefaultMaxBuildsPerApp || hostBuilds >= model.DefaultMaxRetainedBuilds) {
			return model.ErrAppBuildQuota
		}
		return fmt.Errorf("%w: %s", model.ErrAppNotFound, operation.AppID)
	}
	return tx.Commit()
}

// hostBuildCountSQL is the host quota: retained attachments plus pending uploads.
const hostBuildCountSQL = `(SELECT COUNT(*) FROM app_builds) + (SELECT COUNT(*) FROM app_build_operations
	 WHERE status IN ('receiving','queued','processing'))`

// unreferencedBuildSQL excludes Builds selected by a runtime or bound to a
// Session whose Sandbox has not terminated. It expects app_builds aliased as b.
const unreferencedBuildSQL = `NOT EXISTS (SELECT 1 FROM app_session_runtimes r WHERE r.active_build_id=b.build_id)
		AND NOT EXISTS (SELECT 1 FROM sessions s LEFT JOIN sandboxes m ON m.id=s.sandbox_id
		 WHERE s.build_id=b.build_id AND (m.id IS NULL OR m.state<>'terminated'))`

// Retention removes quota-counted attachments, not catalog images or release
// artifacts. The newest 20 versions preserve rollback and the version high-water
// mark. Historical Sessions do not pin builds after their Sandbox terminates.
func pruneAppBuildsForAdmission(tx *sql.Tx, appID string, at time.Time) error {
	var appCount, hostCount int
	if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM app_builds WHERE app_id=?), `+hostBuildCountSQL, appID).Scan(&appCount, &hostCount); err != nil {
		return fmt.Errorf("count retained Builds: %w", err)
	}
	if appCount < model.DefaultMaxBuildsPerApp && hostCount < model.DefaultMaxRetainedBuilds {
		return nil
	}
	return retireSelectedBuilds(tx, at, `SELECT b.app_id, b.version FROM app_builds b
		WHERE b.app_id=? AND b.version NOT IN
		 (SELECT version FROM app_builds WHERE app_id=? ORDER BY version DESC LIMIT ?)
		AND `+unreferencedBuildSQL+`
		AND NOT EXISTS (SELECT 1 FROM sandboxes m WHERE m.state<>'terminated'
		 AND NOT EXISTS (SELECT 1 FROM sessions s WHERE s.sandbox_id=m.id))
		ORDER BY b.version`, appID, appID, model.DefaultRetainedBuildsPerApp)
}

// Archived Apps cannot receive another Build, but their attachments still count
// against the host quota. Reclaim only unreferenced older versions, preserving
// each App's version high-water mark and every operation ID as a tombstone.
// Archived Workspaces cannot start Sessions, so unbound Sandboxes cannot pin
// these Builds.
func pruneArchivedBuildsForAdmission(tx *sql.Tx, at time.Time) error {
	var hostCount int
	if err := tx.QueryRow(`SELECT ` + hostBuildCountSQL).Scan(&hostCount); err != nil {
		return fmt.Errorf("count host Builds: %w", err)
	}
	excess := hostCount - model.DefaultMaxRetainedBuilds + 1
	if excess <= 0 {
		return nil
	}
	return retireSelectedBuilds(tx, at, `SELECT b.app_id, b.version FROM app_builds b
		JOIN apps a ON a.id=b.app_id
		WHERE a.deleted_at IS NOT NULL
		AND b.version < (SELECT MAX(version) FROM app_builds WHERE app_id=b.app_id)
		AND `+unreferencedBuildSQL+`
		ORDER BY a.deleted_at, b.version LIMIT ?`, excess)
}

// retireSelectedBuilds retires every (app_id, version) row the query returns.
func retireSelectedBuilds(tx *sql.Tx, at time.Time, query string, args ...any) error {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return fmt.Errorf("select Build cleanup: %w", err)
	}
	type attachment struct {
		appID   string
		version int
	}
	var candidates []attachment
	for rows.Next() {
		var b attachment
		if err := rows.Scan(&b.appID, &b.version); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, b := range candidates {
		if err := retireBuildAttachment(tx, b.appID, b.version, at); err != nil {
			return err
		}
	}
	return nil
}

func retireBuildAttachment(tx *sql.Tx, appID string, version int, at time.Time) error {
	// Preserve the command ID so a delayed replay cannot restore a retired Build.
	if _, err := tx.Exec(`UPDATE app_build_operations SET status='failed',
	 build_id=NULL, build_version=NULL, error_code='build_retired',
	 updated_at=CASE WHEN updated_at<? THEN ? ELSE updated_at END
	 WHERE app_id=? AND build_version=?`, formatTime(at), formatTime(at), appID, version); err != nil {
		return fmt.Errorf("retire Build operation receipt: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM app_builds WHERE app_id=? AND version=?`, appID, version); err != nil {
		return fmt.Errorf("prune retained Build: %w", err)
	}
	return nil
}

func (r *BuildOperations) Get(tenantID, appID, operationID string) (model.AppBuildOperation, error) {
	if model.ValidateComputeTenantID(tenantID) != nil || model.ValidateResourceID(appID) != nil || !model.ValidAppBuildOperationID(operationID) {
		return model.AppBuildOperation{}, fmt.Errorf("%w: %s", model.ErrAppBuildOperationNotFound, operationID)
	}
	operation, err := scanBuildOperation(r.db.QueryRow(`SELECT b.id, b.app_id, b.status, b.size_bytes, b.build_id, b.build_version,
		b.error_code, b.created_at, b.updated_at FROM app_build_operations b
		JOIN app_tenants o ON o.app_id = b.app_id JOIN apps a ON a.id = b.app_id
		WHERE b.id = ? AND b.app_id = ? AND o.tenant_id = ? AND a.deleted_at IS NULL`, operationID, appID, tenantID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.AppBuildOperation{}, fmt.Errorf("%w: %s", model.ErrAppBuildOperationNotFound, operationID)
	}
	return operation, err
}

// CompletePrepared records a Build prepared outside the host and leaves the
// active Build unchanged; activation happens through release selection.
func (r *BuildOperations) CompletePrepared(operationID, buildID string, at time.Time) (model.AppBuildOperation, model.AppBuild, error) {
	if !model.ValidAppBuildOperationID(operationID) || model.ValidateBuildID(buildID) != nil || at.IsZero() {
		return model.AppBuildOperation{}, model.AppBuild{}, model.ErrInvalidAppBuildOperation
	}
	tx, err := r.db.Begin()
	if err != nil {
		return model.AppBuildOperation{}, model.AppBuild{}, fmt.Errorf("begin App Build upload completion: %w", err)
	}
	defer tx.Rollback()
	operation, err := scanBuildOperation(tx.QueryRow(`SELECT id, app_id, status, size_bytes, build_id, build_version, error_code, created_at, updated_at
		FROM app_build_operations WHERE id = ?`, operationID))
	if err != nil || operation.Status != model.AppBuildOperationReceiving {
		if err != nil {
			return model.AppBuildOperation{}, model.AppBuild{}, err
		}
		return model.AppBuildOperation{}, model.AppBuild{}, model.ErrInvalidAppBuildOperation
	}
	var deletedAt sql.NullString
	if err := tx.QueryRow(`SELECT deleted_at FROM apps WHERE id = ?`, operation.AppID).Scan(&deletedAt); err != nil {
		return model.AppBuildOperation{}, model.AppBuild{}, fmt.Errorf("read App before Build completion: %w", err)
	}
	if deletedAt.Valid {
		if _, err := tx.Exec(`UPDATE app_build_operations SET status = 'failed', error_code = 'app_archived', updated_at = ? WHERE id = ?`, formatTime(at), operation.ID); err != nil {
			return model.AppBuildOperation{}, model.AppBuild{}, err
		}
		if err := tx.Commit(); err != nil {
			return model.AppBuildOperation{}, model.AppBuild{}, err
		}
		return model.AppBuildOperation{}, model.AppBuild{}, fmt.Errorf("%w: %s", model.ErrAppNotFound, operation.AppID)
	}
	build, err := r.ensureBuild(tx, operation.AppID, buildID, at)
	if err != nil {
		return model.AppBuildOperation{}, model.AppBuild{}, err
	}
	stamp := formatTime(at)
	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE apps SET updated_at = CASE WHEN updated_at < ? THEN ? ELSE updated_at END WHERE id = ?`, []any{stamp, stamp, operation.AppID}},
		{`UPDATE app_build_operations SET status = 'succeeded', build_id = ?, build_version = ?, updated_at = ? WHERE id = ?`, []any{buildID, build.Version, stamp, operation.ID}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement.query, statement.args...); err != nil {
			return model.AppBuildOperation{}, model.AppBuild{}, fmt.Errorf("complete App Build upload: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return model.AppBuildOperation{}, model.AppBuild{}, fmt.Errorf("commit App Build upload completion: %w", err)
	}
	operation.Status, operation.BuildID, operation.BuildVersion, operation.UpdatedAt = model.AppBuildOperationSucceeded, buildID, build.Version, at.UTC()
	return operation, build, nil
}

func (r *BuildOperations) ensureBuild(tx *sql.Tx, appID, buildID string, at time.Time) (model.AppBuild, error) {
	var build model.AppBuild
	var createdAt string
	err := tx.QueryRow(`SELECT app_id, version, build_id, created_at FROM app_builds WHERE app_id = ? AND build_id = ?`, appID, buildID).Scan(
		&build.AppID, &build.Version, &build.BuildID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.QueryRow(`SELECT COALESCE(MAX(version), 0) + 1 FROM app_builds WHERE app_id = ?`, appID).Scan(&build.Version); err != nil {
			return model.AppBuild{}, fmt.Errorf("assign App Build version: %w", err)
		}
		build.AppID, build.BuildID, build.CreatedAt = appID, buildID, at.UTC()
		if _, err := tx.Exec(`INSERT INTO app_builds (app_id, version, build_id, created_at) VALUES (?, ?, ?, ?)`, build.AppID, build.Version, build.BuildID, formatTime(build.CreatedAt)); err != nil {
			return model.AppBuild{}, fmt.Errorf("insert completed App Build: %w", err)
		}
		return build, nil
	}
	if err != nil {
		return model.AppBuild{}, fmt.Errorf("read completed App Build: %w", err)
	}
	build.CreatedAt, err = parseTime(createdAt)
	return build, err
}

func scanBuildOperation(row scanner) (model.AppBuildOperation, error) {
	var operation model.AppBuildOperation
	var status, createdAt, updatedAt string
	var buildID, errorCode sql.NullString
	var buildVersion sql.NullInt64
	if err := row.Scan(&operation.ID, &operation.AppID, &status, &operation.SizeBytes, &buildID, &buildVersion, &errorCode, &createdAt, &updatedAt); err != nil {
		return model.AppBuildOperation{}, fmt.Errorf("read App Build operation: %w", err)
	}
	operation.Status = model.AppBuildOperationStatus(status)
	operation.BuildID, operation.BuildVersion, operation.ErrorCode = buildID.String, int(buildVersion.Int64), errorCode.String
	var err error
	if operation.CreatedAt, err = parseTime(createdAt); err != nil {
		return model.AppBuildOperation{}, err
	}
	if operation.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return model.AppBuildOperation{}, err
	}
	return operation, operation.Validate()
}

func isConstraint(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "constraint failed") || strings.Contains(err.Error(), "UNIQUE constraint failed"))
}

// Cancel persists a receipt even when cancellation overtakes the submit request.
// Replays of that command can never create or activate a Build afterward.
func (r *BuildOperations) Cancel(tenantID, appID, operationID string, at time.Time) (model.AppBuildOperation, error) {
	if model.ValidateComputeTenantID(tenantID) != nil || model.ValidateResourceID(appID) != nil || !model.ValidAppBuildOperationID(operationID) || at.IsZero() {
		return model.AppBuildOperation{}, model.ErrInvalidAppBuildOperation
	}
	_, err := r.db.Exec(`INSERT INTO app_build_operations (id, app_id, status, error_code, created_at, updated_at)
 SELECT ?, a.id, 'failed', 'deployment_cancelled', ?, ? FROM apps a JOIN app_tenants o ON o.app_id=a.id
 WHERE a.id=? AND o.tenant_id=? AND a.deleted_at IS NULL
 ON CONFLICT(id) DO UPDATE SET status='failed', error_code='deployment_cancelled', updated_at=excluded.updated_at
 WHERE app_build_operations.app_id=excluded.app_id AND app_build_operations.status IN ('receiving','queued','processing')`,
		operationID, formatTime(at), formatTime(at), appID, tenantID)
	if err != nil {
		return model.AppBuildOperation{}, fmt.Errorf("cancel App Build: %w", err)
	}
	return r.Get(tenantID, appID, operationID)
}
