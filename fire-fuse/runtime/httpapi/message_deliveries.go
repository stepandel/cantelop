package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
)

type MessageDeliveryController interface {
	EnqueueMessageDelivery(string, string, string, string, int, json.RawMessage, bool) (messagequeue.Message, bool, error)
	GetMessageDelivery(string) (messagequeue.Message, error)
	ClaimNextMessageDelivery() (messagequeue.Message, bool, error)
	RequeueHandlingMessageDeliveries() (int64, error)
	RequeueMessageDelivery(string) (messagequeue.Message, error)
	SaveMessageReply(string, json.RawMessage) (messagequeue.Message, error)
	FailMessageDelivery(string, string, string) (messagequeue.Message, error)
}

var messageQueueUnavailable = unavailable("message_queue_unavailable", "Message delivery storage is unavailable")

// messageAdmissionSignal is a hint: durable claims remain the source of truth.
// Closing a generation wakes all waiting workers without losing an enqueue
// between their queue check and wait.
type messageAdmissionSignal struct {
	mu         sync.Mutex
	changed    chan struct{}
	generation uint64
}

func newMessageAdmissionSignal() *messageAdmissionSignal {
	return &messageAdmissionSignal{changed: make(chan struct{})}
}
func (s *messageAdmissionSignal) subscribe() (uint64, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation, s.changed
}
func (s *messageAdmissionSignal) notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	close(s.changed)
	s.changed = make(chan struct{})
}

// enqueueMessageDelivery wakes waiting claim workers through admissions after
// each successful enqueue.
func enqueueMessageDelivery(controller MessageDeliveryController, admissions *messageAdmissionSignal) http.HandlerFunc {
	if controller == nil {
		return messageQueueUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID               string          `json:"id"`
			WorkspaceID      string          `json:"workspace_id"`
			LogicalSessionID string          `json:"logical_session_id"`
			RequestID        string          `json:"request_id,omitempty"`
			KeepAliveSeconds int             `json:"keep_alive_seconds"`
			Payload          json.RawMessage `json:"payload"`
			ReplyRequested   bool            `json:"reply_requested,omitempty"`
		}
		if err := decodeRequest(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		value, created, err := controller.EnqueueMessageDelivery(
			request.ID, request.WorkspaceID, request.LogicalSessionID, request.RequestID,
			request.KeepAliveSeconds, request.Payload, request.ReplyRequested,
		)
		if err != nil {
			writeMessageDeliveryError(w, err)
			return
		}
		admissions.notify()
		status := http.StatusOK
		if created {
			status = http.StatusAccepted
		}
		writeJSON(w, status, value)
	}
}

// getMessageDelivery reads one Message. With a non-nil replies signal it waits
// up to a second for a pending Message to settle; with nil it never waits.
func getMessageDelivery(controller MessageDeliveryController, replies *messageReplySignal) http.HandlerFunc {
	if controller == nil {
		return messageQueueUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var changed <-chan struct{}
		if replies != nil {
			var unsubscribe func()
			changed, unsubscribe = replies.subscribe(r.PathValue("id"))
			defer unsubscribe()
		}
		// Subscribe before reading durable state so a concurrent commit cannot
		// fall between the state check and the wait. The signal is only a hint.
		value, err := controller.GetMessageDelivery(r.PathValue("id"))
		if err == nil && changed != nil && value.Status != messagequeue.StatusHandled && value.Status != messagequeue.StatusFailed {
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-r.Context().Done():
				return
			case <-changed:
			case <-timer.C:
			}
			value, err = controller.GetMessageDelivery(r.PathValue("id"))
		}
		if err != nil {
			writeMessageDeliveryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, value)
	}
}

func claimMessageDelivery(controller MessageDeliveryController) http.HandlerFunc {
	if controller == nil {
		return messageQueueUnavailable
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		value, found, err := controller.ClaimNextMessageDelivery()
		if err != nil {
			writeMessageDeliveryError(w, err)
			return
		}
		if !found {
			writeJSON(w, http.StatusOK, struct {
				Message *messagequeue.Message `json:"message"`
			}{})
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Message *messagequeue.Message `json:"message"`
		}{Message: &value})
	}
}

func waitMessageDeliveryNotification(signal *messageAdmissionSignal) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		after, err := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "after must be an unsigned integer")
			return
		}
		generation, changed := signal.subscribe()
		if generation == after {
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-r.Context().Done():
				return
			case <-changed:
			case <-timer.C:
			}
			generation, _ = signal.subscribe()
		}
		writeJSON(w, http.StatusOK, struct {
			Generation uint64 `json:"generation"`
		}{generation})
	}
}

func requeueHandlingMessageDeliveries(controller MessageDeliveryController) http.HandlerFunc {
	if controller == nil {
		return messageQueueUnavailable
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		count, err := controller.RequeueHandlingMessageDeliveries()
		if err != nil {
			writeMessageDeliveryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Requeued int64 `json:"requeued"`
		}{count})
	}
}

func requeueMessageDelivery(controller MessageDeliveryController) http.HandlerFunc {
	return messageTransition(controller, func(controller MessageDeliveryController, id string, _ messageTransitionRequest) (messagequeue.Message, error) {
		return controller.RequeueMessageDelivery(id)
	})
}

func saveMessageReply(controller MessageDeliveryController) http.HandlerFunc {
	if controller == nil {
		return messageQueueUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Reply json.RawMessage `json:"reply"`
		}
		if err := decodeRequest(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		value, err := controller.SaveMessageReply(r.PathValue("id"), request.Reply)
		if err != nil {
			writeMessageDeliveryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, value)
	}
}

// failMessageDelivery wakes requests waiting on the Message through replies.
func failMessageDelivery(controller MessageDeliveryController, replies *messageReplySignal) http.HandlerFunc {
	return messageTransition(controller, func(controller MessageDeliveryController, id string, request messageTransitionRequest) (messagequeue.Message, error) {
		value, err := controller.FailMessageDelivery(id, request.SessionID, request.ErrorCode)
		if err == nil {
			replies.notify(id)
		}
		return value, err
	})
}

type messageTransitionRequest struct {
	SessionID string `json:"session_id"`
	ErrorCode string `json:"error_code"`
}

func messageTransition(
	controller MessageDeliveryController,
	transition func(MessageDeliveryController, string, messageTransitionRequest) (messagequeue.Message, error),
) http.HandlerFunc {
	if controller == nil {
		return messageQueueUnavailable
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var request messageTransitionRequest
		if r.ContentLength != 0 {
			if err := decodeRequest(w, r, &request); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
		}
		value, err := transition(controller, r.PathValue("id"), request)
		if err != nil {
			writeMessageDeliveryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, value)
	}
}

func writeMessageDeliveryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, messagequeue.ErrStorageQuota):
		writeError(w, http.StatusTooManyRequests, "storage_quota", "Message storage quota would be exceeded")
	case errors.Is(err, messagequeue.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_message", "Message delivery is invalid")
	case errors.Is(err, messagequeue.ErrNotFound):
		writeError(w, http.StatusNotFound, "message_not_found", "Message delivery does not exist")
	case errors.Is(err, messagequeue.ErrConflict):
		writeError(w, http.StatusConflict, "message_conflict", "Message delivery conflicts with its current state")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Message delivery storage failed")
	}
}
