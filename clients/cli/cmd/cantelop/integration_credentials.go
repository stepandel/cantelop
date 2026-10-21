package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/clients/cli/internal/integration"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

const appCredentialsUsage = "usage: cantelop app credentials [rotate [--grace DURATION | --now | --retry] [--no-wait]] [--app APP_ID_OR_SLUG] [--format dotenv|app-config|token] [-config cantelop.json]"

const (
	defaultIntegrationGrace = 24 * time.Hour
	maxIntegrationGrace     = 7 * 24 * time.Hour
)

var (
	integrationTokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	edgeURLPattern          = regexp.MustCompile(`^https://[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)
)

// Status lines go to stderr so stdout can be piped into a host's secret store.
var credentialStatusOutput io.Writer = os.Stderr

var (
	rotationPollInterval = 2 * time.Second
	rotationWaitTimeout  = 2 * time.Minute
)

type integrationCredentialsDocument struct {
	AppID                  string                     `json:"app_id"`
	Slug                   string                     `json:"slug"`
	EdgeURL                string                     `json:"edge_url"`
	Token                  string                     `json:"token"`
	PreviousTokenExpiresAt *time.Time                 `json:"previous_token_expires_at"`
	Rotation               *integrationRotationStatus `json:"rotation"`
}

type integrationRotationStatus struct {
	Revision  int64     `json:"revision"`
	RotatedAt time.Time `json:"rotated_at"`
	Status    string    `json:"status"`
}

type appCredentialsOptions struct {
	retry      bool
	rotate     bool
	configPath string
	app        string
	format     string
	grace      time.Duration
	wait       bool
}

func parseAppCredentialsOptions(args []string) (appCredentialsOptions, error) {
	options := appCredentialsOptions{rotate: len(args) > 0 && args[0] == "rotate"}
	if options.rotate {
		args = args[1:]
	}
	flags := flag.NewFlagSet("cantelop app credentials", flag.ContinueOnError)
	flags.StringVar(&options.configPath, "config", projectconfig.DefaultPath, "project configuration path")
	flags.StringVar(&options.app, "app", "", "App ID or slug (defaults to the current project)")
	flags.StringVar(&options.format, "format", "dotenv", "dotenv, app-config, or token")
	grace := flags.Duration("grace", defaultIntegrationGrace, "how long the previous token keeps working")
	flags.BoolVar(&options.retry, "retry", false, "resend the existing rotation without issuing a new token")
	now := flags.Bool("now", false, "revoke the previous token immediately")
	noWait := flags.Bool("no-wait", false, "return before the new token reaches the Edge")
	if err := parseFlags(flags, args, appCredentialsUsage); err != nil {
		return options, err
	}
	rotationFlags := false
	graceSet := false
	flags.Visit(func(set *flag.Flag) {
		switch set.Name {
		case "grace":
			graceSet = true
			rotationFlags = true
		case "now", "no-wait", "retry":
			rotationFlags = true
		}
	})
	if (rotationFlags && !options.rotate) || (graceSet && *now) || (options.retry && (graceSet || *now)) || (options.app != "" && !ids.ValidAppID(options.app) && !ids.ValidRoutingSlug(options.app)) {
		return options, errors.New(appCredentialsUsage)
	}
	switch options.format {
	case "dotenv", "app-config", "token":
	default:
		return options, errors.New(appCredentialsUsage)
	}
	options.grace = *grace
	if *now {
		options.grace = 0
	} else if options.rotate && (options.grace < time.Second || options.grace > maxIntegrationGrace || options.grace%time.Second != 0) {
		return options, errors.New("--grace must be whole seconds between 1s and 168h; use --now to revoke the previous token immediately")
	}
	options.wait = !*noWait
	return options, nil
}

func runAppCredentials(ctx context.Context, args []string, output io.Writer, session *controlapi.Session) error {
	options, err := parseAppCredentialsOptions(args)
	if err != nil {
		return err
	}
	_, reference, err := projectAppReference(options.configPath, options.app)
	if err != nil {
		return err
	}
	appID, err := resolveAppReference(ctx, session, reference)
	if err != nil {
		return err
	}
	var credentials integrationCredentialsDocument
	if options.rotate {
		credentials, err = rotateIntegrationCredentials(ctx, session, appID, options)
	} else {
		credentials, err = readIntegrationCredentials(ctx, session, appID)
	}
	if err != nil {
		return err
	}
	if writesToTerminal(output) {
		_, _ = fmt.Fprintln(credentialStatusOutput, "These credentials authorize every Workspace of the App. Keep them in your backend's secret store.")
	}
	return writeIntegrationCredentials(output, credentials, options.format)
}

func readIntegrationCredentials(ctx context.Context, session *controlapi.Session, appID string) (integrationCredentialsDocument, error) {
	var credentials integrationCredentialsDocument
	if err := session.Request(ctx, http.MethodGet, "/v1/apps/"+appID+"/integration", "", nil, &credentials); err != nil {
		if controlapi.IsHTTPStatus(err, http.StatusNotFound) {
			return credentials, fmt.Errorf("read App credentials: %w (an App has credentials after its first cantelop deploy)", err)
		}
		return credentials, fmt.Errorf("read App credentials: %w", err)
	}
	if err := validateIntegrationCredentials(credentials, appID); err != nil {
		return credentials, err
	}
	return credentials, nil
}

func rotateIntegrationCredentials(ctx context.Context, session *controlapi.Session, appID string, options appCredentialsOptions) (integrationCredentialsDocument, error) {
	request := map[string]any{"grace_seconds": int64(options.grace / time.Second)}
	if options.retry {
		request = map[string]any{"retry": true}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return integrationCredentialsDocument{}, err
	}
	var credentials integrationCredentialsDocument
	if err := session.Request(ctx, http.MethodPost, "/v1/apps/"+appID+"/integration/rotate", "application/json", bytes.NewReader(body), &credentials); err != nil {
		var statusError *controlapi.StatusError
		if errors.As(err, &statusError) && statusError.Detail == "integration_rotation_unresolved" {
			return credentials, fmt.Errorf("rotate App credentials: %w (retry the existing rotation with --retry before issuing another token)", err)
		}
		if errors.As(err, &statusError) && statusError.Detail == "integration_rotation_unsupported" {
			return credentials, fmt.Errorf("rotate App credentials: %w (the live release predates rotation support: deploy with a newer @cantelop/sdk, or use --now to revoke the current token immediately)", err)
		}
		return credentials, fmt.Errorf("rotate App credentials: %w", err)
	}
	if err := validateIntegrationCredentials(credentials, appID); err != nil {
		return credentials, err
	}
	if credentials.Rotation == nil || (!options.retry && (options.grace > 0) != (credentials.PreviousTokenExpiresAt != nil)) {
		return credentials, errors.New("control plane returned an invalid rotation")
	}
	if options.retry {
		_, _ = fmt.Fprintf(credentialStatusOutput, "Retrying the existing integration rotation for %s. The token and grace period are unchanged.\n", credentials.Slug)
	} else if credentials.PreviousTokenExpiresAt != nil {
		_, _ = fmt.Fprintf(credentialStatusOutput, "Rotated the integration token for %s. The previous token keeps working until %s.\n",
			credentials.Slug, credentials.PreviousTokenExpiresAt.UTC().Format(time.RFC3339))
	} else {
		_, _ = fmt.Fprintf(credentialStatusOutput, "Rotated the integration token for %s. The previous token stops working once the Edge has the new one.\n", credentials.Slug)
	}
	revision := credentials.Rotation.Revision
	if options.wait {
		credentials, err = waitForIntegrationRotation(ctx, session, appID, credentials)
		if err != nil {
			return credentials, err
		}
	}
	if credentials.Rotation == nil || credentials.Rotation.Revision < revision {
		return credentials, errors.New("control plane returned a different rotation")
	}
	if credentials.Rotation.Status != "applied" {
		return credentials, nil
	}
	recorded, err := integration.ReplaceToken("default", appID, credentials.Token)
	if err != nil {
		return credentials, fmt.Errorf("token rotated, but updating the local SDK profile failed: %w", err)
	}
	if recorded {
		_, _ = fmt.Fprintln(credentialStatusOutput, "Updated the local default SDK profile.")
	}
	return credentials, nil
}

// waitForIntegrationRotation polls until the Edge has the rotated token. A
// later rotation may land meanwhile; its token supersedes this one.
func waitForIntegrationRotation(ctx context.Context, session *controlapi.Session, appID string, credentials integrationCredentialsDocument) (integrationCredentialsDocument, error) {
	deadline := time.Now().Add(rotationWaitTimeout)
	for credentials.Rotation.Status == "pending" {
		if time.Now().After(deadline) {
			_, _ = fmt.Fprintln(credentialStatusOutput, "Edge has not confirmed the new token yet. Check with cantelop app credentials before switching callers to it.")
			return credentials, errors.New("timed out waiting for the integration token to reach Edge; the local SDK profile is unchanged")
		}
		select {
		case <-ctx.Done():
			return credentials, ctx.Err()
		case <-time.After(rotationPollInterval):
		}
		next, err := readIntegrationCredentials(ctx, session, appID)
		if err != nil {
			return credentials, err
		}
		if next.Rotation == nil {
			return credentials, errors.New("control plane returned an invalid rotation")
		}
		credentials = next
	}
	if credentials.Rotation.Status == "failed" {
		return credentials, errors.New("integration configuration sync failed; Edge propagation may be uncertain and the local SDK profile is unchanged. Use cantelop app credentials rotate --retry to resend the existing token")
	}
	_, _ = fmt.Fprintln(credentialStatusOutput, "The new token is live on the Edge.")
	return credentials, nil
}

func validateIntegrationCredentials(credentials integrationCredentialsDocument, appID string) error {
	if credentials.AppID != appID || !ids.ValidRoutingSlug(credentials.Slug) || !integrationTokenPattern.MatchString(credentials.Token) ||
		!edgeURLPattern.MatchString(credentials.EdgeURL) ||
		(credentials.Rotation != nil && (credentials.Rotation.Revision < 1 || credentials.Rotation.RotatedAt.IsZero() ||
			(credentials.Rotation.Status != "pending" && credentials.Rotation.Status != "applied" && credentials.Rotation.Status != "failed"))) {
		return errors.New("control plane returned invalid App credentials")
	}
	return nil
}

func writeIntegrationCredentials(output io.Writer, credentials integrationCredentialsDocument, format string) error {
	switch format {
	case "token":
		_, err := fmt.Fprintln(output, credentials.Token)
		return err
	case "app-config":
		app := map[string]string{"id": credentials.AppID, "slug": credentials.Slug, "edgeUrl": credentials.EdgeURL, "accessToken": credentials.Token}
		document, err := json.Marshal(map[string]any{
			"schemaVersion": 1,
			"activeProfile": "default",
			"profiles": map[string]any{"default": map[string]any{
				"defaultApp": map[string]string{"slug": credentials.Slug},
				"apps":       []map[string]string{app},
			}},
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "%s\n", document)
		return err
	default:
		lines := fmt.Sprintf("CANTELOP_APP_SLUG=%s\nCANTELOP_INTEGRATION_TOKEN=%s\n", credentials.Slug, credentials.Token)
		if credentials.EdgeURL != "https://"+credentials.Slug+".cantelop.dev" {
			lines += "CANTELOP_EDGE_URL=" + credentials.EdgeURL + "\n"
		}
		_, err := io.WriteString(output, lines)
		return err
	}
}

func writesToTerminal(output io.Writer) bool {
	file, ok := output.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
