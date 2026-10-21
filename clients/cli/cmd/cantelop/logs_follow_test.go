package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
)

func logEventsJSON(first, last int64) string {
	items := make([]string, 0, last-first+1)
	for sequence := last; sequence >= first; sequence-- {
		items = append(items, fmt.Sprintf(`{"sequence":%d,"event_id":"evt_%d","workspace_id":"wsp_example","received_at":"2026-08-17T12:00:00Z","status_code":200,"duration_ms":1}`, sequence, sequence))
	}
	return `{"items":[` + strings.Join(items, ",") + `]}`
}

// followLogs runs logs --follow --json against serve, which answers each logs
// request by its after_sequence ("" for the latest page), until serve cancels.
func followLogs(t *testing.T, serve func(after string, cancel context.CancelFunc) (int, string)) []string {
	t.Helper()
	lines, err := followLogsResult(t, serve)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runLogs() error = %v", err)
	}
	return lines
}

func followLogsResult(t *testing.T, serve func(after string, cancel context.CancelFunc) (int, string)) ([]string, error) {
	t.Helper()
	interval := logsPollInterval
	logsPollInterval = time.Millisecond
	t.Cleanup(func() { logsPollInterval = interval })
	project, credentials := operationalTestProject(t)
	t.Setenv("CANTELOP_CONFIG", credentials)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, ""
		switch request.URL.Path {
		case "/api/cli/token":
			body = `{"access_token":"tenant-jwt","token_type":"Bearer","expires_in":60,"control_url":"` + operationalTestOrigin + `"}`
		case "/v1/apps/by-slug/vera":
			body = `{"id":"` + operationalTestAppID + `","slug":"vera"}`
		case "/v1/apps/" + operationalTestAppID + "/logs":
			if request.URL.Query().Get("limit") != "500" {
				t.Errorf("logs request = %s", request.URL)
			}
			status, body = serve(request.URL.Query().Get("after_sequence"), cancel)
		default:
			t.Errorf("unexpected request %s", request.URL)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	var output bytes.Buffer
	err := runLogs(ctx, []string{"--follow", "--json", "--config", project}, &output, client)
	return strings.Split(strings.TrimSpace(output.String()), "\n"), err
}

func TestLogsFollowPagesForwardThroughBursts(t *testing.T) {
	var requests []string
	lines := followLogs(t, func(after string, cancel context.CancelFunc) (int, string) {
		requests = append(requests, after)
		switch after {
		case "":
			return http.StatusOK, logEventsJSON(1, 3)
		case "3":
			return http.StatusOK, logEventsJSON(4, 503)
		case "503":
			return http.StatusOK, logEventsJSON(504, 505)
		default:
			cancel()
			return http.StatusOK, `{"items":[]}`
		}
	})
	if strings.Join(requests, ",") != ",3,503,505" {
		t.Fatalf("after_sequence requests = %q", requests)
	}
	if len(lines) != 505 {
		t.Fatalf("printed %d events, want 505", len(lines))
	}
	for index, line := range lines {
		if !strings.HasPrefix(line, fmt.Sprintf(`{"sequence":%d,`, index+1)) {
			t.Fatalf("line %d = %s", index, line)
		}
	}
}

func TestLogsFollowFallsBackWhenAfterSequenceIsUnsupported(t *testing.T) {
	var requests []string
	lines := followLogs(t, func(after string, cancel context.CancelFunc) (int, string) {
		requests = append(requests, after)
		switch len(requests) {
		case 1:
			return http.StatusOK, logEventsJSON(1, 2)
		case 2:
			return http.StatusBadRequest, `{"error":"invalid_request"}`
		case 3:
			return http.StatusOK, logEventsJSON(1, 4)
		default:
			cancel()
			return http.StatusOK, logEventsJSON(1, 4)
		}
	})
	if strings.Join(requests, ",") != ",2,," {
		t.Fatalf("after_sequence requests = %q", requests)
	}
	if len(lines) != 4 || !strings.HasPrefix(lines[3], `{"sequence":4,`) {
		t.Fatalf("output = %q", lines)
	}
}

func TestLogsFollowSurfacesOtherHTTPErrorsWithoutFallingBack(t *testing.T) {
	for _, test := range []struct {
		status int
		code   string
	}{
		{http.StatusServiceUnavailable, "observer_unavailable"},
		{http.StatusTooManyRequests, "rate_limited"},
		{http.StatusUnauthorized, "authentication_required"},
		{http.StatusNotFound, "not_found"},
		{http.StatusBadRequest, "other_bad_request"},
	} {
		t.Run(test.code, func(t *testing.T) {
			var requests []string
			lines, err := followLogsResult(t, func(after string, cancel context.CancelFunc) (int, string) {
				requests = append(requests, after)
				switch len(requests) {
				case 1:
					return http.StatusOK, logEventsJSON(1, 2)
				case 2:
					return test.status, fmt.Sprintf(`{"error":%q}`, test.code)
				default:
					cancel()
					// A fallback would silently skip sequences 3 through 602.
					return http.StatusOK, logEventsJSON(603, 1102)
				}
			})
			var status *controlapi.StatusError
			if !errors.As(err, &status) || status.StatusCode != test.status || status.Detail != test.code {
				t.Fatalf("runLogs() error = %v, want HTTP %d: %s", err, test.status, test.code)
			}
			if strings.Join(requests, ",") != ",2" || len(lines) != 2 {
				t.Fatalf("requests = %q, printed %d events; must stop without falling back", requests, len(lines))
			}
		})
	}
}
