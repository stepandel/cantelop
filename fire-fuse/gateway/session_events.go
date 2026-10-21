package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/edgeprotocol"
)

func (h *Handler) serveEdgeSessionEvents(
	w http.ResponseWriter,
	r *http.Request,
	target edgeprotocol.DispatchTarget,
	sessionID string,
) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "runtime_method_not_allowed", "Session events require GET")
		return
	}
	workspaceID, after, ok := parseSessionEventCursor(w, r)
	if !ok {
		return
	}
	if _, ok := h.edgeWorkspace(w, r, target.AppID, workspaceID); !ok {
		return
	}
	webSocket := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	if !webSocket && !strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") {
		writeError(w, http.StatusNotAcceptable, "event_transport_required", "Session events require text/event-stream")
		return
	}

	batch, err := h.events.ReadSessionEvents(r.Context(), workspaceID, sessionID, after, false)
	if err != nil {
		h.writeSessionEventReadError(w, err)
		return
	}
	expected := r.URL.Query().Get("stream_id")
	if expected != "" && expected != batch.StreamID {
		writeError(w, http.StatusConflict, "event_stream_reset", "Session output stream was replaced")
		return
	}
	streamID := batch.StreamID
	if webSocket {
		h.serveSessionEventWebSocket(w, r, target.Hostname, workspaceID, sessionID, after, batch)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	// Send body bytes as well as headers: intermediary proxies can otherwise
	// hold an empty stream until the first event or 15-second heartbeat.
	// This SSE comment acknowledges the subscription, not agent readiness,
	// and does not create an event or advance the replay cursor. The loop
	// below flushes it together with the initial batch.
	if _, err := w.Write([]byte(": connected\n\n")); err != nil {
		return
	}

	for {
		if err := writeSSEBatch(w, batch, &after); err != nil {
			return
		}
		flusher.Flush()
		if batch.Closed {
			return
		}
		batch, err = h.events.ReadSessionEvents(r.Context(), workspaceID, sessionID, after, true)
		if err != nil {
			if r.Context().Err() == nil {
				code := "event_broker_unavailable"
				var controlErr *controlclient.Error
				if errors.As(err, &controlErr) && controlErr.Code == "event_cursor_expired" {
					code = controlErr.Code
				}
				_, _ = fmt.Fprintf(w, "event: error\ndata: {\"code\":%q}\n\n", code)
				flusher.Flush()
			}
			return
		}
		if batch.StreamID != streamID {
			_, _ = w.Write([]byte("event: error\ndata: {\"code\":\"event_stream_reset\"}\n\n"))
			flusher.Flush()
			return
		}
		if len(batch.Events) == 0 {
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
		}
	}
}

const sessionEventWebSocketProtocol = "cantelop.events.v1"

func (h *Handler) serveSessionEventWebSocket(
	w http.ResponseWriter,
	r *http.Request,
	publicHostname, workspaceID, sessionID string,
	after uint64,
	batch controlclient.SessionEventBatch,
) {
	if !headerTokenContains(r.Header.Get("Sec-WebSocket-Protocol"), sessionEventWebSocketProtocol) {
		writeError(w, http.StatusBadRequest, "event_protocol_required", "Session WebSocket protocol is required")
		return
	}
	if !validWebSocketOrigin(r.Header.Values("Origin"), publicHostname) {
		writeError(w, http.StatusForbidden, "event_origin_denied", "Session WebSocket Origin is not allowed")
		return
	}
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{sessionEventWebSocketProtocol},
		// The gateway validates the preserved public Origin against trusted edge
		// dispatch metadata above; its private Host necessarily differs.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	streamContext := connection.CloseRead(r.Context())
	streamID := batch.StreamID
	for {
		if batch.StreamID != streamID {
			_ = connection.Close(websocket.StatusPolicyViolation, "event_stream_reset")
			return
		}
		for _, event := range batch.Events {
			document, err := json.Marshal(event)
			if err != nil {
				_ = connection.Close(websocket.StatusInternalError, "event encoding failed")
				return
			}
			writeContext, cancel := context.WithTimeout(streamContext, 15*time.Second)
			err = connection.Write(writeContext, websocket.MessageText, document)
			cancel()
			if err != nil {
				return
			}
			after = event.Sequence
		}
		if batch.Closed {
			_ = connection.Close(websocket.StatusNormalClosure, "Session terminated")
			return
		}
		batch, err = h.events.ReadSessionEvents(streamContext, workspaceID, sessionID, after, true)
		if err != nil {
			if streamContext.Err() == nil {
				code := "event_broker_unavailable"
				var controlErr *controlclient.Error
				if errors.As(err, &controlErr) && controlErr.Code == "event_cursor_expired" {
					code = controlErr.Code
				}
				document, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code}})
				_ = connection.Write(streamContext, websocket.MessageText, document)
				_ = connection.Close(websocket.StatusInternalError, code)
			}
			return
		}
	}
}

func validWebSocketOrigin(values []string, publicHostname string) bool {
	if len(values) != 1 {
		return false
	}
	origin, err := url.Parse(values[0])
	return err == nil && origin.Scheme == "https" && origin.Host == publicHostname &&
		origin.User == nil && origin.RawQuery == "" && origin.Fragment == "" &&
		(origin.Path == "" || origin.Path == "/")
}

func headerTokenContains(value, expected string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.TrimSpace(token) == expected {
			return true
		}
	}
	return false
}

func parseSessionEventCursor(w http.ResponseWriter, r *http.Request) (string, uint64, bool) {
	values := r.URL.Query()
	for key, entries := range values {
		if (key != "workspace_id" && key != "after" && key != "stream_id") || len(entries) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_event_cursor", "Session event cursor is invalid")
			return "", 0, false
		}
	}
	workspaceID := values.Get("workspace_id")
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "invalid_event_cursor", "Session event Workspace is required")
		return "", 0, false
	}
	afterValues := values["after"]
	lastEventIDs := r.Header.Values("Last-Event-ID")
	if len(lastEventIDs) > 1 || (len(afterValues) == 1 && len(lastEventIDs) == 1 && lastEventIDs[0] != "") {
		writeError(w, http.StatusBadRequest, "invalid_event_cursor", "Session event cursor is ambiguous")
		return "", 0, false
	}
	raw := "0"
	if len(afterValues) == 1 {
		raw = afterValues[0]
	} else if len(lastEventIDs) == 1 && lastEventIDs[0] != "" {
		parts := strings.Split(lastEventIDs[0], ":")
		if len(parts) != 2 || values.Get("stream_id") != "" {
			writeError(w, http.StatusBadRequest, "invalid_event_cursor", "Session event cursor requires stream identity")
			return "", 0, false
		}
		values.Set("stream_id", parts[0])
		r.URL.RawQuery = values.Encode()
		raw = parts[1]
	}
	after, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || (after > 0 && values.Get("stream_id") == "") || (values.Get("stream_id") != "" && !validEventStreamID(values.Get("stream_id"))) {
		writeError(w, http.StatusBadRequest, "invalid_event_cursor", "Session event cursor is invalid")
		return "", 0, false
	}
	return workspaceID, after, true
}

func (h *Handler) writeSessionEventReadError(w http.ResponseWriter, err error) {
	var controlErr *controlclient.Error
	if errors.As(err, &controlErr) && controlErr.Code == "event_cursor_expired" {
		writeError(w, http.StatusConflict, controlErr.Code, "Session event cursor is no longer available")
		return
	}
	h.logger.Error("could not read Session events", "error", err)
	writeError(w, http.StatusServiceUnavailable, "event_broker_unavailable", "Session event broker is temporarily unavailable")
}

func writeSSEBatch(w http.ResponseWriter, batch controlclient.SessionEventBatch, after *uint64) error {
	for _, event := range batch.Events {
		document, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "id: %s:%d\ndata: %s\n\n", event.StreamID, event.Sequence, document); err != nil {
			return err
		}
		*after = event.Sequence
	}
	return nil
}

func validEventStreamID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
