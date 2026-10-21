package controlclient

import (
	"context"
	"errors"
	"net/http"
	"regexp"
)

type WorkspaceSync interface {
	SyncWorkspace(context.Context, string, string, *http.Request) (*http.Response, error)
}

var syncAppID = regexp.MustCompile(`^app_[0-9a-f]{32}$`)
var syncWorkspaceID = regexp.MustCompile(`^wsp_[0-9a-f]{32}$`)

func (c *Client) SyncWorkspace(ctx context.Context, appID, workspaceID string, source *http.Request) (*http.Response, error) {
	if !syncAppID.MatchString(appID) || !syncWorkspaceID.MatchString(workspaceID) {
		return nil, errors.New("invalid sync scope")
	}
	endpoint := *c.baseURL
	endpoint.Path = "/v1/apps/" + appID + "/workspace-sync/" + workspaceID
	endpoint.RawQuery = source.URL.RawQuery
	request, err := http.NewRequestWithContext(ctx, source.Method, endpoint.String(), source.Body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	return c.client.Do(request)
}
