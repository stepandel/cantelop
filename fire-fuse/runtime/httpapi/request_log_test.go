package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLoggingRecordsSafeCompletionFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{id}", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	handler := withRequestID(withRequestLogging(logger, mux))
	request := httptest.NewRequest(
		http.MethodGet,
		"/v1/sessions/ses_test?token=query-secret",
		strings.NewReader("body-secret"),
	)
	request.Header.Set(RequestIDHeader, "request-test")
	request.Header.Set("Authorization", "Bearer header-secret")

	handler.ServeHTTP(httptest.NewRecorder(), request)

	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatalf("decode request log: %v; output = %q", err, output.String())
	}
	want := map[string]any{
		"msg":         "HTTP request completed",
		"request_id":  "request-test",
		"method":      http.MethodGet,
		"route":       "GET /v1/sessions/{id}",
		"status":      float64(http.StatusServiceUnavailable),
		"resource_id": "ses_test",
		"error_class": "server_error",
	}
	for key, value := range want {
		if event[key] != value {
			t.Errorf("log field %q = %#v, want %#v", key, event[key], value)
		}
	}
	if _, exists := event["duration"]; !exists {
		t.Error("request log has no duration")
	}
	for _, secret := range []string{"query-secret", "body-secret", "header-secret"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("request log leaks %q: %s", secret, output.String())
		}
	}
}

func TestRequestLoggingClassifiesClientErrorsAndUnmatchedRoutes(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := withRequestID(withRequestLogging(logger, http.NotFoundHandler()))

	handler.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/attacker-controlled-path", nil),
	)

	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatalf("decode request log: %v", err)
	}
	if event["route"] != "unmatched" {
		t.Errorf("route = %#v, want unmatched", event["route"])
	}
	if event["error_class"] != "client_error" {
		t.Errorf("error_class = %#v, want client_error", event["error_class"])
	}
	if strings.Contains(output.String(), "attacker-controlled-path") {
		t.Errorf("request log includes unmatched raw path: %s", output.String())
	}
}

func TestRequestLogResponseWriterCapturesImplicitOK(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &requestLogResponseWriter{ResponseWriter: recorder}

	if _, err := writer.Write([]byte("ok")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if writer.status != http.StatusOK {
		t.Errorf("captured status = %d, want %d", writer.status, http.StatusOK)
	}
	if writer.Unwrap() != recorder {
		t.Error("Unwrap() did not return the underlying writer")
	}
}

func TestHTTPErrorClass(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{status: http.StatusAccepted, want: ""},
		{status: http.StatusTemporaryRedirect, want: ""},
		{status: http.StatusBadRequest, want: "client_error"},
		{status: 499, want: "client_error"},
		{status: http.StatusInternalServerError, want: "server_error"},
		{status: 599, want: "server_error"},
	}
	for _, test := range tests {
		if got := httpErrorClass(test.status); got != test.want {
			t.Errorf("httpErrorClass(%d) = %q, want %q", test.status, got, test.want)
		}
	}
}
