package controlauth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestRequireRejectsMissingCredential(t *testing.T) {
	handler, err := Require(testToken, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unauthenticated request reached control handler")
	}))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/sessions", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}

func TestRequireAllowsUnauthenticatedReadiness(t *testing.T) {
	handler, err := Require(testToken, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
}

func TestAuthenticateClientSuppliesCredential(t *testing.T) {
	base := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer "+testToken {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	client, err := AuthenticateClient(&http.Client{Transport: base}, testToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get("http://127.0.0.1:8080/readyz"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTokenFileRejectsOtherPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(testToken+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTokenFile(path); err == nil {
		t.Fatal("world-readable token accepted")
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if token, err := LoadTokenFile(path); err != nil || token != testToken {
		t.Fatalf("LoadTokenFile() = %q, %v", token, err)
	}
}

func TestScopedCredentialsEnforceDistinctRouteCapabilities(t *testing.T) {
	readToken := strings.Repeat("a", 64)
	writeToken := strings.Repeat("b", 64)
	readScope, err := NewScope("GET /v1/items/{id}")
	if err != nil {
		t.Fatal(err)
	}
	writeScope, err := NewScope("POST /v1/items")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := RequireScoped([]Credential{{Token: readToken, Scope: readScope}, {Token: writeToken, Scope: writeScope}}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, token string
		status                    int
	}{
		{"read", http.MethodGet, "/v1/items/one?ignored=true", readToken, http.StatusNoContent},
		{"cross_scope", http.MethodPost, "/v1/items", readToken, http.StatusForbidden},
		{"write", http.MethodPost, "/v1/items", writeToken, http.StatusNoContent},
		{"wrong_method", http.MethodPut, "/v1/items", writeToken, http.StatusForbidden},
		{"unknown", http.MethodGet, "/v1/items/one", strings.Repeat("c", 64), http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			request.Header.Set("Authorization", "Bearer "+tc.token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status = %d", response.Code)
			}
		})
	}
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusNoContent {
		t.Fatalf("readiness status = %d", ready.Code)
	}
	duplicateRequest := httptest.NewRequest(http.MethodGet, "/v1/items/one", nil)
	duplicateRequest.Header.Add("Authorization", "Bearer "+readToken)
	duplicateRequest.Header.Add("Authorization", "Bearer "+readToken)
	duplicate := httptest.NewRecorder()
	handler.ServeHTTP(duplicate, duplicateRequest)
	if duplicate.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate authorization status = %d", duplicate.Code)
	}
}

func TestScopedCredentialsRejectCollapsedAuthority(t *testing.T) {
	token := strings.Repeat("a", 64)
	scope, err := NewScope("GET /v1/items")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RequireScoped([]Credential{{Token: token, Scope: scope}, {Token: token, Scope: scope}}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); err == nil {
		t.Fatal("duplicate credential accepted")
	}
	if _, err := NewScope("GET /v1/{x}", "GET /v1/{y}"); err == nil {
		t.Fatal("conflicting route patterns accepted")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
