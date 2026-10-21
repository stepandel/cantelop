package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/clients/cli/internal/render"
)

func runDatabaseMigrations(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	const usage = "usage: cantelop database migrations WORKSPACE_ID [--json] [-config cantelop.json]"
	options, err := parseOperationalOptions(args, false, 1, usage)
	if err != nil || len(options.positionals) != 1 || !ids.ValidLocalWorkspaceID(options.positionals[0]) {
		return errors.New(usage)
	}
	_, session, appID, err := resolveTarget(ctx, client, options.configPath, "")
	if err != nil {
		return err
	}
	var document struct {
		WorkspaceID string `json:"workspace_id"`
		Application []struct {
			Digest     string   `json:"digest"`
			Statements []string `json:"statements"`
			AppliedAt  int64    `json:"applied_at"`
		} `json:"application"`
		ExpectedDigest *string `json:"expected_application_digest"`
	}
	if err := session.Request(ctx, http.MethodGet, "/v1/apps/"+appID+"/workspaces/"+options.positionals[0]+"/database/migrations", "", nil, &document); err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	if document.WorkspaceID != options.positionals[0] {
		return errors.New("control plane returned a different Workspace")
	}
	if options.jsonOutput {
		return render.WriteJSON(output, document)
	}
	writer := render.NewTable(output)
	_, _ = fmt.Fprintln(writer, "MIGRATION\tAPPLIED")
	for _, row := range document.Application {
		_, _ = fmt.Fprintf(writer, "%s\t%s\n", row.Digest, time.UnixMilli(row.AppliedAt).UTC().Format(time.RFC3339))
	}
	return writer.Flush()
}
