package model

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

const maxConfigValueBytes = 64 * 1024

var (
	ErrInvalidConfigValue       = errors.New("invalid configuration value")
	ErrConfigQuotaExceeded      = errors.New("configuration quota exceeded")
	ErrConfigKindConflict       = errors.New("configuration name already uses another kind")
	ErrConfigEncryptionRequired = errors.New("configuration secret encryption is not configured")
	ErrConfigDecryption         = errors.New("could not decrypt configuration secret")
	ErrStaleConfigProjection    = errors.New("configuration projection revision is stale")
	configNamePattern           = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

type ConfigValueKind string

const (
	ConfigValueVariable ConfigValueKind = "variable"
	ConfigValueSecret   ConfigValueKind = "secret"
)

// ConfigValue is the internal plaintext representation consumed when a new
// Session is configured. API projections must redact Value for secrets.
type ConfigValue struct {
	Name      string
	Kind      ConfigValueKind
	Value     string
	UpdatedAt time.Time
}

func (v ConfigValue) Validate() error {
	if ValidateConfigName(v.Name) != nil ||
		(v.Kind != ConfigValueVariable && v.Kind != ConfigValueSecret) ||
		len(v.Value) > maxConfigValueBytes || strings.IndexByte(v.Value, 0) >= 0 ||
		v.UpdatedAt.IsZero() {
		return ErrInvalidConfigValue
	}
	return nil
}

func ValidateConfigName(name string) error {
	if !configNamePattern.MatchString(name) || strings.HasPrefix(name, "CANTELOP_") {
		return ErrInvalidConfigValue
	}
	return nil
}

func ValidConfigName(name string) bool { return configNamePattern.MatchString(name) }
