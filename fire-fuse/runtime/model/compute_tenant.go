package model

import (
	"errors"
	"regexp"
	"time"
)

var (
	ErrInvalidComputeTenant = errors.New("invalid compute tenant")
	ErrComputeTenantMissing = errors.New("compute tenant not found")
	computeTenantIDPattern  = regexp.MustCompile(`^usr_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$`)
)

// ComputeTenant is an opaque ownership key projected by Console. Runtime
// deliberately stores no identity, account status, or quota data.
type ComputeTenant struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

func (t ComputeTenant) Validate() error {
	if ValidateComputeTenantID(t.ID) != nil || t.CreatedAt.IsZero() {
		return ErrInvalidComputeTenant
	}
	return nil
}

func ValidateComputeTenantID(id string) error {
	if !computeTenantIDPattern.MatchString(id) {
		return ErrInvalidComputeTenant
	}
	return nil
}
