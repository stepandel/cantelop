// Package firecracker communicates with the Firecracker host API.
package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

const (
	requestTimeout = 10 * time.Second
	// snapshotRequestTimeout bounds snapshot create and load, which write or
	// read the whole guest memory file before Firecracker answers. It applies
	// only when the caller supplies no deadline of its own.
	snapshotRequestTimeout = 30 * time.Minute
	maxAPIErrorBodyBytes   = 64 << 10
)

var (
	ErrInvalidSocketPath = errors.New("invalid Firecracker API socket path")
	ErrInvalidEndpoint   = errors.New("invalid Firecracker API endpoint")
)

// APIError reports a non-successful response from Firecracker.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	message := strings.TrimSpace(e.Body)
	if message == "" {
		return fmt.Sprintf("Firecracker API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("Firecracker API returned HTTP %d: %s", e.StatusCode, message)
}

// Client sends JSON requests to one Firecracker process over its Unix socket.
type Client struct {
	httpClient *http.Client
	// snapshotClient carries no fixed timeout. http.Client.Timeout caps the
	// whole exchange and cannot be extended per request, so the shared 10s
	// bound would abort any snapshot larger than that window regardless of the
	// caller's context.
	snapshotClient *http.Client
}

// NewClient constructs a client pinned to an absolute Unix-socket path.
func NewClient(socketPath string) (*Client, error) {
	if strings.TrimSpace(socketPath) == "" || !filepath.IsAbs(socketPath) {
		return nil, ErrInvalidSocketPath
	}

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}

	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   requestTimeout,
		},
		snapshotClient: &http.Client{Transport: transport},
	}, nil
}

// Put sends one JSON PUT request to a Firecracker API endpoint.
func (c *Client) Put(ctx context.Context, endpoint string, value any) error {
	return c.sendJSON(ctx, c.httpClient, http.MethodPut, endpoint, value)
}

// Patch sends one JSON PATCH request to a Firecracker API endpoint.
func (c *Client) Patch(ctx context.Context, endpoint string, value any) error {
	return c.sendJSON(ctx, c.httpClient, http.MethodPatch, endpoint, value)
}

// Get decodes one JSON response from a Firecracker API endpoint.
func (c *Client) Get(ctx context.Context, endpoint string, result any) error {
	if !strings.HasPrefix(endpoint, "/") || strings.HasPrefix(endpoint, "//") {
		return ErrInvalidEndpoint
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://firecracker"+endpoint, nil)
	if err != nil {
		return fmt.Errorf("build Firecracker request: %w", err)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("call Firecracker API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxAPIErrorBodyBytes))
		return &APIError{StatusCode: response.StatusCode, Body: string(body)}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxAPIErrorBodyBytes)).Decode(result); err != nil {
		return fmt.Errorf("decode Firecracker response: %w", err)
	}
	return nil
}

// putSnapshot sends one snapshot request, whose duration scales with the guest
// memory size rather than fitting the short configuration-call budget.
func (c *Client) putSnapshot(ctx context.Context, endpoint string, value any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, snapshotRequestTimeout)
		defer cancel()
	}
	return c.sendJSON(ctx, c.snapshotClient, http.MethodPut, endpoint, value)
}

func (c *Client) sendJSON(
	ctx context.Context,
	httpClient *http.Client,
	method, endpoint string,
	value any,
) error {
	if !strings.HasPrefix(endpoint, "/") || strings.HasPrefix(endpoint, "//") {
		return ErrInvalidEndpoint
	}

	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode Firecracker request: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		method,
		"http://firecracker"+endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("build Firecracker request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("call Firecracker API: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}

	errorBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxAPIErrorBodyBytes))
	if readErr != nil {
		return fmt.Errorf("read Firecracker API error: %w", readErr)
	}
	return &APIError{StatusCode: response.StatusCode, Body: string(errorBody)}
}

// CloseIdleConnections releases sockets held by the HTTP transport.
func (c *Client) CloseIdleConnections() {
	c.httpClient.CloseIdleConnections()
}
