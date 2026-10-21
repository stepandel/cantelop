package model

import (
	"errors"
	"regexp"
	"time"
)

var (
	ErrInvalidWorkLease  = errors.New("invalid Session work lease")
	ErrWorkLeaseNotFound = errors.New("Session work lease not found")
	workLeaseIDPattern   = regexp.MustCompile(`^lease_[0-9a-f]{32}$`)
)

const (
	MinSessionWorkLeaseTTL = time.Second
	MaxSessionWorkLeaseTTL = 5 * time.Minute
)

type SessionWorkLease struct {
	SessionID string    `json:"session_id"`
	LeaseID   string    `json:"lease_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (value SessionWorkLease) Validate() error {
	if ValidateSessionID(value.SessionID) != nil || !workLeaseIDPattern.MatchString(value.LeaseID) ||
		value.CreatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) || !value.ExpiresAt.After(value.UpdatedAt) {
		return ErrInvalidWorkLease
	}
	return nil
}

func ValidateWorkLeaseID(value string) error {
	if !workLeaseIDPattern.MatchString(value) {
		return ErrInvalidWorkLease
	}
	return nil
}
