package devruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

func TestLoadLocalEnvironmentSupportsDotenvWithoutReservedBindings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env")
	if err := os.WriteFile(path, []byte("# local values\nMODEL=fast\nexport TOKEN='secret value'\nJSON=\"one\\ntwo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := projectconfig.LoadLocalEnvironment(root, ".env", false); err == nil {
		t.Fatal("environment with an embedded newline was accepted")
	}
	if err := os.WriteFile(path, []byte("MODEL=fast\nexport TOKEN='secret value'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := projectconfig.LoadLocalEnvironment(root, ".env", false)
	if err != nil {
		t.Fatal(err)
	}
	if values["MODEL"] != "fast" || values["TOKEN"] != "secret value" || len(values) != 2 {
		t.Fatalf("environment = %#v", values)
	}
	if err := os.WriteFile(path, []byte("CANTELOP_INTERNAL_PORT=9000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := projectconfig.LoadLocalEnvironment(root, ".env", false); err == nil {
		t.Fatal("reserved environment binding was accepted")
	}
}

func TestLocalBuildWatcherReportsIncrementalEvents(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is not installed")
	}
	const program = `
process.stdout.write("CANTELOP_LOCAL_BUILD_READY\n");
setTimeout(() => process.stdout.write('CANTELOP_LOCAL_BUILD_EVENT {"component":"session-runtime"}\n'), 10);
await new Promise(() => {});
`
	var output bytes.Buffer
	watcher, err := startLocalBuildWatcherProgram(
		context.Background(), projectconfig.Config{Root: t.TempDir()}, "api", "session-runtime", "http://127.0.0.1:1", &output, program,
	)
	if err != nil {
		t.Fatalf("startLocalBuildWatcherProgram() = %v, output = %s", err, output.String())
	}
	t.Cleanup(watcher.Close)
	select {
	case event := <-watcher.events:
		if event.Component != "session-runtime" || event.Error != "" {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for incremental build event")
	}
}

func TestLoadLocalEnvironmentAllowsMissingDefaultButNotExplicitFile(t *testing.T) {
	root := t.TempDir()
	values, err := projectconfig.LoadLocalEnvironment(root, ".env", false)
	if err != nil || len(values) != 0 {
		t.Fatalf("missing default environment = %#v, %v", values, err)
	}
	if _, err := projectconfig.LoadLocalEnvironment(root, "missing.env", true); err == nil {
		t.Fatal("missing explicit environment file was accepted")
	}
}

func TestProjectEnvironmentDefaultsAreOverriddenByDotenvValues(t *testing.T) {
	model, fallback := "gpt-4.1-mini", "https://api.example.com"
	config := projectconfig.Config{Environment: map[string]projectconfig.EnvironmentDeclaration{
		"OPENAI_MODEL":   {Default: &model},
		"API_BASE_URL":   {Default: &fallback},
		"OPENAI_API_KEY": {Secret: true, Required: true},
	}}
	values := applyProjectEnvironmentDefaults(config, map[string]string{"OPENAI_MODEL": "gpt-5"})
	if values["OPENAI_MODEL"] != "gpt-5" || values["API_BASE_URL"] != fallback || len(values) != 2 {
		t.Fatalf("merged environment = %#v", values)
	}
}

func TestWriteLocalRuntimeFilesKeepsSecretsOutOfArgumentsAndSortsDockerEnvironment(t *testing.T) {
	root := t.TempDir()
	environmentFile, bindingsFile, hostFile, err := writeLocalRuntimeFiles(root, map[string]string{
		"TOKEN": "secret", "MODEL": "fast",
	})
	if err != nil {
		t.Fatal(err)
	}
	document, _ := os.ReadFile(environmentFile)
	if string(document) != "MODEL=fast\nTOKEN=secret\n" {
		t.Fatalf("Docker environment = %q", document)
	}
	bindings, _ := os.ReadFile(bindingsFile)
	if !strings.Contains(string(bindings), `"TOKEN":"secret"`) {
		t.Fatalf("API bindings = %s", bindings)
	}
	host, _ := os.ReadFile(hostFile)
	if !strings.Contains(string(host), "CANTELOP_LOCAL_READY") {
		t.Fatal("embedded API host was not written")
	}
}

func TestLocalSessionRuntimeDockerfilePinsPlatformBunAndSessionRuntimeProtocolEntrypoint(t *testing.T) {
	dockerfile := localSessionRuntimeDockerfile()
	for _, expected := range []string{"FROM oven/bun:1.3.10", "ld-linux-x86-64.so.2", "/__cantelop/bin/bun", "/opt/cantelop/session-runtime.mjs", "ENTRYPOINT []", "--no-install"} {
		if !strings.Contains(dockerfile, expected) {
			t.Errorf("local Dockerfile does not contain %q", expected)
		}
	}
}

func TestLocalBuildProgramsRequireCompatibleSDKProtocol(t *testing.T) {
	for name, program := range map[string]string{
		"container": sdkLocalBuildProgram,
		"native":    sdkLocalWatchProgram,
	} {
		if !strings.HasPrefix(program, projectconfig.SDKLoadProgram) || !strings.Contains(program, "await loadSDK(") {
			t.Errorf("%s build program does not enforce SDK compatibility", name)
		}
	}
}

func TestLocalAPIHostServesWorkerWithAppBindings(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is not installed")
	}
	directory := t.TempDir()
	hostFile := filepath.Join(directory, "host.mjs")
	workerFile := filepath.Join(directory, "worker.mjs")
	bindingsFile := filepath.Join(directory, "bindings.json")
	for path, contents := range map[string]string{
		hostFile: localAPIHostProgram,
		workerFile: `export default { fetch(request, bindings) {
  return Response.json({ path: new URL(request.url).pathname, model: bindings.MODEL });
} };`,
		bindingsFile: `{"MODEL":"local"}`,
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	var output bytes.Buffer
	host, err := startLocalAPIHost(context.Background(), hostFile, workerFile, bindingsFile, "http://127.0.0.1:1", strings.Repeat("a", 64), port, &output)
	if err != nil {
		t.Fatalf("startLocalAPIHost() error = %v, output = %s", err, output.String())
	}
	t.Cleanup(host.Close)
	response, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	document, _ := io.ReadAll(response.Body)
	var body map[string]string
	if err := json.Unmarshal(document, &body); err != nil {
		t.Fatal(err)
	}
	if body["path"] != "/health" || body["model"] != "local" {
		t.Fatalf("response = %#v", body)
	}
	for _, scenario := range []struct {
		name, host, origin, site string
	}{
		{"dns rebinding host", "attacker.example", "", ""},
		{"cross origin post", "", "https://attacker.example", "cross-site"},
		{"null origin", "", "null", "cross-site"},
		{"cross site navigation", "", "", "cross-site"},
		{"same site different origin", "", "", "same-site"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(port)+"/health", strings.NewReader("test"))
			if err != nil {
				t.Fatal(err)
			}
			if scenario.host != "" {
				request.Host = scenario.host
			}
			if scenario.origin != "" {
				request.Header.Set("Origin", scenario.origin)
			}
			if scenario.site != "" {
				request.Header.Set("Sec-Fetch-Site", scenario.site)
			}
			blocked, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer blocked.Body.Close()
			if blocked.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d", blocked.StatusCode)
			}
		})
	}
	sameOrigin, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(port)+"/health", strings.NewReader("test"))
	if err != nil {
		t.Fatal(err)
	}
	sameOrigin.Header.Set("Origin", "http://127.0.0.1:"+strconv.Itoa(port))
	sameOrigin.Header.Set("Sec-Fetch-Site", "same-origin")
	allowed, err := http.DefaultClient.Do(sameOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer allowed.Body.Close()
	if allowed.StatusCode != http.StatusOK {
		t.Fatalf("same-origin status = %d", allowed.StatusCode)
	}
	if err := os.WriteFile(workerFile, []byte(`export default { fetch() {
  return Response.json({ path: "rebuilt-worker", model: "local-reloaded" });
} };`), 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Body.Close()
	document, _ = io.ReadAll(reloaded.Body)
	if err := json.Unmarshal(document, &body); err != nil {
		t.Fatal(err)
	}
	if body["path"] != "rebuilt-worker" || body["model"] != "local-reloaded" {
		t.Fatalf("reloaded response = %#v", body)
	}
}

func TestLocalAPIHostAuthenticatesBridgeFetch(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is not installed")
	}
	const token = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bridgeListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	bridge := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get(localBridgeTokenHeader) != token {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	bridge.Listener = bridgeListener
	bridge.Start()
	defer bridge.Close()
	directory := t.TempDir()
	hostFile := filepath.Join(directory, "host.mjs")
	workerFile := filepath.Join(directory, "worker.mjs")
	bindingsFile := filepath.Join(directory, "bindings.json")
	for path, contents := range map[string]string{
		hostFile:     localAPIHostProgram,
		workerFile:   `export default { async fetch(request) { const result = await fetch(new URL('/__cantelop/v1/workspaces', request.url).toString().replace(new URL(request.url).origin, '` + bridge.URL + `')); return new Response(null, { status: result.status }); } };`,
		bindingsFile: `{}`,
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	var output bytes.Buffer
	host, err := startLocalAPIHost(context.Background(), hostFile, workerFile, bindingsFile, bridge.URL, token, port, &output)
	if err != nil {
		t.Fatalf("start API host: %v, %s", err, output.String())
	}
	defer host.Close()
	unauthorized, err := http.Get(bridge.URL + "/__cantelop/v1/workspaces")
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("direct bridge status = %d", unauthorized.StatusCode)
	}
	authorized, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/bridge")
	if err != nil {
		t.Fatal(err)
	}
	_ = authorized.Body.Close()
	if authorized.StatusCode != http.StatusNoContent {
		t.Fatalf("API host bridge status = %d, output = %s", authorized.StatusCode, output.String())
	}
}
