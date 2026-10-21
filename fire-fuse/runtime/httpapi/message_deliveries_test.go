package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/configcipher"
)

func TestMessageDeliveryAPIAcceptsAndReadsProjection(t *testing.T) {
	store, err := microvm.OpenSQLiteStore(
		filepath.Join(t.TempDir(), "state.db"),
		microvm.WithConfigEncryptionKey(bytes.Repeat([]byte{9}, configcipher.KeyBytes)),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspaceID := seedMessageWorkspace(t, store)
	handler := NewHandler(microvm.NewService(store))
	messageID := "msg_0123456789abcdef0123456789abcdef"
	body := `{"id":"` + messageID + `","workspace_id":"` + workspaceID +
		`","logical_session_id":"agent:primary","request_id":"req_0123456789abcdef0123456789abcdef","keep_alive_seconds":300,"payload":{"private":"value"}}`

	accepted := httptest.NewRecorder()
	handler.ServeHTTP(accepted, httptest.NewRequest(http.MethodPost, "/v1/message-deliveries", strings.NewReader(body)))
	if accepted.Code != http.StatusAccepted || !strings.Contains(accepted.Body.String(), `"state":"accepted"`) {
		t.Fatalf("POST Message delivery = %d: %s", accepted.Code, accepted.Body.String())
	}

	loaded := httptest.NewRecorder()
	handler.ServeHTTP(loaded, httptest.NewRequest(http.MethodGet, "/v1/message-deliveries/"+messageID, nil))
	if loaded.Code != http.StatusOK || !strings.Contains(loaded.Body.String(), `"logical_session_id":"agent:primary"`) ||
		!strings.Contains(loaded.Body.String(), `"request_id":"req_0123456789abcdef0123456789abcdef"`) {
		t.Fatalf("GET Message delivery = %d: %s", loaded.Code, loaded.Body.String())
	}
}

func seedMessageWorkspace(t *testing.T, store *microvm.SQLiteStore) string {
	t.Helper()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	workspaceID := "wsp_0123456789abcdef0123456789abcdef"
	buildID := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := store.EnsureComputeTenant(microvm.ComputeTenant{ID: "usr_messages", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	app := microvm.App{ID: "app_messages", Slug: "messages", CreatedAt: now, UpdatedAt: now}
	if err := createHTTPTestApp(store, "usr_messages", app, workspaceID, "production", buildID, now); err != nil {
		t.Fatal(err)
	}
	return workspaceID
}
