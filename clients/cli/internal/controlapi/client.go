// Package controlapi is the CLI's client for the Cantelop console and control API:
// authenticated requests, App and release documents, and artifact uploads.
package controlapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RetryTransient runs operation up to attempts times, backing off between
// attempts while it fails with a network error or a 5xx response.
func RetryTransient(ctx context.Context, attempts int, operation func() error) error {
	delay := TransientRetryDelay
	for attempt := 1; ; attempt++ {
		err := operation()
		if err == nil || attempt >= attempts || !transientRequestError(ctx, err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
		delay = min(2*delay, maxTransientRetryDelay)
	}
}

// transientRequestError reports whether a request may succeed when repeated:
// the network failed or the server answered 5xx, and ctx is still live.
func transientRequestError(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var status *StatusError
	if errors.As(err, &status) {
		return status.StatusCode >= 500
	}
	var network *url.Error
	return errors.As(err, &network)
}

func RequestJSON(ctx context.Context, client *http.Client, method, endpoint, contentType string, body io.Reader, output any, headers ...string) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	for index := 0; index+1 < len(headers); index += 2 {
		request.Header.Set(headers[index], headers[index+1])
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	document, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if readErr != nil || len(document) > maxResponseBytes {
		return errors.New("response was invalid")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(document, &failure)
		return &StatusError{StatusCode: response.StatusCode, Detail: failure.Error}
	}
	if output != nil && json.Unmarshal(document, output) != nil {
		return errors.New("response was invalid")
	}
	return nil
}

type StatusError struct {
	StatusCode int
	Detail     string
}

func (e *StatusError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Detail)
	}
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

func IsHTTPStatus(err error, statusCode int) bool {
	var statusError *StatusError
	return errors.As(err, &statusError) && statusError.StatusCode == statusCode
}

func ValidatePublicOrigin(raw string) (string, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.User != nil || endpoint.Host == "" || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return "", errors.New("URL must be an origin")
	}
	local := endpoint.Scheme == "http" && (endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "::1" || endpoint.Hostname() == "localhost")
	if endpoint.Scheme != "https" && !local {
		return "", errors.New("URL must use HTTPS")
	}
	return strings.TrimSuffix(endpoint.String(), "/"), nil
}

const maxResponseBytes = 1 << 20

var (
	TransientRetryDelay = time.Second
)
