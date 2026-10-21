package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

func TestProjectInitRefusesToOverwriteManifest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, projectconfig.DefaultPath)
	if err := os.WriteFile(path, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runProjectInit([]string{"-app", "vera", "-provider", "openai", "-config", path}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("runProjectInit() error = %v", err)
	}
	document, readErr := os.ReadFile(path)
	if readErr != nil || string(document) != "keep me" {
		t.Fatalf("existing manifest changed: %q, %v", document, readErr)
	}
}

func TestProjectInitCreatesProviderStarters(t *testing.T) {
	for _, template := range []string{"openai", "claude", "pi"} {
		t.Run(template, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			var output bytes.Buffer
			if err := runProjectInit([]string{"-app", "vera", "-provider", template}, &output); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{projectconfig.DefaultPath, "src/cantelop.ts", "src/agent.ts", "package.json"} {
				if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
					t.Errorf("generated %s: %v", path, err)
				}
			}
			manifest, err := os.ReadFile(projectconfig.DefaultPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(manifest), `"$schema"`) {
				t.Fatalf("generated manifest exposes schema selection: %s", manifest)
			}
			if !strings.Contains(string(manifest), `"definition": "src/cantelop.ts"`) {
				t.Fatalf("generated manifest must require the App authentication public key: %s", manifest)
			}
			session, err := os.ReadFile("src/agent.ts")
			if err != nil {
				t.Fatal(err)
			}
			api, err := os.ReadFile("src/cantelop.ts")
			if err != nil {
				t.Fatal(err)
			}
			packageDocument, err := os.ReadFile("package.json")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(session), "SessionContext") ||
				!strings.Contains(string(api), "cantelop.app<{ prompt: string }>({") ||
				strings.Contains(string(packageDocument), "@cantelop/sdk") ||
				!strings.Contains(output.String(), "npm install @cantelop/sdk, then") ||
				strings.Contains(output.String(), "@cantelop/sdk@") ||
				!strings.Contains(output.String(), template+" starter") {
				t.Fatalf("session/output = %s / %q", session, output.String())
			}
			if _, err := projectconfig.Load(projectconfig.DefaultPath); err == nil || !strings.Contains(err.Error(), "@cantelop/sdk is not installed") {
				t.Fatalf("generated manifest is invalid: %v", err)
			}
		})
	}
}

func TestProjectInitSupportsCustomAPIAndConfigPaths(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "project", "deploy.json")
	if err := runProjectInit([]string{
		"-app", "vera", "-provider", "pi", "-config", config, "-definition", "edge/index.ts",
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{config, filepath.Join(root, "project/edge/index.ts"), filepath.Join(root, "project/edge/agent.ts")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("generated %s: %v", path, err)
		}
	}
}

func TestProjectInitTemplateRefusesPartialOverwrite(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(root, "src", "cantelop.ts")
	if err := os.WriteFile(existing, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, projectconfig.DefaultPath)
	err := runProjectInit([]string{"-app", "vera", "-provider", "claude", "-config", config}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("runProjectInit() error = %v", err)
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatalf("manifest was created after preflight failure: %v", err)
	}
	document, _ := os.ReadFile(existing)
	if string(document) != "keep me" {
		t.Fatalf("existing source changed: %q", document)
	}
}

func TestProjectInitRejectsInvalidTargetsAndPaths(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"-app", "Bad App"},
		{"-app", "vera"},
		{"-app", "vera", "-provider", "openai", "-definition", "../api.ts"},
		{"-app", "vera", "-provider", "openai", "-definition", "src/agent.ts"},
		{"-app", "vera", "-provider", "/tmp/session.ts"},
		{"-app", "vera", "-provider", "unknown"},
		{"-app", "vera", "-template", "pi"},
	} {
		if err := runProjectInit(args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "usage: cantelop init") {
			t.Errorf("runProjectInit(%q) error = %v", args, err)
		}
	}
}
