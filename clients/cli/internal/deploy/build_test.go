package deploy

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

func TestLoadAPIArtifactAcceptsPublishedRoutes(t *testing.T) {
	load := func(manifest string) (projectconfig.APIArtifact, error) {
		directory := t.TempDir()
		if err := os.WriteFile(filepath.Join(directory, "cantelop-api.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "worker.mjs"), []byte("export default {};"), 0o644); err != nil {
			t.Fatal(err)
		}
		return projectconfig.LoadAPIArtifact(directory)
	}
	manifest := func(routes string) string {
		return `{"schema_version":3,"kind":"cantelop-edge-api","main_module":"worker.mjs","routes":` + routes + `}`
	}

	artifact, err := load("{\n  \"schema_version\": 3,\n  \"kind\": \"cantelop-edge-api\",\n  \"main_module\": \"worker.mjs\",\n" +
		"  \"routes\": [\n    { \"method\": \"GET\", \"path\": \"/chat\" },\n    { \"method\": \"POST\", \"path\": \"/chat\" },\n    { \"method\": \"GET\", \"path\": \"/health\" }\n  ]\n}\n")
	if err != nil {
		t.Fatalf("load routes: %v", err)
	}
	routes, discovered, err := artifact.Manifest.DeclaredRoutes()
	if err != nil || !discovered || len(routes) != 3 || routes[1] != (projectconfig.APIRoute{Method: "POST", Path: "/chat"}) {
		t.Fatalf("routes = %#v, %v, %v", routes, discovered, err)
	}
	forwarded, err := json.Marshal(artifact.Manifest)
	if err != nil || string(forwarded) != manifest(`[{"method":"GET","path":"/chat"},{"method":"POST","path":"/chat"},{"method":"GET","path":"/health"}]`) {
		t.Fatalf("forwarded manifest = %s, %v", forwarded, err)
	}
	var output bytes.Buffer
	if err := writeAPIRoutes(&output, artifact.Manifest); err != nil ||
		output.String() != "API routes (3)\n  GET     /chat\n  POST    /chat\n  GET     /health\n" {
		t.Fatalf("route report = %q, %v", output.String(), err)
	}

	for _, routes := range []string{"null", "[]"} {
		artifact, err := load(manifest(routes))
		if err != nil {
			t.Fatalf("load %s routes: %v", routes, err)
		}
		_, discovered, _ := artifact.Manifest.DeclaredRoutes()
		forwarded, _ := json.Marshal(artifact.Manifest)
		if discovered != (routes == "[]") || string(forwarded) != manifest(routes) {
			t.Fatalf("%s routes: discovered %v, forwarded %s", routes, discovered, forwarded)
		}
	}
	legacy, err := load(`{"schema_version":2,"kind":"cantelop-edge-api","main_module":"worker.mjs"}`)
	forwarded, _ = json.Marshal(legacy.Manifest)
	if err != nil || string(forwarded) != `{"schema_version":2,"kind":"cantelop-edge-api","main_module":"worker.mjs"}` {
		t.Fatalf("legacy manifest = %s, %v", forwarded, err)
	}
	output.Reset()
	if err := writeAPIRoutes(&output, legacy.Manifest); err != nil || output.Len() != 0 {
		t.Fatalf("legacy route report = %q, %v", output.String(), err)
	}

	for _, invalid := range []string{
		`{"schema_version":3,"kind":"cantelop-edge-api","main_module":"worker.mjs"}`,
		`{"schema_version":2,"kind":"cantelop-edge-api","main_module":"worker.mjs","routes":[]}`,
		manifest(`{}`),
		manifest(`[{"method":"GET"}]`),
		manifest(`[{"method":"GET","path":"/health","summary":"Health"}]`),
		manifest(`[{"method":"TRACE","path":"/health"}]`),
		manifest(`[{"method":"GET","path":"health"}]`),
		manifest(`[{"method":"GET","path":"/health/"}]`),
		manifest(`[{"method":"GET","path":"/with space"}]`),
		manifest(`[{"method":"GET","path":"/health"},{"method":"GET","path":"/chat"}]`),
		manifest(`[{"method":"GET","path":"/health"},{"method":"GET","path":"/health"}]`),
		manifest("[" + strings.TrimSuffix(strings.Repeat(`{"method":"GET","path":"/a"},`, 101), ",") + "]"),
	} {
		if _, err := load(invalid); err == nil {
			t.Fatalf("accepted invalid manifest %s", invalid)
		}
	}
}

func TestDeployBuildProgramRequiresCompatibleSDKProtocol(t *testing.T) {
	if !strings.HasPrefix(SDKBuildProgram, projectconfig.SDKLoadProgram) ||
		!strings.Contains(SDKBuildProgram, `await loadSDK("buildSessionRuntime")`) {
		t.Fatal("deployment build program does not enforce SDK compatibility")
	}
	protocols, _ := json.Marshal(projectconfig.SupportedSDKBuildProtocolVersions)
	if !strings.Contains(projectconfig.SDKLoadProgram, "const supportedProtocols = "+strings.ReplaceAll(string(protocols), ",", ", ")+";") {
		t.Fatalf("SDK load program does not support build protocols %s", protocols)
	}

}

// The build program warns, without failing, when the SDK predates the durable
// mailbox. It shares the minimum with doctor and reads the SDK's own manifest
// wherever its build entry point sits inside the package.
func TestBuildProgramWarnsWhenSDKPredatesApplicationMailbox(t *testing.T) {
	if !strings.Contains(SDKBuildProgram, `const applicationMailboxMinimumSDK = "`+projectconfig.ApplicationMailboxMinimumSDKVersion+`";`) {
		t.Fatal("build program minimum does not match applicationMailboxMinimumSDKVersion")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is unavailable")
	}
	const module = "export const CANTELOP_CLI_BUILD_PROTOCOL_VERSION = 5; export async function buildApi() { return {}; } export async function buildSessionRuntime() {}"
	for name, test := range map[string]struct {
		version, buildPath string
		warn               bool
	}{
		"older":      {"0.13.0", "dist/build.js", true},
		"prerelease": {"0.16.0-rc.1", "dist/build.js", true},
		"current":    {"0.16.0", "dist/build.js", false},
		"newer":      {"0.17.0", "build.js", false},
	} {
		t.Run(name, func(t *testing.T) {
			project := t.TempDir()
			sdk := filepath.Join(project, "node_modules", "@cantelop", "sdk")
			if err := os.MkdirAll(filepath.Dir(filepath.Join(sdk, test.buildPath)), 0o755); err != nil {
				t.Fatal(err)
			}
			manifest := `{"name":"@cantelop/sdk","version":"` + test.version + `","type":"module","exports":{"./build":"./` + test.buildPath + `"}}`
			if err := os.WriteFile(filepath.Join(sdk, "package.json"), []byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sdk, test.buildPath), []byte(module), 0o644); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("node", "--input-type=module", "--eval", SDKBuildProgram, "api.ts", "api", "session.ts", "session")
			command.Dir = project
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("build program failed: %v\n%s", err, output)
			}
			if warned := strings.Contains(string(output), "Warning: the project-installed @cantelop/sdk "+projectconfig.ApplicationMailboxSDKWarning); warned != test.warn {
				t.Fatalf("warned = %v, output:\n%s", warned, output)
			}
		})
	}
}

// The remedy depends on which side is behind, and never names an SDK version.
func TestSDKLoadProgramSaysWhichSideToUpgrade(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is unavailable")
	}
	const tooOld = "too old for this CLI; run npm install @cantelop/sdk@latest"
	for name, test := range map[string]struct{ module, want string }{
		"missing":   {"", "is not installed in this project; run npm install @cantelop/sdk@latest"},
		"no build":  {"-", tooOld},
		"older":     {"export const CANTELOP_CLI_BUILD_PROTOCOL_VERSION = 3; export function buildApi() {}", tooOld},
		"partial":   {"export const CANTELOP_CLI_BUILD_PROTOCOL_VERSION = 5;", tooOld},
		"newer":     {"export const CANTELOP_CLI_BUILD_PROTOCOL_VERSION = 7; export function buildApi() {}", "newer than this CLI supports; upgrade the Cantelop CLI"},
		"supported": {"export const CANTELOP_CLI_BUILD_PROTOCOL_VERSION = 5; export function buildApi() {}", ""},
	} {
		t.Run(name, func(t *testing.T) {
			project := t.TempDir()
			if test.module != "" {
				sdk := filepath.Join(project, "node_modules", "@cantelop", "sdk")
				exports := `{"./build":"./build.js"}`
				if test.module == "-" {
					exports = `{".":"./build.js"}`
				}
				if err := os.MkdirAll(sdk, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(sdk, "package.json"), []byte(`{"name":"@cantelop/sdk","type":"module","exports":`+exports+`}`), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(sdk, "build.js"), []byte(strings.TrimPrefix(test.module, "-")), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("node", "--input-type=module", "--eval", projectconfig.SDKLoadProgram+`await loadSDK("buildApi");`)
			command.Dir = project
			output, err := command.CombinedOutput()
			if test.want == "" {
				if err != nil {
					t.Fatalf("supported SDK rejected: %s", output)
				}
				return
			}
			if err == nil || !strings.HasSuffix(strings.TrimSpace(string(output)), test.want) ||
				strings.Contains(string(output), "@cantelop/sdk@0") || strings.Contains(string(output), "    at ") {
				t.Fatalf("output = %s", output)
			}
		})
	}
}
