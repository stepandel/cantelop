package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
	"github.com/stepandel/cantelop/clients/cli/internal/render"
)

type appRouteTraffic struct {
	Requests         int       `json:"requests"`
	ClientErrors     int       `json:"client_errors"`
	ServerErrors     int       `json:"server_errors"`
	MedianDurationUS *int64    `json:"median_duration_us,omitempty"`
	LastSeenAt       time.Time `json:"last_seen_at"`
	Messages         int       `json:"messages"`
	FailedAttempts   int       `json:"failed_attempts"`
}

type appRouteDetailDocument struct {
	Method   string `json:"method"`
	Path     string `json:"path"`
	Declared bool   `json:"declared"`
	HTTP     struct {
		Requests     int `json:"requests"`
		StatusCounts []struct {
			Status   int `json:"status"`
			Requests int `json:"requests"`
		} `json:"status_counts"`
		P50DurationUS *int64     `json:"p50_duration_us,omitempty"`
		P95DurationUS *int64     `json:"p95_duration_us,omitempty"`
		FirstSeenAt   *time.Time `json:"first_seen_at,omitempty"`
		LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	} `json:"http"`
	Downstream struct {
		Messages          int    `json:"messages"`
		Attempts          int    `json:"attempts"`
		Completed         int    `json:"completed"`
		Failed            int    `json:"failed"`
		Pending           int    `json:"pending"`
		P50TimeToStartUS  *int64 `json:"p50_time_to_start_us,omitempty"`
		P50TimeToFinishUS *int64 `json:"p50_time_to_finish_us,omitempty"`
		ErrorCodes        []struct {
			Code     string `json:"code"`
			Attempts int    `json:"attempts"`
		} `json:"error_codes"`
	} `json:"downstream"`
	RecentFailures []struct {
		ID               string    `json:"id"`
		TraceID          string    `json:"trace_id"`
		StatusCode       *int      `json:"status_code,omitempty"`
		DurationUS       *int64    `json:"duration_us,omitempty"`
		ReceivedAt       time.Time `json:"received_at"`
		AttemptOutcome   string    `json:"attempt_outcome,omitempty"`
		AttemptErrorCode string    `json:"attempt_error_code,omitempty"`
	} `json:"recent_failures"`
	TrafficWindowHours int `json:"traffic_window_hours"`
}

type appRouteDocument struct {
	Method  string           `json:"method"`
	Path    string           `json:"path"`
	Traffic *appRouteTraffic `json:"traffic,omitempty"`
}

type appRouteRelease struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

type appRoutesDocument struct {
	Origin             string             `json:"origin"`
	Release            *appRouteRelease   `json:"release,omitempty"`
	Declaration        string             `json:"declaration"`
	Routes             []appRouteDocument `json:"routes"`
	ObservedOnly       []appRouteDocument `json:"observed_only"`
	TrafficWindowHours *int               `json:"traffic_window_hours,omitempty"`
}

// Why a release declares no routes, as the console's API page words it.
var undeclaredRouteNotes = map[string]string{
	"unpublished":  "The live release predates route publication. Upgrade @cantelop/sdk, then deploy again to list the routes this API declares.",
	"undiscovered": "Route discovery failed when the live release was built. cantelop build prints the reason.",
	"none":         "Nothing is live for this App yet. Its routes appear after the first deployment.",
}

func runRoutes(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	const usage = "usage: cantelop routes [METHOD PATH] [--json | --openapi] [-config cantelop.json]"
	openAPI := slices.Contains(args, "--openapi")
	args = slices.DeleteFunc(slices.Clone(args), func(argument string) bool { return argument == "--openapi" })
	options, err := parseOperationalOptions(args, false, -1, usage)
	if err != nil || (openAPI && options.jsonOutput) || (len(options.positionals) != 0 && len(options.positionals) != 2) ||
		(len(options.positionals) == 2 && openAPI) {
		return errors.New(usage)
	}
	config, session, appID, err := resolveTarget(ctx, client, options.configPath, "")
	if err != nil {
		return err
	}
	if len(options.positionals) == 2 {
		return runRouteDetail(ctx, session, appID, options.positionals[0], options.positionals[1], options.jsonOutput, output)
	}
	var document appRoutesDocument
	if err := session.Request(ctx, http.MethodGet, "/v1/apps/"+appID+"/routes", "", nil, &document); err != nil {
		return fmt.Errorf("list routes: %w", err)
	}
	if err := validateAppRoutes(document); err != nil {
		return err
	}
	switch {
	case openAPI:
		if document.Declaration != "declared" {
			return errors.New("the live release declares no routes to describe. " + undeclaredRouteNotes[document.Declaration])
		}
		// Indented, unlike --json: this document is saved and read, not piped.
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(routesOpenAPI(config.AppSlug, document))
	case options.jsonOutput:
		return render.WriteJSON(output, document)
	default:
		return writeHumanRoutes(output, document, time.Now())
	}
}

func validateAppRoutes(document appRoutesDocument) error {
	origin, err := url.Parse(document.Origin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" ||
		origin.Fragment != "" || origin.User != nil {
		return errors.New("control plane returned an invalid App origin")
	}
	if _, known := undeclaredRouteNotes[document.Declaration]; !known && document.Declaration != "declared" {
		return errors.New("control plane returned an invalid route declaration")
	}
	if document.Declaration != "declared" && len(document.Routes) != 0 {
		return errors.New("control plane returned routes without a declaration")
	}
	for _, route := range document.Routes {
		if !slices.Contains(projectconfig.APIRouteMethods, route.Method) || !projectconfig.APIRoutePathPattern.MatchString(route.Path) {
			return errors.New("control plane returned an invalid route")
		}
	}
	// Observed routes are whatever clients sent: bound them, do not expect a
	// registered method or a listable path.
	for _, route := range append(slices.Clone(document.Routes), document.ObservedOnly...) {
		if route.Method == "" || len(route.Method) > 32 || !strings.HasPrefix(route.Path, "/") || len(route.Path) > 2048 ||
			(route.Traffic != nil && (route.Traffic.Requests < 1 || route.Traffic.ClientErrors < 0 || route.Traffic.ServerErrors < 0)) {
			return errors.New("control plane returned an invalid route")
		}
	}
	return nil
}

func writeHumanRoutes(output io.Writer, document appRoutesDocument, now time.Time) error {
	heading := document.Origin
	if document.Release != nil {
		heading += fmt.Sprintf(" · release v%d", document.Release.Version)
	}
	if document.TrafficWindowHours != nil {
		heading += fmt.Sprintf(" · traffic over %s", routeWindow(*document.TrafficWindowHours))
	} else {
		heading += " · traffic unavailable"
	}
	if _, err := fmt.Fprintln(output, heading); err != nil {
		return err
	}
	if document.Declaration != "declared" {
		if _, err := fmt.Fprintln(output, undeclaredRouteNotes[document.Declaration]); err != nil {
			return err
		}
	} else if len(document.Routes) == 0 {
		if _, err := fmt.Fprintln(output, "The live release declares no routes."); err != nil {
			return err
		}
	} else if err := writeRouteTable(output, document.Routes, now); err != nil {
		return err
	}
	if len(document.ObservedOnly) == 0 {
		return nil
	}
	title := "Undeclared traffic"
	if document.Declaration != "declared" {
		title = "Observed routes"
	}
	if _, err := fmt.Fprintf(output, "\n%s\n", title); err != nil {
		return err
	}
	return writeRouteTable(output, document.ObservedOnly, now)
}

func writeRouteTable(output io.Writer, routes []appRouteDocument, now time.Time) error {
	writer := render.NewTable(output)
	_, _ = fmt.Fprintln(writer, "METHOD\tPATH\tREQUESTS\tERRORS\tFAILED WORK\tMEDIAN\tLAST SEEN")
	for _, route := range routes {
		requests, failures, failedWork, median, seen := "-", "-", "-", "-", "never"
		if traffic := route.Traffic; traffic != nil {
			requests = fmt.Sprint(traffic.Requests)
			failures = fmt.Sprint(traffic.ClientErrors + traffic.ServerErrors)
			if traffic.Messages > 0 {
				failedWork = fmt.Sprintf("%d/%d", traffic.FailedAttempts, traffic.Messages)
			}
			if traffic.MedianDurationUS != nil {
				median = microsDuration(*traffic.MedianDurationUS)
			}
			seen = now.Sub(traffic.LastSeenAt).Truncate(time.Second).String() + " ago"
		}
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", terminalSafe(route.Method), terminalSafe(route.Path), requests, failures, failedWork, median, seen)
	}
	return writer.Flush()
}

func microsDuration(micros int64) string {
	return (time.Duration(micros) * time.Microsecond).Round(time.Millisecond / 10).String()
}

func runRouteDetail(ctx context.Context, session *controlapi.Session, appID, method, path string, jsonOutput bool, output io.Writer) error {
	if !slices.Contains(projectconfig.APIRouteMethods, method) || !strings.HasPrefix(path, "/") || len(path) > 2048 {
		return errors.New("route must be a METHOD and a /path")
	}
	query := url.Values{"method": {method}, "path": {path}}
	var detail appRouteDetailDocument
	if err := session.Request(ctx, http.MethodGet, "/v1/apps/"+appID+"/routes/detail?"+query.Encode(), "", nil, &detail); err != nil {
		return fmt.Errorf("read route: %w", err)
	}
	if detail.Method != method || detail.Path != path || detail.HTTP.Requests < 0 || detail.Downstream.Messages < 0 ||
		detail.Downstream.Attempts < detail.Downstream.Completed+detail.Downstream.Failed+detail.Downstream.Pending {
		return errors.New("control plane returned an invalid route")
	}
	if jsonOutput {
		return render.WriteJSON(output, detail)
	}
	return writeHumanRouteDetail(output, detail, time.Now())
}

func writeHumanRouteDetail(output io.Writer, detail appRouteDetailDocument, now time.Time) error {
	window := routeWindow(detail.TrafficWindowHours)
	declared := ""
	if !detail.Declared {
		declared = " · not declared by the live release"
	}
	if _, err := fmt.Fprintf(output, "%s %s%s · traffic over %s\n", detail.Method, terminalSafe(detail.Path), declared, window); err != nil {
		return err
	}
	optional := func(value *int64) string {
		if value == nil {
			return "-"
		}
		return microsDuration(*value)
	}
	h := detail.HTTP
	if h.Requests == 0 {
		if _, err := fmt.Fprintf(output, "\nNo requests reached this route in the last %s.\n", window); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(output, "\nAt the edge: %d requests · p50 %s · p95 %s · last %s ago\n",
			h.Requests, optional(h.P50DurationUS), optional(h.P95DurationUS), now.Sub(*h.LastSeenAt).Truncate(time.Second)); err != nil {
			return err
		}
		writer := render.NewTable(output)
		_, _ = fmt.Fprintln(writer, "STATUS\tREQUESTS\tSHARE")
		for _, count := range h.StatusCounts {
			_, _ = fmt.Fprintf(writer, "%d\t%d\t%.1f%%\n", count.Status, count.Requests, 100*float64(count.Requests)/float64(h.Requests))
		}
		if err := writer.Flush(); err != nil {
			return err
		}
	}
	d := detail.Downstream
	if d.Messages == 0 {
		if _, err := fmt.Fprintln(output, "\nIn sessions: nothing dispatched."); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(output, "\nIn sessions: %d messages · %d attempts · time to start %s · time to finish %s\n",
			d.Messages, d.Attempts, optional(d.P50TimeToStartUS), optional(d.P50TimeToFinishUS)); err != nil {
			return err
		}
		writer := render.NewTable(output)
		_, _ = fmt.Fprintln(writer, "OUTCOME\tATTEMPTS\tSHARE")
		for _, outcome := range []struct {
			name  string
			count int
		}{{"completed", d.Completed}, {"failed", d.Failed}, {"pending", d.Pending}} {
			if outcome.count > 0 {
				_, _ = fmt.Fprintf(writer, "%s\t%d\t%.1f%%\n", outcome.name, outcome.count, 100*float64(outcome.count)/float64(d.Attempts))
			}
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		if len(d.ErrorCodes) > 0 {
			if _, err := fmt.Fprintln(output, "\nFailure reasons"); err != nil {
				return err
			}
			writer = render.NewTable(output)
			for _, code := range d.ErrorCodes {
				_, _ = fmt.Fprintf(writer, "  %s\t%d\n", terminalSafe(code.Code), code.Attempts)
			}
			if err := writer.Flush(); err != nil {
				return err
			}
		}
	}
	if len(detail.RecentFailures) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(output, "\nRecent failures"); err != nil {
		return err
	}
	writer := render.NewTable(output)
	_, _ = fmt.Fprintln(writer, "RECEIVED\tSTATUS\tWORK\tREQUEST\tTRACE")
	for _, failure := range detail.RecentFailures {
		status, work := "-", "nothing dispatched"
		if failure.StatusCode != nil {
			status = fmt.Sprint(*failure.StatusCode)
		}
		if failure.AttemptOutcome != "" {
			work = terminalSafe(failure.AttemptOutcome)
			if failure.AttemptErrorCode != "" {
				work += " · " + terminalSafe(failure.AttemptErrorCode)
			}
		}
		_, _ = fmt.Fprintf(writer, "%s ago\t%s\t%s\t%s\t%s\n", now.Sub(failure.ReceivedAt).Truncate(time.Second), status, work,
			terminalSafe(failure.ID), terminalSafe(failure.TraceID))
	}
	return writer.Flush()
}

// Observed paths are attacker-chosen bytes; keep them from driving the terminal.
func terminalSafe(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return '?'
		}
		return character
	}, value)
}

func routeWindow(hours int) string {
	if hours%24 == 0 {
		return fmt.Sprintf("%dd", hours/24)
	}
	return fmt.Sprintf("%dh", hours)
}

// Structs fix the conventional key order; paths and operations sort by name.
type openAPIDocument struct {
	OpenAPI string          `json:"openapi"`
	Info    openAPIInfo     `json:"info"`
	Servers []openAPIServer `json:"servers"`
	Paths   map[string]any  `json:"paths"`
}

type openAPIInfo struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

type openAPIServer struct {
	URL string `json:"url"`
}

// routesOpenAPI describes the declared routes as an OpenAPI 3.1 document that
// API clients can import. The platform knows each route's method and path, not
// its parameters, bodies, responses, or the authorization the App expects, so
// those are left open rather than invented.
func routesOpenAPI(appSlug string, document appRoutesDocument) openAPIDocument {
	version := "0"
	if document.Release != nil {
		version = fmt.Sprintf("v%d", document.Release.Version)
	}
	paths := map[string]any{}
	operationIDs := map[string]int{}
	for _, route := range document.Routes {
		// /a-b and /a/b read the same in camel case; number the later ones.
		operationID := routeOperationID(route)
		operationIDs[operationID]++
		if count := operationIDs[operationID]; count > 1 {
			operationID = fmt.Sprintf("%s%d", operationID, count)
		}
		operation := map[string]any{
			"operationId": operationID,
			"responses":   map[string]any{"default": map[string]any{"description": "Defined by the App."}},
		}
		if slices.Contains([]string{"PATCH", "POST", "PUT"}, route.Method) {
			operation["requestBody"] = map[string]any{
				"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{}}},
			}
		}
		item, _ := paths[route.Path].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[route.Path] = item
		}
		item[strings.ToLower(route.Method)] = operation
	}
	return openAPIDocument{
		OpenAPI: "3.1.0",
		Info: openAPIInfo{
			Title:       appSlug + " API",
			Version:     version,
			Description: "Routes declared by the live Cantelop release. Parameters, bodies, responses, and authorization are defined by the App and are not described here.",
		},
		Servers: []openAPIServer{{URL: document.Origin}},
		Paths:   paths,
	}
}

// routeOperationID is the method, then the path's letters and digits in camel
// case, e.g. POST /slack/events -> postSlackEvents.
func routeOperationID(route appRouteDocument) string {
	var id strings.Builder
	id.WriteString(strings.ToLower(route.Method))
	upper := true
	for _, character := range route.Path {
		switch {
		case unicode.IsLetter(character) || unicode.IsDigit(character):
			if upper {
				character = unicode.ToUpper(character)
			}
			id.WriteRune(character)
			upper = false
		default:
			upper = true
		}
	}
	if route.Path == "/" {
		id.WriteString("Root")
	}
	return id.String()
}
