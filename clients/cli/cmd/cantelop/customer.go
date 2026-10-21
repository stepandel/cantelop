package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/auth"
	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
	"github.com/stepandel/cantelop/clients/cli/internal/deploy"
	"github.com/stepandel/cantelop/clients/cli/internal/ids"
)

const defaultConsoleURL = "https://cantelop.com"

type cliPublicConfig struct {
	ControlURL string `json:"control_url"`
}

func runCustomer(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	return runCustomerWithInput(ctx, args, os.Stdin, output, client)
}

func runCustomerWithInput(ctx context.Context, args []string, input io.Reader, output io.Writer, client *http.Client) error {
	if client == nil {
		client = http.DefaultClient
	}
	// App uploads intentionally have no whole-request timeout: the archive is
	// streamed directly and may be very large. Bounded metadata calls use
	// request contexts instead.
	client = &http.Client{Transport: client.Transport, CheckRedirect: client.CheckRedirect, Jar: client.Jar}
	return dispatchCommand(ctx, args, input, output, client)
}

// newTenantSession authenticates control-plane requests with the stored CLI login.
func newTenantSession(client *http.Client) (*controlapi.Session, error) {
	credentials, err := auth.LoadCredentials(auth.CredentialsPath())
	if err != nil {
		return nil, err
	}
	return &controlapi.Session{Client: client, Credentials: credentials}, nil
}

func runAppList(ctx context.Context, args []string, output io.Writer, session *controlapi.Session) error {
	if len(args) != 0 {
		return errors.New("usage: cantelop app list")
	}
	var page struct {
		Items []controlapi.App `json:"items"`
	}
	if err := session.Request(ctx, http.MethodGet, "/v1/apps", "", nil, &page); err != nil {
		return fmt.Errorf("list Apps: %w", err)
	}
	for _, app := range page.Items {
		if !ids.ValidAppID(app.ID) || !ids.ValidRoutingSlug(app.Slug) {
			return errors.New("control plane returned an invalid App")
		}
		_, _ = fmt.Fprintf(output, "%s\t%s\n", app.ID, app.Slug)
	}
	return nil
}

func runAppInspect(ctx context.Context, args []string, output io.Writer, session *controlapi.Session) error {
	if len(args) != 1 || !ids.ValidAppID(args[0]) {
		return errors.New("usage: cantelop app inspect APP_ID")
	}
	var app controlapi.App
	if err := session.Request(ctx, http.MethodGet, "/v1/apps/"+args[0], "", nil, &app); err != nil {
		return fmt.Errorf("inspect App: %w", err)
	}
	if app.ID != args[0] || !ids.ValidRoutingSlug(app.Slug) {
		return errors.New("control plane returned an invalid App")
	}
	_, err := fmt.Fprintf(output, "App: %s\nSlug: %s\n", app.ID, app.Slug)
	return err
}

func runAppArchive(ctx context.Context, args []string, output io.Writer, session *controlapi.Session) error {
	if len(args) != 1 || !ids.ValidAppID(args[0]) {
		return errors.New("usage: cantelop app archive APP_ID")
	}
	if err := session.Request(ctx, http.MethodDelete, "/v1/apps/"+args[0], "", nil, nil); err != nil {
		return fmt.Errorf("archive App: %w", err)
	}
	_, err := fmt.Fprintf(output, "Archive requested for %s\n", args[0])
	return err
}

func runConfigCommand(ctx context.Context, args []string, input io.Reader, output io.Writer, session *controlapi.Session) error {
	const usage = "usage: cantelop app env|secret set|list|unset RESOURCE_ID [NAME[=VALUE]]"
	if len(args) < 3 || (args[0] != "env" && args[0] != "secret") {
		return errors.New(usage)
	}
	kind, verb, appID := args[0], args[1], args[2]
	if !ids.ValidAppID(appID) {
		return errors.New(usage)
	}
	basePath := "/v1/apps/" + appID + "/config"
	pathKind := "variables"
	if kind == "secret" {
		pathKind = "secrets"
	}
	switch verb {
	case "list":
		if len(args) != 3 {
			return errors.New(usage)
		}
		var page controlapi.ConfigValuePage
		if err := session.Request(ctx, http.MethodGet, basePath, "", nil, &page); err != nil {
			return fmt.Errorf("list configuration: %w", err)
		}
		for _, item := range page.Items {
			if item.Kind == "variable" && kind == "env" && item.Value != nil {
				_, _ = fmt.Fprintf(output, "%s=%s\n", item.Name, *item.Value)
			}
			if item.Kind == "secret" && kind == "secret" && item.Value == nil {
				_, _ = fmt.Fprintln(output, item.Name)
			}
		}
		return nil
	case "set":
		return runConfigSet(ctx, kind, args[3:], basePath+"/"+pathKind, input, output, session)
	case "unset":
		if len(args) != 4 || !ids.ConfigNamePattern.MatchString(args[3]) {
			return errors.New(usage)
		}
		if err := session.Request(ctx, http.MethodDelete, basePath+"/"+pathKind+"/"+args[3], "", nil, nil); err != nil {
			return fmt.Errorf("unset configuration: %w", err)
		}
		_, _ = fmt.Fprintf(output, "Unset %s\n", args[3])
		return nil
	default:
		return errors.New(usage)
	}
}

func runLogin(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	const usage = "usage: cantelop login [-console URL]"
	flags := flag.NewFlagSet("cantelop login", flag.ContinueOnError)
	consoleURL := flags.String("console", defaultConsoleURL, "Cantelop console origin")
	if err := parseFlags(flags, args, usage); err != nil {
		return err
	}
	origin, err := controlapi.ValidatePublicOrigin(*consoleURL)
	if err != nil {
		return fmt.Errorf("invalid console URL: %w", err)
	}
	var config cliPublicConfig
	requestContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = controlapi.RequestJSON(requestContext, client, http.MethodGet, origin+"/api/cli/config", "", nil, &config)
	cancel()
	controlOrigin, validationErr := controlapi.ValidatePublicOrigin(config.ControlURL)
	if err != nil {
		return fmt.Errorf("read CLI configuration: %w", err)
	}
	if validationErr != nil || controlOrigin != config.ControlURL {
		return errors.New("console returned invalid CLI configuration")
	}
	refreshToken, err := auth.BrowserLogin(ctx, origin, output)
	if err != nil {
		return err
	}
	credentials := controlapi.Credentials{ConsoleURL: origin, ControlURL: controlOrigin, RefreshToken: refreshToken}
	requestContext, cancel = context.WithTimeout(ctx, 10*time.Second)
	_, err = controlapi.ExchangeTenantToken(requestContext, client, credentials)
	cancel()
	if err != nil {
		return fmt.Errorf("validate login: %w", err)
	}
	if err := auth.SaveCredentials(auth.CredentialsPath(), credentials); err != nil {
		return fmt.Errorf("save login: %w", err)
	}
	_, _ = fmt.Fprintln(output, "Logged in to Cantelop")
	return nil
}

func runAppCreate(ctx context.Context, args []string, output io.Writer, session *controlapi.Session) error {
	const usage = "usage: cantelop app create -slug APP_SLUG"
	flags := flag.NewFlagSet("cantelop app create", flag.ContinueOnError)
	slug := flags.String("slug", "", "public App slug")
	if err := parseFlags(flags, args, usage); err != nil {
		return err
	}
	if !ids.ValidRoutingSlug(*slug) {
		return errors.New(usage)
	}
	app, err := controlapi.CreateApp(ctx, session, *slug)
	if err != nil {
		return fmt.Errorf("create App: %w", err)
	}
	_, _ = fmt.Fprintf(output, "Created %s\n", app.ID)
	return nil
}

func runAppDeploy(ctx context.Context, args []string, output io.Writer, session *controlapi.Session) error {
	const usage = "usage: cantelop app deploy APP_ID -image IMAGE -artifact DIRECTORY"
	if len(args) == 0 || !ids.ValidAppID(args[0]) {
		return errors.New(usage)
	}
	appID := args[0]
	flags := flag.NewFlagSet("cantelop app deploy", flag.ContinueOnError)
	image := flags.String("image", "", "local Docker image reference")
	artifactDirectory := flags.String("artifact", "", "SDK-built API artifact directory")
	if err := parseFlags(flags, args[1:], usage); err != nil {
		return err
	}
	if !validImage(*image) || *artifactDirectory == "" {
		return errors.New(usage)
	}
	return deploy.Release(ctx, appID, *image, *artifactDirectory, output, session)
}

func validImage(image string) bool {
	return image != "" && len(image) <= 512 && image == strings.TrimSpace(image) && !strings.HasPrefix(image, "-") && !strings.ContainsAny(image, "\x00\r\n")
}
