package model

import (
	"errors"
	"fmt"
	"regexp"
)

const MaxWorkspaceIDLength = 64

var (
	ErrInvalidWorkspaceID = errors.New("invalid workspace ID")
	workspaceIDPattern    = regexp.MustCompile(`^wsp_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$`)
)

// ValidateWorkspaceID checks the canonical public Workspace identity.
func ValidateWorkspaceID(id string) error {
	if len(id) > MaxWorkspaceIDLength || !workspaceIDPattern.MatchString(id) {
		return fmt.Errorf("%w: %q", ErrInvalidWorkspaceID, id)
	}
	return nil
}
