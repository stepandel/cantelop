package microvm

import "time"

var _ WorkspaceStore = (*SQLiteStore)(nil)

func (s *SQLiteStore) GetAppSessionRuntime(appID string) (AppSessionRuntime, error) {
	return s.catalog.GetSessionRuntime(appID)
}

func (s *SQLiteStore) ListAppBuildsForApp(appID string) ([]AppBuild, error) {
	return s.catalog.ListAppBuilds(appID)
}

func (s *SQLiteStore) CreateWorkspace(workspace Workspace) error {
	return s.workspaces.Create(workspace)
}

func (s *SQLiteStore) GetWorkspace(workspaceID string) (Workspace, error) {
	return s.workspaces.Get(workspaceID)
}

func (s *SQLiteStore) ListWorkspaces(appID string) ([]Workspace, error) {
	return s.workspaces.List(appID)
}

func (s *SQLiteStore) GetWorkspaceByAppIDAndSlug(appID, workspaceSlug string) (Workspace, error) {
	return s.workspaces.GetByAppIDAndSlug(appID, workspaceSlug)
}

func (s *SQLiteStore) ArchiveWorkspace(workspaceID string, at time.Time) (Workspace, error) {
	return s.workspaces.Archive(workspaceID, at)
}

func (s *SQLiteStore) PurgeWorkspaceMetadata(workspaceID string) error {
	return s.workspaces.PurgeMetadata(workspaceID)
}
