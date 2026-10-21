// Package sessionoutput continuously hands SDK Session output from a Sandbox
// to the platform Session event stream.
package sessionoutput

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
	"github.com/stepandel/cantelop/fire-fuse/sandboxhttp"
)

const (
	maxResponseBytes = (1 << 20) + (64 << 10)
	requestTimeout   = 30 * time.Second
)

var errPollTimeout = errors.New("Session output poll timed out")

// Collector drains one Sandbox output stream until its context is cancelled.
type Collector struct{ transport http.RoundTripper }

func NewCollector(transport http.RoundTripper) *Collector {
	if transport == nil {
		transport = sandboxhttp.NewTransport()
	}
	return &Collector{transport: transport}
}

// Collect resumes at the Sandbox-local output acknowledgement and then
// long-polls independently of Message lifecycle reconciliation.
func (c *Collector) Collect(ctx context.Context, route session.Route, appendEvent func(messageprotocol.OutputEvent) error) error {
	if appendEvent == nil {
		return errors.New("Session output recorder unavailable")
	}
	var status messageprotocol.RuntimeStatus
	if err := c.request(ctx, route, http.MethodGet, messageprotocol.RuntimePath, nil, &status); err != nil {
		return err
	}
	if status.SandboxID != route.SandboxID || status.Protocol != 2 || status.Events.Latest > messageprotocol.MaxGeneration || status.Events.Acknowledged > status.Events.Latest || status.Events.Earliest > status.Events.Latest+1 {
		return errors.New("invalid Session runtime output position")
	}
	after := status.Events.Acknowledged
	for {
		var batch messageprotocol.OutputBatch
		path := messageprotocol.OutputPath + "?after=" + strconv.FormatUint(after, 10)
		if err := c.request(ctx, route, http.MethodGet, path, nil, &batch); err != nil {
			if errors.Is(err, errPollTimeout) {
				continue
			}
			return err
		}
		if err := batch.Validate(after, false); err != nil {
			return err
		}
		for _, event := range batch.Events {
			if err := appendEvent(event); err != nil {
				return err
			}
		}
		after = batch.Events[len(batch.Events)-1].Cursor
		if err := c.request(ctx, route, http.MethodPost, messageprotocol.OutputPath+"/ack", struct {
			Through uint64 `json:"through"`
		}{after}, nil); err != nil {
			return err
		}
	}
}

func (c *Collector) request(ctx context.Context, route session.Route, method, path string, body, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if !messageprotocol.ValidSandboxID(route.SandboxID) {
		return errors.New("invalid Session output Sandbox")
	}
	target, err := route.Endpoint.TargetURL()
	if err != nil {
		return err
	}
	headers, err := sandboxhttp.EndpointHeaders(route.Endpoint.Headers)
	if err != nil {
		return err
	}
	endpoint := *target
	parsed, err := url.Parse(path)
	if err != nil {
		return err
	}
	endpoint.Path, endpoint.RawQuery = parsed.Path, parsed.RawQuery
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(requestContext, method, endpoint.String(), reader)
	if err != nil {
		return err
	}
	request.Header = headers.Clone()
	request.Header.Set(messageprotocol.SandboxHeader, route.SandboxID)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.transport.RoundTrip(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.Header.Get(messageprotocol.SandboxHeader) != route.SandboxID || response.Header.Get(messageprotocol.ProtocolHeader) != "2" {
		return errors.New("invalid Session output runtime identity")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// The SDK expires an idle long poll after 25 seconds. This is an
		// ordinary empty wait, so immediately establish the next long poll.
		if method == http.MethodGet && parsed.Path == messageprotocol.OutputPath && response.StatusCode == http.StatusRequestTimeout {
			return errPollTimeout
		}
		return fmt.Errorf("Session output %s returned HTTP %d", path, response.StatusCode)
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes+1))
		return err
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes+1))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		return err
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("Session output response contains multiple documents")
	}
	return nil
}
