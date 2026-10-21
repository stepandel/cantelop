package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/workspacesync"
)

// This is a local cross-repository wire test, not the JuiceFS/NFS deployment
// qualification. It allocates no Sandbox and needs no platform credentials.
func TestWorkspaceSyncSDK(t *testing.T) {
	sdk := os.Getenv("CANTELOP_SYNC_SDK")
	helper := os.Getenv("CANTELOP_SYNC_HELPER")
	if sdk == "" || helper == "" {
		t.Skip("set CANTELOP_SYNC_SDK and CANTELOP_SYNC_HELPER to built worktrees")
	}
	server, root, id := newWorkspaceSyncFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(sdk, "test/fixtures/sync-native.mjs"), server.URL, t.TempDir(), filepath.Join(root, id, "my-project"))
	command.Env = append(os.Environ(), "CANTELOP_SYNC_HELPER="+helper, "CANTELOP_SYNC_STATE_HOME="+t.TempDir())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("SDK/native transfer failed: %v\n%s", err, output)
	}
}

func newWorkspaceSyncFixture(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	root := t.TempDir()
	store, err := workspacesync.New(root, true)
	if err != nil {
		t.Fatal(err)
	}
	store.WorkspaceMode = 0700
	id := "wsp_11111111111111111111111111111111"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { store.Serve(w, r, id) }))
	t.Cleanup(server.Close)
	return server, root, id
}

func TestJuicedNamedProjectSync(t *testing.T) {
	sdk, helper, juiced := os.Getenv("CANTELOP_SYNC_SDK"), os.Getenv("CANTELOP_SYNC_HELPER"), os.Getenv("CANTELOP_JUICED_DIRECTORY")
	if sdk == "" || helper == "" || juiced == "" {
		t.Skip("set the SDK, helper, and Juiced worktree paths")
	}
	server, root, id := newWorkspaceSyncFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "--import", filepath.Join(juiced, "node_modules/tsx/dist/loader.mjs"), filepath.Join(juiced, "test/fixtures/project-sync-native.ts"), server.URL, filepath.Join(t.TempDir(), "My Project"), filepath.Join(root, id), sdk)
	command.Dir = juiced
	command.Env = append(os.Environ(), "CANTELOP_SYNC_HELPER="+helper, "CANTELOP_SYNC_STATE_HOME="+t.TempDir())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Juiced/native sync failed: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
}
