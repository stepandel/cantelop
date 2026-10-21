package microvm

import (
	"context"
	"errors"
	"fmt"
)

var ErrWorkspaceUnavailable = errors.New("workspace is unavailable")

const MaxWorkspacesPerApp = 25

type RegisterComputeWorkspaceSpec struct {
	ID    string
	AppID string
	Slug  string
}

// RegisterComputeWorkspaceForUser idempotently installs one addressable
// Workspace. Persistent storage uses the same stable Workspace identity.
func (s *Service) RegisterComputeWorkspaceForUser(userID string, desired RegisterComputeWorkspaceSpec) (Workspace, bool, error) {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	if _, err := s.store.GetComputeTenant(userID); err != nil {
		return Workspace{}, false, err
	}
	_, err := s.store.GetAppForComputeTenant(userID, desired.AppID)
	if err != nil {
		return Workspace{}, false, err
	}
	if existing, err := s.store.GetWorkspace(desired.ID); err == nil {
		if existing.AppID != desired.AppID || existing.Slug != desired.Slug {
			return Workspace{}, false, ErrAlreadyExists
		}
		return existing, false, nil
	} else if !errors.Is(err, ErrWorkspaceNotFound) {
		return Workspace{}, false, err
	}
	count, err := s.store.CountWorkspaces(desired.AppID)
	if err != nil {
		return Workspace{}, false, err
	}
	if count >= MaxWorkspacesPerApp {
		return Workspace{}, false, ErrProviderCapacity
	}
	if err := s.managedWorkspaceQuota(desired.AppID, desired.ID, count); err != nil {
		return Workspace{}, false, err
	}
	now := s.now().UTC()
	workspace := Workspace{ID: desired.ID, AppID: desired.AppID, Slug: desired.Slug, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateWorkspace(workspace); err != nil {
		if store, ok := s.store.(managedStore); ok {
			err = errors.Join(err, store.ReleaseManagedStorage(desired.ID))
		}
		return Workspace{}, false, err
	}
	return workspace, true, nil
}

// ArchiveWorkspaceForUser removes one Workspace from routing, then terminates
// every Sandbox still attached to it. Durable workspace data remains retained
// for operator-controlled recovery.
func (s *Service) ArchiveWorkspaceForUser(ctx context.Context, userID, appID, workspaceID string) (bool, error) {
	s.resourceMu.Lock()
	if _, err := s.store.GetComputeTenant(userID); err != nil {
		s.resourceMu.Unlock()
		return false, err
	}
	// Cleanup must remain authorized after the App itself is archived. The
	// immutable tenant binding survives archival even though active App reads
	// intentionally stop returning the App.
	if err := s.store.OwnsAppForComputeTenant(userID, appID); err != nil {
		s.resourceMu.Unlock()
		return false, err
	}
	workspace, err := s.store.GetWorkspace(workspaceID)
	if err != nil {
		s.resourceMu.Unlock()
		return false, err
	}
	if workspace.AppID != appID {
		s.resourceMu.Unlock()
		return false, ErrWorkspaceNotFound
	}
	archived := workspace.ArchivedAt == nil
	if archived {
		if _, err := s.store.ArchiveWorkspace(workspaceID, s.now().UTC()); err != nil {
			s.resourceMu.Unlock()
			return false, err
		}
	}
	machines, err := s.store.ListActive()
	s.resourceMu.Unlock()
	if err != nil {
		return archived, wrapStateStoreError("list archived Workspace sandboxes", err)
	}
	var terminationErrors []error
	if s.syncFiles != nil {
		if err := s.syncFiles.Drain(ctx, workspaceID); err != nil {
			terminationErrors = append(terminationErrors, err)
		}
	}
	for _, machine := range machines {
		if machine.WorkspaceID != workspaceID {
			continue
		}
		if _, terminateErr := s.Terminate(ctx, machine.ID); terminateErr != nil {
			terminationErrors = append(terminationErrors, fmt.Errorf("terminate %s: %w", machine.ID, terminateErr))
		}
	}
	return archived, errors.Join(terminationErrors...)
}

// PurgeWorkspaceForUser archives a Workspace, drains its Sandboxes, removes
// its durable JuiceFS directory, and finally erases runtime history and
// encrypted Message material. Each step is idempotent so a failed control-plane
// attempt can safely retry from the beginning.
func (s *Service) PurgeWorkspaceForUser(ctx context.Context, userID, appID, workspaceID string) error {
	if _, err := s.ArchiveWorkspaceForUser(ctx, userID, appID, workspaceID); err != nil {
		return err
	}
	purger, ok := s.supervisor.(WorkspaceDataPurger)
	if !ok {
		return errors.New("Workspace data purge is unavailable")
	}
	if err := purger.PurgeWorkspace(ctx, workspaceID); err != nil {
		return fmt.Errorf("purge Workspace data: %w", err)
	}
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	if store, ok := s.store.(managedStore); ok {
		if err := store.ReleaseManagedStorage(workspaceID); err != nil {
			return err
		}
	}
	if err := s.store.PurgeWorkspaceMetadata(workspaceID); err != nil {
		return wrapStateStoreError("purge Workspace metadata", err)
	}
	return nil
}

func (s *Service) ResolveWorkspaceRuntime(workspaceID string) (WorkspaceRuntimeConfig, error) {
	config, err := s.GetWorkspaceConfig(workspaceID)
	if err != nil {
		return WorkspaceRuntimeConfig{}, err
	}
	if config.Workspace.ArchivedAt != nil || config.SessionRuntime.ActiveBuildID == "" {
		return WorkspaceRuntimeConfig{}, fmt.Errorf("%w: %s", ErrWorkspaceUnavailable, workspaceID)
	}
	return config, nil
}

func (s *Service) GetWorkspaceConfig(workspaceID string) (WorkspaceRuntimeConfig, error) {
	workspace, err := s.store.GetWorkspace(workspaceID)
	if err != nil {
		return WorkspaceRuntimeConfig{}, err
	}
	app, err := s.store.GetAppRecord(workspace.AppID)
	if err != nil {
		return WorkspaceRuntimeConfig{}, err
	}
	sessionRuntime, err := s.store.GetAppSessionRuntime(workspace.AppID)
	if err != nil {
		return WorkspaceRuntimeConfig{}, err
	}
	return WorkspaceRuntimeConfig{App: app, Workspace: workspace, SessionRuntime: sessionRuntime}, nil
}

func (s *Service) GetWorkspaceConfigByAppIDAndSlug(appID, workspaceSlug string) (WorkspaceRuntimeConfig, error) {
	workspace, err := s.store.GetWorkspaceByAppIDAndSlug(appID, workspaceSlug)
	if err != nil {
		return WorkspaceRuntimeConfig{}, err
	}
	return s.GetWorkspaceConfig(workspace.ID)
}

func (s *Service) ListWorkspaceConfigs(appID string) ([]WorkspaceRuntimeConfig, error) {
	workspaces, err := s.store.ListWorkspaces(appID)
	if err != nil {
		return nil, err
	}
	configs := make([]WorkspaceRuntimeConfig, len(workspaces))
	for i := range workspaces {
		configs[i], err = s.GetWorkspaceConfig(workspaces[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return configs, nil
}
