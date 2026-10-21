package messagedelivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
	"github.com/stepandel/cantelop/fire-fuse/sandboxhttp"
	"io"
	"net/http"
	"strconv"
	"time"
)

// MailboxClient only performs bounded requests against one pinned sandbox.
// Lifecycle ownership and retries belong to the dispatcher, never the transport.
type MailboxClient struct{ transport http.RoundTripper }

func NewMailboxClient(transport http.RoundTripper) *MailboxClient {
	if transport == nil {
		transport = sandboxhttp.NewTransport()
	}
	return &MailboxClient{transport: transport}
}
func (c *MailboxClient) request(ctx context.Context, route session.Route, method, path string, body, out any) error {
	return c.requestWithTimeout(ctx, route, method, path, body, out, 10*time.Second)
}
func (c *MailboxClient) requestWithTimeout(ctx context.Context, route session.Route, method, path string, body, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if !messageprotocol.ValidSandboxID(route.SandboxID) {
		return ErrRuntimeProtocol
	}
	target, err := route.Endpoint.TargetURL()
	if err != nil {
		return err
	}
	headers, err := sandboxhttp.EndpointHeaders(route.Endpoint.Headers)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := runtimeRequest(ctx, method, target, path, headers, reader)
	if err != nil {
		return err
	}
	request.Header.Set(messageprotocol.SandboxHeader, route.SandboxID)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.transport.RoundTrip(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.Header.Get(messageprotocol.SandboxHeader) != route.SandboxID || response.Header.Get(messageprotocol.ProtocolHeader) != "2" {
		return fmt.Errorf("%w: sandbox identity or protocol mismatch", ErrRuntimeProtocol)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &RuntimeResponseError{Operation: path, StatusCode: response.StatusCode}
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(response.Body, maxOutputBodyBytes+1))
		return err
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxOutputBodyBytes+1))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		return errors.Join(ErrRuntimeProtocol, err)
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrRuntimeProtocol
	}
	return nil
}
func (c *MailboxClient) Admit(ctx context.Context, route session.Route, request messageprotocol.Request, deadline time.Time) (messageprotocol.Receipt, error) {
	var receipt messageprotocol.Receipt
	body := struct {
		messageprotocol.Request
		Deadline time.Time `json:"deadline"`
	}{request, deadline}
	err := c.request(ctx, route, http.MethodPost, messageprotocol.MessagePath, body, &receipt)
	if err == nil {
		err = receipt.Validate(request.Message.ID, route.SandboxID)
	}
	return receipt, err
}
func (c *MailboxClient) Recover(ctx context.Context, route session.Route, request messageprotocol.RecoveryRequest) (messageprotocol.RecoveryReceipt, error) {
	var receipt messageprotocol.RecoveryReceipt
	if request.Validate() != nil {
		return receipt, ErrRuntimeProtocol
	}
	err := c.request(ctx, route, http.MethodPost, messageprotocol.RecoveryPath, request, &receipt)
	if err == nil {
		err = receipt.Validate(request.RecoveryID)
	}
	return receipt, err
}
func (c *MailboxClient) Status(ctx context.Context, route session.Route, id string) (messageprotocol.Receipt, error) {
	var r messageprotocol.Receipt
	if !messageprotocol.ValidMessageID(id) {
		return r, ErrRuntimeProtocol
	}
	err := c.request(ctx, route, http.MethodGet, messageprotocol.MessagePath+"/"+id, nil, &r)
	if err == nil {
		err = r.Validate(id, route.SandboxID)
	}
	return r, err
}
func (c *MailboxClient) Reply(ctx context.Context, route session.Route, id string) (messageprotocol.MessageReply, error) {
	var reply messageprotocol.MessageReply
	if !messageprotocol.ValidMessageID(id) {
		return reply, ErrRuntimeProtocol
	}
	err := c.request(ctx, route, http.MethodGet, messageprotocol.MessagePath+"/"+id+"/reply", nil, &reply)
	if err == nil {
		err = reply.Validate()
	}
	return reply, err
}
func (c *MailboxClient) Cancel(ctx context.Context, route session.Route, id string) error {
	return c.request(ctx, route, http.MethodPost, messageprotocol.MessagePath+"/"+id+"/cancel", nil, nil)
}
func (c *MailboxClient) CancelActivity(ctx context.Context, route session.Route, activityID string) error {
	return c.request(ctx, route, http.MethodPost, messageprotocol.RuntimePath+"/activity/cancel", map[string]string{"activity_id": activityID}, nil)
}
func (c *MailboxClient) Inspect(ctx context.Context, route session.Route) (messageprotocol.RuntimeStatus, error) {
	var r messageprotocol.RuntimeStatus
	err := c.request(ctx, route, http.MethodGet, messageprotocol.RuntimePath, nil, &r)
	if err == nil {
		err = r.Validate(route.SandboxID, time.Now())
		if err != nil {
			err = errors.Join(ErrRuntimeProtocol, err)
		}
	}
	return r, err
}

// CollectObservations takes one nonblocking snapshot for lifecycle tracing.
// Session output uses its own continuous collector.
func (c *MailboxClient) CollectObservations(ctx context.Context, route session.Route, observe func(messageprotocol.ObservationEvent) error) error {
	snapshot, err := c.Inspect(ctx, route)
	if err != nil {
		return err
	}
	return c.collectObservations(ctx, route, snapshot, observe)
}
func (c *MailboxClient) collectObservations(ctx context.Context, route session.Route, snapshot messageprotocol.RuntimeStatus, observe func(messageprotocol.ObservationEvent) error) error {
	var err error
	var observations messageprotocol.ObservationBatch
	if err = c.request(ctx, route, http.MethodGet, messageprotocol.ObservationPath+"?after="+strconv.FormatUint(snapshot.Observations.Acknowledged, 10)+"&wait=0", nil, &observations); err != nil {
		return err
	}
	if err = observations.Validate(snapshot.Observations.Acknowledged, true); err != nil {
		return err
	}
	for _, event := range observations.Observations {
		if observe != nil {
			if err = observe(event); err != nil {
				return err
			}
		}
	}
	if len(observations.Observations) > 0 {
		if err = c.ack(ctx, route, messageprotocol.ObservationPath, observations.Observations[len(observations.Observations)-1].Cursor); err != nil {
			return err
		}
	}
	return nil
}
func (c *MailboxClient) ack(ctx context.Context, route session.Route, path string, through uint64) error {
	return c.request(ctx, route, http.MethodPost, path+"/ack", struct {
		Through uint64 `json:"through"`
	}{through}, nil)
}

// WaitForCompletions observes a hint only; reconciliation verifies receipts.
func (c *MailboxClient) WaitForCompletions(ctx context.Context, route session.Route, after uint64) (uint64, error) {
	var result struct {
		Generation uint64 `json:"generation"`
	}
	err := c.requestWithTimeout(ctx, route, http.MethodGet, messageprotocol.RuntimePath+"/completions?after="+strconv.FormatUint(after, 10), nil, &result, 30*time.Second)
	if err == nil && (result.Generation <= after || result.Generation > messageprotocol.MaxGeneration) {
		err = ErrRuntimeProtocol
	}
	return result.Generation, err
}
