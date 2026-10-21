package model

import (
	"testing"
	"time"
)

func TestAppWorkspaceAggregateValidation(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	app := App{ID: "app-1", Slug: "editor", CreatedAt: now, UpdatedAt: now}
	workspace := Workspace{ID: "wsp_primary", AppID: app.ID, Slug: "production", CreatedAt: now, UpdatedAt: now}
	for name, validate := range map[string]func() error{"app": app.Validate, "workspace": workspace.Validate} {
		if err := validate(); err != nil {
			t.Fatalf("%s Validate() = %v", name, err)
		}
	}
}
