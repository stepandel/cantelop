package microvm

import (
	"errors"
	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

func (s *SQLiteStore) ListManagedSandboxes(app string) ([]Sandbox, error) {
	return decodeSQLiteSandboxes(s.managedApps.ListManagedSandboxes(app))
}
func (s *SQLiteStore) ReadManaged(app string) (ManagedState, error) {
	state, err := s.managedApps.ReadManaged(app)
	if errors.Is(err, model.ErrManagedStateNotFound) {
		err = ErrNotFound
	}
	return state, err
}
func (s *SQLiteStore) WriteManaged(app string, state ManagedState, usage *ManagedUsage) error {
	return s.managedApps.WriteManaged(app, state, usage)
}
func (s *SQLiteStore) ManagedUsage(app string) ([]ManagedUsage, error) {
	return s.managedApps.ManagedUsage(app)
}
func (s *SQLiteStore) ManagedUsageExists(id string) (bool, error) {
	return s.managedApps.ManagedUsageExists(id)
}
func (s *SQLiteStore) AckManagedUsage(app, id string) error {
	return s.managedApps.AckManagedUsage(app, id)
}
func (s *SQLiteStore) ReserveManagedStorage(app, workspace string, maxWorkspaces int, bytes int64) error {
	return s.managedApps.ReserveManagedStorage(app, workspace, maxWorkspaces, bytes)
}
func (s *SQLiteStore) ReleaseManagedStorage(workspace string) error {
	return s.managedApps.ReleaseManagedStorage(workspace)
}
