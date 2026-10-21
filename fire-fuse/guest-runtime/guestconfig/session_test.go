package guestconfig

import (
	"errors"
	"slices"
	"testing"
)

func managedConfig() Config {
	return Config{SchemaVersion: SchemaVersion, Process: Process{
		Args: []string{PlatformRuntimeContainerPath, SessionModule},
		Env:  SessionEnvironment(), WorkingDirectory: SessionWorkspace,
		UID: SessionUID, GID: SessionGID,
	}, Workspace: Workspace{Enabled: true, ContainerPath: SessionWorkspace}}
}

func TestManagedSessionContractRejectsRuntimeOverrides(t *testing.T) {
	if err := managedConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"uid":           func(c *Config) { c.Process.UID = 1234 },
		"gid":           func(c *Config) { c.Process.GID = 1234 },
		"cwd":           func(c *Config) { c.Process.WorkingDirectory = "/app" },
		"entrypoint":    func(c *Config) { c.Process.Args[0] = "/app/server" },
		"module":        func(c *Config) { c.Process.Args[1] = "/app/server.mjs" },
		"home":          func(c *Config) { c.Process.Env[1] = "HOME=/root" },
		"version":       func(c *Config) { c.Process.Env[0] = SessionContractEnvironment + "=2" },
		"missing-cache": func(c *Config) { c.Process.Env = c.Process.Env[:len(c.Process.Env)-1] },
		"mount":         func(c *Config) { c.Workspace.ContainerPath = "/data" },
	} {
		t.Run(name, func(t *testing.T) {
			config := managedConfig()
			change(&config)
			if err := config.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}

func TestManagedEnvironmentWinsOverAppDefaults(t *testing.T) {
	config := managedConfig()
	merged, err := MergeProcessEnvironment(config.Process, []string{"HOME=/app", "TMPDIR=/bad", "MODEL=custom", "CANTELOP_INTERNAL_PORT=12345"})
	if err != nil {
		t.Fatal(err)
	}
	config.Process.Env = merged
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(merged, "MODEL=custom") || !slices.Contains(merged, "CANTELOP_INTERNAL_PORT=12345") {
		t.Fatal(merged)
	}
	legacy := Process{Env: []string{"HOME=/legacy"}}
	merged, err = MergeProcessEnvironment(legacy, []string{"HOME=/custom"})
	if err != nil || !slices.Equal(merged, []string{"HOME=/custom"}) {
		t.Fatalf("legacy environment = %v, %v", merged, err)
	}
}
