package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/edgeprotocol"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
)

func (h *Handler) serveEdgeMessageAcceptance(
	w http.ResponseWriter,
	r *http.Request,
	target edgeprotocol.DispatchTarget,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "runtime_method_not_allowed", "Message acceptance requires POST")
		return
	}
	var request messageprotocol.Request
	if err := decodeEdgeJSON(w, r, &request, 1<<20); err != nil || request.Validate() != nil {
		writeError(w, http.StatusBadRequest, "invalid_message_request", "Message input is invalid")
		return
	}
	if request.Reply {
		writeError(w, http.StatusBadRequest, "invalid_message_request", "Message input is invalid")
		return
	}
	stored, ok := h.admitEdgeMessage(w, r, target, request)
	if !ok {
		return
	}
	writeEdgeJSON(w, http.StatusAccepted, struct {
		ID         string    `json:"id"`
		Status     string    `json:"status"`
		AcceptedAt time.Time `json:"accepted_at"`
	}{ID: stored.ID, Status: "accepted", AcceptedAt: stored.AcceptedAt})
}

// admitEdgeMessage durably enqueues one validated Message or request for the
// dispatched App and writes the error response itself when admission fails.
func (h *Handler) admitEdgeMessage(
	w http.ResponseWriter,
	r *http.Request,
	target edgeprotocol.DispatchTarget,
	request messageprotocol.Request,
) (MessageDelivery, bool) {
	workspace, ok := h.edgeWorkspace(w, r, target.AppID, request.Session.WorkspaceID)
	if !ok {
		return MessageDelivery{}, false
	}

	stored, err := h.messages.EnqueueMessageDelivery(r.Context(), MessageDelivery{
		ID: request.Message.ID, WorkspaceID: workspace.ID,
		LogicalSessionID: request.Session.ID, RequestID: target.RequestID,
		KeepAliveSeconds: request.Session.KeepAliveSeconds,
		Payload:          request.Message.Payload, ReplyRequested: request.Reply,
	})
	if err != nil {
		var controlErr *ControlError
		switch {
		case errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusTooManyRequests:
			writeError(w, http.StatusTooManyRequests, "storage_quota", "Message storage quota would be exceeded")
		case errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusConflict:
			writeError(w, http.StatusConflict, "message_conflict", "Message identity conflicts with an existing delivery")
		case errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusBadRequest:
			writeError(w, http.StatusBadRequest, "invalid_message_request", "Message input is invalid")
		default:
			h.logger.Error("could not persist Message delivery", "workspace_id", workspace.ID, "message_id", request.Message.ID, "error", err)
			writeError(w, http.StatusServiceUnavailable, "message_queue_unavailable", "Message could not be accepted")
		}
		return MessageDelivery{}, false
	}
	return stored, true
}

func (h *Handler) serveEdgeSessionRequest(w http.ResponseWriter, r *http.Request, target edgeprotocol.DispatchTarget) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "runtime_method_not_allowed", "Session request requires POST")
		return
	}
	var envelope struct {
		messageprotocol.Request
		TimeoutMS int `json:"timeout_ms"`
	}
	if err := decodeEdgeJSON(w, r, &envelope, 1<<20); err != nil || envelope.Request.Validate() != nil ||
		envelope.Reply || envelope.TimeoutMS < 1 || envelope.TimeoutMS > 300_000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "Session request input is invalid")
		return
	}
	request := envelope.Request
	request.Reply = true
	stored, ok := h.admitEdgeMessage(w, r, target, request)
	if !ok {
		return
	}
	waitContext, cancel := context.WithTimeout(r.Context(), time.Duration(envelope.TimeoutMS)*time.Millisecond)
	defer cancel()
	for {
		if stored.State == "handled" && len(stored.Reply) > 0 {
			writeEdgeJSON(w, http.StatusOK, struct {
				ID    string          `json:"id"`
				Reply json.RawMessage `json:"reply"`
			}{stored.ID, stored.Reply})
			return
		}
		if stored.State == "handled" && stored.ReplyRequested {
			writeError(w, http.StatusGone, "request_reply_expired", "Session request reply is no longer available")
			return
		}
		if stored.State == "failed" {
			writeError(w, http.StatusUnprocessableEntity, stored.ErrorCode, "Session request failed")
			return
		}
		var err error
		stored, err = h.messages.WaitForMessageReply(waitContext, request.Message.ID)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			if waitContext.Err() != nil {
				writeError(w, http.StatusGatewayTimeout, "request_wait_timeout", "Session request is still running")
				return
			}
			writeError(w, http.StatusServiceUnavailable, "message_queue_unavailable", "Session request status is unavailable")
			return
		}
	}
}

func (h *Handler) serveEdgeMessageStatus(
	w http.ResponseWriter,
	r *http.Request,
	target edgeprotocol.DispatchTarget,
	logicalSessionID, messageID string,
) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "runtime_method_not_allowed", "Message status requires GET")
		return
	}
	delivery, err := h.messages.GetMessageDelivery(r.Context(), messageID)
	if err != nil {
		var controlErr *ControlError
		if errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusNotFound {
			writeUnknownMessage(w, messageID)
			return
		}
		h.logger.Error("could not read Message delivery", "message_id", messageID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "message_queue_unavailable", "Message status is temporarily unavailable")
		return
	}
	if delivery.LogicalSessionID != logicalSessionID {
		writeUnknownMessage(w, messageID)
		return
	}
	workspace, err := h.workspaces.GetWorkspace(r.Context(), delivery.WorkspaceID)
	if err != nil {
		var controlErr *ControlError
		if errors.As(err, &controlErr) && controlErr.StatusCode == http.StatusNotFound {
			writeUnknownMessage(w, messageID)
			return
		}
		h.logger.Error("could not authorize Message status", "message_id", messageID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "control_unavailable", "Message status is temporarily unavailable")
		return
	}
	if workspace.AppID != target.AppID {
		writeUnknownMessage(w, messageID)
		return
	}

	if delivery.Execution != nil {
		e := delivery.Execution
		outcome := "pending"
		if e.Receipt != nil && e.Receipt.State.Terminal() {
			outcome = string(e.Receipt.State)
		}
		if (e.Phase == messageprotocol.PhaseUnavailable || e.Phase == messageprotocol.PhaseRetrying) && outcome == "pending" {
			outcome = "unknown"
		}
		writeEdgeJSON(w, http.StatusOK, map[string]any{"id": delivery.ID, "workspace_id": delivery.WorkspaceID, "state": delivery.State, "accepted_at": delivery.AcceptedAt, "started_at": delivery.StartedAt, "handled_at": delivery.HandledAt, "failed_at": delivery.HandledAt, "error": map[string]string{"code": delivery.ErrorCode}, "execution": map[string]any{"phase": e.Phase, "phase_at": e.PhaseAt, "outcome": outcome, "sandbox_id": e.SandboxID, "deadline": e.ExecutionDeadline, "receipt": e.Receipt, "work_state": delivery.WorkState, "activity": e.Activity, "reason": e.ErrorCode}})
		return
	}
	switch delivery.State {
	case "accepted", "handling", "handled", "failed":
		writeEdgeJSON(w, http.StatusOK, map[string]any{"id": delivery.ID, "workspace_id": delivery.WorkspaceID, "state": delivery.State, "accepted_at": delivery.AcceptedAt, "started_at": delivery.StartedAt, "handled_at": delivery.HandledAt, "failed_at": delivery.HandledAt, "error": map[string]string{"code": delivery.ErrorCode}})
	default:
		writeUnknownMessage(w, messageID)
	}

}

func writeUnknownMessage(w http.ResponseWriter, messageID string) {
	writeEdgeJSON(w, http.StatusOK, struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}{messageID, "unknown"})
}
