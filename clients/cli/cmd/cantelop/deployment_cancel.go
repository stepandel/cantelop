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
	"github.com/stepandel/cantelop/clients/cli/internal/deploy"
	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

type cancelledUploadDocument struct {
	controlapi.DeploymentUpload
	CleanupPending bool   `json:"cleanup_pending"`
	IdempotencyKey string `json:"idempotency_key"`
}

func runDeploymentCancel(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	const usage = "usage: cantelop deploy cancel [--app APP_ID_OR_SLUG] [--upload UPLOAD_ID | --release RELEASE_ID] [-config cantelop.json]"
	flags := flag.NewFlagSet("cantelop deploy cancel", flag.ContinueOnError)
	configPath := flags.String("config", projectconfig.DefaultPath, "project configuration path")
	app := flags.String("app", "", "App ID or slug (defaults to the current project)")
	releaseID := flags.String("release", "", "queued or provisioning release to cancel")
	uploadID := flags.String("upload", "", "specific upload to cancel (defaults to the pending upload)")
	if err := parseFlags(flags, args, usage); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" ||
		(*app != "" && !ids.ValidAppID(*app) && !ids.ValidRoutingSlug(*app)) ||
		(*uploadID != "" && !ids.DeploymentUploadIDPattern.MatchString(*uploadID)) ||
		(*releaseID != "" && !ids.ReleaseIDPattern.MatchString(*releaseID)) || (*releaseID != "" && *uploadID != "") {
		return errors.New(usage)
	}
	_, session, appID, err := resolveTarget(ctx, client, *configPath, *app)
	if err != nil {
		return err
	}
	if *releaseID != "" {
		return requestDeploymentRecovery(ctx, session, appID, *releaseID, "cancel", output)
	}
	basePath := "/v1/apps/" + appID + "/deployments/uploads"
	if *uploadID == "" {
		var page struct {
			Items []controlapi.DeploymentUpload `json:"items"`
		}
		if err := session.Request(ctx, http.MethodGet, basePath, "", nil, &page); err != nil {
			return fmt.Errorf("find pending deployment upload: %w", err)
		}
		if len(page.Items) == 0 {
			_, err := fmt.Fprintln(output, "No pending deployment upload; queued and active releases are unchanged.")
			return err
		}
		if len(page.Items) != 1 || !ids.DeploymentUploadIDPattern.MatchString(page.Items[0].ID) ||
			page.Items[0].AppID != appID || (page.Items[0].Status != "preparing" && page.Items[0].Status != "uploading") {
			return errors.New("control plane returned invalid pending deployment uploads")
		}
		*uploadID = page.Items[0].ID
	}
	var cancelled cancelledUploadDocument
	if err := session.Request(ctx, http.MethodDelete, basePath+"/"+*uploadID, "", nil, &cancelled); err != nil {
		if controlapi.IsHTTPStatus(err, http.StatusConflict) {
			return fmt.Errorf("deployment upload already became a release; queued and active releases cannot be cancelled with this command: %w", err)
		}
		return fmt.Errorf("cancel deployment upload: %w", err)
	}
	if cancelled.ID != *uploadID || cancelled.AppID != appID || cancelled.Status != "cancelled" || cancelled.ReleaseID != "" {
		return errors.New("control plane returned an invalid cancelled deployment upload")
	}
	_, _ = fmt.Fprintf(output, "Cancelled deployment upload %s; App upload lock released.\n", cancelled.ID)
	if cancelled.CleanupPending {
		_, _ = fmt.Fprintf(output, "Storage cleanup is pending; retry with cantelop deploy cancel --app %s --upload %s. New deployments can proceed.\n", appID, cancelled.ID)
	}
	if err := deploy.ClearCancelledRetry(appID, cancelled.IdempotencyKey); err != nil {
		return fmt.Errorf("upload cancelled, but clearing local retry state failed: %w", err)
	}
	return nil
}
