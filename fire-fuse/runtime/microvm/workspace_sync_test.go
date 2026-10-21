package microvm

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/workspacesync"
)

func TestNativeSyncRechecksWorkspaceBindingAndArchive(t *testing.T) {
	store := openTestSQLiteStore(t)
	app := "app-test"
	id := "wsp_" + strings.Repeat("2", 32)
	seedTestApp(t, store, app, id)
	service := NewService(store)
	files, err := workspacesync.New(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	files.WorkspaceMode = 0700
	service.ConfigureWorkspaceSync(files)
	call := func(app string) int {
		r := httptest.NewRequest("GET", "http://private/sync?op=scan", nil)
		r.SetPathValue("app_id", app)
		r.SetPathValue("workspace_id", id)
		w := httptest.NewRecorder()
		service.ServeWorkspaceSync(w, r)
		return w.Code
	}
	if code := call("app_" + strings.Repeat("3", 32)); code != 404 {
		t.Fatal(code)
	}
	if code := call(app); code != 200 {
		t.Fatal(code)
	}
	if _, err := service.ArchiveWorkspaceForUser(context.Background(), "usr_test", app, id); err != nil {
		t.Fatal(err)
	}
	if code := call(app); code != 404 {
		t.Fatal(code)
	}
}
