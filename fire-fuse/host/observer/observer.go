// Package observer exposes a deliberately read-only view of fire-fuse state.
package observer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

const (
	maxControlResponseBytes          = 2 << 20
	maxSandboxInventoryResponseBytes = 8 << 20
	// controlReadConcurrency bounds the control-plane requests one snapshot
	// keeps in flight, so a large host fans out without flooding the API.
	controlReadConcurrency = 8
	// maxSnapshotWorkspaces bounds one scoped snapshot query so its URL stays
	// well inside proxy limits. Larger tenants read the unscoped snapshot.
	maxSnapshotWorkspaces = 256
)

var sandboxIDPattern = regexp.MustCompile(`^sbx-[0-9a-f]{32}$`)

var sessionIDPattern = regexp.MustCompile(`^ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$`)
var appIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var workspaceIDPattern = regexp.MustCompile(`^wsp_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$`)

type Workspace struct {
	ID         string     `json:"id"`
	AppID      string     `json:"app_id"`
	Slug       string     `json:"slug"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
}

type SessionState string

const (
	SessionStateActive SessionState = "active"
	SessionStateIdle   SessionState = "idle"
)

// Session is the safe, public lifecycle record shown by the console.
type Session struct {
	ID               string       `json:"id"`
	WorkspaceID      string       `json:"workspace_id"`
	AppID            string       `json:"app_id"`
	BuildID          string       `json:"build_id"`
	KeepAliveSeconds int          `json:"keep_alive_seconds"`
	State            SessionState `json:"state"`
	Startup          *Startup     `json:"startup,omitempty"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

type SandboxState string

// Sandbox is the host resource record shown separately from its public
// Session identity. A Session may outlive one backing Sandbox and be rebound
// to another, so the observer deliberately preserves both inventories.
type Sandbox struct {
	RuntimeTiming *RuntimeTiming           `json:"runtime_timing,omitempty"`
	ID            string                   `json:"id"`
	WorkspaceID   string                   `json:"workspace_id"`
	AppID         string                   `json:"app_id"`
	Profile       string                   `json:"profile"`
	VCPUs         int                      `json:"vcpus"`
	MemoryMiB     int                      `json:"memory_mib"`
	MemoryMaxMiB  int                      `json:"memory_max_mib"`
	BuildID       string                   `json:"build_id"`
	State         SandboxState             `json:"state"`
	Startup       *Startup                 `json:"startup,omitempty"`
	Network       *model.NetworkAssignment `json:"network,omitempty"`
	Failure       *Failure                 `json:"failure,omitempty"`
	Usage         *Usage                   `json:"usage,omitempty"`
	CreatedAt     time.Time                `json:"created_at"`
	UpdatedAt     time.Time                `json:"updated_at"`
}

// RuntimeTiming preserves measured process intervals from the runtime API.
// Missing timing belongs to an older runtime and remains unknown to callers.
type RuntimeTiming struct {
	Status    string     `json:"status"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	StoppedAt *time.Time `json:"stopped_at,omitempty"`
}

type Startup struct {
	CompletedAt time.Time `json:"completed_at"`
	DurationMS  float64   `json:"duration_ms"`
}

// Usage combines host cgroup accounting and recent guest consumption.
type Usage struct {
	ObservedAt           time.Time `json:"observed_at"`
	CPUUsageUS           uint64    `json:"cpu_usage_us"`
	MemoryCurrentBytes   uint64    `json:"memory_current_bytes"`
	GuestMemoryMiB       int       `json:"guest_memory_mib"`
	GuestMemoryUsedBytes *uint64   `json:"guest_memory_used_bytes,omitempty"`
}

type Failure struct {
	Reason string `json:"reason"`
}

type Snapshot struct {
	Ready      bool         `json:"ready"`
	ObservedAt time.Time    `json:"observed_at"`
	Capacity   HostCapacity `json:"capacity"`
	Workspaces []Workspace  `json:"workspaces"`
	Sessions   []Session    `json:"sessions"`
	Sandboxes  []Sandbox    `json:"sandboxes"`
}

type HostCapacity struct {
	WarmSlots WarmSlotCapacity `json:"warm_slots"`
}

type WarmSlotCapacity struct {
	Target    int `json:"target"`
	Ready     int `json:"ready"`
	Claimed   int `json:"claimed"`
	Preparing int `json:"preparing"`
}

type Source interface {
	Snapshot(context.Context) (Snapshot, error)
}

// WorkspaceSnapshotSource reads a snapshot scoped to a set of Workspaces
// without reading every other Workspace's Sessions.
type WorkspaceSnapshotSource interface {
	WorkspaceSnapshot(context.Context, []string) (Snapshot, error)
}

type RequestEventSource interface {
	RequestEvents(context.Context, model.RequestEventQuery) (RequestEventPage, error)
}

type SandboxMutator interface {
	TerminateSandbox(context.Context, string) (Sandbox, int, error)
	CrashSandbox(context.Context, string) (Sandbox, int, error)
}

// AppBuildSource reports the Builds the host still retains for one App.
// Retention and image collection remove older ones.
type AppBuildSource interface {
	AppBuilds(context.Context, string) ([]AppBuild, int, error)
}

type AppBuild struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
}

type SessionMutator interface {
	TerminateSession(context.Context, string) (Session, int, error)
}

type RequestEvent struct {
	Sequence    int64     `json:"sequence"`
	EventID     string    `json:"event_id"`
	RequestID   string    `json:"request_id,omitempty"`
	WorkspaceID string    `json:"workspace_id"`
	SessionID   string    `json:"session_id,omitempty"`
	ReceivedAt  time.Time `json:"received_at"`
	StatusCode  int       `json:"status_code"`
	DurationMS  float64   `json:"duration_ms"`
}

type RequestEventPage struct {
	Items              []RequestEvent `json:"items"`
	NextBeforeSequence int64          `json:"next_before_sequence,omitempty"`
}

type ControlClient struct {
	baseURL *url.URL
	client  *http.Client
	now     func() time.Time
}

func NewControlClient(rawURL string, client *http.Client) (*ControlClient, error) {
	baseURL, err := parseLoopbackOrigin(rawURL)
	if err != nil {
		return nil, fmt.Errorf("control-plane URL: %w", err)
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &ControlClient{baseURL: baseURL, client: client, now: time.Now}, nil
}

func parseLoopbackOrigin(rawURL string) (*url.URL, error) {
	origin, err := url.Parse(rawURL)
	if err != nil || origin.Scheme != "http" || origin.User != nil ||
		origin.RawQuery != "" || origin.Fragment != "" || strings.Trim(origin.Path, "/") != "" {
		return nil, errors.New("must be an HTTP origin")
	}
	endpoint, err := netip.ParseAddrPort(origin.Host)
	if err != nil || endpoint.Port() == 0 || !endpoint.Addr().Unmap().IsLoopback() {
		return nil, errors.New("must use a numeric loopback address and port")
	}
	return origin, nil
}

func (c *ControlClient) Snapshot(ctx context.Context) (Snapshot, error) {
	return c.snapshot(ctx, nil)
}

// WorkspaceSnapshot reads the same state as Snapshot restricted to the named
// Workspaces. Sessions are read only for those Workspaces, and every other
// Workspace's Sandboxes are dropped before the snapshot leaves the observer.
// IDs the host does not know are ignored.
func (c *ControlClient) WorkspaceSnapshot(ctx context.Context, workspaceIDs []string) (Snapshot, error) {
	return c.snapshot(ctx, workspaceSet(workspaceIDs))
}

// snapshot reads the host inventories with at most controlReadConcurrency
// control-plane requests in flight. A nil wanted set reads every Workspace.
// The control plane lists Workspaces per App and Sessions per Workspace, so
// those reads fan out after the App inventory arrives.
func (c *ControlClient) snapshot(ctx context.Context, wanted map[string]struct{}) (Snapshot, error) {
	var (
		ready     bool
		capacity  HostCapacity
		appIDs    []string
		sandboxes []Sandbox
		usagePage sandboxUsagePage
	)
	reads := []func(context.Context) error{
		func(ctx context.Context) (err error) {
			ready, err = c.readiness(ctx)
			return err
		},
		func(ctx context.Context) error {
			_, err := c.get(ctx, c.baseURL, "/v1/host-capacity", &capacity)
			return err
		},
	}
	// An empty Workspace scope still reports readiness but owns nothing, so it
	// skips the inventories entirely.
	if wanted == nil || len(wanted) > 0 {
		reads = append(reads,
			func(ctx context.Context) (err error) {
				appIDs, err = c.appIDs(ctx)
				return err
			},
			func(ctx context.Context) error {
				var sandboxPage struct {
					Items []Sandbox `json:"items"`
				}
				if _, err := c.get(ctx, c.baseURL, "/v1/sandboxes", &sandboxPage); err != nil {
					return err
				}
				sandboxes = sandboxPage.Items
				return nil
			},
			func(ctx context.Context) error {
				_, err := c.get(ctx, c.baseURL, "/v1/sandbox-usage", &usagePage)
				return err
			},
		)
	}
	if err := forEachBounded(ctx, len(reads), controlReadConcurrency, func(ctx context.Context, i int) error {
		return reads[i](ctx)
	}); err != nil {
		return Snapshot{}, err
	}
	warm := capacity.WarmSlots
	if warm.Target < 0 || warm.Ready < 0 || warm.Claimed < 0 || warm.Preparing < 0 ||
		warm.Ready+warm.Claimed+warm.Preparing > warm.Target {
		return Snapshot{}, errors.New("invalid host capacity")
	}

	workspaces, sessions, err := c.workspaceSessions(ctx, appIDs, wanted)
	if err != nil {
		return Snapshot{}, err
	}

	usageBySandbox := make(map[string]Usage, len(usagePage.Items))
	for _, reading := range usagePage.Items {
		usageBySandbox[reading.SandboxID] = Usage{
			ObservedAt: usagePage.ObservedAt, CPUUsageUS: reading.CPUUsageUS,
			MemoryCurrentBytes:   reading.MemoryCurrentBytes,
			GuestMemoryMiB:       reading.GuestMemoryMiB,
			GuestMemoryUsedBytes: reading.GuestMemoryUsedBytes,
		}
	}
	scopedSandboxes := make([]Sandbox, 0, len(sandboxes))
	for i := range sandboxes {
		if !inWorkspaceSet(wanted, sandboxes[i].WorkspaceID) {
			continue
		}
		if usage, ok := usageBySandbox[sandboxes[i].ID]; ok {
			sandboxes[i].Usage = &usage
		}
		scopedSandboxes = append(scopedSandboxes, sandboxes[i])
	}
	return Snapshot{
		Ready: ready, ObservedAt: c.now().UTC(),
		Capacity:   capacity,
		Workspaces: workspaces, Sessions: sessions, Sandboxes: scopedSandboxes,
	}, nil
}

func (c *ControlClient) appIDs(ctx context.Context) ([]string, error) {
	var apps struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if _, err := c.get(ctx, c.baseURL, "/v1/apps", &apps); err != nil {
		return nil, err
	}
	appIDs := make([]string, len(apps.Items))
	for i := range apps.Items {
		appIDs[i] = apps.Items[i].ID
	}
	return appIDs, nil
}

// workspaceSessions lists the wanted Workspaces of the given Apps and their
// Sessions, since the control plane lists Workspaces per App and Sessions
// per Workspace.
func (c *ControlClient) workspaceSessions(ctx context.Context, appIDs []string, wanted map[string]struct{}) ([]Workspace, []Session, error) {
	workspacePages := make([][]Workspace, len(appIDs))
	if err := forEachBounded(ctx, len(appIDs), controlReadConcurrency, func(ctx context.Context, i int) error {
		var workspacePage struct {
			Items []Workspace `json:"items"`
		}
		reference := &url.URL{Path: "/v1/workspaces", RawQuery: url.Values{"app_id": {appIDs[i]}}.Encode()}
		if _, err := c.getReference(ctx, c.baseURL, reference, &workspacePage); err != nil {
			return err
		}
		workspacePages[i] = workspacePage.Items
		return nil
	}); err != nil {
		return nil, nil, err
	}
	workspaces := make([]Workspace, 0)
	for _, page := range workspacePages {
		for _, workspace := range page {
			if inWorkspaceSet(wanted, workspace.ID) {
				workspaces = append(workspaces, workspace)
			}
		}
	}

	sessionPages := make([][]Session, len(workspaces))
	if err := forEachBounded(ctx, len(workspaces), controlReadConcurrency, func(ctx context.Context, i int) error {
		var runtimePage struct {
			Items []Session `json:"items"`
		}
		reference := &url.URL{Path: "/v1/sessions", RawQuery: url.Values{"workspace_id": {workspaces[i].ID}}.Encode()}
		if _, err := c.getReference(ctx, c.baseURL, reference, &runtimePage); err != nil {
			return err
		}
		for j := range runtimePage.Items {
			runtimePage.Items[j].AppID = workspaces[i].AppID
		}
		sessionPages[i] = runtimePage.Items
		return nil
	}); err != nil {
		return nil, nil, err
	}
	sessions := make([]Session, 0)
	for _, page := range sessionPages {
		sessions = append(sessions, page...)
	}
	return workspaces, sessions, nil
}

// hasSession reports whether the Session listings contain id. It reads only
// the App, Workspace, and Session inventories.
func (c *ControlClient) hasSession(ctx context.Context, id string) (bool, error) {
	if !sessionIDPattern.MatchString(id) {
		return false, errors.New("invalid Session ID")
	}
	appIDs, err := c.appIDs(ctx)
	if err != nil {
		return false, err
	}
	_, sessions, err := c.workspaceSessions(ctx, appIDs, nil)
	if err != nil {
		return false, err
	}
	for _, sessionRecord := range sessions {
		if sessionRecord.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// sandboxExists reads one persisted Sandbox, returning 404 with an error when
// this pool does not own it.
func (c *ControlClient) sandboxExists(ctx context.Context, id string) (int, error) {
	if !sandboxIDPattern.MatchString(id) {
		return 0, errors.New("invalid Sandbox ID")
	}
	var sandbox struct {
		ID string `json:"id"`
	}
	status, err := c.get(ctx, c.baseURL, "/v1/sandboxes/"+id, &sandbox)
	if err != nil {
		return status, err
	}
	if sandbox.ID != id {
		return 0, errors.New("invalid Sandbox record")
	}
	return status, nil
}

type sandboxUsagePage struct {
	ObservedAt time.Time `json:"observed_at"`
	Items      []struct {
		SandboxID            string  `json:"sandbox_id"`
		CPUUsageUS           uint64  `json:"cpu_usage_us"`
		MemoryCurrentBytes   uint64  `json:"memory_current_bytes"`
		GuestMemoryMiB       int     `json:"guest_memory_mib"`
		GuestMemoryUsedBytes *uint64 `json:"guest_memory_used_bytes,omitempty"`
	} `json:"items"`
}

func workspaceSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// inWorkspaceSet reports whether a record belongs to the scope; a nil set is
// the unscoped operator view.
func inWorkspaceSet(set map[string]struct{}, id string) bool {
	if set == nil {
		return true
	}
	_, ok := set[id]
	return ok
}

// restrictSnapshot keeps only the scoped Workspaces' records. The handler
// applies it to every scoped response, whatever the Source, so a Source that
// cannot filter upstream still never returns another Workspace's state.
func restrictSnapshot(snapshot Snapshot, set map[string]struct{}) Snapshot {
	workspaces := make([]Workspace, 0, len(snapshot.Workspaces))
	for _, workspace := range snapshot.Workspaces {
		if inWorkspaceSet(set, workspace.ID) {
			workspaces = append(workspaces, workspace)
		}
	}
	sessions := make([]Session, 0, len(snapshot.Sessions))
	for _, sessionRecord := range snapshot.Sessions {
		if inWorkspaceSet(set, sessionRecord.WorkspaceID) {
			sessions = append(sessions, sessionRecord)
		}
	}
	sandboxes := make([]Sandbox, 0, len(snapshot.Sandboxes))
	for _, sandboxRecord := range snapshot.Sandboxes {
		if inWorkspaceSet(set, sandboxRecord.WorkspaceID) {
			sandboxes = append(sandboxes, sandboxRecord)
		}
	}
	snapshot.Workspaces, snapshot.Sessions, snapshot.Sandboxes = workspaces, sessions, sandboxes
	return snapshot
}

func (c *ControlClient) RequestEvents(
	ctx context.Context,
	query model.RequestEventQuery,
) (RequestEventPage, error) {
	if err := query.Validate(); err != nil {
		return RequestEventPage{}, err
	}
	values := make(url.Values)
	values.Set("limit", fmt.Sprint(query.Limit))
	if query.WorkspaceID != "" {
		values.Set("workspace_id", query.WorkspaceID)
	}
	if query.AppID != "" {
		values.Set("app_id", query.AppID)
	}
	if query.BeforeSequence > 0 {
		values.Set("before_sequence", fmt.Sprint(query.BeforeSequence))
	}
	if query.AfterSequence > 0 {
		values.Set("after_sequence", fmt.Sprint(query.AfterSequence))
	}
	var page RequestEventPage
	reference := &url.URL{Path: "/v1/request-events", RawQuery: values.Encode()}
	if _, err := c.getReference(ctx, c.baseURL, reference, &page); err != nil {
		return RequestEventPage{}, err
	}
	if page.Items == nil {
		page.Items = []RequestEvent{}
	}
	return page, nil
}

func (c *ControlClient) TerminateSession(ctx context.Context, id string) (Session, int, error) {
	if !sessionIDPattern.MatchString(id) {
		return Session{}, 0, errors.New("invalid Session ID")
	}
	var sessionRecord Session
	status, err := c.requestReference(ctx, http.MethodDelete, c.baseURL, &url.URL{Path: "/v1/sessions/" + id}, &sessionRecord)
	return sessionRecord, status, err
}

func (c *ControlClient) TerminateSandbox(ctx context.Context, id string) (Sandbox, int, error) {
	if !sandboxIDPattern.MatchString(id) {
		return Sandbox{}, 0, errors.New("invalid Sandbox ID")
	}
	var sandboxRecord Sandbox
	status, err := c.requestReference(ctx, http.MethodDelete, c.baseURL, &url.URL{Path: "/v1/sandboxes/" + id}, &sandboxRecord)
	return sandboxRecord, status, err
}

func (c *ControlClient) CrashSandbox(ctx context.Context, id string) (Sandbox, int, error) {
	if !sandboxIDPattern.MatchString(id) {
		return Sandbox{}, 0, errors.New("invalid Sandbox ID")
	}
	var sandboxRecord Sandbox
	status, err := c.requestReference(ctx, http.MethodPost, c.baseURL, &url.URL{Path: "/v1/sandboxes/" + id + "/crash"}, &sandboxRecord)
	return sandboxRecord, status, err
}

func (c *ControlClient) AppBuilds(ctx context.Context, id string) ([]AppBuild, int, error) {
	var app struct {
		Builds []AppBuild `json:"builds"`
	}
	status, err := c.getReference(ctx, c.baseURL, &url.URL{Path: "/v1/apps/" + id}, &app)
	if err != nil {
		return nil, status, err
	}
	if app.Builds == nil {
		app.Builds = []AppBuild{}
	}
	return app.Builds, status, nil
}

func (c *ControlClient) readiness(ctx context.Context) (bool, error) {
	status, err := c.get(ctx, c.baseURL, "/readyz", nil)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusServiceUnavailable:
		return false, nil
	default:
		return false, fmt.Errorf("control plane readiness returned HTTP %d", status)
	}
}

func (c *ControlClient) get(ctx context.Context, base *url.URL, path string, destination any) (int, error) {
	return c.getReference(ctx, base, &url.URL{Path: path}, destination)
}

func (c *ControlClient) getReference(
	ctx context.Context,
	base *url.URL,
	reference *url.URL,
	destination any,
) (int, error) {
	return c.requestReference(ctx, http.MethodGet, base, reference, destination)
}

func (c *ControlClient) requestReference(
	ctx context.Context,
	method string,
	base *url.URL,
	reference *url.URL,
	destination any,
) (int, error) {
	endpoint := base.ResolveReference(reference)
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("build control-plane request: %w", err)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("call control plane: %w", err)
	}
	defer response.Body.Close()
	responseLimit := maxControlResponseBytes
	if method == http.MethodGet && reference.Path == "/v1/sandboxes" {
		responseLimit = maxSandboxInventoryResponseBytes
	}
	limited := io.LimitReader(response.Body, int64(responseLimit+1))
	document, err := io.ReadAll(limited)
	if err != nil {
		return 0, fmt.Errorf("read control-plane response: %w", err)
	}
	if len(document) > responseLimit {
		return 0, fmt.Errorf("control-plane response exceeds %d MiB", responseLimit>>20)
	}
	if destination == nil {
		return response.StatusCode, nil
	}
	if response.StatusCode != http.StatusOK {
		return response.StatusCode, fmt.Errorf("control plane returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(strings.NewReader(string(document)))
	if err := decoder.Decode(destination); err != nil {
		return response.StatusCode, fmt.Errorf("decode control-plane response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return response.StatusCode, errors.New("control-plane response must contain one JSON value")
	}
	return response.StatusCode, nil
}

func NewHandlerWithSessionMutations(source Source, observerToken, mutationToken string) (http.Handler, error) {
	return NewHandlerWithManagedDatabaseAccess(source, nil, observerToken, mutationToken)
}

// Managed verification has its own runtime client. A missing or unavailable
// managed pool never falls back to another pool's Sandbox inventory.
func NewHandlerWithManagedDatabaseAccess(source Source, managed DatabaseAccessSource, observerToken, mutationToken string) (http.Handler, error) {
	if len(mutationToken) < 32 || len(mutationToken) > 512 || strings.TrimSpace(mutationToken) != mutationToken || mutationToken == observerToken {
		return nil, errors.New("a distinct 32-512 byte Session mutation token is required")
	}
	return newHandler(source, observerToken, mutationToken, managed)
}

func newHandler(source Source, token, mutationToken string, managed DatabaseAccessSource) (http.Handler, error) {
	if source == nil || len(token) < 32 || len(token) > 512 || strings.TrimSpace(token) != token {
		return nil, errors.New("observer source and a 32-512 byte token are required")
	}
	expected := sha256.Sum256([]byte(token))
	mutationExpected := sha256.Sum256([]byte(mutationToken))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		sandboxCrash := r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/sandboxes/") && strings.HasSuffix(r.URL.Path, "/crash")
		if (r.Method == http.MethodDelete && (strings.HasPrefix(r.URL.Path, "/sessions/") || strings.HasPrefix(r.URL.Path, "/sandboxes/"))) || sandboxCrash {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if mutationToken == "" || provided == r.Header.Get("Authorization") || sha256.Sum256([]byte(provided)) != mutationExpected {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if sandboxCrash {
				serveSandboxCrash(w, r, source)
			} else if strings.HasPrefix(r.URL.Path, "/sandboxes/") {
				serveSandboxTermination(w, r, source)
			} else {
				serveSessionTermination(w, r, source)
			}
			return
		}
		appBuilds := strings.HasPrefix(r.URL.Path, "/apps/") && strings.HasSuffix(r.URL.Path, "/builds")
		databaseAccess := strings.HasPrefix(r.URL.Path, "/sandboxes/") && strings.HasSuffix(r.URL.Path, "/database-access")
		managedDatabaseAccess := strings.HasPrefix(r.URL.Path, "/managed/sandboxes/") && strings.HasSuffix(r.URL.Path, "/database-access")
		if r.Method != http.MethodGet || (r.URL.Path != "/snapshot" && r.URL.Path != "/requests" && r.URL.Path != "/infrastructure" && !appBuilds && !databaseAccess && !managedDatabaseAccess) {
			http.NotFound(w, r)
			return
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if provided == r.Header.Get("Authorization") || sha256.Sum256([]byte(provided)) != expected {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if managedDatabaseAccess {
			if managed == nil {
				http.Error(w, "Managed Sandbox verification is unavailable", http.StatusServiceUnavailable)
				return
			}
			forwarded := r.Clone(r.Context())
			forwarded.URL.Path = strings.TrimPrefix(r.URL.Path, "/managed")
			forwarded.URL.RawPath = ""
			serveDatabaseAccess(w, forwarded, managed)
			return
		}
		if r.URL.Path == "/requests" {
			serveRequestEvents(w, r, source)
			return
		}
		if r.URL.Path == "/infrastructure" {
			serveInfrastructure(w, r, source)
			return
		}
		if appBuilds {
			serveAppBuilds(w, r, source)
			return
		}
		if databaseAccess {
			serveDatabaseAccess(w, r, source)
			return
		}
		serveSnapshot(w, r, source)
	}), nil
}

// serveSnapshot returns the whole host, or with workspace_ids only the named
// Workspaces' records. The scope is applied here as well as in the Source.
func serveSnapshot(w http.ResponseWriter, r *http.Request, source Source) {
	workspaceIDs, scoped, err := parseSnapshotScope(r)
	if err != nil {
		http.Error(w, "invalid snapshot query", http.StatusBadRequest)
		return
	}
	var snapshot Snapshot
	if !scoped {
		snapshot, err = source.Snapshot(r.Context())
	} else if reader, ok := source.(WorkspaceSnapshotSource); ok {
		snapshot, err = reader.WorkspaceSnapshot(r.Context(), workspaceIDs)
	} else {
		snapshot, err = source.Snapshot(r.Context())
	}
	if err != nil {
		http.Error(w, "Session state is unavailable", http.StatusBadGateway)
		return
	}
	if scoped {
		snapshot = restrictSnapshot(snapshot, workspaceSet(workspaceIDs))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshot)
}

// parseSnapshotScope reads the optional workspace_ids parameter: a
// comma-separated list of Workspace IDs, where an empty value is an empty
// scope rather than the whole host.
func parseSnapshotScope(r *http.Request) ([]string, bool, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, false, err
	}
	for key, entries := range values {
		if key != "workspace_ids" || len(entries) != 1 {
			return nil, false, errors.New("invalid snapshot query")
		}
	}
	if !values.Has("workspace_ids") {
		return nil, false, nil
	}
	raw := values.Get("workspace_ids")
	if raw == "" {
		return []string{}, true, nil
	}
	ids := strings.Split(raw, ",")
	if len(ids) > maxSnapshotWorkspaces {
		return nil, false, errors.New("too many Workspaces in snapshot scope")
	}
	for _, id := range ids {
		if !workspaceIDPattern.MatchString(id) {
			return nil, false, errors.New("invalid Workspace in snapshot scope")
		}
	}
	return ids, true, nil
}

func serveAppBuilds(w http.ResponseWriter, r *http.Request, source Source) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/apps/"), "/builds")
	if !appIDPattern.MatchString(id) || r.URL.RawQuery != "" {
		http.Error(w, "invalid App", http.StatusBadRequest)
		return
	}
	reader, ok := source.(AppBuildSource)
	if !ok {
		http.Error(w, "App Builds are unavailable", http.StatusBadGateway)
		return
	}
	builds, status, err := reader.AppBuilds(r.Context(), id)
	if status == http.StatusNotFound {
		document := struct {
			AppID string `json:"app_id"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}{AppID: id}
		document.Error.Code = "app_not_found"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(document)
		return
	}
	if err != nil {
		http.Error(w, "App Builds are unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		AppID  string     `json:"app_id"`
		Builds []AppBuild `json:"builds"`
	}{AppID: id, Builds: builds})
}

func serveInfrastructure(w http.ResponseWriter, r *http.Request, source Source) {
	infrastructure, ok := source.(InfrastructureSource)
	if !ok {
		http.Error(w, "Infrastructure metrics are unavailable", http.StatusBadGateway)
		return
	}
	window, err := parseInfrastructureRange(r)
	if err != nil {
		http.Error(w, "invalid infrastructure query", http.StatusBadRequest)
		return
	}
	snapshot, err := infrastructure.Infrastructure(r.Context(), window)
	if err != nil {
		http.Error(w, "Infrastructure metrics are unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshot)
}

func serveSessionTermination(w http.ResponseWriter, r *http.Request, source Source) {
	id := strings.TrimPrefix(r.URL.Path, "/sessions/")
	if !sessionIDPattern.MatchString(id) || r.URL.RawQuery != "" {
		http.Error(w, "invalid Session", http.StatusBadRequest)
		return
	}
	mutator, ok := source.(SessionMutator)
	if !ok {
		http.Error(w, "Session mutation is unavailable", http.StatusServiceUnavailable)
		return
	}
	sessionRecord, status, err := mutator.TerminateSession(r.Context(), id)
	if err != nil {
		if status == http.StatusNotFound || status == http.StatusConflict {
			http.Error(w, http.StatusText(status), status)
			return
		}
		http.Error(w, "Session mutation is unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sessionRecord)
}

func serveSandboxTermination(w http.ResponseWriter, r *http.Request, source Source) {
	id := strings.TrimPrefix(r.URL.Path, "/sandboxes/")
	if !sandboxIDPattern.MatchString(id) || r.URL.RawQuery != "" {
		http.Error(w, "invalid Sandbox", http.StatusBadRequest)
		return
	}
	mutator, ok := source.(SandboxMutator)
	if !ok {
		http.Error(w, "Sandbox mutation is unavailable", http.StatusServiceUnavailable)
		return
	}
	sandboxRecord, status, err := mutator.TerminateSandbox(r.Context(), id)
	if err != nil {
		if status == http.StatusNotFound || status == http.StatusConflict {
			http.Error(w, http.StatusText(status), status)
			return
		}
		http.Error(w, "Sandbox mutation is unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sandboxRecord)
}

func serveSandboxCrash(w http.ResponseWriter, r *http.Request, source Source) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/sandboxes/"), "/crash")
	if !sandboxIDPattern.MatchString(id) || r.URL.RawQuery != "" {
		http.Error(w, "invalid Sandbox", http.StatusBadRequest)
		return
	}
	mutator, ok := source.(SandboxMutator)
	if !ok {
		http.Error(w, "Sandbox mutation is unavailable", http.StatusServiceUnavailable)
		return
	}
	sandboxRecord, status, err := mutator.CrashSandbox(r.Context(), id)
	if err != nil {
		if status == http.StatusNotFound || status == http.StatusConflict {
			http.Error(w, http.StatusText(status), status)
			return
		}
		http.Error(w, "Sandbox mutation is unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sandboxRecord)
}

func serveRequestEvents(w http.ResponseWriter, r *http.Request, source Source) {
	history, ok := source.(RequestEventSource)
	if !ok {
		http.Error(w, "request history is unavailable", http.StatusBadGateway)
		return
	}
	query, err := parseRequestEventQuery(r)
	if err != nil {
		http.Error(w, "invalid request history query", http.StatusBadRequest)
		return
	}
	page, err := history.RequestEvents(r.Context(), query)
	if err != nil {
		http.Error(w, "request history is unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

func parseRequestEventQuery(r *http.Request) (model.RequestEventQuery, error) {
	values := r.URL.Query()
	for key, entries := range values {
		if (key != "workspace_id" && key != "app_id" && key != "before_sequence" && key != "after_sequence" && key != "limit") || len(entries) != 1 {
			return model.RequestEventQuery{}, model.ErrInvalidRequestEvent
		}
	}
	query := model.RequestEventQuery{WorkspaceID: values.Get("workspace_id"), AppID: values.Get("app_id"), Limit: 100}
	if values.Has("workspace_id") && query.WorkspaceID == "" {
		return model.RequestEventQuery{}, model.ErrInvalidRequestEvent
	}
	if values.Has("app_id") && query.AppID == "" {
		return model.RequestEventQuery{}, model.ErrInvalidRequestEvent
	}
	if values.Has("before_sequence") {
		value, err := strconv.ParseInt(values.Get("before_sequence"), 10, 64)
		if err != nil || value < 1 {
			return model.RequestEventQuery{}, model.ErrInvalidRequestEvent
		}
		query.BeforeSequence = value
	}
	if values.Has("after_sequence") {
		value, err := strconv.ParseInt(values.Get("after_sequence"), 10, 64)
		if err != nil || value < 1 {
			return model.RequestEventQuery{}, model.ErrInvalidRequestEvent
		}
		query.AfterSequence = value
	}
	if values.Has("limit") {
		value, err := strconv.Atoi(values.Get("limit"))
		if err != nil {
			return model.RequestEventQuery{}, model.ErrInvalidRequestEvent
		}
		query.Limit = value
	}
	return query, query.Validate()
}
