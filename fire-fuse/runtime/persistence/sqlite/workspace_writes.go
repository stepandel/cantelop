package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

func (r *Workspaces) Create(workspace model.Workspace) error {
	if err := workspace.Validate(); err != nil || workspace.ArchivedAt != nil {
		return model.ErrInvalidWorkspace
	}
	var appExists int
	if err := r.db.QueryRow(`SELECT count(*) FROM apps WHERE id = ? AND deleted_at IS NULL`, workspace.AppID).Scan(&appExists); err != nil {
		return fmt.Errorf("look up Workspace App: %w", err)
	}
	if appExists != 1 {
		return model.ErrAppNotFound
	}
	_, err := r.db.Exec(`INSERT INTO workspaces (id, app_id, slug, created_at, updated_at, archived_at) VALUES (?, ?, ?, ?, ?, NULL)`,
		workspace.ID, workspace.AppID, workspace.Slug, formatTime(workspace.CreatedAt), formatTime(workspace.UpdatedAt))
	if err != nil {
		if strings.Contains(err.Error(), "constraint failed") {
			return fmt.Errorf("%w: Workspace %s", model.ErrAlreadyExists, workspace.ID)
		}
		return fmt.Errorf("insert Workspace: %w", err)
	}
	return nil
}

// PurgeMetadata removes all runtime history and encrypted Message material for
// an archived Workspace while retaining its small catalog tombstone. Keeping
// the tombstone makes deletion idempotent and prevents the same opaque identity
// from being recreated after a restore.
func (r *Workspaces) PurgeMetadata(workspaceID string) error {
	if model.ValidateWorkspaceID(workspaceID) != nil {
		return model.ErrInvalidWorkspace
	}
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin Workspace metadata purge: %w", err)
	}
	defer tx.Rollback()
	var archivedAt sql.NullString
	if err := tx.QueryRow("SELECT archived_at FROM workspaces WHERE id = ?", workspaceID).Scan(&archivedAt); errors.Is(err, sql.ErrNoRows) {
		return model.ErrWorkspaceNotFound
	} else if err != nil {
		return fmt.Errorf("read Workspace before metadata purge: %w", err)
	}
	if !archivedAt.Valid {
		return fmt.Errorf("%w: Workspace must be archived before metadata purge", model.ErrInvalidWorkspace)
	}
	var active int
	if err := tx.QueryRow(`SELECT count(*) FROM sandboxes
WHERE json_extract(record, '$.workspace_id') = ? AND state <> 'terminated'`, workspaceID).Scan(&active); err != nil {
		return fmt.Errorf("inspect Workspace sandboxes before metadata purge: %w", err)
	}
	if active != 0 {
		return fmt.Errorf("Workspace metadata purge has %d nonterminal Sandboxes", active)
	}
	for _, statement := range []string{
		"DELETE FROM session_stream_entries WHERE stream_id IN (SELECT stream_id FROM session_streams WHERE workspace_id = ?)",
		"DELETE FROM session_streams WHERE workspace_id = ?",
		"DELETE FROM message_deliveries WHERE workspace_id = ?",
		"DELETE FROM request_events WHERE workspace_id = ?",
		"DELETE FROM session_keys WHERE workspace_id = ?",
		"DELETE FROM sessions WHERE workspace_id = ?",
		"DELETE FROM sandboxes WHERE json_extract(record, '$.workspace_id') = ? AND state = 'terminated'",
	} {
		if _, err := tx.Exec(statement, workspaceID); err != nil {
			return fmt.Errorf("purge Workspace runtime metadata: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Workspace metadata purge: %w", err)
	}
	return nil
}

func (r *Workspaces) Archive(workspaceID string, at time.Time) (model.Workspace, error) {
	if model.ValidateWorkspaceID(workspaceID) != nil || at.IsZero() {
		return model.Workspace{}, model.ErrInvalidWorkspace
	}
	if _, err := r.db.Exec(`UPDATE workspaces SET archived_at = COALESCE(archived_at, ?), updated_at = CASE WHEN archived_at IS NULL THEN ? ELSE updated_at END WHERE id = ?`, formatTime(at), formatTime(at), workspaceID); err != nil {
		return model.Workspace{}, fmt.Errorf("archive Workspace: %w", err)
	}
	return r.Get(workspaceID)
}
