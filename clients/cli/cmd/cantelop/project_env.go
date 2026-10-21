package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

type projectEnvSyncOptions struct {
	configPath string
	app        string
	envFile    string
	dryRun     bool
}

func runProjectEnvSyncCommand(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	options, err := parseProjectEnvSyncOptions(args)
	if err != nil {
		return err
	}
	if options.dryRun {
		return runProjectEnvSync(ctx, options, output, nil)
	}
	session, err := newTenantSession(client)
	if err != nil {
		return err
	}
	return runProjectEnvSync(ctx, options, output, session)
}

func parseProjectEnvSyncOptions(args []string) (projectEnvSyncOptions, error) {
	const usage = "usage: cantelop env sync [--env-file .env] [--dry-run] [-config cantelop.json]"
	flags := flag.NewFlagSet("cantelop env sync", flag.ContinueOnError)
	configPath := flags.String("config", projectconfig.DefaultPath, "project configuration path")
	app := flags.String("app", "", "App name")
	envFile := flags.String("env-file", ".env", "project-relative environment file")
	dryRun := flags.Bool("dry-run", false, "validate and preview without changing App configuration")
	if err := parseFlags(flags, args, usage); err != nil {
		return projectEnvSyncOptions{}, err
	}
	if strings.TrimSpace(*configPath) == "" || strings.TrimSpace(*envFile) == "" {
		return projectEnvSyncOptions{}, errors.New(usage)
	}
	return projectEnvSyncOptions{configPath: *configPath, app: *app, envFile: *envFile, dryRun: *dryRun}, nil
}

func runProjectEnvSync(ctx context.Context, options projectEnvSyncOptions, output io.Writer, session *controlapi.Session) error {
	config, err := projectconfig.LoadSelected(options.configPath, options.app)
	if err != nil {
		return err
	}
	updates, err := loadProjectDeploymentEnvironment(config, options.envFile)
	if err != nil {
		return err
	}
	if options.dryRun {
		for _, update := range updates {
			_, _ = fmt.Fprintf(output, "Would set %s (%s)\n", update.name, update.kind)
		}
		_, err = fmt.Fprintf(output, "Dry run succeeded for %s; no configuration was changed.\n", config.AppSlug)
		return err
	}
	if session == nil {
		return errors.New("environment sync requires an authenticated tenant session")
	}
	appID, err := controlapi.ResolveAppID(ctx, session, config.AppSlug)
	if err != nil {
		return err
	}
	if err := applyConfigUpdates(ctx, updates, "/v1/apps/"+appID+"/config", output, session); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Updated %d configuration values for %s.\n", len(updates), config.AppSlug)
	return err
}
