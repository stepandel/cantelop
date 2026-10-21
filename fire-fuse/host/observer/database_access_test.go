package observer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDatabaseAccessReadsOnlyCurrentSandboxAndStopsRenewal(t *testing.T) {
	id := "sbx-0123456789abcdef0123456789abcdef"
	state, calls := "starting", 0
	client, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/v1/sandboxes/"+id {
			t.Fatalf("unexpected inventory or mutation request: %s %s", r.Method, r.URL)
		}
		body := `{"id":"` + id + `","app_id":"app_one","workspace_id":"wsp_one","state":"` + state + `","private":"do-not-expose"}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	readToken, mutationToken := strings.Repeat("r", 32), strings.Repeat("m", 32)
	handler, err := NewHandlerWithSessionMutations(client, readToken, mutationToken)
	if err != nil {
		t.Fatal(err)
	}
	path := "/sandboxes/" + id + "/database-access"
	for _, token := range []string{"", mutationToken} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || calls != 0 {
			t.Fatalf("unauthorized read = %d, calls = %d", response.Code, calls)
		}
	}
	for _, next := range []string{"starting", "running", "stopping", "failed", "terminated", "", "running"} {
		state = next
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+readToken)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("assignment must never be cached")
		}
		if state != "starting" && state != "running" {
			if response.Code != http.StatusNotFound {
				t.Fatalf("%q status = %d", state, response.Code)
			}
			continue
		}
		want := `{"sandboxId":"` + id + `","appId":"app_one","workspaceId":"wsp_one","active":true}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("assignment = %d %s", response.Code, response.Body.String())
		}
	}
	if calls != 7 {
		t.Fatalf("each verification must read current persisted state: calls = %d", calls)
	}
	for _, path := range []string{"/sandboxes/bad/database-access", path + "?workspace_id=other", path + "?"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+readToken)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || calls != 7 {
			t.Fatalf("invalid selector = %d, calls = %d", response.Code, calls)
		}
	}
}

func TestDatabaseAccessFailsClosedOnUpstreamErrorsAndInvalidAssignments(t *testing.T) {
	id := "sbx-0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name         string
		body         string
		status, want int
	}{
		{"missing", "private-provider-error", 404, 404},
		{"unavailable", "private-provider-error", 503, 502},
		{"invalid JSON", "private-provider-error", 200, 502},
		{"wrong identity", `{"id":"sbx-ffffffffffffffffffffffffffffffff","state":"running","app_id":"app_one","workspace_id":"wsp_one"}`, 200, 502},
		{"missing assignment", `{"id":"` + id + `","state":"running"}`, 200, 502},
		{"released assignment", `{"id":"` + id + `","state":"terminated"}`, 200, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			token := strings.Repeat("r", 32)
			handler, err := NewHandler(client, token)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "/sandboxes/"+id+"/database-access", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.want || strings.Contains(response.Body.String(), "private-provider-error") {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			if tc.want == http.StatusNotFound && response.Body.String() != `{"error":{"code":"not_found"}}`+"\n" {
				t.Fatalf("inactive response must be distinguishable from a missing route: %s", response.Body.String())
			}
		})
	}
}

func TestDatabaseAccessPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Context().Err() != context.Canceled {
			t.Fatal("request cancellation was lost")
		}
		return nil, r.Context().Err()
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.DatabaseAccess(ctx, "sbx-0123456789abcdef0123456789abcdef")
	if err == nil {
		t.Fatal("canceled verification succeeded")
	}
}

func TestDatabaseAccessDoesNotFallBackToSnapshot(t *testing.T) {
	token := strings.Repeat("r", 32)
	handler, err := NewHandler(sourceFunc(func(context.Context) (Snapshot, error) {
		t.Fatal("database verification enumerated the dashboard")
		return Snapshot{}, nil
	}), token)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/sandboxes/sbx-0123456789abcdef0123456789abcdef/database-access", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", response.Code)
	}
	var document DatabaseAccess
	if json.Unmarshal(response.Body.Bytes(), &document) == nil {
		t.Fatal("unavailable observer returned an assignment")
	}
}
