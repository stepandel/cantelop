package microvm

import "github.com/stepandel/cantelop/fire-fuse/runtime/model"

var ErrReleaseAdmissionConflict = model.ErrReleaseAdmissionConflict

type ReleaseAdmission = model.ReleaseAdmission

type ReleaseAdmissionStore interface {
	GetReleaseAdmissionForComputeTenant(tenantID, appID string) (ReleaseAdmission, error)
	SetReleaseAdmissionForComputeTenant(tenantID, appID, generation string, closed bool) (ReleaseAdmission, error)
}

func (s *Service) GetReleaseAdmissionForComputeTenant(tenantID, appID string) (ReleaseAdmission, error) {
	s.resourceMu.RLock()
	defer s.resourceMu.RUnlock()
	return s.store.GetReleaseAdmissionForComputeTenant(tenantID, appID)
}

func (s *Service) SetReleaseAdmissionForComputeTenant(tenantID, appID, generation string, closed bool) (ReleaseAdmission, error) {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	return s.store.SetReleaseAdmissionForComputeTenant(tenantID, appID, generation, closed)
}
