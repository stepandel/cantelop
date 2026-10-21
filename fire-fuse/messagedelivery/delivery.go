// Package messagedelivery implements the sandbox-fenced mailbox protocol.
package messagedelivery

import (
	"context"
	"errors"
	"fmt"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"io"
	"net/http"
	"net/url"
)

const maxOutputBodyBytes = (1 << 20) + (64 << 10)

var ErrRuntimeProtocol = errors.New("invalid Sandbox Message protocol response")

type RuntimeResponseError struct {
	Operation  string
	StatusCode int
}

func (e *RuntimeResponseError) Error() string {
	return fmt.Sprintf("%s returned HTTP %d", e.Operation, e.StatusCode)
}
func (e *RuntimeResponseError) Unwrap() error { return ErrRuntimeProtocol }

type ObservationRecord struct {
	AppID             string
	WorkspaceID       string
	SessionID         string
	ConcreteSessionID string
	SandboxID         string
	MessageID         string
	AttemptID         string
	TraceID           string
	Observation       messageprotocol.RuntimeObservation
}

type ObservationRecorder func(context.Context, ObservationRecord) error

func runtimeRequest(
	ctx context.Context,
	method string,
	target *url.URL,
	path string,
	headers http.Header,
	body io.Reader,
) (*http.Request, error) {
	endpoint := *target
	parsed, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	endpoint.Path, endpoint.RawQuery = parsed.Path, parsed.RawQuery
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	request.Header = headers.Clone()
	return request, nil
}
