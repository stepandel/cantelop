package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

func TestDeploymentEnvironmentUsesManifest(t *testing.T) {
	root := t.TempDir()
	fallback := "local-only"
	config := projectconfig.Config{Root: root, Environment: map[string]projectconfig.EnvironmentDeclaration{
		"TOKEN": {Required: true, Secret: true}, "EMPTY": {Required: true}, "OPTIONAL": {Default: &fallback},
	}}
	file := filepath.Join(root, ".env")
	if err := os.WriteFile(file, []byte("TOKEN='private=value'\nEMPTY=\nEXTRA=ignored\n"), 0600); err != nil {
		t.Fatal(err)
	}
	updates, err := loadProjectDeploymentEnvironment(config, ".env")
	if err != nil || len(updates) != 2 {
		t.Fatalf("updates=%v err=%v", updates, err)
	}
	if updates[0] != (configUpdate{name: "EMPTY", value: "", kind: "variable"}) || updates[1] != (configUpdate{name: "TOKEN", value: "private=value", kind: "secret"}) {
		t.Fatal("incorrect manifest mapping")
	}
	config.Environment["OPTIONAL"] = projectconfig.EnvironmentDeclaration{Required: true, Default: &fallback}
	if _, err := loadProjectDeploymentEnvironment(config, ".env"); err == nil || !strings.Contains(err.Error(), "OPTIONAL") {
		t.Fatalf("missing required variable: %v", err)
	}
	if _, err := loadProjectDeploymentEnvironment(config, "missing.env"); err == nil {
		t.Fatal("accepted missing file")
	}
	config.Environment = map[string]projectconfig.EnvironmentDeclaration{"TOKEN": {Required: true}}
	if err := os.WriteFile(file, []byte("TOKEN="+strings.Repeat("x", projectconfig.MaxConfigInputBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProjectDeploymentEnvironment(config, ".env"); err == nil {
		t.Fatal("accepted oversized value")
	}
}

func TestDeployEnvironmentFlag(t *testing.T) {
	options, err := parseProjectDeployOptions([]string{"--env-file", ".env", "--dry-run"})
	if err != nil || options.envFile != ".env" || !options.dryRun {
		t.Fatalf("options=%v err=%v", options, err)
	}
	if _, err := parseProjectDeployOptions([]string{"--env-file", ""}); err == nil {
		t.Fatal("accepted empty env file")
	}
	options, err = parseProjectDeployOptions(nil)
	if err != nil || options.envFile != "" {
		t.Fatal("default deployment must not implicitly import a file")
	}
}
