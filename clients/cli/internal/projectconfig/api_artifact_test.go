package projectconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAPIArtifactRejectsUnknownManifestAndOversizedModule(t *testing.T) {
	directory := t.TempDir()
	manifest := `{"schema_version":2,"kind":"cantelop-edge-api","main_module":"worker.mjs","provider":"cloudflare"}`
	if err := os.WriteFile(filepath.Join(directory, "cantelop-api.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.mjs"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAPIArtifact(directory); err == nil {
		t.Fatal("artifact loader accepted provider-specific manifest data")
	}
	manifest = `{"schema_version":2,"kind":"cantelop-edge-api","main_module":"worker.mjs"}`
	if err := os.WriteFile(filepath.Join(directory, "cantelop-api.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.mjs"), bytes.Repeat([]byte("x"), maxAPIModuleBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAPIArtifact(directory); err == nil {
		t.Fatal("artifact loader accepted oversized module")
	}
}

func TestLoadQualifiedSDKAPIArtifact(t *testing.T) {
	directory := os.Getenv("CANTELOP_QUALIFICATION_API_ARTIFACT")
	if directory == "" {
		t.Skip("adjacent SDK artifact qualification is not running")
	}
	artifact, err := LoadAPIArtifact(directory)
	if err != nil {
		t.Fatalf("load SDK artifact: %v", err)
	}
	if (artifact.Manifest.Kind != "cantelop-edge-api" && artifact.Manifest.Kind != "cantelop-protocol-edge") || artifact.Manifest.MainModule != "worker.mjs" ||
		len(artifact.Module) == 0 {
		t.Fatalf("SDK artifact kind=%q main=%q bytes=%d", artifact.Manifest.Kind, artifact.Manifest.MainModule, len(artifact.Module))
	}
}

func TestLoadAPIArtifactAcceptsIntegrationRotationBindings(t *testing.T) {
	load := func(optional string) (APIArtifact, error) {
		directory := t.TempDir()
		manifest := `{"schema_version":1,"kind":"cantelop-protocol-edge","main_module":"worker.mjs","cli_build_protocol_version":6,"integration_protocol_version":2,` +
			`"session_runtime_id":"agent.v1","app_name":"agent","required_bindings":["CANTELOP_INTEGRATION_TOKEN"],` + optional +
			`"default_keep_alive_binding":"CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS","webhooks":[],"homepage":false}`
		if err := os.WriteFile(filepath.Join(directory, "cantelop-edge.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "worker.mjs"), []byte("export default {};"), 0o644); err != nil {
			t.Fatal(err)
		}
		return LoadAPIArtifact(directory)
	}
	if _, err := load(""); err != nil {
		t.Fatalf("manifest without rotation bindings: %v", err)
	}
	artifact, err := load(`"optional_bindings":["CANTELOP_INTEGRATION_TOKEN_PREVIOUS","CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT"],`)
	if err != nil {
		t.Fatalf("manifest with rotation bindings: %v", err)
	}
	forwarded, _ := json.Marshal(artifact.Manifest)
	if !strings.Contains(string(forwarded), `"optional_bindings":["CANTELOP_INTEGRATION_TOKEN_PREVIOUS","CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT"]`) {
		t.Fatalf("forwarded manifest dropped rotation bindings: %s", forwarded)
	}
	for _, invalid := range []string{
		`"optional_bindings":["CANTELOP_INTEGRATION_TOKEN_PREVIOUS"],`,
		`"optional_bindings":["CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT","CANTELOP_INTEGRATION_TOKEN_PREVIOUS"],`,
		`"optional_bindings":["CANTELOP_INTEGRATION_TOKEN_PREVIOUS","CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT","OTHER"],`,
	} {
		if _, err := load(invalid); err == nil {
			t.Fatalf("accepted invalid optional bindings %s", invalid)
		}
	}
}
