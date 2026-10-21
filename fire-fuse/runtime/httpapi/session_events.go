package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
)

func appendSessionEvent(broker sessionevents.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			View            *json.RawMessage `json:"view,omitempty"`
			SourceSandboxID string           `json:"source_sandbox_id,omitempty"`
			SourceCursor    uint64           `json:"source_cursor,omitempty"`
			WorkspaceID     string           `json:"workspace_id"`
			SessionID       string           `json:"session_id"`
			MessageID       string           `json:"message_id"`
			Data            json.RawMessage  `json:"data"`
		}
		if err := decodeRequest(w, r, &request); err != nil || !validSessionEventTarget(request.WorkspaceID, request.SessionID) ||
			!messageprotocol.ValidMessageID(request.MessageID) || (request.SourceSandboxID != "" && (!messageprotocol.ValidSandboxID(request.SourceSandboxID) || request.SourceCursor == 0)) || (request.SourceSandboxID == "" && request.SourceCursor != 0) {
			writeError(w, http.StatusBadRequest, "invalid_session_event", "invalid Session event")
			return
		}
		event, err := broker.Append(sessionevents.Append{
			Key:  sessionevents.Key{WorkspaceID: request.WorkspaceID, SessionID: request.SessionID},
			View: request.View, MessageID: request.MessageID, Data: request.Data, SourceSandboxID: request.SourceSandboxID, SourceCursor: request.SourceCursor,
		})
		if err != nil {
			if errors.Is(err, sessionevents.ErrCapacity) {
				writeError(w, http.StatusServiceUnavailable, "event_broker_capacity", "Session event broker capacity reached")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_session_event", "invalid Session event")
			return
		}
		document, err := sessionevents.MarshalEvent(event)
		writeSessionEventJSON(w, http.StatusCreated, document, err)
	}
}

func readSessionEvents(broker sessionevents.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		waitValues := values["wait"]
		if (len(values) != 3 && len(values) != 4) || len(values["workspace_id"]) != 1 || len(values["session_id"]) != 1 || len(values["after"]) != 1 ||
			(len(waitValues) > 1 || (len(waitValues) == 1 && waitValues[0] != "0")) ||
			!validSessionEventTarget(values.Get("workspace_id"), values.Get("session_id")) {
			writeError(w, http.StatusBadRequest, "invalid_session_event_cursor", "invalid Session event cursor")
			return
		}
		after, err := strconv.ParseUint(values.Get("after"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_session_event_cursor", "invalid Session event cursor")
			return
		}
		key := sessionevents.Key{WorkspaceID: values.Get("workspace_id"), SessionID: values.Get("session_id")}
		var batch sessionevents.Batch
		if len(waitValues) == 1 {
			batch, err = broker.ReadAvailable(key, after)
		} else {
			waitContext, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			batch, err = broker.Read(waitContext, key, after)
			cancel()
			if errors.Is(err, context.DeadlineExceeded) {
				err = nil
				batch, err = broker.ReadAvailable(key, after)
			}
		}
		switch {
		case errors.Is(err, sessionevents.ErrCapacity):
			writeError(w, http.StatusServiceUnavailable, "event_broker_capacity", "Session event subscription capacity reached")
		case errors.Is(err, sessionevents.ErrCursorExpired):
			writeJSON(w, http.StatusConflict, sessionEventCursorErrorResponse{Error: sessionEventCursorError{
				Code: "event_cursor_expired", Message: "Session event cursor is no longer available",
				EarliestSequence: batch.EarliestSequence,
			}})
		case err != nil:
			return
		default:
			document, err := sessionevents.MarshalBatch(batch)
			writeSessionEventJSON(w, http.StatusOK, document, err)
		}
	}
}

type sessionEventCursorError struct {
	Code             string `json:"code"`
	Message          string `json:"message"`
	EarliestSequence uint64 `json:"earliest_sequence"`
}

type sessionEventCursorErrorResponse struct {
	Error sessionEventCursorError `json:"error"`
}

func closeSessionEvents(broker sessionevents.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			WorkspaceID string `json:"workspace_id"`
			SessionID   string `json:"session_id"`
		}
		if err := decodeRequest(w, r, &request); err != nil || !validSessionEventTarget(request.WorkspaceID, request.SessionID) {
			writeError(w, http.StatusBadRequest, "invalid_session_event", "invalid Session event stream")
			return
		}
		if err := broker.Close(sessionevents.Key{WorkspaceID: request.WorkspaceID, SessionID: request.SessionID}); err != nil {
			if errors.Is(err, sessionevents.ErrCapacity) {
				writeError(w, http.StatusServiceUnavailable, "event_broker_capacity", "Session event broker capacity reached")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_session_event", "invalid Session event stream")
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Closed bool `json:"closed"`
		}{true})
	}
}

func validSessionEventTarget(workspaceID, sessionID string) bool {
	return workspaceID != "" && sessionID != "" && !strings.Contains(workspaceID, "/") && !strings.Contains(sessionID, "/")
}

func writeSessionEventJSON(w http.ResponseWriter, status int, document []byte, err error) {
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_event_encoding_failed", "could not encode Session events")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(document)
}
