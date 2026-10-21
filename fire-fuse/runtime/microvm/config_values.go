package microvm

import "github.com/stepandel/cantelop/fire-fuse/runtime/model"

var (
	ErrInvalidConfigValue       = model.ErrInvalidConfigValue
	ErrConfigQuotaExceeded      = model.ErrConfigQuotaExceeded
	ErrConfigKindConflict       = model.ErrConfigKindConflict
	ErrConfigEncryptionRequired = model.ErrConfigEncryptionRequired
	ErrConfigDecryption         = model.ErrConfigDecryption
	ErrStaleConfigProjection    = model.ErrStaleConfigProjection
)

type ConfigValueKind = model.ConfigValueKind

const (
	ConfigValueVariable = model.ConfigValueVariable
	ConfigValueSecret   = model.ConfigValueSecret
)

type ConfigValue = model.ConfigValue

// ConfigStore persists configuration shared by every runtime of an App. Secret
// values are returned internally for runtime assembly but must never be
// exposed by an API listing operation.
type ConfigStore interface {
	SetAppConfig(appID string, value ConfigValue) error
	ListAppConfig(appID string) ([]ConfigValue, error)
	DeleteAppConfig(appID, name string) (bool, error)
	ResolveAppConfig(appID string) ([]string, error)
	ReplaceAppConfig(appID string, revision int64, values []ConfigValue) (bool, error)
}
