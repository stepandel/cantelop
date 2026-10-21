package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginCallbackMovesFragmentCredentialIntoBoundProcess(t *testing.T) {
	result := make(chan loginResult, 1)
	handler := loginCallbackHandler("expected-state", result)
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/callback", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "location.hash") ||
		strings.Contains(page.Body.String(), "cantelop-refresh") {
		t.Fatalf("callback page = %d %s", page.Code, page.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "/callback", strings.NewReader("code=cantelop-refresh&state=expected-state"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("callback response = %d %s", response.Code, response.Body.String())
	}
	select {
	case completed := <-result:
		if completed.code != "cantelop-refresh" || completed.err != nil {
			t.Fatalf("login result = %#v", completed)
		}
	default:
		t.Fatal("callback did not deliver the CLI credential")
	}
}
