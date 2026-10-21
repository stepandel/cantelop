package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

func TestDoctorReportsHealthyProjectAndLiveConfiguration(t *testing.T) {
	root, client := prepareDoctorTest(t, true)
	var output bytes.Buffer
	if err := runDoctor(context.Background(), []string{"-config", filepath.Join(root, "cantelop.json")}, &output, client); err != nil {
		t.Fatalf("runDoctor() = %v\n%s", err, output.String())
	}
	for _, expected := range []string{
		"[pass] Node.js:",
		"[pass] Bun:",
		"[pass] Docker CLI:",
		"[pass] Docker daemon: linux/amd64",
		"[pass] Docker architecture: linux/amd64 is native",
		"[pass] Project files:",
		"[pass] App target: vera (app_89abcdef0123456789abcdef01234567)",
		"[pass] CLI/SDK compatibility: @cantelop/sdk 0.16.0, build protocol 5",
		"[pass] Authentication:",
		"[pass] App configuration: all required configuration is available",
		"Cantelop doctor found no problems.",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("output does not contain %q:\n%s", expected, output.String())
		}
	}
}

func TestDoctorReportsIncompatibleSDKAndMissingDeclaredSecret(t *testing.T) {
	root, client := prepareDoctorTest(t, false)
	commands := filepath.Join(root, "commands")
	writeDoctorExecutable(t, commands, "node", `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo v22.14.0
else
  echo '{"version":"0.1.0","protocol":1,"buildApi":true,"buildSessionRuntime":true}'
fi
`)
	var output bytes.Buffer
	err := runDoctor(context.Background(), []string{"-config", filepath.Join(root, "cantelop.json")}, &output, client)
	if err == nil || !strings.Contains(err.Error(), "2 problem") || !strings.Contains(output.String(), "@cantelop/sdk 0.1.0 is too old for this CLI; run npm install @cantelop/sdk@latest") ||
		!strings.Contains(output.String(), "missing secrets: API_KEY") {
		t.Fatalf("runDoctor() = %v\n%s", err, output.String())
	}
}

// An SDK that still builds but predates the application-owned mailbox is a warning, not
// a failure: its Apps work until the platform enables the application-owned mailbox.
func TestDoctorWarnsWhenSDKPredatesApplicationMailbox(t *testing.T) {
	root, client := prepareDoctorTest(t, true)
	writeDoctorExecutable(t, filepath.Join(root, "commands"), "node", `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo v22.14.0
else
  echo '{"version":"0.13.0","protocol":5,"buildApi":true,"buildLocalApi":true,"buildSessionRuntime":true,"watchLocalProject":true,"localDatabaseProtocol":1}'
fi
`)
	var output bytes.Buffer
	if err := runDoctor(context.Background(), []string{"-config", filepath.Join(root, "cantelop.json")}, &output, client); err != nil {
		t.Fatalf("runDoctor() = %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "[warn] CLI/SDK compatibility: @cantelop/sdk 0.13.0, build protocol 5, "+projectconfig.ApplicationMailboxSDKWarning) ||
		!strings.Contains(output.String(), "Cantelop doctor found no problems.") {
		t.Fatalf("output:\n%s", output.String())
	}
}

func TestSDKVersionBefore(t *testing.T) {
	for version, want := range map[string]bool{
		"0.7.0": true, "0.13.9": true, "0.16.0-rc.1": true,
		"0.16.0": false, "0.16.1": false, "0.15.0": true, "1.0.0": false, "0.16.0+build.1": false,
		"": false, "latest": false, "0.14": false,
	} {
		if got := sdkVersionBefore(version, projectconfig.ApplicationMailboxMinimumSDKVersion); got != want {
			t.Errorf("sdkVersionBefore(%q) = %v, want %v", version, got, want)
		}
	}
}

func TestDoctorSendsANewerSDKToACLIUpgrade(t *testing.T) {
	root, client := prepareDoctorTest(t, false)
	writeDoctorExecutable(t, filepath.Join(root, "commands"), "node", `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo v22.14.0
else
  echo '{"version":"9.0.0","protocol":99,"buildApi":true,"buildLocalApi":true,"buildSessionRuntime":true,"watchLocalProject":true,"localDatabaseProtocol":1}'
fi
`)
	var output bytes.Buffer
	err := runDoctor(context.Background(), []string{"-config", filepath.Join(root, "cantelop.json")}, &output, client)
	if err == nil || !strings.Contains(output.String(), "@cantelop/sdk 9.0.0 is newer than this CLI supports; upgrade the Cantelop CLI") {
		t.Fatalf("runDoctor() = %v\n%s", err, output.String())
	}
}

func TestMissingProjectConfigurationHonorsRequiredAndDefaults(t *testing.T) {
	model := "gpt-4.1-mini"
	config := projectconfig.Config{Environment: map[string]projectconfig.EnvironmentDeclaration{
		"OPENAI_MODEL":    {Default: &model},
		"OPENAI_API_KEY":  {Secret: true, Required: true},
		"OPTIONAL_FLAG":   {},
		"REQUIRED_REGION": {Required: true},
	}}
	variables, secrets := missingProjectConfiguration(config, controlapi.ConfigValuePage{Items: []controlapi.ConfigValue{
		{Name: "OPENAI_API_KEY", Kind: "secret"},
	}})
	if len(variables) != 1 || variables[0] != "REQUIRED_REGION" || len(secrets) != 0 {
		t.Fatalf("missing variables=%v secrets=%v", variables, secrets)
	}
}

func prepareDoctorTest(t *testing.T, includeSecret bool) (string, *http.Client) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api.ts", "server.ts"} {
		if err := os.WriteFile(filepath.Join(root, "src", name), []byte("export {}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	config := `{"app":"vera","api":"src/api.ts","session":"src/server.ts","environment":{"MODEL":{"default":"gpt-4.1-mini"},"API_KEY":{"secret":true,"required":true}}}`
	if err := os.WriteFile(filepath.Join(root, "cantelop.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	commands := filepath.Join(root, "commands")
	if err := os.Mkdir(commands, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDoctorExecutable(t, commands, "node", `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo v22.14.0
else
  echo '{"version":"0.16.0","protocol":5,"buildApi":true,"buildLocalApi":true,"buildSessionRuntime":true,"watchLocalProject":true,"localDatabaseProtocol":1}'
fi
`)
	writeDoctorExecutable(t, commands, "bun", "#!/bin/sh\necho 1.3.10\n")
	writeDoctorExecutable(t, commands, "docker", `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo 'Docker version 28.0.0'
elif [ "$1" = "info" ]; then
  echo 'linux/amd64'
else
  exit 2
fi
`)
	t.Setenv("PATH", commands)
	credentials := filepath.Join(root, "credentials.json")
	credentialDocument := `{"console_url":"https://console.test","control_url":"https://control.test","refresh_token":"refresh"}`
	if err := os.WriteFile(credentials, []byte(credentialDocument), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANTELOP_CONFIG", credentials)

	items := `{"items":[{"name":"MODEL","kind":"variable","value":"fast"}]}`
	if includeSecret {
		items = `{"items":[{"name":"MODEL","kind":"variable","value":"fast"},{"name":"API_KEY","kind":"secret"}]}`
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var document string
		switch request.URL.String() {
		case "https://console.test/api/cli/token":
			document = `{"access_token":"access","token_type":"Bearer","expires_in":60,"control_url":"https://control.test"}`
		case "https://control.test/v1/apps/app_89abcdef0123456789abcdef01234567/config":
			if request.Header.Get("Authorization") != "Bearer access" {
				t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
			}
			document = items
		case "https://control.test/v1/apps/by-slug/vera":
			document = `{"id":"app_89abcdef0123456789abcdef01234567","slug":"vera"}`
		default:
			return nil, fmt.Errorf("unexpected request %s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(document)),
		}, nil
	})}
	return root, client
}

func writeDoctorExecutable(t *testing.T, directory, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorReportsMissingLocalDatabaseSDKSupport(t *testing.T) {
	root, client := prepareDoctorTest(t, true)
	writeDoctorExecutable(t, filepath.Join(root, "commands"), "node", `#!/bin/sh
if [ "$1" = "--version" ]; then
 echo v22.14.0
else
 echo '{"version":"0.16.0","protocol":5,"buildApi":true,"buildLocalApi":true,"buildSessionRuntime":true,"watchLocalProject":true}'
fi
`)
	var output bytes.Buffer
	if err := runDoctor(context.Background(), []string{"-config", filepath.Join(root, "cantelop.json")}, &output, client); err == nil || !strings.Contains(output.String(), "Local Workspace databases require a newer @cantelop/sdk") {
		t.Fatalf("doctor = %v\n%s", err, output.String())
	}
}
