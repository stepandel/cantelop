package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

var sandboxLifecycleUnavailable = unavailable("runtime_unavailable", "Sandbox lifecycle is unavailable")

type createSandboxRequest struct {
	ID          string `json:"id"`
	AppID       string `json:"app_id"`
	WorkspaceID string `json:"workspace_id"`
}

type sandboxResponse struct {
	RuntimeTiming microvm.RuntimeTiming `json:"runtime_timing"`
	ID            string                `json:"id"`
	AppID         string                `json:"app_id"`
	WorkspaceID   string                `json:"workspace_id"`
	Profile       string                `json:"profile"`
	BuildID       string                `json:"build_id"`
	VCPUs         int                   `json:"vcpus"`
	MemoryMiB     int                   `json:"memory_mib"`
	MemoryMaxMiB  int                   `json:"memory_max_mib"`
	State         microvm.State         `json:"state"`
	Network       *networkResponse      `json:"network,omitempty"`
	Failure       *failureResponse      `json:"failure,omitempty"`
	Startup       *startupResponse      `json:"startup,omitempty"`
	CreatedAt     time.Time             `json:"created_at"`
	UpdatedAt     time.Time             `json:"updated_at"`
}

type startupResponse struct {
	CompletedAt time.Time `json:"completed_at"`
	DurationMS  float64   `json:"duration_ms"`
}

type networkResponse struct {
	GuestIP   string `json:"guest_ip"`
	GatewayIP string `json:"gateway_ip"`
	Subnet    string `json:"subnet"`
	GuestMAC  string `json:"guest_mac"`
}

type failureResponse struct {
	Reason      microvm.FailureReason `json:"reason"`
	RuntimeExit *runtimeExitResponse  `json:"runtime_exit,omitempty"`
}

type runtimeExitResponse struct {
	PID      int    `json:"pid"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Signal   string `json:"signal,omitempty"`
}

type sandboxListResponse struct {
	Items []sandboxResponse `json:"items"`
}

type sandboxUsageResponse struct {
	ObservedAt time.Time          `json:"observed_at"`
	Items      []sandboxUsageItem `json:"items"`
}

type sandboxUsageItem struct {
	SandboxID            string  `json:"sandbox_id"`
	CPUUsageUS           uint64  `json:"cpu_usage_us"`
	MemoryCurrentBytes   uint64  `json:"memory_current_bytes"`
	GuestMemoryMiB       int     `json:"guest_memory_mib"`
	GuestMemoryUsedBytes *uint64 `json:"guest_memory_used_bytes,omitempty"`
}

type hostCapacityResponse struct {
	WarmSlots warmSlotCapacityResponse `json:"warm_slots"`
}

type warmSlotCapacityResponse struct {
	Target    int `json:"target"`
	Ready     int `json:"ready"`
	Claimed   int `json:"claimed"`
	Preparing int `json:"preparing"`
}

func createSandbox(creator Creator) http.HandlerFunc {
	if creator == nil {
		return sandboxLifecycleUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		finishDecode := warmuptrace.Start(r.Context(), "http.decode_create_request")
		var request createSandboxRequest
		err := decodeRequest(w, r, &request)
		finishDecode()
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}

		sandbox, err := creator.CreateAndStartWithID(
			r.Context(), request.ID, microvm.CreateSpec{AppID: request.AppID, WorkspaceID: request.WorkspaceID},
		)
		if errors.Is(err, microvm.ErrStateStore) {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not persist sandbox state")
			return
		}
		if errors.Is(err, microvm.ErrInvalidSpec) {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if errors.Is(err, microvm.ErrUnknownProfile) {
			writeError(w, http.StatusBadRequest, "unknown_profile", "requested sandbox profile does not exist")
			return
		}
		if errors.Is(err, microvm.ErrUnknownImage) {
			writeError(w, http.StatusBadRequest, "unknown_image", "requested container image does not exist")
			return
		}
		if errors.Is(err, microvm.ErrUnknownApp) {
			writeError(w, http.StatusBadRequest, "unknown_app", "requested app does not exist")
			return
		}
		if errors.Is(err, microvm.ErrUnknownWorkspace) {
			writeError(w, http.StatusBadRequest, "unknown_workspace", "requested Workspace does not exist for this app")
			return
		}
		if errors.Is(err, microvm.ErrWorkspaceUnavailable) {
			writeError(w, http.StatusConflict, "workspace_unavailable", "requested Workspace is archived")
			return
		}
		if errors.Is(err, microvm.ErrUnsupportedShape) {
			writeError(w, http.StatusBadRequest, "unsupported_shape", "requested sandbox shape has no compatible snapshot")
			return
		}
		if errors.Is(err, microvm.ErrAdmissionLimit) {
			writeError(w, http.StatusTooManyRequests, "admission_limit", "host admission limits would be exceeded")
			return
		}
		if errors.Is(err, microvm.ErrSandboxConflict) {
			writeError(w, http.StatusConflict, "sandbox_conflict", "sandbox ID already exists with different settings")
			return
		}
		if errors.Is(err, microvm.ErrRuntimeUnavailable) || errors.Is(err, microvm.ErrSupervisorUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "sandbox runtime could not be started")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not create sandbox")
			return
		}

		finishResponse := warmuptrace.Start(r.Context(), "http.write_create_response")
		writeJSON(w, http.StatusAccepted, newSandboxResponse(sandbox))
		finishResponse()
	}
}

func listSandboxes(reader Reader) http.HandlerFunc {
	if reader == nil {
		return sandboxLifecycleUnavailable
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		sandboxes, err := reader.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not list sandboxes")
			return
		}
		items := make([]sandboxResponse, len(sandboxes))
		for i, sandbox := range sandboxes {
			items[i] = newSandboxResponse(sandbox)
		}

		writeJSON(w, http.StatusOK, sandboxListResponse{Items: items})
	}
}

func listSandboxUsage(reader UsageReader) http.HandlerFunc {
	if reader == nil {
		return unavailable("runtime_unavailable", "sandbox usage is unavailable")
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		readings, err := reader.ListUsage()
		if errors.Is(err, microvm.ErrSupervisorUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "sandbox usage is unavailable")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not read sandbox usage")
			return
		}
		items := make([]sandboxUsageItem, len(readings))
		for index, reading := range readings {
			items[index] = sandboxUsageItem{
				SandboxID:            reading.SandboxID,
				CPUUsageUS:           reading.CPUUsageUS,
				MemoryCurrentBytes:   reading.MemoryCurrentBytes,
				GuestMemoryMiB:       reading.GuestMemoryMiB,
				GuestMemoryUsedBytes: reading.GuestMemoryUsedBytes,
			}
		}
		writeJSON(w, http.StatusOK, sandboxUsageResponse{
			ObservedAt: time.Now().UTC(),
			Items:      items,
		})
	}
}

func readHostCapacity(reader HostCapacityReader) http.HandlerFunc {
	if reader == nil {
		return unavailable("runtime_unavailable", "host capacity is unavailable")
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		capacity, err := reader.HostCapacity()
		if errors.Is(err, microvm.ErrSupervisorUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "host capacity is unavailable")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not read host capacity")
			return
		}
		writeJSON(w, http.StatusOK, hostCapacityResponse{WarmSlots: warmSlotCapacityResponse{
			Target: capacity.Target, Ready: capacity.Ready,
			Claimed: capacity.Claimed, Preparing: capacity.Preparing,
		}})
	}
}

func getSandbox(reader Reader) http.HandlerFunc {
	if reader == nil {
		return sandboxLifecycleUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		sandbox, err := reader.Get(r.PathValue("id"))
		if errors.Is(err, microvm.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not retrieve sandbox")
			return
		}

		writeJSON(w, http.StatusOK, newSandboxResponse(sandbox))
	}
}

func terminateSandbox(terminator Terminator) http.HandlerFunc {
	if terminator == nil {
		return sandboxLifecycleUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		sandbox, err := terminator.Terminate(r.Context(), r.PathValue("id"))
		if errors.Is(err, microvm.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
			return
		}
		if errors.Is(err, microvm.ErrInvalidTransition) {
			writeError(w, http.StatusConflict, "invalid_state", "sandbox cannot be terminated from its current state")
			return
		}
		if errors.Is(err, microvm.ErrRuntimeUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "sandbox runtime could not be stopped")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not terminate sandbox")
			return
		}

		writeJSON(w, http.StatusOK, newSandboxResponse(sandbox))
	}
}

func crashSandbox(crasher Crasher) http.HandlerFunc {
	if crasher == nil {
		return unavailable("runtime_unavailable", "Sandbox crash injection is unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		sandbox, err := crasher.Crash(r.Context(), r.PathValue("id"))
		if errors.Is(err, microvm.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
			return
		}
		if errors.Is(err, microvm.ErrInvalidTransition) {
			writeError(w, http.StatusConflict, "invalid_state", "sandbox cannot be crashed from its current state")
			return
		}
		if errors.Is(err, microvm.ErrRuntimeUnavailable) || errors.Is(err, microvm.ErrSupervisorUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "Sandbox crash injection failed")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not crash sandbox")
			return
		}
		writeJSON(w, http.StatusOK, newSandboxResponse(sandbox))
	}
}

func newSandboxResponse(sandbox microvm.Sandbox) sandboxResponse {
	response := sandboxResponse{
		RuntimeTiming: sandbox.RuntimeTiming,
		ID:            sandbox.ID,
		AppID:         sandbox.AppID,
		WorkspaceID:   sandbox.WorkspaceID,
		Profile:       sandbox.Profile,
		BuildID:       sandbox.BuildID,
		VCPUs:         sandbox.VCPUs,
		MemoryMiB:     sandbox.MemoryMiB,
		MemoryMaxMiB:  microvm.DefaultSandboxMemoryMiB + microvm.SandboxHotplugMemoryMiB,
		State:         sandbox.State,
		CreatedAt:     sandbox.CreatedAt,
		UpdatedAt:     sandbox.UpdatedAt,
	}
	if response.RuntimeTiming.Status == "" {
		response.RuntimeTiming.Status = microvm.RuntimeTimingUnknown
	}
	if sandbox.Network != (microvm.NetworkAssignment{}) {
		response.Network = &networkResponse{
			GuestIP:   sandbox.Network.GuestIP,
			GatewayIP: sandbox.Network.GatewayIP,
			Subnet:    sandbox.Network.Subnet,
			GuestMAC:  sandbox.Network.GuestMAC,
		}
	}
	if !sandbox.StartupCompletedAt.IsZero() && sandbox.StartupDuration > 0 {
		response.Startup = &startupResponse{
			CompletedAt: sandbox.StartupCompletedAt,
			DurationMS:  float64(sandbox.StartupDuration) / float64(time.Millisecond),
		}
	}
	if sandbox.Failure.Reason == "" {
		return response
	}

	response.Failure = &failureResponse{Reason: sandbox.Failure.Reason}
	if sandbox.Failure.RuntimeExit.PID == 0 {
		return response
	}
	exit := sandbox.Failure.RuntimeExit
	exitResponse := &runtimeExitResponse{PID: exit.PID, Signal: exit.Signal}
	if exit.Signal == "" {
		exitResponse.ExitCode = &exit.ExitCode
	}
	response.Failure.RuntimeExit = exitResponse
	return response
}
