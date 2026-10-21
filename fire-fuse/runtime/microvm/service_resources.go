package microvm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

func newResourceID(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate resource ID: %w", err)
	}
	return prefix + hex.EncodeToString(random[:]), nil
}

// CreateAppSpec is the complete immutable definition of a routable App.
type CreateAppSpec struct {
	Managed        bool
	ID             string
	Slug           string
	BuildID        string
	SessionRuntime AppSessionRuntime
}

// RegisterComputeAppSpec is provider desired state supplied by the external
// control plane. Every identity is explicit so replaying the command cannot
// create a second provider resource.
type RegisterComputeAppSpec struct {
	CreateAppSpec
}

// RegisterComputeAppForUser idempotently installs one provisioning App on
// this compute provider. userID is treated strictly as an opaque ownership key
// projected by the control plane.
func (s *Service) RegisterComputeAppForUser(userID string, desired RegisterComputeAppSpec) (App, bool, error) {
	if s.managedOnly && !desired.Managed {
		return App{}, false, ErrManagedAdmission
	}
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	if err := ValidateComputeTenantID(userID); err != nil {
		return App{}, false, err
	}
	if _, err := s.store.EnsureComputeTenant(ComputeTenant{ID: userID, CreatedAt: s.now().UTC()}); err != nil {
		return App{}, false, s.classifyComputeRegistrationError(err)
	}

	app, err := s.store.GetAppForComputeTenant(userID, desired.ID)
	if errors.Is(err, ErrAppNotFound) {
		app, err = s.createApp(userID, desired.CreateAppSpec)
		return app, err == nil, s.classifyComputeRegistrationError(err)
	}
	if err != nil {
		return App{}, false, wrapStateStoreError("read compute App", err)
	}
	sessionRuntime, sessionRuntimeErr := s.store.GetAppSessionRuntime(app.ID)
	wantedSessionRuntime := desired.SessionRuntime.WithDefaults()
	if sessionRuntimeErr != nil || app.Slug != desired.Slug || app.Managed != desired.Managed ||
		sessionRuntime.ActiveBuildID != desired.BuildID ||
		sessionRuntime.Runtime != wantedSessionRuntime.Runtime {
		return App{}, false, ErrAlreadyExists
	}
	return app, false, nil
}

func (s *Service) classifyComputeRegistrationError(err error) error {
	if err == nil || errors.Is(err, ErrInvalidComputeTenant) ||
		errors.Is(err, ErrInvalidApp) || errors.Is(err, ErrInvalidWorkspace) ||
		errors.Is(err, ErrInvalidAppSessionRuntime) || errors.Is(err, ErrInvalidWorkspace) ||
		errors.Is(err, ErrAlreadyExists) || errors.Is(err, ErrUnknownImage) ||
		errors.Is(err, ErrUnknownProfile) || errors.Is(err, ErrUnsupportedShape) {
		return err
	}
	return wrapStateStoreError("register compute App", err)
}

// ArchiveAppForUser removes one projected App from routing,
// then terminates its Sandboxes. Ownership, request history, and workspace data
// remain persisted for audit and operator-controlled retention.
func (s *Service) ArchiveAppForUser(ctx context.Context, userID, appID string) (bool, error) {
	archived, sandboxIDs, err := s.archiveAppRecord(userID, appID)
	if err != nil {
		return archived, err
	}
	// Terminate outside resourceMu. Each Terminate blocks on guest shutdown and
	// host teardown, and the exclusive lock would hold off every other tenant's
	// Create, workspace change, and administration read for the sum of those
	// timeouts. Only the record change above needs the lock.
	var terminationErrors []error
	if s.syncFiles != nil {
		workspaces, listErr := s.store.ListWorkspaces(appID)
		if listErr != nil {
			terminationErrors = append(terminationErrors, listErr)
		}
		for _, workspace := range workspaces {
			if err := s.syncFiles.Drain(ctx, workspace.ID); err != nil {
				terminationErrors = append(terminationErrors, err)
			}
		}
	}
	for _, sandboxID := range sandboxIDs {
		if _, terminateErr := s.Terminate(ctx, sandboxID); terminateErr != nil {
			terminationErrors = append(terminationErrors, fmt.Errorf("terminate %s: %w", sandboxID, terminateErr))
		}
	}
	return archived, errors.Join(terminationErrors...)
}

// archiveAppRecord archives the App and returns the Sandboxes left to terminate.
func (s *Service) archiveAppRecord(userID, appID string) (bool, []string, error) {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	if _, err := s.store.GetComputeTenant(userID); err != nil {
		return false, nil, err
	}
	archived, err := s.store.ArchiveAppForComputeTenant(userID, appID, s.now().UTC())
	if err != nil {
		if errors.Is(err, ErrAppNotFound) || errors.Is(err, ErrAppBuildUploadPending) {
			return false, nil, err
		}
		return false, nil, wrapStateStoreError("archive app", err)
	}
	machines, err := s.store.ListActive()
	if err != nil {
		return archived, nil, wrapStateStoreError("list archived app sandboxes", err)
	}
	var sandboxIDs []string
	for _, machine := range machines {
		if machine.AppID != appID {
			continue
		}
		sandboxIDs = append(sandboxIDs, machine.ID)
	}
	return archived, sandboxIDs, nil
}

func (s *Service) createApp(userID string, spec CreateAppSpec) (App, error) {
	now := s.now().UTC()
	app := App{ID: spec.ID, Slug: spec.Slug, Managed: spec.Managed, CreatedAt: now, UpdatedAt: now}
	sessionRuntime := spec.SessionRuntime.WithDefaults()
	sessionRuntime.ActiveBuildID = ""
	if err := sessionRuntime.Validate(); err != nil {
		return App{}, err
	}
	if spec.BuildID != "" {
		if validator, ok := s.supervisor.(BuildValidator); ok {
			if err := validator.ValidateBuild(spec.BuildID); err != nil {
				return App{}, classifyRuntimeStartError(err)
			}
		}
		sessionRuntime.ActiveBuildID = spec.BuildID
		build := AppBuild{AppID: spec.ID, Version: 1, BuildID: spec.BuildID, CreatedAt: now}
		if err := s.store.CreateAppForComputeTenant(userID, app, sessionRuntime, build); err != nil {
			return App{}, s.classifyCreateAppError(err)
		}
		return app, nil
	}
	if err := s.store.CreateProvisioningAppForComputeTenant(userID, app, sessionRuntime); err != nil {
		return App{}, s.classifyCreateAppError(err)
	}
	return app, nil
}

func (s *Service) classifyCreateAppError(err error) error {
	if errors.Is(err, ErrInvalidApp) || errors.Is(err, ErrInvalidWorkspace) ||
		errors.Is(err, ErrInvalidAppBuild) || errors.Is(err, ErrInvalidAppSessionRuntime) ||
		errors.Is(err, ErrInvalidWorkspace) || errors.Is(err, ErrAlreadyExists) {
		return err
	}
	return wrapStateStoreError("create app", err)
}

// BeginOrGetAppBuildUpload starts a provider-command Build with a stable ID or
// returns the existing operation. The caller owns archive delivery only when
// created is true.
func (s *Service) BeginOrGetAppBuildUpload(userID, appID, operationID string) (operation AppBuildOperation, created bool, err error) {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	if _, err := s.store.GetComputeTenant(userID); err != nil {
		return AppBuildOperation{}, false, err
	}
	if !ValidAppBuildOperationID(operationID) || !resourceIDPattern.MatchString(appID) {
		return AppBuildOperation{}, false, ErrInvalidAppBuildOperation
	}
	existing, getErr := s.store.GetAppBuildOperationForComputeTenant(userID, appID, operationID)
	if getErr == nil {
		return existing, false, nil
	}
	if !errors.Is(getErr, ErrAppBuildOperationNotFound) {
		return AppBuildOperation{}, false, wrapStateStoreError("read idempotent App Build upload", getErr)
	}
	now := s.now().UTC()
	operation = AppBuildOperation{
		ID: operationID, AppID: appID, Status: AppBuildOperationReceiving,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.BeginAppBuildUpload(userID, operation); err != nil {
		if errors.Is(err, ErrAppNotFound) || errors.Is(err, ErrAppBuildUploadPending) ||
			errors.Is(err, ErrAppBuildQuota) || errors.Is(err, ErrInvalidAppBuildOperation) {
			return AppBuildOperation{}, false, err
		}
		return AppBuildOperation{}, false, wrapStateStoreError("begin idempotent App Build upload", err)
	}
	return operation, true, nil
}

func (s *Service) CompletePreparedAppBuild(operationID, buildID string) (AppBuildOperation, AppBuild, error) {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	return s.store.CompletePreparedAppBuild(operationID, buildID, s.now().UTC())
}

func (s *Service) SelectAppBuildForComputeTenant(tenantID, appID, buildID string) error {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	gate, err := s.store.GetReleaseAdmissionForComputeTenant(tenantID, appID)
	if err != nil {
		return err
	}
	if !gate.Closed {
		return ErrReleaseAdmissionConflict
	}
	return s.store.SelectAppBuildForComputeTenant(tenantID, appID, buildID, s.now().UTC())
}

func (s *Service) GetActiveAppBuildForComputeTenant(tenantID, appID string) (string, error) {
	s.resourceMu.RLock()
	defer s.resourceMu.RUnlock()
	if _, err := s.store.GetReleaseAdmissionForComputeTenant(tenantID, appID); err != nil {
		return "", err
	}
	runtime, err := s.store.GetAppSessionRuntime(appID)
	if err != nil {
		return "", err
	}
	return runtime.ActiveBuildID, nil
}

// GetAppBuildForComputeTenant returns one Build still attached to a tenant's
// App. Only attached Builds can be selected, so a rollback can check its target
// before closing release admission.
func (s *Service) GetAppBuildForComputeTenant(tenantID, appID, buildID string) (AppBuild, error) {
	s.resourceMu.RLock()
	defer s.resourceMu.RUnlock()
	if _, err := s.store.GetReleaseAdmissionForComputeTenant(tenantID, appID); err != nil {
		return AppBuild{}, err
	}
	builds, err := s.store.ListAppBuildsForApp(appID)
	if err != nil {
		return AppBuild{}, err
	}
	for _, build := range builds {
		if build.BuildID == buildID {
			return build, nil
		}
	}
	return AppBuild{}, fmt.Errorf("%w: %s", ErrAppBuildNotFound, buildID)
}

func (s *Service) GetAppSessionRuntime(appID string) (AppSessionRuntime, error) {
	return s.store.GetAppSessionRuntime(appID)
}

func (s *Service) ListAppBuildsForApp(appID string) ([]AppBuild, error) {
	return s.store.ListAppBuildsForApp(appID)
}

func (s *Service) GetAppResource(id string) (App, error) {
	return s.store.GetAppRecord(id)
}

func (s *Service) ListAppResources() ([]App, error) {
	return s.store.ListApps()
}

func (s *Service) CancelAppBuildUpload(userID, appID, operationID string) (AppBuildOperation, error) {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	return s.store.CancelAppBuildUpload(userID, appID, operationID, s.now().UTC())
}
