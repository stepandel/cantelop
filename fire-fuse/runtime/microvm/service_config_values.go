package microvm

// ReplaceComputeAppConfigForUser applies one complete external-control-plane
// projection to the encrypted host runtime cache. The revision prevents a
// delayed command from replacing newer runtime configuration.
func (s *Service) ReplaceComputeAppConfigForUser(userID, appID string, revision int64, values []ConfigValue) (bool, error) {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	if _, err := s.store.GetComputeTenant(userID); err != nil {
		return false, err
	}
	if _, err := s.store.GetAppForComputeTenant(userID, appID); err != nil {
		return false, err
	}
	at := s.now().UTC()
	projection := make([]ConfigValue, len(values))
	for index, value := range values {
		projection[index] = ConfigValue{Name: value.Name, Kind: value.Kind, Value: value.Value, UpdatedAt: at}
	}
	return s.store.ReplaceAppConfig(appID, revision, projection)
}
