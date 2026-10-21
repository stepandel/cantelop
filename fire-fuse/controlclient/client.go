package controlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
	"github.com/stepandel/cantelop/internal/jsonwire"
)

const maxResponseBytes = 2 << 20

var (
	sessionIDPattern   = regexp.MustCompile(`^ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$`)
	workLeaseIDPattern = regexp.MustCompile(`^lease_[0-9a-f]{32}$`)
	slugPattern        = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Client calls the host-local Fire Fuse control plane.
type Client struct {
	baseURL *url.URL
	client  *http.Client
}

var (
	_ MessageDeliveries    = (*Client)(nil)
	_ WorkspaceDirectory   = (*Client)(nil)
	_ Sessions             = (*Client)(nil)
	_ RequestEventRecorder = (*Client)(nil)
	_ SessionEvents        = (*Client)(nil)
)

// New constructs a client for a numeric loopback HTTP origin.
func New(rawURL string, client *http.Client) (*Client, error) {
	baseURL, err := url.Parse(rawURL)
	if err != nil || baseURL.Scheme != "http" || baseURL.User != nil ||
		baseURL.RawQuery != "" || baseURL.Fragment != "" || strings.Trim(baseURL.Path, "/") != "" {
		return nil, errors.New("control-plane URL must be an HTTP origin")
	}
	endpoint, err := netip.ParseAddrPort(baseURL.Host)
	if err != nil || endpoint.Port() == 0 || !endpoint.Addr().Unmap().IsLoopback() {
		return nil, errors.New("control-plane URL must use a numeric loopback address and port")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Client{baseURL: baseURL, client: client}, nil
}

func (c *Client) listApps(ctx context.Context) ([]App, error) {
	var response struct {
		Items []App `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/apps", nil, &response); err != nil {
		return nil, err
	}
	return response.Items, nil
}

func (c *Client) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	apps, err := c.listApps(ctx)
	if err != nil {
		return nil, err
	}
	workspaces := make([]Workspace, 0, len(apps))
	for _, app := range apps {
		items, err := c.ListAppWorkspaces(ctx, app.ID)
		if err != nil {
			return nil, err
		}
		workspaces = append(workspaces, items...)
	}
	return workspaces, nil
}

func (c *Client) ListAppWorkspaces(ctx context.Context, appID string) ([]Workspace, error) {
	if appID == "" || strings.Contains(appID, "/") {
		return nil, errors.New("invalid App ID")
	}
	var response struct {
		Items []Workspace `json:"items"`
	}
	path := "/v1/workspaces?app_id=" + url.QueryEscape(appID)
	if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	return response.Items, nil
}

func (c *Client) ResolveAppWorkspace(ctx context.Context, appID, workspaceSlug string) (Workspace, error) {
	if appID == "" || strings.Contains(appID, "/") || !slugPattern.MatchString(workspaceSlug) {
		return Workspace{}, errors.New("invalid App Workspace route")
	}
	var workspace Workspace
	path := "/v1/apps/" + url.PathEscape(appID) + "/workspaces/by-slug/" + url.PathEscape(workspaceSlug)
	if err := c.do(ctx, http.MethodGet, path, nil, &workspace); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

func (c *Client) ListSessions(ctx context.Context, workspaceID string) ([]Session, error) {
	if workspaceID == "" {
		return nil, errors.New("invalid Workspace ID")
	}
	var response struct {
		Items []Session `json:"items"`
	}
	path := "/v1/sessions?workspace_id=" + url.QueryEscape(workspaceID)
	if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	return response.Items, nil
}

func (c *Client) GetWorkspace(ctx context.Context, id string) (Workspace, error) {
	if id == "" || strings.Contains(id, "/") {
		return Workspace{}, errors.New("invalid Workspace ID")
	}
	var workspace Workspace
	if err := c.do(ctx, http.MethodGet, "/v1/workspaces/"+url.PathEscape(id), nil, &workspace); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

func (c *Client) GetSession(ctx context.Context, id string) (Session, error) {
	if !sessionIDPattern.MatchString(id) {
		return Session{}, errors.New("invalid Session ID")
	}
	var record Session
	if err := c.do(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(id), nil, &record); err != nil {
		return Session{}, err
	}
	return record, nil
}

func (c *Client) GetSessionRoute(ctx context.Context, id string) (session.Route, error) {
	if !sessionIDPattern.MatchString(id) {
		return session.Route{}, errors.New("invalid Session ID")
	}
	var route session.Route
	if err := c.do(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(id)+"/route", nil, &route); err != nil {
		return session.Route{}, err
	}
	if !session.ValidSandboxID(route.SandboxID) {
		return session.Route{}, errors.New("control plane returned invalid Session Sandbox identity")
	}
	if _, err := route.Endpoint.TargetURL(); err != nil {
		return session.Route{}, fmt.Errorf("control plane returned invalid Session endpoint: %w", err)
	}
	return route, nil
}

func (c *Client) CreateSession(ctx context.Context, spec CreateSessionSpec) (Session, error) {
	body, err := json.Marshal(spec)
	if err != nil {
		return Session{}, fmt.Errorf("encode Session creation: %w", err)
	}
	var record Session
	if err := c.do(ctx, http.MethodPost, "/v1/sessions", bytes.NewReader(body), &record); err != nil {
		return Session{}, err
	}
	return record, nil
}

func (c *Client) TerminateSession(ctx context.Context, id string) (Session, error) {
	if id == "" || strings.Contains(id, "/") {
		return Session{}, errors.New("invalid Session ID")
	}
	var record Session
	if err := c.do(ctx, http.MethodDelete, "/v1/sessions/"+url.PathEscape(id), nil, &record); err != nil {
		return Session{}, err
	}
	return record, nil
}

func (c *Client) AcquireSessionWorkLease(ctx context.Context, sessionID, leaseID string, ttl time.Duration) (WorkLease, error) {
	if !sessionIDPattern.MatchString(sessionID) || !workLeaseIDPattern.MatchString(leaseID) ||
		ttl < time.Second || ttl > 5*time.Minute || ttl%time.Second != 0 {
		return WorkLease{}, errors.New("invalid Session work lease")
	}
	body, err := json.Marshal(struct {
		LeaseID    string `json:"lease_id"`
		TTLSeconds int    `json:"ttl_seconds"`
	}{leaseID, int(ttl / time.Second)})
	if err != nil {
		return WorkLease{}, err
	}
	var value WorkLease
	if err := c.do(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(sessionID)+"/work-leases", bytes.NewReader(body), &value); err != nil {
		return WorkLease{}, err
	}
	return value, nil
}

func (c *Client) RenewSessionWorkLease(ctx context.Context, sessionID, leaseID string, ttl time.Duration) (WorkLease, error) {
	if !sessionIDPattern.MatchString(sessionID) || !workLeaseIDPattern.MatchString(leaseID) ||
		ttl < time.Second || ttl > 5*time.Minute || ttl%time.Second != 0 {
		return WorkLease{}, errors.New("invalid Session work lease")
	}
	body, err := json.Marshal(struct {
		TTLSeconds int `json:"ttl_seconds"`
	}{int(ttl / time.Second)})
	if err != nil {
		return WorkLease{}, err
	}
	var value WorkLease
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/work-leases/" + url.PathEscape(leaseID) + "/renew"
	if err := c.do(ctx, http.MethodPost, path, bytes.NewReader(body), &value); err != nil {
		return WorkLease{}, err
	}
	return value, nil
}

func (c *Client) ReleaseSessionWorkLease(ctx context.Context, sessionID, leaseID string) (Session, error) {
	if !sessionIDPattern.MatchString(sessionID) || !workLeaseIDPattern.MatchString(leaseID) {
		return Session{}, errors.New("invalid Session work lease")
	}
	var value Session
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/work-leases/" + url.PathEscape(leaseID)
	if err := c.do(ctx, http.MethodDelete, path, nil, &value); err != nil {
		return Session{}, err
	}
	return value, nil
}

func (c *Client) EnqueueMessageDelivery(ctx context.Context, value MessageDelivery) (MessageDelivery, error) {
	request := messageprotocol.Request{
		Session: messageprotocol.SessionEnvelope{
			ID: value.LogicalSessionID, WorkspaceID: value.WorkspaceID,
			KeepAliveSeconds: value.KeepAliveSeconds,
		},
		Message: messageprotocol.Message{ID: value.ID, Payload: value.Payload},
	}
	if err := request.Validate(); err != nil {
		return MessageDelivery{}, err
	}
	document, err := json.Marshal(struct {
		ID               string          `json:"id"`
		WorkspaceID      string          `json:"workspace_id"`
		LogicalSessionID string          `json:"logical_session_id"`
		RequestID        string          `json:"request_id,omitempty"`
		KeepAliveSeconds int             `json:"keep_alive_seconds"`
		Payload          json.RawMessage `json:"payload"`
		ReplyRequested   bool            `json:"reply_requested,omitempty"`
	}{value.ID, value.WorkspaceID, value.LogicalSessionID, value.RequestID, value.KeepAliveSeconds, value.Payload, value.ReplyRequested})
	if err != nil {
		return MessageDelivery{}, fmt.Errorf("encode Message delivery: %w", err)
	}
	var stored MessageDelivery
	if err := c.do(ctx, http.MethodPost, "/v1/message-deliveries", bytes.NewReader(document), &stored); err != nil {
		return MessageDelivery{}, err
	}
	return stored, nil
}

func (c *Client) GetMessageDelivery(ctx context.Context, id string) (MessageDelivery, error) {
	if !messageprotocol.ValidMessageID(id) {
		return MessageDelivery{}, errors.New("invalid Message ID")
	}
	var stored MessageDelivery
	if err := c.do(ctx, http.MethodGet, "/v1/message-deliveries/"+url.PathEscape(id), nil, &stored); err != nil {
		return MessageDelivery{}, err
	}
	return stored, nil
}

// WaitForMessageReply returns durable state on a change or a bounded timeout.
func (c *Client) WaitForMessageReply(ctx context.Context, id string) (MessageDelivery, error) {
	if !messageprotocol.ValidMessageID(id) {
		return MessageDelivery{}, errors.New("invalid Message ID")
	}
	var stored MessageDelivery
	err := c.do(ctx, http.MethodGet, "/v1/message-deliveries/"+url.PathEscape(id)+"/wait", nil, &stored)
	return stored, err
}

func (c *Client) ClaimNextMessageDelivery(ctx context.Context) (*MessageDelivery, error) {
	var response struct {
		Message *MessageDelivery `json:"message"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/message-deliveries/claim", nil, &response); err != nil {
		return nil, err
	}
	return response.Message, nil
}

// WaitForMessageDeliveries waits for an enqueue generation change, bounded by
// one second. Notifications are hints; workers still atomically claim storage.
func (c *Client) WaitForMessageDeliveries(ctx context.Context, after uint64) (uint64, error) {
	var response struct {
		Generation uint64 `json:"generation"`
	}
	path := fmt.Sprintf("/v1/message-delivery-notifications?after=%d", after)
	if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
		return after, err
	}
	return response.Generation, nil
}

func (c *Client) RequeueHandlingMessageDeliveries(ctx context.Context) (int64, error) {
	var response struct {
		Requeued int64 `json:"requeued"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/message-deliveries/requeue-handling", nil, &response); err != nil {
		return 0, err
	}
	return response.Requeued, nil
}

func (c *Client) RequeueMessageDelivery(ctx context.Context, id string) error {
	return c.messageTransition(ctx, id, "requeue", nil)
}

func (c *Client) SaveMessageReply(ctx context.Context, id string, reply json.RawMessage) error {
	return c.messageTransition(ctx, id, "reply", struct {
		Reply json.RawMessage `json:"reply"`
	}{reply})
}

func (c *Client) FailMessageDelivery(ctx context.Context, id, sessionID, errorCode string) error {
	return c.messageTransition(ctx, id, "fail", struct {
		SessionID string `json:"session_id,omitempty"`
		ErrorCode string `json:"error_code"`
	}{sessionID, errorCode})
}

func (c *Client) messageTransition(ctx context.Context, id, operation string, value any) error {
	if !messageprotocol.ValidMessageID(id) {
		return errors.New("invalid Message ID")
	}
	var body io.Reader
	if value != nil {
		// json.Marshal would HTML-escape a raw reply past its validated size.
		var document bytes.Buffer
		encoder := json.NewEncoder(&document)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(value); err != nil {
			return err
		}
		body = &document
	}
	var stored MessageDelivery
	return c.do(ctx, http.MethodPost, "/v1/message-deliveries/"+url.PathEscape(id)+"/"+operation, body, &stored)
}

func (c *Client) RecordRequestEvent(ctx context.Context, event ProxyRequestEvent) error {
	body, err := json.Marshal(struct {
		EventID     string    `json:"event_id"`
		RequestID   string    `json:"request_id,omitempty"`
		WorkspaceID string    `json:"workspace_id"`
		SessionID   string    `json:"session_id,omitempty"`
		ReceivedAt  time.Time `json:"received_at"`
		StatusCode  int       `json:"status_code"`
		DurationMS  float64   `json:"duration_ms"`
	}{
		EventID: event.EventID, RequestID: event.RequestID, WorkspaceID: event.WorkspaceID,
		SessionID: event.SessionID, ReceivedAt: event.ReceivedAt, StatusCode: event.StatusCode,
		DurationMS: float64(event.Duration.Microseconds()) / 1000,
	})
	if err != nil {
		return fmt.Errorf("encode request event: %w", err)
	}
	var response struct {
		EventID string `json:"event_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/request-events", bytes.NewReader(body), &response); err != nil {
		return err
	}
	if response.EventID != event.EventID {
		return errors.New("control plane returned a different request event ID")
	}
	return nil
}

func (c *Client) AppendSessionEvent(ctx context.Context, value SessionEventAppend) (SessionEvent, error) {
	if err := validateSessionEventTarget(value.WorkspaceID, value.SessionID); err != nil ||
		!messageprotocol.ValidMessageID(value.MessageID) || len(value.Data) == 0 || !json.Valid(value.Data) {
		return SessionEvent{}, errors.New("invalid Session event")
	}
	// Keep SDK-encoded data within its original byte budget, including Unicode
	// line separators that encoding/json can escape with SetEscapeHTML(false).
	metadata := struct {
		SessionEventAppend
		Data json.RawMessage `json:"data,omitempty"`
	}{SessionEventAppend: value}
	document, err := jsonwire.MarshalWithRawField(metadata, "data", value.Data)
	if err != nil {
		return SessionEvent{}, err
	}
	var event SessionEvent
	if err := c.do(ctx, http.MethodPost, "/v1/session-events", bytes.NewReader(document), &event); err != nil {
		return SessionEvent{}, err
	}
	return event, nil
}

func (c *Client) ReadSessionEvents(
	ctx context.Context,
	workspaceID, sessionID string,
	after uint64,
	wait bool,
) (SessionEventBatch, error) {
	if err := validateSessionEventTarget(workspaceID, sessionID); err != nil {
		return SessionEventBatch{}, err
	}
	values := url.Values{}
	values.Set("workspace_id", workspaceID)
	values.Set("session_id", sessionID)
	values.Set("after", fmt.Sprint(after))
	if !wait {
		values.Set("wait", "0")
	}
	var batch SessionEventBatch
	if err := c.do(ctx, http.MethodGet, "/v1/session-events?"+values.Encode(), nil, &batch); err != nil {
		return SessionEventBatch{}, err
	}
	return batch, nil
}

func (c *Client) CloseSessionEvents(ctx context.Context, workspaceID, sessionID string) error {
	if err := validateSessionEventTarget(workspaceID, sessionID); err != nil {
		return err
	}
	document, err := json.Marshal(struct {
		WorkspaceID string `json:"workspace_id"`
		SessionID   string `json:"session_id"`
	}{workspaceID, sessionID})
	if err != nil {
		return err
	}
	var response struct {
		Closed bool `json:"closed"`
	}
	return c.do(ctx, http.MethodPost, "/v1/session-events/close", bytes.NewReader(document), &response)
}

func validateSessionEventTarget(workspaceID, sessionID string) error {
	if workspaceID == "" || sessionID == "" || strings.Contains(workspaceID, "/") || strings.Contains(sessionID, "/") {
		return errors.New("invalid Session event target")
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, destination any) error {
	return Do(ctx, c.client, c.baseURL, method, path, body, destination)
}

// Do sends one JSON control-plane request to a path below baseURL and decodes
// exactly one JSON response value into destination. Responses are limited to
// 2 MiB, and non-2xx responses are returned as *Error.
func Do(
	ctx context.Context,
	client *http.Client,
	baseURL *url.URL,
	method, path string,
	body io.Reader,
	destination any,
) error {
	reference, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(reference.Path, "/") || reference.IsAbs() || reference.Host != "" {
		return errors.New("invalid control-plane request path")
	}
	endpoint := baseURL.ResolveReference(reference)
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("build control-plane request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("call control plane: %w", err)
	}
	defer response.Body.Close()
	document, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read control-plane response: %w", err)
	}
	if len(document) > maxResponseBytes {
		return errors.New("control-plane response exceeds 2 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(document, &failure)
		return &Error{StatusCode: response.StatusCode, Code: failure.Error.Code}
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode control-plane response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("control-plane response must contain one JSON value")
	}
	return nil
}
