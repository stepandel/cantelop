package microvm

import (
	"errors"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

var (
	ErrInvalidComputeTenant = model.ErrInvalidComputeTenant
	ErrComputeTenantMissing = model.ErrComputeTenantMissing
	ErrProviderCapacity     = errors.New("compute provider capacity exceeded")
)

type ComputeTenant = model.ComputeTenant

func ValidateComputeTenantID(id string) error {
	return model.ValidateComputeTenantID(id)
}

// ComputeProjectionStore persists only the provider-local ownership and
// runtime projections required to execute control-plane commands.
type ComputeProjectionStore interface {
	CancelAppBuildUpload(tenantID, appID, operationID string, at time.Time) (AppBuildOperation, error)
	EnsureComputeTenant(ComputeTenant) (bool, error)
	GetComputeTenant(string) (ComputeTenant, error)
	CreateAppForComputeTenant(tenantID string, app App, sessionRuntime AppSessionRuntime, build AppBuild) error
	CreateProvisioningAppForComputeTenant(tenantID string, app App, sessionRuntime AppSessionRuntime) error
	BeginAppBuildUpload(tenantID string, operation AppBuildOperation) error
	GetAppBuildOperationForComputeTenant(tenantID, appID, operationID string) (AppBuildOperation, error)
	CompletePreparedAppBuild(operationID, buildID string, at time.Time) (AppBuildOperation, AppBuild, error)
	SelectAppBuildForComputeTenant(tenantID, appID, buildID string, at time.Time) error
	ArchiveAppForComputeTenant(tenantID, appID string, at time.Time) (bool, error)
	GetAppForComputeTenant(tenantID, appID string) (App, error)
	OwnsAppForComputeTenant(tenantID, appID string) error
}
