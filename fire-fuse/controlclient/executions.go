package controlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
	"net/http"
	"net/url"
)

// MessageExecution is the sandbox-bound lifecycle contract returned by control.
type MessageExecution = messagequeue.Execution

// MaxMessageAttempts bounds delivery attempts, including redeliveries, for one Message.
const MaxMessageAttempts = messagequeue.MaxAttempts

// RuntimeInspection is exposed through the control-plane execution contract.
type RuntimeInspection = messagequeue.RuntimeInspection

func (c *Client) SaveExecution(ctx context.Context, e messagequeue.Execution) (messagequeue.Execution, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	var result messagequeue.Execution
	err = c.do(ctx, http.MethodPost, "/v1/message-executions", bytes.NewReader(data), &result)
	return result, err
}
func (c *Client) ListExecutions(ctx context.Context) ([]messagequeue.Execution, error) {
	var r struct {
		Items []messagequeue.Execution `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/message-executions", nil, &r)
	return r.Items, err
}
func (c *Client) GetPinnedRoute(ctx context.Context, id, sandboxID string) (session.Route, error) {
	var result session.Route
	err := c.do(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(id)+"/route?sandbox_id="+url.QueryEscape(sandboxID), nil, &result)
	return result, err
}
func (c *Client) TerminateSandbox(ctx context.Context, id string) error {
	var result struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := c.do(ctx, http.MethodDelete, "/v1/sandboxes/"+url.PathEscape(id), nil, &result); err != nil {
		return err
	}
	if result.ID != id || result.State != "terminated" {
		return errors.New("sandbox termination not confirmed")
	}
	return nil
}

func (c *Client) PinnedWorkLease(ctx context.Context, sessionID, sandboxID, leaseID string, renew bool) error {
	data, _ := json.Marshal(struct {
		SessionID string `json:"session_id"`
		SandboxID string `json:"sandbox_id"`
		LeaseID   string `json:"lease_id"`
		Renew     bool   `json:"renew"`
	}{sessionID, sandboxID, leaseID, renew})
	var result map[string]any
	return c.do(ctx, http.MethodPost, "/v1/pinned-work-leases", bytes.NewReader(data), &result)
}

func (c *Client) ReleasePinnedWorkLease(ctx context.Context, sessionID, sandboxID, leaseID string) error {
	data, _ := json.Marshal(map[string]any{"session_id": sessionID, "sandbox_id": sandboxID, "lease_id": leaseID, "release": true})
	var result Session
	return c.do(ctx, http.MethodPost, "/v1/pinned-work-leases", bytes.NewReader(data), &result)
}
