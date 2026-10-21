package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

type Workspaces struct{ db *sql.DB }

func NewWorkspaces(db *sql.DB) *Workspaces { return &Workspaces{db: db} }

func (r *Workspaces) Get(id string) (model.Workspace, error) {
	return scanWorkspace(r.db.QueryRow(`SELECT id, app_id, slug, created_at, updated_at, archived_at FROM workspaces WHERE id = ?`, id), id)
}

func (r *Workspaces) List(appID string) ([]model.Workspace, error) {
	if model.ValidateResourceID(appID) != nil {
		return nil, model.ErrAppNotFound
	}
	return queryAll(r.db, "Workspaces", func(row scanner) (model.Workspace, error) {
		return scanWorkspace(row, "")
	}, `SELECT id, app_id, slug, created_at, updated_at, archived_at FROM workspaces WHERE app_id = ? ORDER BY id`, appID)
}

func (r *Workspaces) GetByAppIDAndSlug(appID, workspaceSlug string) (model.Workspace, error) {
	return scanWorkspace(r.db.QueryRow(`SELECT w.id, w.app_id, w.slug, w.created_at, w.updated_at, w.archived_at
FROM workspaces w JOIN apps a ON a.id = w.app_id
WHERE w.app_id = ? AND w.slug = ? AND a.deleted_at IS NULL AND w.archived_at IS NULL`, appID, workspaceSlug), workspaceSlug+"--"+appID)
}

func scanWorkspace(row scanner, id string) (model.Workspace, error) {
	var w model.Workspace
	var created, updated string
	var archived sql.NullString
	if err := row.Scan(&w.ID, &w.AppID, &w.Slug, &created, &updated, &archived); errors.Is(err, sql.ErrNoRows) {
		return model.Workspace{}, fmt.Errorf("%w: %s", model.ErrWorkspaceNotFound, id)
	} else if err != nil {
		return model.Workspace{}, fmt.Errorf("read Workspace: %w", err)
	}
	var err error
	if w.CreatedAt, err = parseTime(created); err != nil {
		return model.Workspace{}, err
	}
	if w.UpdatedAt, err = parseTime(updated); err != nil {
		return model.Workspace{}, err
	}
	if archived.Valid {
		value, err := parseTime(archived.String)
		if err != nil {
			return model.Workspace{}, err
		}
		w.ArchivedAt = &value
	}
	return w, w.Validate()
}
