package microvm

func (s *SQLiteStore) SetAppConfig(appID string, value ConfigValue) error {
	return s.configValues.SetApp(appID, value)
}

func (s *SQLiteStore) ListAppConfig(appID string) ([]ConfigValue, error) {
	return s.configValues.ListApp(appID)
}

func (s *SQLiteStore) DeleteAppConfig(appID, name string) (bool, error) {
	return s.configValues.DeleteApp(appID, name)
}

func (s *SQLiteStore) ResolveAppConfig(appID string) ([]string, error) {
	return s.configValues.ResolveApp(appID)
}

func (s *SQLiteStore) ReplaceAppConfig(appID string, revision int64, values []ConfigValue) (bool, error) {
	return s.configValues.ReplaceApp(appID, revision, values)
}
