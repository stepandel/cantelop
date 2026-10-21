package sqlite

import "fmt"

// PublishedBuildReferences returns every Build ID attached to or selected by an
// unarchived App, or used by a live Session, plus the number of uploads that may
// publish a Build before attaching it. A Session whose Sandbox record is missing
// counts as live, matching retention.
func (r *BuildOperations) PublishedBuildReferences() (map[string]struct{}, int, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, 0, fmt.Errorf("begin Build reference read: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT b.build_id FROM app_builds b
		 JOIN apps a ON a.id=b.app_id WHERE a.deleted_at IS NULL
		UNION SELECT r.active_build_id FROM app_session_runtimes r
		 JOIN apps a ON a.id=r.app_id WHERE a.deleted_at IS NULL AND r.active_build_id IS NOT NULL
		UNION SELECT s.build_id FROM sessions s LEFT JOIN sandboxes m ON m.id=s.sandbox_id
		 WHERE m.id IS NULL OR m.state<>'terminated'`)
	if err != nil {
		return nil, 0, fmt.Errorf("list Build references: %w", err)
	}
	references := make(map[string]struct{})
	for rows.Next() {
		var buildID string
		if err := rows.Scan(&buildID); err != nil {
			rows.Close()
			return nil, 0, err
		}
		references[buildID] = struct{}{}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	var pending int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM app_build_operations
		WHERE status IN ('receiving','queued','processing')`).Scan(&pending); err != nil {
		return nil, 0, fmt.Errorf("count pending Build uploads: %w", err)
	}
	return references, pending, nil
}
