package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

func runDeploymentRestart(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	const usage = "usage: cantelop deploy restart --release RELEASE_ID [--app APP_ID_OR_SLUG] [-config cantelop.json]"
	flags := flag.NewFlagSet("cantelop deploy restart", flag.ContinueOnError)
	app := flags.String("app", "", "App ID or slug")
	releaseID := flags.String("release", "", "release to restart using uploaded artifacts")
	configPath := flags.String("config", projectconfig.DefaultPath, "project configuration path")
	if err := parseFlags(flags, args, usage); err != nil {
		return err
	}
	if !ids.ReleaseIDPattern.MatchString(*releaseID) || (*app != "" && !ids.ValidAppID(*app) && !ids.ValidRoutingSlug(*app)) {
		return errors.New(usage)
	}
	_, session, appID, err := resolveTarget(ctx, client, *configPath, *app)
	if err != nil {
		return err
	}
	return requestDeploymentRecovery(ctx, session, appID, *releaseID, "restart", output)
}

func requestDeploymentRecovery(ctx context.Context, session *controlapi.Session, appID, releaseID, action string, output io.Writer) error {
	var release controlapi.Release
	if err := session.Request(ctx, http.MethodPost, "/v1/apps/"+appID+"/deployments/"+releaseID+"/"+action, "", nil, &release); err != nil {
		return fmt.Errorf("%s deployment: %w", action, err)
	}
	if err := controlapi.ValidateRelease(release, appID); err != nil {
		return err
	}
	if action == "cancel" && (release.ID != releaseID || (release.Status != "cancelling" && release.Status != "cancelled")) {
		return errors.New("control plane returned an invalid cancellation")
	}
	if action == "restart" && release.ID == releaseID && release.Status != "cancelling" {
		return errors.New("control plane returned an invalid restart")
	}
	if release.Status == "cancelling" {
		_, err := fmt.Fprintf(output, "Deployment %s is cancelling. The App stays locked until provider cancellation completes.\n", releaseID)
		if err != nil {
			return err
		}
		if action == "restart" {
			_, err = fmt.Fprintln(output, "A new release will start automatically using the uploaded artifacts.")
			return err
		}
		return nil
	}
	_, err := fmt.Fprintf(output, "Deployment v%d (%s): %s\n", release.Version, release.ID, release.Status)
	return err
}
