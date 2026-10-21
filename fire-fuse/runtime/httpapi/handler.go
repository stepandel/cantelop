// Package httpapi exposes fire-fuse's HTTP interface.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/runtime/runtimeprovider"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

// Creator, Reader, and Terminator expose the loopback-only technical Sandbox
// lifecycle used by host operations and performance qualification.
type Creator interface {
	CreateAndStartWithID(context.Context, string, microvm.CreateSpec) (microvm.Sandbox, error)
}

type Reader interface {
	Get(string) (microvm.Sandbox, error)
	List() ([]microvm.Sandbox, error)
}

type Terminator interface {
	Terminate(context.Context, string) (microvm.Sandbox, error)
}

type Crasher interface {
	Crash(context.Context, string) (microvm.Sandbox, error)
}

type UsageReader interface {
	ListUsage() ([]microvm.RuntimeUsage, error)
}

type HostCapacityReader interface {
	HostCapacity() (microvm.HostCapacity, error)
}

type SandboxLifecycle interface {
	Creator
	Reader
	Terminator
}

// ResourceController exposes Apps at the target-model boundary.
type ResourceController interface {
	GetAppResource(id string) (microvm.App, error)
	ListAppResources() ([]microvm.App, error)
	GetAppSessionRuntime(appID string) (microvm.AppSessionRuntime, error)
	ListAppBuildsForApp(appID string) ([]microvm.AppBuild, error)
}

// ReadinessCheck verifies dependencies needed to serve lifecycle requests.
type ReadinessCheck func() error

type workspaceSyncServer interface {
	ServeWorkspaceSync(http.ResponseWriter, *http.Request)
}

type sessionEventSource interface {
	SessionEventStore() sessionevents.Store
}

// controlCapabilities records which optional ports one control plane offers.
// They are resolved once so handlers hold typed dependencies; a missing
// capability is an explicit nil whose routes answer 503.
type controlCapabilities struct {
	resources     ResourceController
	sandboxes     SandboxLifecycle
	crashes       Crasher
	usage         UsageReader
	hostCapacity  HostCapacityReader
	workspaces    WorkspaceController
	workspaceSync workspaceSyncServer
	pinnedLeases  pinnedWorkLeaseController
	executions    executionController
	messages      MessageDeliveryController
	messageEvents MessageDeliveryEventController
	requestEvents RequestEventController
	sessionEvents sessionevents.Store
}

func resolveControlCapabilities(controlPlane any) controlCapabilities {
	capabilities := controlCapabilities{sessionEvents: sessionevents.NewBroker()}
	if durable, ok := controlPlane.(sessionEventSource); ok {
		capabilities.sessionEvents = durable.SessionEventStore()
	}
	capabilities.resources, _ = controlPlane.(ResourceController)
	capabilities.sandboxes, _ = controlPlane.(SandboxLifecycle)
	capabilities.crashes, _ = controlPlane.(Crasher)
	capabilities.usage, _ = controlPlane.(UsageReader)
	capabilities.hostCapacity, _ = controlPlane.(HostCapacityReader)
	capabilities.workspaces, _ = controlPlane.(WorkspaceController)
	capabilities.workspaceSync, _ = controlPlane.(workspaceSyncServer)
	capabilities.pinnedLeases, _ = controlPlane.(pinnedWorkLeaseController)
	capabilities.executions, _ = controlPlane.(executionController)
	capabilities.messages, _ = controlPlane.(MessageDeliveryController)
	capabilities.messageEvents, _ = controlPlane.(MessageDeliveryEventController)
	capabilities.requestEvents, _ = controlPlane.(RequestEventController)
	return capabilities
}

// NewHandlerWithRuntimeProviderAndReadinessAndLogger builds the complete HTTP
// API. controlPlane may implement any subset of the capability interfaces.
func NewHandlerWithRuntimeProviderAndReadinessAndLogger(
	controlPlane any,
	runtimes runtimeprovider.Provider,
	ready ReadinessCheck,
	logger *slog.Logger,
) http.Handler {
	mux := http.NewServeMux()
	control := resolveControlCapabilities(controlPlane)
	admissionSignal := newMessageAdmissionSignal()
	replySignal := newMessageReplySignal()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", readiness(ready))
	mux.HandleFunc("GET /v1/apps", listApps(control.resources))
	mux.HandleFunc("GET /v1/apps/{id}", getApp(control.resources))
	mux.HandleFunc("GET /v1/apps/{app_id}/workspaces/by-slug/{workspace_slug}", resolveAppWorkspaceBySlug(control.workspaces))
	mux.HandleFunc("GET /v1/workspaces", listWorkspaces(control.workspaces))
	mux.HandleFunc("GET /v1/workspaces/{id}", getWorkspace(control.workspaces))
	mux.HandleFunc("/v1/apps/{app_id}/workspace-sync/{workspace_id}", serveWorkspaceSync(control.workspaceSync))
	mux.HandleFunc("POST /v1/sessions", createSession(runtimes))
	mux.HandleFunc("GET /v1/sessions", listSessions(runtimes))
	mux.HandleFunc("GET /v1/sessions/{id}", getSession(runtimes))
	mux.HandleFunc("GET /v1/sessions/{id}/route", getSessionRoute(runtimes))
	mux.HandleFunc("DELETE /v1/sessions/{id}", terminateSession(runtimes))
	mux.HandleFunc("POST /v1/sessions/{id}/work-leases", acquireSessionWorkLease(runtimes))
	mux.HandleFunc("POST /v1/sessions/{id}/work-leases/{lease_id}/renew", renewSessionWorkLease(runtimes))
	mux.HandleFunc("DELETE /v1/sessions/{id}/work-leases/{lease_id}", releaseSessionWorkLease(runtimes))
	mux.HandleFunc("POST /v1/pinned-work-leases", pinnedWorkLease(control.pinnedLeases))
	mux.HandleFunc("GET /v1/message-executions", executions(control.executions, replySignal))
	mux.HandleFunc("POST /v1/message-executions", executions(control.executions, replySignal))
	mux.HandleFunc("POST /v1/message-deliveries", enqueueMessageDelivery(control.messages, admissionSignal))
	mux.HandleFunc("POST /v1/message-deliveries/claim", claimMessageDelivery(control.messages))
	mux.HandleFunc("GET /v1/message-delivery-notifications", waitMessageDeliveryNotification(admissionSignal))
	mux.HandleFunc("POST /v1/message-deliveries/requeue-handling", requeueHandlingMessageDeliveries(control.messages))
	mux.HandleFunc("GET /v1/message-delivery-events", listMessageDeliveryEvents(control.messageEvents))
	mux.HandleFunc("GET /v1/message-deliveries/{id}", getMessageDelivery(control.messages, nil))
	mux.HandleFunc("GET /v1/message-deliveries/{id}/wait", getMessageDelivery(control.messages, replySignal))
	mux.HandleFunc("POST /v1/message-deliveries/{id}/requeue", requeueMessageDelivery(control.messages))
	mux.HandleFunc("POST /v1/message-deliveries/{id}/reply", saveMessageReply(control.messages))
	mux.HandleFunc("POST /v1/message-deliveries/{id}/fail", failMessageDelivery(control.messages, replySignal))
	mux.HandleFunc("POST /v1/sandboxes", createSandbox(control.sandboxes))
	mux.HandleFunc("GET /v1/sandboxes", listSandboxes(control.sandboxes))
	mux.HandleFunc("GET /v1/sandboxes/{id}", getSandbox(control.sandboxes))
	mux.HandleFunc("DELETE /v1/sandboxes/{id}", terminateSandbox(control.sandboxes))
	mux.HandleFunc("POST /v1/sandboxes/{id}/crash", crashSandbox(control.crashes))
	mux.HandleFunc("GET /v1/sandbox-usage", listSandboxUsage(control.usage))
	mux.HandleFunc("GET /v1/host-capacity", readHostCapacity(control.hostCapacity))
	mux.HandleFunc("POST /v1/request-events", recordRequestEvent(control.requestEvents))
	mux.HandleFunc("GET /v1/request-events", listRequestEvents(control.requestEvents))
	mux.HandleFunc("POST /v1/session-events", appendSessionEvent(control.sessionEvents))
	mux.HandleFunc("GET /v1/session-view", readSessionView(control.sessionEvents))
	mux.HandleFunc("GET /v1/session-events", readSessionEvents(control.sessionEvents))
	mux.HandleFunc("POST /v1/session-events/close", closeSessionEvents(control.sessionEvents))

	handler := http.Handler(mux)
	if logger != nil {
		handler = withRequestLogging(logger, handler)
	}
	handler = withWarmupTrace(handler)
	return withRequestID(handler)
}

func serveWorkspaceSync(files workspaceSyncServer) http.HandlerFunc {
	if files == nil {
		return unavailable("sync_unavailable", "Workspace sync is unavailable")
	}
	return files.ServeWorkspaceSync
}

func withWarmupTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost ||
			(r.URL.Path != "/v1/sessions" && r.URL.Path != "/v1/sandboxes") {
			next.ServeHTTP(w, r)
			return
		}
		trace := warmuptrace.New()
		next.ServeHTTP(w, r.WithContext(warmuptrace.WithContext(r.Context(), trace)))
	})
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
}

func readiness(check ReadinessCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if check == nil || check() != nil {
			writeError(w, http.StatusServiceUnavailable, "not_ready", "host prerequisites are unavailable")
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Status string `json:"status"`
		}{Status: "ready"})
	}
}
