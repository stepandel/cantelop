package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type databaseProviderStub struct{ appID, workspaceID string }

func (s *databaseProviderStub) DatabaseCredentials(_ context.Context, appID, workspaceID string) (DatabaseCredentials, error) {
	s.appID, s.workspaceID = appID, workspaceID
	return DatabaseCredentials{URL: "libsql://test.turso.io", AuthToken: "database-only", ExpiresAt: "2026-10-05T00:15:00Z"}, nil
}
func TestDatabaseCredentialsUseTrustedAppContext(t *testing.T) {
	handler, err := newTestHandler(&fakeControl{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider := &databaseProviderStub{}
	handler.databaseCredentials = provider
	app := "app_0123456789abcdef0123456789abcdef"
	workspace := "wsp_0123456789abcdef0123456789abcdef"
	request := trustedEdgeRequest(http.MethodPost, "http://edge-origin.cantelop.dev/__cantelop/v1/workspaces/database/credentials", app, "agent.cantelop.dev")
	request.Body = io.NopCloser(strings.NewReader(`{"workspace_id":"` + workspace + `"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != 200 || provider.appID != app || provider.workspaceID != workspace {
		t.Fatalf("credential routing: %d, %+v", recorder.Code, provider)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credentials must not be cached by intermediaries")
	}
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodPost, request.URL.String(), strings.NewReader(`{}`)))
	if unauthenticated.Code == 200 {
		t.Fatal("public request accepted")
	}
}
