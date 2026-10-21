package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/internal/resourceid"
)

var ErrAdmissionLimit = controlclient.ErrAdmissionLimit

type ControlError = controlclient.Error
type App = controlclient.App
type Workspace = controlclient.Workspace
type Session = controlclient.Session
type CreateSessionSpec = controlclient.CreateSessionSpec
type MessageDelivery = controlclient.MessageDelivery

type MessageDeliveries interface {
	EnqueueMessageDelivery(context.Context, MessageDelivery) (MessageDelivery, error)
	GetMessageDelivery(context.Context, string) (MessageDelivery, error)
	// WaitForMessageReply returns durable state on a change or a bounded timeout.
	WaitForMessageReply(context.Context, string) (MessageDelivery, error)
}
type WorkspaceDirectory = controlclient.WorkspaceDirectory

// WorkspaceCreator owns App-scoped Workspace provisioning and deletion.
type WorkspaceCreator interface {
	CreateWorkspace(context.Context, string, string) (Workspace, error)
	OpenWorkspace(context.Context, string, string) (Workspace, error)
	WorkspaceDeleter
}

type WorkspaceDeletion struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	HostArchived bool   `json:"hostArchived"`
}
type WorkspaceDeleter interface {
	DeleteWorkspace(context.Context, string, string) (WorkspaceDeletion, error)
}

type ProductClient struct {
	baseURL *url.URL
	client  *http.Client
}

func NewProductClient(rawURL string, client *http.Client) (*ProductClient, error) {
	baseURL, err := url.Parse(rawURL)
	if err != nil || baseURL.Scheme != "https" || baseURL.User != nil ||
		baseURL.RawQuery != "" || baseURL.Fragment != "" || strings.Trim(baseURL.Path, "/") != "" ||
		baseURL.Hostname() == "" {
		return nil, errors.New("product-control URL must be an HTTPS origin")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &ProductClient{baseURL: baseURL, client: client}, nil
}

func (c *ProductClient) CreateWorkspace(ctx context.Context, appID, slug string) (Workspace, error) {
	return c.workspaceRequest(ctx, appID, slug, "/workspaces")
}

func (c *ProductClient) OpenWorkspace(ctx context.Context, appID, slug string) (Workspace, error) {
	return c.workspaceRequest(ctx, appID, slug, "/workspaces/open")
}

func (c *ProductClient) DeleteWorkspace(ctx context.Context, appID, workspaceID string) (WorkspaceDeletion, error) {
	if !resourceid.ValidAppID(appID) || !resourceid.ValidWorkspaceID(workspaceID) {
		return WorkspaceDeletion{}, errors.New("invalid App-scoped Workspace deletion")
	}
	var result WorkspaceDeletion
	path := "/internal/v1/apps/" + appID + "/workspaces/" + workspaceID
	if err := controlclient.Do(ctx, c.client, c.baseURL, http.MethodDelete, path, nil, &result); err != nil {
		return WorkspaceDeletion{}, err
	}
	if result.ID != workspaceID || result.Status != "deleting" {
		return WorkspaceDeletion{}, errors.New("product control returned a different Workspace deletion")
	}
	return result, nil
}

func (c *ProductClient) workspaceRequest(ctx context.Context, appID, slug, suffix string) (Workspace, error) {
	if !resourceid.ValidAppID(appID) || !resourceid.ValidSlug(slug) {
		return Workspace{}, errors.New("invalid App-scoped Workspace creation")
	}
	document, err := json.Marshal(struct {
		Slug string `json:"slug"`
	}{Slug: slug})
	if err != nil {
		return Workspace{}, err
	}
	var workspace Workspace
	path := "/internal/v1/apps/" + url.PathEscape(appID) + suffix
	if err := controlclient.Do(ctx, c.client, c.baseURL, http.MethodPost, path, bytes.NewReader(document), &workspace); err != nil {
		return Workspace{}, err
	}
	if workspace.AppID != appID || workspace.Slug != slug || workspace.ID == "" {
		return Workspace{}, errors.New("product control returned a different Workspace")
	}
	return workspace, nil
}
