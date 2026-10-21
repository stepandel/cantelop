package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
	"github.com/stepandel/cantelop/clients/cli/internal/render"
)

type operationalOptions struct {
	configPath  string
	jsonOutput  bool
	follow      bool
	positionals []string
}

type sessionDocument struct {
	ID               string `json:"id"`
	WorkspaceID      string `json:"workspace_id"`
	AppID            string `json:"app_id"`
	BuildID          string `json:"build_id"`
	KeepAliveSeconds int    `json:"keep_alive_seconds"`
	State            string `json:"state"`
	Startup          *struct {
		CompletedAt time.Time `json:"completed_at"`
		DurationMS  float64   `json:"duration_ms"`
	} `json:"startup,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type sessionPage struct {
	Items []sessionDocument `json:"items"`
}

type requestEventDocument struct {
	Sequence    int64     `json:"sequence"`
	EventID     string    `json:"event_id"`
	WorkspaceID string    `json:"workspace_id"`
	SessionID   string    `json:"session_id,omitempty"`
	ReceivedAt  time.Time `json:"received_at"`
	StatusCode  int       `json:"status_code"`
	DurationMS  float64   `json:"duration_ms"`
}

type requestEventPage struct {
	Items              []requestEventDocument `json:"items"`
	NextBeforeSequence int64                  `json:"next_before_sequence,omitempty"`
}

type releasePage struct {
	Items []controlapi.Release `json:"items"`
}

var logsPollInterval = time.Second

// logsPageSize is the page size fetchLogs requests.
const logsPageSize = 500

func runLogs(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	options, err := parseOperationalOptions(args, true, 0, "usage: cantelop logs [--follow] [--json] [-config cantelop.json]")
	if err != nil {
		return err
	}
	return runLogsWithContext(ctx, options, output, client)
}

func runLogsWithContext(ctx context.Context, options operationalOptions, output io.Writer, client *http.Client) error {
	_, session, appID, err := resolveTarget(ctx, client, options.configPath, "")
	if err != nil {
		return err
	}
	page, err := fetchLogs(ctx, session, appID, 0)
	if err != nil {
		return fmt.Errorf("list logs: %w", err)
	}
	if err := validateLogPage(page); err != nil {
		return err
	}
	if !options.follow {
		if options.jsonOutput {
			return render.WriteJSON(output, page)
		}
		return writeHumanLogs(output, page.Items)
	}
	lastSequence, err := writeNewLogs(output, page, 0, options.jsonOutput)
	if err != nil {
		return err
	}
	// Follow pages forward from the last printed sequence, so a burst larger
	// than one page is printed in full. A console that predates after_sequence
	// rejects it; then follow the latest page, as before, which can skip events
	// when more than one page arrives between polls.
	afterSupported, afterConfirmed := true, false
	drained := true
	for {
		if drained {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(logsPollInterval):
			}
		}
		after := int64(0)
		if afterSupported {
			after = lastSequence
		}
		page, err = fetchLogs(ctx, session, appID, after)
		var status *controlapi.StatusError
		if err != nil && after > 0 && !afterConfirmed && errors.As(err, &status) &&
			status.StatusCode == http.StatusBadRequest && status.Detail == "invalid_request" {
			afterSupported, drained = false, false
			continue
		}
		if err != nil {
			return fmt.Errorf("follow logs: %w", err)
		}
		afterConfirmed = afterConfirmed || after > 0
		if err := validateLogPage(page); err != nil {
			return err
		}
		if lastSequence, err = writeNewLogs(output, page, lastSequence, options.jsonOutput); err != nil {
			return err
		}
		drained = after == 0 || len(page.Items) < logsPageSize
	}
}

// writeNewLogs prints, oldest first, the events in page after lastSequence
// and returns the new last sequence.
func writeNewLogs(output io.Writer, page requestEventPage, lastSequence int64, jsonOutput bool) (int64, error) {
	fresh := make([]requestEventDocument, 0, len(page.Items))
	for _, event := range page.Items {
		if event.Sequence > lastSequence {
			fresh = append(fresh, event)
		}
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].Sequence < fresh[j].Sequence })
	if err := writeLogStream(output, fresh, jsonOutput); err != nil {
		return lastSequence, err
	}
	if len(fresh) > 0 {
		lastSequence = fresh[len(fresh)-1].Sequence
	}
	return lastSequence, nil
}

func runSessionList(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	options, err := parseOperationalOptions(args, false, 0, "usage: cantelop session list [--json] [-config cantelop.json]")
	if err != nil {
		return err
	}
	_, session, appID, err := resolveTarget(ctx, client, options.configPath, "")
	if err != nil {
		return err
	}
	var page sessionPage
	if err := session.Request(ctx, http.MethodGet, "/v1/apps/"+appID+"/sessions", "", nil, &page); err != nil {
		return fmt.Errorf("list Sessions: %w", err)
	}
	if err := validateSessionPage(page, appID); err != nil {
		return err
	}
	if options.jsonOutput {
		return render.WriteJSON(output, page)
	}
	return writeHumanSessions(output, page.Items)
}

// runSessionAction inspects or terminates one Session.
func runSessionAction(ctx context.Context, action string, args []string, output io.Writer, client *http.Client) error {
	usage := "usage: cantelop session " + action + " SESSION_ID [--json] [-config cantelop.json]"
	options, err := parseOperationalOptions(args, false, 1, usage)
	if err != nil {
		return err
	}
	sessionID := options.positionals[0]
	if !ids.ValidSessionID(sessionID) {
		return errors.New("Session ID is invalid")
	}
	_, session, appID, err := resolveTarget(ctx, client, options.configPath, "")
	if err != nil {
		return err
	}
	method := http.MethodGet
	if action == "terminate" {
		method = http.MethodDelete
	}
	var document sessionDocument
	if err := session.Request(ctx, method, "/v1/apps/"+appID+"/sessions/"+sessionID, "", nil, &document); err != nil {
		return fmt.Errorf("%s Session: %w", action, err)
	}
	if err := validateSession(document, appID); err != nil || document.ID != sessionID {
		return errors.New("control plane returned an invalid Session")
	}
	if options.jsonOutput {
		return render.WriteJSON(output, document)
	}
	if action == "terminate" {
		_, err = fmt.Fprintf(output, "Terminated %s\n", document.ID)
		return err
	}
	return writeHumanSession(output, document)
}

func runReleases(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	options, err := parseOperationalOptions(args, false, 0, "usage: cantelop releases [--json] [-config cantelop.json]")
	if err != nil {
		return err
	}
	_, session, appID, err := resolveTarget(ctx, client, options.configPath, "")
	if err != nil {
		return err
	}
	var page releasePage
	if err := session.Request(ctx, http.MethodGet, "/v1/apps/"+appID+"/deployments", "", nil, &page); err != nil {
		return fmt.Errorf("list releases: %w", err)
	}
	for _, release := range page.Items {
		if err := controlapi.ValidateRelease(release, appID); err != nil {
			return err
		}
	}
	sort.Slice(page.Items, func(i, j int) bool { return page.Items[i].Version > page.Items[j].Version })
	if options.jsonOutput {
		return render.WriteJSON(output, page)
	}
	writer := render.NewTable(output)
	_, _ = fmt.Fprintln(writer, "VERSION\tSTATUS\tRELEASE\tACTIVATED")
	for _, release := range page.Items {
		activated := "-"
		if release.ActivatedAt != nil {
			activated = release.ActivatedAt.Local().Format(time.RFC3339)
		}
		_, _ = fmt.Fprintf(writer, "%d\t%s\t%s\t%s\n", release.Version, release.Status, release.ID, activated)
	}
	return writer.Flush()
}

func runRollback(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	options, err := parseOperationalOptions(args, false, 0, "usage: cantelop rollback [--json] [-config cantelop.json]")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	config, session, appID, err := resolveTarget(ctx, client, options.configPath, "")
	if err != nil {
		return err
	}
	var release controlapi.Release
	if err := session.Request(ctx, http.MethodPost, "/v1/apps/"+appID+"/deployments/rollback", "", nil, &release); err != nil {
		var status *controlapi.StatusError
		if errors.As(err, &status) && status.Detail == "target_build_missing" {
			return errors.New("rollback release: the previous release's Build is no longer stored on the host; deploy that version again to restore it")
		}
		return fmt.Errorf("rollback release: %w", err)
	}
	if err := controlapi.ValidateRelease(release, appID); err != nil || release.Status != "superseded" && release.Status != "active" {
		return errors.New("control plane returned an invalid rollback release")
	}
	for release.Status == "superseded" {
		select {
		case <-ctx.Done():
			return fmt.Errorf("rollback did not complete: %w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
		if err := session.Request(ctx, http.MethodGet, "/v1/apps/"+appID+"/deployments/"+release.ID, "", nil, &release); err != nil {
			return fmt.Errorf("read rollback release: %w", err)
		}
		if err := controlapi.ValidateRelease(release, appID); err != nil || release.Status != "superseded" && release.Status != "active" {
			return errors.New("control plane returned an invalid rollback release")
		}
	}
	if options.jsonOutput {
		return render.WriteJSON(output, release)
	}
	_, err = fmt.Fprintf(output, "Rolled back %s to release v%d (%s)\n", config.AppSlug, release.Version, release.ID)
	return err
}

// resolveTarget resolves the App a command operates on and authenticates a
// session for it. app names the App by ID or slug; when it is empty, the App
// is the project's at configPath, whose configuration is also returned.
func resolveTarget(ctx context.Context, client *http.Client, configPath, app string) (projectconfig.Config, *controlapi.Session, string, error) {
	config, reference, err := projectAppReference(configPath, app)
	if err != nil {
		return projectconfig.Config{}, nil, "", err
	}
	session, err := newTenantSession(client)
	if err != nil {
		return projectconfig.Config{}, nil, "", err
	}
	appID, err := resolveAppReference(ctx, session, reference)
	if err != nil {
		return projectconfig.Config{}, nil, "", err
	}
	return config, session, appID, nil
}

// projectAppReference returns app, or the project's App slug when app is empty.
func projectAppReference(configPath, app string) (projectconfig.Config, string, error) {
	if app != "" {
		return projectconfig.Config{}, app, nil
	}
	config, err := projectconfig.Load(configPath)
	if err != nil {
		return projectconfig.Config{}, "", err
	}
	return config, config.AppSlug, nil
}

// resolveAppReference returns the ID of an App named by ID or slug.
func resolveAppReference(ctx context.Context, session *controlapi.Session, reference string) (string, error) {
	if ids.ValidAppID(reference) {
		return reference, nil
	}
	return controlapi.ResolveAppID(ctx, session, reference)
}

func parseOperationalOptions(args []string, allowFollow bool, positionalCount int, usage string) (operationalOptions, error) {
	options := operationalOptions{}
	flags := flag.NewFlagSet("cantelop", flag.ContinueOnError)
	flags.BoolVar(&options.jsonOutput, "json", false, "write JSON")
	flags.StringVar(&options.configPath, "config", projectconfig.DefaultPath, "project configuration path")
	if allowFollow {
		flags.BoolVar(&options.follow, "follow", false, "keep printing new entries")
	}
	positionals, err := parseInterspersedFlags(flags, args, usage)
	if err != nil {
		return operationalOptions{}, err
	}
	options.positionals = positionals
	if (positionalCount >= 0 && len(options.positionals) != positionalCount) || strings.TrimSpace(options.configPath) == "" {
		return operationalOptions{}, errors.New(usage)
	}
	return options, nil
}

// fetchLogs reads the latest page of request logs or, when afterSequence is
// positive, the oldest page of logs after it.
func fetchLogs(ctx context.Context, session *controlapi.Session, appID string, afterSequence int64) (requestEventPage, error) {
	path := "/v1/apps/" + appID + "/logs?limit=500"
	if afterSequence > 0 {
		path += "&after_sequence=" + strconv.FormatInt(afterSequence, 10)
	}
	var page requestEventPage
	err := session.Request(ctx, http.MethodGet, path, "", nil, &page)
	return page, err
}

func validateSessionPage(page sessionPage, appID string) error {
	for _, session := range page.Items {
		if err := validateSession(session, appID); err != nil {
			return err
		}
	}
	return nil
}

func validateSession(session sessionDocument, appID string) error {
	if !ids.ValidSessionID(session.ID) || !ids.WorkspaceIDPattern.MatchString(session.WorkspaceID) ||
		session.AppID != appID || session.BuildID == "" || session.KeepAliveSeconds < 1 ||
		(session.State != "active" && session.State != "idle") || session.CreatedAt.IsZero() || session.UpdatedAt.IsZero() {
		return errors.New("control plane returned an invalid Session")
	}
	return nil
}

func validateLogPage(page requestEventPage) error {
	for _, event := range page.Items {
		if event.Sequence < 1 || event.EventID == "" || !ids.WorkspaceIDPattern.MatchString(event.WorkspaceID) ||
			(event.SessionID != "" && !ids.ValidSessionID(event.SessionID)) || event.ReceivedAt.IsZero() ||
			event.StatusCode < 100 || event.StatusCode > 599 || event.DurationMS < 0 {
			return errors.New("control plane returned invalid logs")
		}
	}
	return nil
}

func writeHumanSessions(output io.Writer, sessions []sessionDocument) error {
	writer := render.NewTable(output)
	_, _ = fmt.Fprintln(writer, "SESSION\tSTATE\tWORKSPACE\tBUILD\tUPDATED")
	for _, session := range sessions {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", session.ID, session.State, session.WorkspaceID,
			session.BuildID, session.UpdatedAt.Local().Format(time.RFC3339))
	}
	return writer.Flush()
}

func writeHumanSession(output io.Writer, session sessionDocument) error {
	_, err := fmt.Fprintf(output, "Session: %s\nState: %s\nWorkspace: %s\nBuild: %s\nKeep alive: %ds\nCreated: %s\nUpdated: %s\n",
		session.ID, session.State, session.WorkspaceID, session.BuildID, session.KeepAliveSeconds,
		session.CreatedAt.Local().Format(time.RFC3339), session.UpdatedAt.Local().Format(time.RFC3339))
	return err
}

func writeHumanLogs(output io.Writer, events []requestEventDocument) error {
	for _, event := range events {
		if err := writeHumanLog(output, event); err != nil {
			return err
		}
	}
	return nil
}

func writeLogStream(output io.Writer, events []requestEventDocument, jsonOutput bool) error {
	for _, event := range events {
		if jsonOutput {
			if err := render.WriteJSON(output, event); err != nil {
				return err
			}
		} else if err := writeHumanLog(output, event); err != nil {
			return err
		}
	}
	return nil
}

func writeHumanLog(output io.Writer, event requestEventDocument) error {
	session := event.SessionID
	if session == "" {
		session = "-"
	}
	_, err := fmt.Fprintf(output, "%s  %d  %sms  %s  %s\n", event.ReceivedAt.Local().Format(time.RFC3339),
		event.StatusCode, strconv.FormatFloat(event.DurationMS, 'f', 1, 64), session, event.WorkspaceID)
	return err
}
