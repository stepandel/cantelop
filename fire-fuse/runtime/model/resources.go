package model

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

const routingSlugSeparator = "--"

var (
	ErrAlreadyExists = errors.New("resource already exists")
	ErrInvalidApp    = errors.New("invalid app")
	ErrAppNotFound   = errors.New("app not found")
	appSlugPattern   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

// App is the aggregate root for Builds, its Session runtime, and Workspaces.
// Build and Session runtime fields live in normalized records.
type App struct {
	Managed   bool      `json:"managed,omitempty"`
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (a App) Validate() error {
	if ValidateResourceID(a.ID) != nil || !ValidRoutingSlug(a.Slug) ||
		a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() || a.UpdatedAt.Before(a.CreatedAt) {
		return ErrInvalidApp
	}
	return nil
}

func ValidRoutingSlug(slug string) bool {
	return appSlugPattern.MatchString(slug) && !strings.Contains(slug, routingSlugSeparator)
}
