package projectconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/clients/cli/internal/ids"
)

func TestLoadProjectConfigAcceptsMinimalManifest(t *testing.T) {
	root := projectTestRoot(t)
	document := `{
  "app": "vera",
  "api": "src/api.ts",
  "session": "src/session.ts"
}`
	path := writeProjectManifest(t, root, document)
	config, err := Load(path)
	if err != nil {
		t.Fatalf("loadProjectConfig() error = %v", err)
	}
	if config.AppSlug != "vera" || config.Root != root ||
		config.APIEntrypoint != filepath.Join(root, "src", "api.ts") ||
		config.SessionEntrypoint != filepath.Join(root, "src", "session.ts") ||
		config.SessionDockerfile != "" {
		t.Fatalf("loadProjectConfig() = %#v", config)
	}
	if !ids.ValidAppID(LocalAppID("vera")) || LocalAppID("vera") != LocalAppID("vera") {
		t.Fatal("local App identity is not stable and contract-valid")
	}
}

func TestLoadProjectConfigRejectsLegacySchemaAnnotation(t *testing.T) {
	root := projectTestRoot(t)
	document := `{
  "$schema": "https://cantelop.dev/schemas/app-v1.json",
  "app": "vera",
  "api": "src/api.ts",
  "session": "src/session.ts"
}`
	if _, err := Load(writeProjectManifest(t, root, document)); err == nil {
		t.Fatal("loadProjectConfig() accepted legacy schema annotation")
	}
}

func TestLoadProjectConfigAcceptsExpandedNativeDependencyConfiguration(t *testing.T) {
	root := projectTestRoot(t)
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch"), 0o644); err != nil {
		t.Fatal(err)
	}
	document := `{
  "app": "vera",
  "api": {"entrypoint": "src/api.ts"},
  "session": {
    "entrypoint": "src/session.ts",
    "dockerfile": "Dockerfile"
  },
  "environment": {
    "MODEL": {"default": "gpt-4.1-mini"},
    "API_KEY": {"secret": true, "required": true}
  }
}`
	config, err := Load(writeProjectManifest(t, root, document))
	if err != nil {
		t.Fatal(err)
	}
	if config.SessionDockerfile != filepath.Join(root, "Dockerfile") ||
		config.Environment["MODEL"].Default == nil || *config.Environment["MODEL"].Default != "gpt-4.1-mini" ||
		!config.Environment["API_KEY"].Secret || !config.Environment["API_KEY"].Required {
		t.Fatalf("expanded configuration = %#v", config)
	}
}

func TestLoadProjectConfigRejectsSessionContext(t *testing.T) {
	root := projectTestRoot(t)
	document := `{
  "app": "vera",
  "api": "src/api.ts",
  "session": {
    "entrypoint": "src/session.ts",
    "context": "."
  }
}`
	path := writeProjectManifest(t, root, document)
	_, err := Load(path)
	want := path + ":6:5\nsession.context: is not supported"
	if err == nil || err.Error() != want {
		t.Fatalf("loadProjectConfig() error = %q, want %q", err, want)
	}
}

func TestPrepareSessionBuildUsesManifestDirectoryForCustomDockerfile(t *testing.T) {
	root := projectTestRoot(t)
	if err := os.Mkdir(filepath.Join(root, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	dockerfile := filepath.Join(root, "docker", "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\nCOPY src /app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := writeProjectManifest(t, root, `{
  "app": "vera",
  "api": "src/api.ts",
  "session": {"entrypoint": "src/session.ts", "dockerfile": "docker/Dockerfile"}
}`)
	// Both -config and a nested Dockerfile must resolve independently of the CLI's cwd.
	t.Chdir(t.TempDir())
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	gotDockerfile, contextDirectory, err := PrepareSessionBuild(config, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if gotDockerfile != dockerfile || contextDirectory != root {
		t.Fatalf("prepareSessionBuild() = (%q, %q), want (%q, %q)",
			gotDockerfile, contextDirectory, dockerfile, root)
	}
}

func TestLoadProjectConfigRejectsSecretDefaultsAndReservedEnvironmentNames(t *testing.T) {
	root := projectTestRoot(t)
	for _, document := range []string{
		`{"app":"vera","api":"src/api.ts","session":"src/session.ts","environment":{"API_KEY":{"secret":true,"default":"leak"}}}`,
		`{"app":"vera","api":"src/api.ts","session":"src/session.ts","environment":{"CANTELOP_INTERNAL_PORT":{"required":true}}}`,
		`{"app":"vera","api":"src/api.ts","session":"src/session.ts","environment":{"MODEL":{"value":"gpt-4.1-mini"}}}`,
	} {
		if _, err := Load(writeProjectManifest(t, root, document)); err == nil {
			t.Fatalf("loadProjectConfig() accepted %s", document)
		}
	}
}

func TestLoadProjectConfigReportsPreciseMissingDockerfile(t *testing.T) {
	root := projectTestRoot(t)
	t.Chdir(root)
	document := `{
  "app": "vera",
  "api": "src/api.ts",
  "session": {
    "entrypoint": "src/session.ts",

    "dockerfile": "missing.Dockerfile"
  }
}`
	writeProjectManifest(t, root, document)
	_, err := Load(DefaultPath)
	want := "cantelop.json:7:5\nsession.dockerfile: file does not exist"
	if err == nil || err.Error() != want {
		t.Fatalf("loadProjectConfig() error = %q, want %q", err, want)
	}
}

func TestLoadProjectConfigRejectsGeneratedIdentityAndVersionFields(t *testing.T) {
	root := projectTestRoot(t)
	for _, test := range []struct {
		name     string
		document string
		want     string
	}{
		{"app ID", `{
  "app": "vera",
  "app_id": "app_0123456789abcdef0123456789abcdef",
  "api": "src/api.ts",
  "session": "src/session.ts"
}`, ":3:3\napp_id: is not supported"},
		{"version", `{
  "schema_version": 1,
  "app": "vera",
  "api": "src/api.ts",
  "session": "src/session.ts"
}`, ":2:3\nschema_version: is not supported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeProjectManifest(t, root, test.document)
			_, err := Load(path)
			if err == nil || !strings.HasSuffix(err.Error(), test.want) {
				t.Fatalf("loadProjectConfig() error = %q, want suffix %q", err, test.want)
			}
		})
	}
}

func TestLoadProjectConfigRequiresEntrypointsAndRejectsEscapingPaths(t *testing.T) {
	root := projectTestRoot(t)
	for _, document := range []string{
		`{"app":"vera","api":"../api.ts","session":"src/session.ts"}`,
		`{"app":"vera","api":{"entrypoint":"src/api.ts","extra":true},"session":"src/session.ts"}`,
		`{"app":"vera","api":"src/api.ts","session":{}}`,
		`{"app":"bad--slug","api":"src/api.ts","session":"src/session.ts"}`,
	} {
		if _, err := Load(writeProjectManifest(t, root, document)); err == nil {
			t.Fatalf("loadProjectConfig() accepted %s", document)
		}
	}
}

func TestLoadProjectConfigDefaultsToCantelopSessionImage(t *testing.T) {
	root := projectTestRoot(t)
	config, err := Load(writeProjectManifest(t, root,
		`{"app":"vera","api":"src/api.ts","session":"src/session.ts"}`))
	if err != nil {
		t.Fatal(err)
	}
	buildRoot := t.TempDir()
	dockerfile, contextDirectory, err := PrepareSessionBuild(config, buildRoot)
	if err != nil {
		t.Fatalf("prepareSessionBuild() error = %v", err)
	}
	document, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"FROM debian:bookworm-slim", "apt-get upgrade --yes", "ca-certificates"} {
		if !strings.Contains(string(document), required) {
			t.Errorf("default Dockerfile does not contain %q", required)
		}
	}
	if contextDirectory != filepath.Dir(dockerfile) {
		t.Fatalf("default context = %q, Dockerfile = %q", contextDirectory, dockerfile)
	}
}

func TestLoadProjectConfigRejectsLegacyEnvironmentLists(t *testing.T) {
	root := projectTestRoot(t)
	document := `{"app":"vera","api":"src/api.ts","session":"src/session.ts","environment":{"variables":["MODEL"],"secrets":["MODEL"]}}`
	if _, err := Load(writeProjectManifest(t, root, document)); err == nil {
		t.Fatalf("loadProjectConfig() error = %v", err)
	}
}

func projectTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api.ts", "session.ts"} {
		if err := os.WriteFile(filepath.Join(root, "src", name), []byte("export {}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func writeProjectManifest(t *testing.T, root, document string) string {
	t.Helper()
	path := filepath.Join(root, DefaultPath)
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
