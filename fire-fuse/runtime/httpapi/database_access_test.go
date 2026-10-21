package httpapi

import (
	"encoding/json"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDatabaseAccessChecksPersistedSandboxLifecycle(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	machine := microvm.Sandbox{ID: "sbx-0123456789abcdef0123456789abcdef", AppID: "app_0123456789abcdef0123456789abcdef", WorkspaceID: "wsp_0123456789abcdef0123456789abcdef",
		Profile: microvm.DefaultSandboxProfileName, BuildID: "sha256:" + strings.Repeat("a", 64),
		VCPUs: microvm.DefaultSandboxVCPUs, MemoryMiB: microvm.DefaultSandboxMemoryMiB,
		State: microvm.StateStarting, CreatedAt: now, UpdatedAt: now}
	if err := store.Create(machine); err != nil {
		t.Fatal(err)
	}
	handler, err := NewPreparedBuildIngressHandler(microvm.NewService(store), "https://r2.example.test", t.TempDir(), filepath.Join(t.TempDir(), "catalog.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/internal/database-access/"+machine.ID, nil))
	if recorder.Code != 200 {
		t.Fatal(recorder.Code, recorder.Body.String())
	}
	var identity map[string]any
	if json.Unmarshal(recorder.Body.Bytes(), &identity) != nil || identity["workspaceId"] != machine.WorkspaceID {
		t.Fatal("incorrect Workspace assignment")
	}
	if _, err := store.Transition(machine.ID, microvm.StateStopping, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	stopped := httptest.NewRecorder()
	handler.ServeHTTP(stopped, httptest.NewRequest(http.MethodGet, "/v1/internal/database-access/"+machine.ID, nil))
	if stopped.Code != 404 {
		t.Fatal("stopping sandbox accepted")
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/internal/database-access/sbx-ffffffffffffffffffffffffffffffff", nil))
	if missing.Code != 404 {
		t.Fatal("missing sandbox accepted")
	}
}
