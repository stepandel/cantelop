package microvm

import "time"

var _ ResourceStore = (*SQLiteStore)(nil)

func (s *SQLiteStore) CreateAppForComputeTenant(tenantID string, app App, sessionRuntime AppSessionRuntime, build AppBuild) error {
	build.Version = 1
	return s.catalog.CreateApp(tenantID, app, sessionRuntime, &build)
}

func (s *SQLiteStore) CreateProvisioningAppForComputeTenant(tenantID string, app App, sessionRuntime AppSessionRuntime) error {
	return s.catalog.CreateApp(tenantID, app, sessionRuntime, nil)
}

func (s *SQLiteStore) ArchiveAppForComputeTenant(tenantID, appID string, at time.Time) (bool, error) {
	return s.catalog.ArchiveApp(tenantID, appID, at)
}

func (s *SQLiteStore) GetAppForComputeTenant(tenantID, appID string) (App, error) {
	return s.catalog.GetOwnedApp(tenantID, appID)
}

func (s *SQLiteStore) OwnsAppForComputeTenant(tenantID, appID string) error {
	return s.catalog.RequireAppOwner(tenantID, appID)
}

func (s *SQLiteStore) CountWorkspaces(appID string) (int, error) {
	return s.catalog.CountWorkspaces(appID)
}

func (s *SQLiteStore) GetAppRecord(id string) (App, error) { return s.catalog.GetAppRecord(id) }

func (s *SQLiteStore) ListApps() ([]App, error) { return s.catalog.ListApps() }

func (s *SQLiteStore) ListAllAppBuilds() ([]AppBuild, error) { return s.catalog.ListBuilds() }
