package devruntime

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/sessionimage"
)

// Opt-in integration coverage exercises Docker's actual COPY ownership and
// inherited metadata semantics, which string assertions cannot establish.
func TestManagedSessionDockerIntegration(t *testing.T) {
	if os.Getenv("CANTELOP_TEST_DOCKER") != "1" {
		t.Skip("set CANTELOP_TEST_DOCKER=1 to exercise Docker")
	}
	for _, distribution := range []string{"debian:bookworm-slim", "oven/bun:1.3.10-alpine"} {
		t.Run(strings.NewReplacer("/", "-", ":", "-").Replace(distribution), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			root := t.TempDir()
			tag := "cantelop-managed-test:" + strings.ReplaceAll(strings.ToLower(t.Name()), "/", "-")
			run := func(args ...string) string {
				t.Helper()
				output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
				if err != nil {
					t.Fatalf("docker %v: %v\n%s", args, err, output)
				}
				return string(output)
			}
			t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", tag, tag+"-base").Run() })
			// No entrypoint/command: inspection must not rely on either. Use a
			// scratch stage to discard Debian's inherited CMD while retaining libc.
			base := "FROM " + distribution + " AS distro\nFROM scratch\nCOPY --from=distro / /\nUSER 2345:2345\nWORKDIR /app\nENV HOME=/root TMPDIR=/bad MODEL=kept\nHEALTHCHECK CMD false\n"
			if err := os.WriteFile(filepath.Join(root, "Dockerfile.base"), []byte(base), 0o600); err != nil {
				t.Fatal(err)
			}
			run("build", "--platform", "linux/amd64", "-f", filepath.Join(root, "Dockerfile.base"), "-t", tag+"-base", root)
			if err := sessionimage.PrepareManagedSessionImage(ctx, tag+"-base", root); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "session-runtime.mjs"), []byte("// test"), 0o600); err != nil {
				t.Fatal(err)
			}
			dockerfile := "FROM " + tag + "-base\n" + sessionimage.ManagedSessionDockerfile() + "CMD [\"/bin/sh\"]\n"
			if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
				t.Fatal(err)
			}
			run("build", "--platform", "linux/amd64", "-t", tag, root)
			output := run("run", "--rm", "--platform", "linux/amd64", tag, "/bin/sh", "-exc", `test "$(id -u)" = 10001; test "$(id -g)" = 10001; test "$(id -un)" = cantelop; test "$PWD" = /workspace; test "$MODEL" = kept; test "$HOME" = /__cantelop/home; touch "$HOME/file" "$TMPDIR/file" "$XDG_CACHE_HOME/file"; test -r /opt/cantelop/session-runtime.mjs; echo managed-ok`)
			if !strings.Contains(output, "managed-ok") {
				t.Fatal(output)
			}
			metadata := run("image", "inspect", "--format", "{{json .Config.Healthcheck.Test}}", tag)
			if strings.TrimSpace(metadata) != `["NONE"]` {
				t.Fatalf("healthcheck = %s", metadata)
			}

			// Exercise the complete local launch path, including a host bind mount,
			// App environment overrides, the packaged loader, and graceful shutdown.
			module := `import { mkdirSync, writeFileSync } from 'node:fs';
if (process.getuid() !== 10001 || process.getgid() !== 10001 || process.cwd() !== '/workspace' || process.env.HOME !== '/__cantelop/home' || process.env.MODEL !== 'override') throw new Error('invalid managed runtime');
mkdirSync('/workspace/nested', {recursive: true});
writeFileSync('/workspace/nested/shared', 'container');
writeFileSync(process.env.TMPDIR + '/test', 'temporary');
process.on('SIGTERM', () => { writeFileSync('/workspace/stopped', 'graceful'); process.exit(0); });
Bun.serve({ hostname: '0.0.0.0', port: Number(process.env.CANTELOP_INTERNAL_PORT), fetch: () => new Response('managed-runtime-ok') });
`
			if err := os.WriteFile(filepath.Join(root, "session-runtime.mjs"), []byte(module), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(localSessionRuntimeDockerfile()), 0o600); err != nil {
				t.Fatal(err)
			}
			run("build", "--platform", "linux/amd64", "--build-arg", "BASE_IMAGE="+tag+"-base", "-t", tag, root)
			envFile := filepath.Join(root, "test.env")
			if err := os.WriteFile(envFile, []byte("HOME=/bad\nTMPDIR=/bad\nMODEL=override\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			factory, err := newDockerLocalSandboxFactory(tag, envFile, filepath.Join(root, "workspaces"))
			if err != nil {
				t.Fatal(err)
			}
			sandbox, err := factory.Start(ctx, localTestWorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			defer sandbox.Close(context.Background())
			var response *http.Response
			deadline := time.Now().Add(10 * time.Second)
			client := &http.Client{Timeout: time.Second}
			for {
				response, err = client.Get(sandbox.(*dockerLocalSandbox).origin)
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					logs, _ := exec.Command("docker", "logs", sandbox.(*dockerLocalSandbox).containerID).CombinedOutput()
					t.Fatalf("Session never served requests: %v\n%s", err, logs)
				}
				time.Sleep(100 * time.Millisecond)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || string(body) != "managed-runtime-ok" {
				t.Fatalf("response = %s, %v", body, err)
			}
			workspace := filepath.Join(root, "workspaces", localTestWorkspaceID)
			if err := os.WriteFile(filepath.Join(workspace, "nested", "shared"), []byte("host"), 0o664); err != nil {
				t.Fatalf("host cannot edit container-created files: %v", err)
			}
			if err := sandbox.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if body, err := os.ReadFile(filepath.Join(workspace, "stopped")); err != nil || string(body) != "graceful" {
				t.Fatalf("graceful stop = %s, %v", body, err)
			}
		})
	}
}
