package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestProductClientCreatesWorkspaceForApp(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/apps/app_0123456789abcdef0123456789abcdef/workspaces" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var input struct {
			Slug string `json:"slug"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Slug != "preview" {
			t.Fatalf("input = %#v, %v", input, err)
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"id":"wsp_0123456789abcdef0123456789abcdef","app_id":"app_0123456789abcdef0123456789abcdef","slug":"preview","created_at":"2026-08-14T12:00:00Z","updated_at":"2026-08-14T12:00:00Z"}`,
			)),
			Request: r,
		}, nil
	})
	client, err := NewProductClient("https://console.cantelop.dev", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := client.CreateWorkspace(
		context.Background(), "app_0123456789abcdef0123456789abcdef", "preview",
	)
	if err != nil || workspace.Slug != "preview" || workspace.CreatedAt.IsZero() {
		t.Fatalf("CreateWorkspace() = %#v, %v", workspace, err)
	}
}

func TestProductClientOpensWorkspaceForApp(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/apps/app_0123456789abcdef0123456789abcdef/workspaces/open" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"id":"wsp_0123456789abcdef0123456789abcdef","app_id":"app_0123456789abcdef0123456789abcdef","slug":"preview","created_at":"2026-08-14T12:00:00Z","updated_at":"2026-08-14T12:00:00Z"}`,
			)),
			Request: r,
		}, nil
	})
	client, err := NewProductClient("https://console.cantelop.dev", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := client.OpenWorkspace(
		context.Background(), "app_0123456789abcdef0123456789abcdef", "preview",
	)
	if err != nil || workspace.Slug != "preview" {
		t.Fatalf("OpenWorkspace() = %#v, %v", workspace, err)
	}
}

func TestProductClientRequiresHTTPSOrigin(t *testing.T) {
	for _, endpoint := range []string{
		"http://console.cantelop.dev", "https://console.cantelop.dev/path", "https://user@console.cantelop.dev",
	} {
		if _, err := NewProductClient(endpoint, nil); err == nil {
			t.Errorf("NewProductClient(%q) error = nil", endpoint)
		}
	}
}
