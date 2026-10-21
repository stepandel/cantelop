package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestIDMiddlewarePreservesValidID(t *testing.T) {
	const supplied = "request-client_123"
	var contextID string
	handler := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextID = RequestID(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(RequestIDHeader, supplied)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if got := recorder.Header().Get(RequestIDHeader); got != supplied {
		t.Errorf("response request ID = %q, want %q", got, supplied)
	}
	if contextID != supplied {
		t.Errorf("context request ID = %q, want %q", contextID, supplied)
	}
}

func TestRequestIDMiddlewareGeneratesValidID(t *testing.T) {
	var contextID string
	handler := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextID = RequestID(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	responseID := recorder.Header().Get(RequestIDHeader)
	if !requestIDPattern.MatchString(responseID) {
		t.Fatalf("generated response request ID = %q, want valid ID", responseID)
	}
	if contextID != responseID {
		t.Errorf("context request ID = %q, want response ID %q", contextID, responseID)
	}
}

func TestRequestIDMiddlewareReplacesInvalidIDs(t *testing.T) {
	invalid := []string{
		"contains whitespace",
		"../path",
		"-leading-dash",
		strings.Repeat("a", 65),
	}
	for _, supplied := range invalid {
		t.Run(supplied, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set(RequestIDHeader, supplied)
			recorder := httptest.NewRecorder()

			withRequestID(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(recorder, request)

			got := recorder.Header().Get(RequestIDHeader)
			if got == supplied || !requestIDPattern.MatchString(got) {
				t.Errorf("replacement request ID = %q for invalid input %q", got, supplied)
			}
		})
	}
}

func TestRequestIDAccessorRejectsUnrelatedContextValues(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := RequestID(request.Context()); got != "" {
		t.Errorf("RequestID() = %q outside middleware, want empty", got)
	}
}

func TestHandlerAddsRequestIDToUnmatchedRoutes(t *testing.T) {
	recorder := httptest.NewRecorder()

	NewHandler(nil).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/not-an-api-route", nil),
	)

	if got := recorder.Header().Get(RequestIDHeader); !requestIDPattern.MatchString(got) {
		t.Errorf("unmatched route request ID = %q, want valid ID", got)
	}
	if recorder.Code != http.StatusNotFound {
		t.Errorf("unmatched route status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}
