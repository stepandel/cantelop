package controlclient

import (
	"context"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
	"net/http"
	"net/url"
)

func (c *Client) SessionView(ctx context.Context, workspaceID, sessionID string) (sessionevents.View, error) {
	var value sessionevents.View
	query := url.Values{"workspace_id": {workspaceID}, "session_id": {sessionID}}
	err := c.do(ctx, http.MethodGet, "/v1/session-view?"+query.Encode(), nil, &value)
	return value, err
}
