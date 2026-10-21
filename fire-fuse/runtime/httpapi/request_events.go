package httpapi

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

const defaultRequestEventPageSize = 100

type RequestEventController interface {
	RecordRequestEvent(microvm.RequestEvent) (microvm.RequestEvent, bool, error)
	ListRequestEvents(microvm.RequestEventQuery) (microvm.RequestEventPage, error)
}

type recordRequestEventRequest struct {
	EventID     string    `json:"event_id"`
	RequestID   string    `json:"request_id"`
	WorkspaceID string    `json:"workspace_id"`
	SessionID   string    `json:"session_id"`
	ReceivedAt  time.Time `json:"received_at"`
	StatusCode  int       `json:"status_code"`
	DurationMS  float64   `json:"duration_ms"`
}

type requestEventResponse struct {
	Sequence    int64     `json:"sequence"`
	EventID     string    `json:"event_id"`
	RequestID   string    `json:"request_id,omitempty"`
	WorkspaceID string    `json:"workspace_id"`
	SessionID   string    `json:"session_id,omitempty"`
	ReceivedAt  time.Time `json:"received_at"`
	StatusCode  int       `json:"status_code"`
	DurationMS  float64   `json:"duration_ms"`
}

type requestEventListResponse struct {
	Items              []requestEventResponse `json:"items"`
	NextBeforeSequence int64                  `json:"next_before_sequence,omitempty"`
}

func recordRequestEvent(controller RequestEventController) http.HandlerFunc {
	if controller == nil {
		return unavailable("event_store_unavailable", "request event storage is unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var request recordRequestEventRequest
		if err := decodeRequest(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if request.DurationMS < 0 || request.DurationMS > float64(math.MaxInt64)/float64(time.Millisecond) {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid request event")
			return
		}
		duration := time.Duration(math.Round(request.DurationMS*1000)) * time.Microsecond
		event, created, err := controller.RecordRequestEvent(microvm.RequestEvent{
			EventID: request.EventID, RequestID: request.RequestID, WorkspaceID: request.WorkspaceID,
			SessionID:  request.SessionID,
			ReceivedAt: request.ReceivedAt, StatusCode: request.StatusCode, Duration: duration,
		})
		switch {
		case errors.Is(err, microvm.ErrInvalidRequestEvent):
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid request event")
		case errors.Is(err, microvm.ErrRequestEventConflict):
			writeError(w, http.StatusConflict, "request_event_conflict", "event ID already records different request data")
		case errors.Is(err, microvm.ErrRequestEventStore):
			writeError(w, http.StatusServiceUnavailable, "event_store_unavailable", "request event storage is unavailable")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "internal_error", "could not persist request event")
		default:
			status := http.StatusOK
			if created {
				status = http.StatusCreated
			}
			writeJSON(w, status, newRequestEventResponse(event))
		}
	}
}

func listRequestEvents(controller RequestEventController) http.HandlerFunc {
	if controller == nil {
		return unavailable("event_store_unavailable", "request event storage is unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		query, err := parseRequestEventQuery(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid request event query")
			return
		}
		page, err := controller.ListRequestEvents(query)
		switch {
		case errors.Is(err, microvm.ErrInvalidRequestEvent):
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid request event query")
		case errors.Is(err, microvm.ErrRequestEventStore):
			writeError(w, http.StatusServiceUnavailable, "event_store_unavailable", "request event storage is unavailable")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "internal_error", "could not list request events")
		default:
			items := make([]requestEventResponse, len(page.Items))
			for index, event := range page.Items {
				items[index] = newRequestEventResponse(event)
			}
			writeJSON(w, http.StatusOK, requestEventListResponse{
				Items: items, NextBeforeSequence: page.NextBeforeSequence,
			})
		}
	}
}

func parseRequestEventQuery(r *http.Request) (microvm.RequestEventQuery, error) {
	values := r.URL.Query()
	for key, entries := range values {
		if key != "workspace_id" && key != "app_id" && key != "before_sequence" && key != "after_sequence" && key != "limit" {
			return microvm.RequestEventQuery{}, microvm.ErrInvalidRequestEvent
		}
		if len(entries) != 1 {
			return microvm.RequestEventQuery{}, microvm.ErrInvalidRequestEvent
		}
	}
	query := microvm.RequestEventQuery{
		WorkspaceID: values.Get("workspace_id"), AppID: values.Get("app_id"),
		Limit: defaultRequestEventPageSize,
	}
	if values.Has("workspace_id") && query.WorkspaceID == "" {
		return microvm.RequestEventQuery{}, microvm.ErrInvalidRequestEvent
	}
	if values.Has("app_id") && query.AppID == "" {
		return microvm.RequestEventQuery{}, microvm.ErrInvalidRequestEvent
	}
	if raw := values.Get("before_sequence"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 {
			return microvm.RequestEventQuery{}, microvm.ErrInvalidRequestEvent
		}
		query.BeforeSequence = value
	} else if values.Has("before_sequence") {
		return microvm.RequestEventQuery{}, microvm.ErrInvalidRequestEvent
	}
	if raw := values.Get("after_sequence"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 {
			return microvm.RequestEventQuery{}, microvm.ErrInvalidRequestEvent
		}
		query.AfterSequence = value
	} else if values.Has("after_sequence") {
		return microvm.RequestEventQuery{}, microvm.ErrInvalidRequestEvent
	}
	if raw := values.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return microvm.RequestEventQuery{}, microvm.ErrInvalidRequestEvent
		}
		query.Limit = value
	} else if values.Has("limit") {
		return microvm.RequestEventQuery{}, microvm.ErrInvalidRequestEvent
	}
	return query, query.Validate()
}

func newRequestEventResponse(event microvm.RequestEvent) requestEventResponse {
	return requestEventResponse{
		Sequence: event.Sequence, EventID: event.EventID,
		RequestID:   event.RequestID,
		WorkspaceID: event.WorkspaceID, SessionID: event.SessionID,
		ReceivedAt: event.ReceivedAt,
		StatusCode: event.StatusCode, DurationMS: float64(event.Duration.Microseconds()) / 1000,
	}
}
