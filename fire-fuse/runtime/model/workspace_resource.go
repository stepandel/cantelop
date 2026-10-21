package model

import (
	"errors"
	"time"
)

var (
	ErrInvalidWorkspace  = errors.New("invalid workspace")
	ErrWorkspaceNotFound = errors.New("workspace not found")
)

// Workspace is one addressable, persistent installation of an App.
type Workspace struct {
	ID         string     `json:"id"`
	AppID      string     `json:"app_id"`
	Slug       string     `json:"slug"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
}

func (e Workspace) Validate() error {
	if ValidateWorkspaceID(e.ID) != nil || ValidateResourceID(e.AppID) != nil || !ValidRoutingSlug(e.Slug) ||
		e.CreatedAt.IsZero() || e.UpdatedAt.IsZero() || e.UpdatedAt.Before(e.CreatedAt) {
		return ErrInvalidWorkspace
	}
	return nil
}

type WorkspaceRuntimeConfig struct {
	App            App               `json:"app"`
	Workspace      Workspace         `json:"workspace"`
	SessionRuntime AppSessionRuntime `json:"session_runtime"`
}
