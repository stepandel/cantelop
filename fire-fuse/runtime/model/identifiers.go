package model

import (
	"errors"
	"regexp"
)

var (
	buildIDPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	sessionIDRule     = regexp.MustCompile(`^ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$`)
)

func ValidateResourceID(id string) error {
	if !resourceIDPattern.MatchString(id) {
		return errors.New("invalid resource ID")
	}
	return nil
}

// ValidateBuildID requires the canonical content-addressed catalog identity.
func ValidateBuildID(buildID string) error {
	if !buildIDPattern.MatchString(buildID) {
		return errors.New("build ID must be sha256:<64 lowercase hexadecimal digits>")
	}
	return nil
}

// ValidateSessionID checks the public runtime identity shared by
// Runtime, Gateway, and provider adapters.
func ValidateSessionID(id string) error {
	if !sessionIDRule.MatchString(id) {
		return errors.New("session ID must use the canonical ses_ prefix")
	}
	return nil
}
