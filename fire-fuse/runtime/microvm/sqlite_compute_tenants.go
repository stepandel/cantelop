package microvm

func (s *SQLiteStore) EnsureComputeTenant(tenant ComputeTenant) (bool, error) {
	return s.computeTenants.Ensure(tenant)
}

func (s *SQLiteStore) GetComputeTenant(id string) (ComputeTenant, error) {
	return s.computeTenants.Get(id)
}
