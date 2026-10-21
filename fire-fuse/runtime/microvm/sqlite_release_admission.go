package microvm

var _ ReleaseAdmissionStore = (*SQLiteStore)(nil)

func (s *SQLiteStore) GetReleaseAdmissionForComputeTenant(tenantID, appID string) (ReleaseAdmission, error) {
	return s.catalog.GetReleaseAdmissionForComputeTenant(tenantID, appID)
}

func (s *SQLiteStore) SetReleaseAdmissionForComputeTenant(tenantID, appID, generation string, closed bool) (ReleaseAdmission, error) {
	return s.catalog.SetReleaseAdmissionForComputeTenant(tenantID, appID, generation, closed)
}
