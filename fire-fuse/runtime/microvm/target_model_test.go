package microvm

import (
	"errors"
	"testing"
	"time"
)

func TestAppSessionRuntimeValidation(t *testing.T) {
	valid := AppSessionRuntime{
		ActiveBuildID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Runtime: AppRuntimeSpec{
			Profile: DefaultSandboxProfileName, InternalPort: 3000,
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("AppSessionRuntime.Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*AppSessionRuntime)
	}{
		{name: "invalid port", mutate: func(h *AppSessionRuntime) { h.Runtime.InternalPort = 0 }},
		{name: "unsupported profile", mutate: func(h *AppSessionRuntime) { h.Runtime.Profile = "large" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := candidate.Validate(); !errors.Is(err, ErrInvalidAppSessionRuntime) {
				t.Fatalf("AppSessionRuntime.Validate() error = %v, want ErrInvalidAppSessionRuntime", err)
			}
		})
	}
	provisioning := valid
	provisioning.ActiveBuildID = ""
	if err := provisioning.Validate(); err != nil {
		t.Fatalf("provisioning AppSessionRuntime.Validate() error = %v", err)
	}
	if defaulted := valid.WithDefaults(); defaulted != valid {
		t.Fatalf("defaulted App Session runtime = %#v", defaulted)
	}
}

func TestSandboxProfileIsFixedPlatformShape(t *testing.T) {
	profile, err := ResolveSandboxProfile(DefaultSandboxProfileName)
	if err != nil {
		t.Fatalf("ResolveSandboxProfile(default) error = %v", err)
	}
	if profile.Name != DefaultSandboxProfileName || profile.VCPUs != 4 || profile.MemoryMiB != 512 {
		t.Fatalf("default Sandbox profile = %#v, want default/4/512", profile)
	}
	if _, err := ResolveSandboxProfile("large"); !errors.Is(err, ErrUnsupportedSandboxProfile) {
		t.Fatalf("ResolveSandboxProfile(large) error = %v, want ErrUnsupportedSandboxProfile", err)
	}
}

func TestWorkspaceValidation(t *testing.T) {
	createdAt := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	workspace := Workspace{
		ID: "wsp_editor", AppID: "app_editor", Slug: "editor-alice",
		CreatedAt: createdAt, UpdatedAt: createdAt.Add(2 * time.Minute),
	}
	if err := workspace.Validate(); err != nil {
		t.Fatalf("Workspace.Validate() error = %v", err)
	}
	invalid := workspace
	invalid.Slug = "Not_DNS"
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidWorkspace) {
		t.Fatalf("Workspace.Validate() error = %v, want ErrInvalidWorkspace", err)
	}
	invalid.Slug = "editor--alice"
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidWorkspace) {
		t.Fatalf("Workspace double-separator slug error = %v, want ErrInvalidWorkspace", err)
	}
}

func TestAppRejectsRoutingSeparatorInSlug(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	app := App{ID: "app_editor", Slug: "text--editor", CreatedAt: now, UpdatedAt: now}
	if err := app.Validate(); !errors.Is(err, ErrInvalidApp) {
		t.Fatalf("App.Validate() error = %v, want ErrInvalidApp", err)
	}
}

func TestSessionValidation(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	instance := Session{
		ID: "ses_editor01", WorkspaceID: "wsp_editor",
		BuildID:          "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		KeepAliveSeconds: 5 * 60,
		CreatedAt:        now, UpdatedAt: now,
	}
	if err := instance.Validate(); err != nil {
		t.Fatalf("Session.Validate() error = %v", err)
	}

	instance.State = "unknown"
	if err := instance.Validate(); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Session.Validate() error = %v, want ErrInvalidSession", err)
	}
}
