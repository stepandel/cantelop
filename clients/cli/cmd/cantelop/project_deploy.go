package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
	"github.com/stepandel/cantelop/clients/cli/internal/deploy"
	"github.com/stepandel/cantelop/clients/cli/internal/integration"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

type projectDeployOptions struct {
	configPath string
	app        string
	envFile    string
	dryRun     bool
	createApp  bool
}

func runProjectBuild(ctx context.Context, args []string, output io.Writer) error {
	const usage = "usage: cantelop build [--app name] [-config cantelop.json]"
	flags := flag.NewFlagSet("cantelop build", flag.ContinueOnError)
	configPath := flags.String("config", projectconfig.DefaultPath, "project configuration path")
	app := flags.String("app", "", "App name when the definition contains multiple Apps")
	if err := parseFlags(flags, args, usage); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" {
		return errors.New(usage)
	}
	config, built, err := deploy.BuildSelected(ctx, *configPath, *app, output)
	if err != nil {
		return err
	}
	defer os.RemoveAll(built.TemporaryRoot)
	_, err = fmt.Fprintf(output, "Build succeeded for %s (Session runtime image %s)\n", config.AppSlug, built.ImageID)
	return err
}

func runProjectDeployCommand(ctx context.Context, args []string, input io.Reader, output io.Writer, client *http.Client) error {
	options, err := parseProjectDeployOptions(args)
	if err != nil {
		return err
	}
	if options.dryRun {
		return runProjectDeployOptions(ctx, options, output, nil, nil)
	}
	session, err := newTenantSession(client)
	if err != nil {
		return err
	}
	var confirm func(string) (bool, error)
	if canPrompt(input) {
		confirm = func(slug string) (bool, error) { return confirmAppCreation(ctx, input, output, slug) }
	}
	return runProjectDeployOptions(ctx, options, output, session, confirm)
}

func parseProjectDeployOptions(args []string) (projectDeployOptions, error) {
	const usage = "usage: cantelop deploy [--dry-run] [--create-app] [--env-file .env] [--app name] [-config cantelop.json]"
	flags := flag.NewFlagSet("cantelop deploy", flag.ContinueOnError)
	configPath := flags.String("config", projectconfig.DefaultPath, "project configuration path")
	app := flags.String("app", "", "App name when the definition contains multiple Apps")
	dryRun := flags.Bool("dry-run", false, "build and validate without uploading a release")
	createApp := flags.Bool("create-app", false, "create the configured App when it does not exist")
	envFile := flags.String("env-file", "", "load declared production environment values from a project-relative file")
	if err := parseFlags(flags, args, usage); err != nil {
		return projectDeployOptions{}, err
	}
	if strings.TrimSpace(*configPath) == "" {
		return projectDeployOptions{}, errors.New(usage)
	}
	invalidEnvFile := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "env-file" && strings.TrimSpace(*envFile) == "" {
			invalidEnvFile = true
		}
	})
	if invalidEnvFile {
		return projectDeployOptions{}, errors.New(usage)
	}
	return projectDeployOptions{configPath: *configPath, app: *app, envFile: *envFile, dryRun: *dryRun, createApp: *createApp}, nil
}

func runProjectDeployOptions(
	ctx context.Context,
	options projectDeployOptions,
	output io.Writer,
	session *controlapi.Session,
	confirm func(string) (bool, error),
) error {
	config, err := projectconfig.LoadSelected(options.configPath, options.app)
	if err != nil {
		return err
	}
	var updates []configUpdate
	if options.envFile != "" {
		updates, err = loadProjectDeploymentEnvironment(config, options.envFile)
		if err != nil {
			return err
		}
	}
	built, err := deploy.Build(ctx, config, output)
	if err != nil {
		return err
	}
	defer os.RemoveAll(built.TemporaryRoot)
	if options.dryRun {
		for _, update := range updates {
			_, _ = fmt.Fprintf(output, "Would set %s (%s)\n", update.name, update.kind)
		}
		_, err = fmt.Fprintf(output, "Dry run succeeded for %s (Session runtime image %s); no release was uploaded.\n",
			config.AppSlug, built.ImageID)
		return err
	}
	if session == nil {
		return errors.New("deployment requires an authenticated tenant session")
	}
	appID, err := resolveProjectApp(ctx, session, config.AppSlug, options.createApp, confirm, output)
	if err != nil {
		return err
	}
	if err := applyConfigUpdates(ctx, updates, "/v1/apps/"+appID+"/config", output, session); err != nil {
		return err
	}
	if config.Definition != "" {
		if err := integration.ConfigureDeployed(ctx, config, appID, session); err != nil {
			return err
		}
	}
	return deploy.Release(ctx, appID, built.ImageID, built.ArtifactDirectory, output, session)
}

func resolveProjectApp(
	ctx context.Context,
	session *controlapi.Session,
	slug string,
	createMissing bool,
	confirm func(string) (bool, error),
	output io.Writer,
) (string, error) {
	appID, err := controlapi.ResolveAppID(ctx, session, slug)
	if err != nil {
		if !controlapi.IsHTTPStatus(err, http.StatusNotFound) {
			return "", err
		}
		approved := createMissing
		if !approved && confirm != nil {
			approved, err = confirm(slug)
			if err != nil {
				return "", err
			}
		}
		if !approved {
			return "", fmt.Errorf("App %q does not exist; rerun with --create-app to create it", slug)
		}
		app, createErr := controlapi.CreateApp(ctx, session, slug)
		if createErr != nil {
			return "", fmt.Errorf("create App %q: %w", slug, createErr)
		}
		appID = app.ID
		_, _ = fmt.Fprintf(output, "Created App %s (%s)\n", slug, appID)
	}
	return appID, nil
}

func canPrompt(input io.Reader) bool {
	if input == nil {
		return false
	}
	file, ok := input.(*os.File)
	if !ok {
		return true
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func confirmAppCreation(ctx context.Context, input io.Reader, output io.Writer, slug string) (bool, error) {
	if _, err := fmt.Fprintf(output, "App %q does not exist. Create it? [y/N] ", slug); err != nil {
		return false, err
	}
	answer, err := readCommandInput(ctx, func() ([]byte, error) {
		return bufio.NewReader(input).ReadBytes('\n')
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read App creation confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(string(answer))) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}
