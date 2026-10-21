package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

const routesTestOverview = `{"origin":"https://vera.cantelop.dev","release":{"id":"rel_0123456789abcdef0123456789abcdef","version":4},"declaration":"declared",` +
	`"routes":[{"method":"GET","path":"/a-b"},{"method":"GET","path":"/a/b"},` +
	`{"method":"POST","path":"/chat","traffic":{"requests":12842,"client_errors":96,"server_errors":7,"median_duration_us":61250,"last_seen_at":"2026-09-21T11:59:48Z","messages":12739,"failed_attempts":214}},` +
	`{"method":"GET","path":"/chat"},{"method":"GET","path":"/"}],` +
	`"observed_only":[{"method":"GET","path":"/.env\u001b[2J","traffic":{"requests":18,"client_errors":18,"server_errors":0,"last_seen_at":"2026-09-21T10:00:00Z","messages":0,"failed_attempts":0}}],` +
	`"traffic_window_hours":72}`

func runRoutesTest(t *testing.T, response string, args ...string) (string, error) {
	t.Helper()
	project, credentials := operationalTestProject(t)
	requests := 0
	client := operationalTestClient(t, &requests, func(request *http.Request) string {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/apps/"+operationalTestAppID+"/routes" {
			t.Fatalf("routes request = %s %s", request.Method, request.URL.Path)
		}
		return response
	})
	t.Setenv("CANTELOP_CONFIG", credentials)
	var output bytes.Buffer
	err := runRoutes(context.Background(), append(args, "--config", project), &output, client)
	return output.String(), err
}

func TestRoutesEmitTheOverviewAsJSON(t *testing.T) {
	output, err := runRoutesTest(t, routesTestOverview, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var document appRoutesDocument
	if json.Unmarshal([]byte(output), &document) != nil || document.Origin != "https://vera.cantelop.dev" ||
		len(document.Routes) != 5 || document.Routes[2].Traffic.Requests != 12842 || len(document.ObservedOnly) != 1 {
		t.Fatalf("JSON output = %q", output)
	}
}

func TestRoutesPrintDeclaredRoutesThenUndeclaredTraffic(t *testing.T) {
	var document appRoutesDocument
	if err := json.Unmarshal([]byte(routesTestOverview), &document); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := writeHumanRoutes(&output, document, time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"https://vera.cantelop.dev · release v4 · traffic over 3d\n",
		"METHOD  PATH   REQUESTS  ERRORS  FAILED WORK  MEDIAN  LAST SEEN\n",
		"POST    /chat  12842     103     214/12739    61.3ms  12s ago\n",
		"GET     /chat  -         -       -            -       never\n",
		"\nUndeclared traffic\n",
		"GET     /.env?[2J  18        18      -            -       2h0m0s ago\n",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, output.String())
		}
	}
	if strings.ContainsRune(output.String(), '\x1b') {
		t.Fatal("an observed path reached the terminal with its escape sequence")
	}
}

func TestRoutesExplainAReleaseWithoutDeclaredRoutes(t *testing.T) {
	output, err := runRoutesTest(t, `{"origin":"https://vera.cantelop.dev","release":{"id":"rel_0123456789abcdef0123456789abcdef","version":2},"declaration":"unpublished","routes":[],`+
		`"observed_only":[{"method":"GET","path":"/health","traffic":{"requests":3,"client_errors":0,"server_errors":0,"last_seen_at":"2026-09-21T10:00:00Z"}}]}`)
	if err != nil || !strings.Contains(output, "release v2 · traffic unavailable\n") ||
		!strings.Contains(output, "predates route publication") || !strings.Contains(output, "\nObserved routes\n") {
		t.Fatalf("output = %q, %v", output, err)
	}
	if _, err := runRoutesTest(t, `{"origin":"https://vera.cantelop.dev","declaration":"none","routes":[],"observed_only":[]}`, "--openapi"); err == nil ||
		!strings.Contains(err.Error(), "declares no routes to describe") || !strings.Contains(err.Error(), "first deployment") {
		t.Fatalf("OpenAPI without declared routes = %v", err)
	}
}

func TestRoutesDescribeDeclaredRoutesAsOpenAPI(t *testing.T) {
	output, err := runRoutesTest(t, routesTestOverview, "--openapi")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		OpenAPI string `json:"openapi"`
		Info    struct{ Title, Version string }
		Servers []struct{ URL string }
		Paths   map[string]map[string]struct {
			OperationID string          `json:"operationId"`
			RequestBody json.RawMessage `json:"requestBody"`
			Responses   map[string]any  `json:"responses"`
		}
	}
	if err := json.Unmarshal([]byte(output), &document); err != nil {
		t.Fatalf("OpenAPI output = %q: %v", output, err)
	}
	chat := document.Paths["/chat"]
	if document.OpenAPI != "3.1.0" || document.Info.Title != "vera API" || document.Info.Version != "v4" ||
		len(document.Servers) != 1 || document.Servers[0].URL != "https://vera.cantelop.dev" || len(document.Paths) != 4 ||
		chat["post"].OperationID != "postChat" || len(chat["post"].RequestBody) == 0 ||
		chat["get"].OperationID != "getChat" || len(chat["get"].RequestBody) != 0 || chat["get"].Responses["default"] == nil ||
		document.Paths["/"]["get"].OperationID != "getRoot" ||
		document.Paths["/a-b"]["get"].OperationID != "getAB" || document.Paths["/a/b"]["get"].OperationID != "getAB2" {
		t.Fatalf("OpenAPI document = %s", output)
	}
	if !strings.HasPrefix(output, "{\n  \"openapi\": \"3.1.0\",\n  \"info\": {") {
		t.Fatalf("OpenAPI document is not indented with openapi first:\n%s", output)
	}
	if strings.Contains(output, ".env") {
		t.Fatal("OpenAPI document describes undeclared traffic")
	}
}

const routesTestDetail = `{"method":"POST","path":"/chat","declared":true,` +
	`"http":{"requests":5,"status_counts":[{"status":202,"requests":4},{"status":400,"requests":1}],"p50_duration_us":12000,"p95_duration_us":27000,"first_seen_at":"2026-09-21T11:00:00Z","last_seen_at":"2026-09-21T11:59:00Z"},` +
	`"downstream":{"messages":4,"attempts":5,"completed":2,"failed":2,"pending":1,"p50_time_to_start_us":1500000,"error_codes":[{"code":"model_timeout","attempts":2}]},` +
	`"recent_failures":[{"id":"req_1","trace_id":"trace1","status_code":202,"received_at":"2026-09-21T11:58:00Z","attempt_outcome":"failed","attempt_error_code":"model_timeout"},` +
	`{"id":"req_2","trace_id":"trace2","status_code":400,"received_at":"2026-09-21T11:57:00Z"}],"traffic_window_hours":72}`

func TestRoutesShowOneRouteWithItsSessionOutcomes(t *testing.T) {
	project, credentials := operationalTestProject(t)
	requests := 0
	client := operationalTestClient(t, &requests, func(request *http.Request) string {
		if request.URL.Path != "/v1/apps/"+operationalTestAppID+"/routes/detail" ||
			request.URL.Query().Get("method") != "POST" || request.URL.Query().Get("path") != "/chat" {
			t.Fatalf("detail request = %s", request.URL.String())
		}
		return routesTestDetail
	})
	t.Setenv("CANTELOP_CONFIG", credentials)
	var output bytes.Buffer
	if err := runRoutes(context.Background(), []string{"POST", "/chat", "--json", "--config", project}, &output, client); err != nil {
		t.Fatal(err)
	}
	var detail appRouteDetailDocument
	if json.Unmarshal(output.Bytes(), &detail) != nil || detail.Downstream.Failed != 2 || len(detail.RecentFailures) != 2 {
		t.Fatalf("JSON output = %q", output.String())
	}

	output.Reset()
	if err := writeHumanRouteDetail(&output, detail, time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"POST /chat · traffic over 3d\n",
		"At the edge: 5 requests · p50 12ms · p95 27ms · last 1m0s ago\n",
		"202     4         80.0%\n",
		"In sessions: 4 messages · 5 attempts · time to start 1.5s · time to finish -\n",
		"failed     2         40.0%\n",
		"\nFailure reasons\n  model_timeout  2\n",
		"2m0s ago  202     failed · model_timeout  req_1    trace1\n",
		"3m0s ago  400     nothing dispatched      req_2    trace2\n",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, output.String())
		}
	}
	for _, args := range [][]string{{"POST"}, {"POST", "/chat", "extra"}, {"POST", "/chat", "--openapi"}} {
		if err := runRoutes(context.Background(), args, &bytes.Buffer{}, nil); err == nil || !strings.HasPrefix(err.Error(), "usage: cantelop routes") {
			t.Fatalf("runRoutes(%v) = %v", args, err)
		}
	}
	for _, args := range [][]string{{"post", "/chat"}, {"POST", "chat"}} {
		_, err := runRoutesTest(t, routesTestDetail, args...)
		if err == nil || !strings.Contains(err.Error(), "METHOD and a /path") {
			t.Fatalf("runRoutes(%v) = %v", args, err)
		}
	}
}

func TestRoutesRejectUsageErrorsAndInvalidOverviews(t *testing.T) {
	for _, args := range [][]string{{"--json", "--openapi"}, {"extra"}, {"--follow"}} {
		if err := runRoutes(context.Background(), args, &bytes.Buffer{}, nil); err == nil || !strings.HasPrefix(err.Error(), "usage: cantelop routes") {
			t.Fatalf("runRoutes(%v) = %v", args, err)
		}
	}
	for name, overview := range map[string]string{
		"origin":      `{"origin":"http://vera.cantelop.dev","declaration":"declared","routes":[],"observed_only":[]}`,
		"origin path": `{"origin":"https://vera.cantelop.dev/x","declaration":"declared","routes":[],"observed_only":[]}`,
		"declaration": `{"origin":"https://vera.cantelop.dev","declaration":"guessed","routes":[],"observed_only":[]}`,
		"undeclared":  `{"origin":"https://vera.cantelop.dev","declaration":"none","routes":[{"method":"GET","path":"/a"}],"observed_only":[]}`,
		"method":      `{"origin":"https://vera.cantelop.dev","declaration":"declared","routes":[{"method":"TRACE","path":"/a"}],"observed_only":[]}`,
		"path":        `{"origin":"https://vera.cantelop.dev","declaration":"declared","routes":[{"method":"GET","path":"a"}],"observed_only":[]}`,
		"observed":    `{"origin":"https://vera.cantelop.dev","declaration":"declared","routes":[],"observed_only":[{"method":"GET","path":"a"}]}`,
		"traffic":     `{"origin":"https://vera.cantelop.dev","declaration":"declared","routes":[{"method":"GET","path":"/a","traffic":{"requests":0,"client_errors":0,"server_errors":0,"last_seen_at":"2026-09-21T10:00:00Z"}}],"observed_only":[]}`,
	} {
		if _, err := runRoutesTest(t, overview, "--json"); err == nil || !strings.Contains(err.Error(), "control plane returned") {
			t.Fatalf("%s: accepted invalid overview: %v", name, err)
		}
	}
}

func TestRouteDetailJSONPreservesStatusCountKeys(t *testing.T) {
	project, credentials := operationalTestProject(t)
	requests := 0
	client := operationalTestClient(t, &requests, func(request *http.Request) string { return routesTestDetail })
	t.Setenv("CANTELOP_CONFIG", credentials)
	var output bytes.Buffer
	if err := runRoutes(context.Background(), []string{"POST", "/chat", "--json", "--config", project}, &output, client); err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	var httpDocument map[string]json.RawMessage
	if err := json.Unmarshal(wire["http"], &httpDocument); err != nil {
		t.Fatal(err)
	}
	var counts []map[string]int
	if err := json.Unmarshal(httpDocument["status_counts"], &counts); err != nil {
		t.Fatal(err)
	}
	if len(counts) != 2 || counts[0]["status"] != 202 || counts[0]["requests"] != 4 || len(counts[0]) != 2 {
		t.Fatalf("unexpected status count keys: %s", httpDocument["status_counts"])
	}
}

func TestRouteDetailShowsHTTPFailuresWithoutMessages(t *testing.T) {
	var detail appRouteDetailDocument
	if err := json.Unmarshal([]byte(`{"method":"GET","path":"/health","http":{"requests":1,"status_counts":[{"status":500,"requests":1}],"last_seen_at":"2026-09-21T11:59:00Z"},"downstream":{"messages":0,"attempts":0},"recent_failures":[{"id":"req_failed","trace_id":"trace_failed","status_code":500,"received_at":"2026-09-21T11:59:00Z"}],"traffic_window_hours":72}`), &detail); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := writeHumanRouteDetail(&output, detail, time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"In sessions: nothing dispatched.", "Recent failures", "req_failed", "trace_failed"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in output:\n%s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "OUTCOME") {
		t.Fatalf("unexpected attempt table:\n%s", output.String())
	}
}
