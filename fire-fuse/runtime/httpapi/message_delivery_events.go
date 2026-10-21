package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

const defaultMessageDeliveryEventPageSize = 100

type MessageDeliveryEventController interface {
	ListMessageDeliveryEvents(messagequeue.EventQuery) (messagequeue.EventPage, error)
}

type messageDeliveryEventResponse struct {
	CurrentExecution *messagequeue.Execution `json:"current_execution,omitempty"`
	Sequence         int64                   `json:"sequence"`
	MessageID        string                  `json:"message_id"`
	WorkspaceID      string                  `json:"workspace_id"`
	LogicalSessionID string                  `json:"logical_session_id"`
	SessionID        string                  `json:"session_id,omitempty"`
	RequestID        string                  `json:"request_id,omitempty"`
	Attempt          int                     `json:"attempt"`
	Generation       uint64                  `json:"generation,omitempty"`
	Transition       messagequeue.Transition `json:"transition"`
	Status           messagequeue.Status     `json:"state"`
	WorkState        messagequeue.WorkState  `json:"work_state"`
	ErrorCode        string                  `json:"error_code,omitempty"`
	OccurredAt       time.Time               `json:"occurred_at"`
}

type messageDeliveryEventListResponse struct {
	Items              []messageDeliveryEventResponse `json:"items"`
	NextBeforeSequence int64                          `json:"next_before_sequence,omitempty"`
}

func listMessageDeliveryEvents(controller MessageDeliveryEventController) http.HandlerFunc {
	if controller == nil {
		return unavailable("event_store_unavailable", "Message event storage is unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		query, err := parseMessageDeliveryEventQuery(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid Message event query")
			return
		}
		page, err := controller.ListMessageDeliveryEvents(messagequeue.EventQuery{
			WorkspaceID: query.WorkspaceID, MessageID: query.MessageID,
			BeforeSequence: query.BeforeSequence, Limit: query.Limit,
		})
		switch {
		case errors.Is(err, messagequeue.ErrInvalidEvent):
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid Message event query")
		case err != nil:
			writeError(w, http.StatusServiceUnavailable, "event_store_unavailable", "Message event storage is unavailable")
		default:
			items := make([]messageDeliveryEventResponse, len(page.Items))
			for index, event := range page.Items {
				items[index] = newMessageDeliveryEventResponse(event)
			}
			writeJSON(w, http.StatusOK, messageDeliveryEventListResponse{
				Items: items, NextBeforeSequence: page.NextBeforeSequence,
			})
		}
	}
}

func parseMessageDeliveryEventQuery(r *http.Request) (model.MessageEventQuery, error) {
	values := r.URL.Query()
	for key, entries := range values {
		if (key != "workspace_id" && key != "message_id" && key != "before_sequence" && key != "limit") || len(entries) != 1 {
			return model.MessageEventQuery{}, model.ErrInvalidMessageEventQuery
		}
	}
	query := model.MessageEventQuery{
		WorkspaceID: values.Get("workspace_id"), MessageID: values.Get("message_id"),
		Limit: defaultMessageDeliveryEventPageSize,
	}
	if values.Has("workspace_id") && query.WorkspaceID == "" {
		return model.MessageEventQuery{}, model.ErrInvalidMessageEventQuery
	}
	if values.Has("message_id") && query.MessageID == "" {
		return model.MessageEventQuery{}, model.ErrInvalidMessageEventQuery
	}
	if values.Has("before_sequence") {
		value, err := strconv.ParseInt(values.Get("before_sequence"), 10, 64)
		if err != nil || value < 1 {
			return model.MessageEventQuery{}, model.ErrInvalidMessageEventQuery
		}
		query.BeforeSequence = value
	}
	if values.Has("limit") {
		value, err := strconv.Atoi(values.Get("limit"))
		if err != nil {
			return model.MessageEventQuery{}, model.ErrInvalidMessageEventQuery
		}
		query.Limit = value
	}
	return query, query.Validate()
}

func newMessageDeliveryEventResponse(event messagequeue.Event) messageDeliveryEventResponse {
	return messageDeliveryEventResponse{
		CurrentExecution: event.CurrentExecution,
		Sequence:         event.Sequence, MessageID: event.MessageID, WorkspaceID: event.WorkspaceID,
		LogicalSessionID: event.LogicalSessionID, SessionID: event.SessionID, RequestID: event.RequestID,
		Attempt: event.Attempt, Generation: event.Generation, Transition: event.Transition,
		Status: event.Status, WorkState: event.WorkState, ErrorCode: event.ErrorCode, OccurredAt: event.OccurredAt,
	}
}
