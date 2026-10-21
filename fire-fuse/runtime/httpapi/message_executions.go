package httpapi

import (
	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"net/http"
	"time"
)

type executionController interface {
	SaveExecution(messagequeue.Execution) (messagequeue.Execution, error)
	ListExecutions() ([]messagequeue.Execution, error)
}

type pinnedWorkLeaseController interface {
	ReleasePinnedSessionWorkLease(string, string, string) (microvm.Session, error)
	AcquirePinnedSessionWorkLease(string, string, string, time.Duration) (microvm.SessionWorkLease, error)
	RenewPinnedSessionWorkLease(string, string, string, time.Duration) (microvm.SessionWorkLease, error)
}

// executions lists or saves Message executions; each save wakes requests
// waiting on that Message through replies.
func executions(c executionController, replies *messageReplySignal) http.HandlerFunc {
	if c == nil {
		return unavailable("execution_unavailable", "Execution store unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			items, err := c.ListExecutions()
			if err != nil {
				writeMessageDeliveryError(w, err)
				return
			}
			writeJSON(w, 200, struct {
				Items []messagequeue.Execution `json:"items"`
			}{items})
			return
		}
		var e messagequeue.Execution
		if err := decodeRequest(w, r, &e); err != nil {
			writeError(w, 400, "invalid_execution", "Invalid execution")
			return
		}
		updated, err := c.SaveExecution(e)
		if err != nil {
			writeMessageDeliveryError(w, err)
			return
		}
		replies.notify(updated.MessageID)
		writeJSON(w, 200, updated)
	}
}

func pinnedWorkLease(c pinnedWorkLeaseController) http.HandlerFunc {
	if c == nil {
		return unavailable("runtime_unavailable", "Pinned leases unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			SessionID string `json:"session_id"`
			SandboxID string `json:"sandbox_id"`
			LeaseID   string `json:"lease_id"`
			Renew     bool   `json:"renew"`
			Release   bool   `json:"release"`
		}
		if decodeRequest(w, r, &request) != nil || request.SandboxID == "" {
			writeError(w, 400, "invalid_request", "Invalid pinned lease")
			return
		}
		if request.Release {
			if request.Renew {
				writeError(w, 400, "invalid_request", "Invalid pinned lease operation")
				return
			}
			value, err := c.ReleasePinnedSessionWorkLease(request.SessionID, request.SandboxID, request.LeaseID)
			if err != nil {
				writeSessionError(w, err, "Pinned lease release unavailable")
				return
			}
			writeJSON(w, 200, value)
			return
		}
		var value microvm.SessionWorkLease
		var err error
		if request.Renew {
			value, err = c.RenewPinnedSessionWorkLease(request.SessionID, request.SandboxID, request.LeaseID, 2*time.Minute)
		} else {
			value, err = c.AcquirePinnedSessionWorkLease(request.SessionID, request.SandboxID, request.LeaseID, 2*time.Minute)
		}
		if err != nil {
			writeSessionError(w, err, "Pinned lease unavailable")
			return
		}
		writeJSON(w, 200, value)
	}
}
