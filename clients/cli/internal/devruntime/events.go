package devruntime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/clients/cli/internal/localhttp"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
)

const localEventWebSocketProtocol = "cantelop.events.v1"

func (runtime *localRuntime) sessionEvents(response http.ResponseWriter, request *http.Request) {
	match := localSessionEventsPattern.FindStringSubmatch(request.URL.Path)
	workspaceID, after, ok := localEventCursor(request)
	if len(match) != 2 || !ok || !ids.ValidLocalWorkspaceID(workspaceID) {
		localhttp.WriteError(response, http.StatusBadRequest, "invalid_event_cursor")
		return
	}
	webSocket := strings.EqualFold(request.Header.Get("Upgrade"), "websocket")
	if !webSocket && !strings.Contains(strings.ToLower(request.Header.Get("Accept")), "text/event-stream") {
		localhttp.WriteError(response, http.StatusNotAcceptable, "event_transport_required")
		return
	}
	key := sessionevents.Key{WorkspaceID: workspaceID, SessionID: match[1]}
	batch, err := runtime.events.ReadAvailable(key, after)
	if errors.Is(err, sessionevents.ErrCursorExpired) {
		localhttp.WriteError(response, http.StatusConflict, "event_cursor_expired")
		return
	}
	if err != nil {
		localhttp.WriteError(response, http.StatusServiceUnavailable, "event_broker_unavailable")
		return
	}
	expected := request.URL.Query().Get("stream_id")
	if expected != "" && expected != batch.StreamID {
		localhttp.WriteError(response, http.StatusConflict, "event_stream_reset")
		return
	}
	if webSocket {
		runtime.sessionEventWebSocket(response, request, key, after, batch)
		return
	}
	runtime.sessionEventSSE(response, request, key, after, batch)
}

func (runtime *localRuntime) sessionEventSSE(
	response http.ResponseWriter,
	request *http.Request,
	key sessionevents.Key,
	after uint64,
	batch sessionevents.Batch,
) {
	response.Header().Set("Connection", "keep-alive")
	response.Header().Set("X-Accel-Buffering", "no")
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	flusher, ok := response.(http.Flusher)
	if !ok {
		return
	}
	// Match the gateway: deliver body bytes immediately so an idle
	// subscription does not wait for the first event or keep-alive. The
	// loop below flushes the comment together with the initial batch.
	if _, err := response.Write([]byte(": connected\n\n")); err != nil {
		return
	}
	streamID := batch.StreamID
	for {
		if batch.StreamID != streamID {
			_, _ = response.Write([]byte("event: error\ndata: {\"code\":\"event_stream_reset\"}\n\n"))
			flusher.Flush()
			return
		}
		for _, event := range batch.Events {
			document, _ := json.Marshal(event)
			if _, err := fmt.Fprintf(response, "id: %s:%d\ndata: %s\n\n", event.StreamID, event.Sequence, document); err != nil {
				return
			}
			after = event.Sequence
		}
		flusher.Flush()
		if batch.Closed {
			return
		}
		waitContext, cancel := context.WithTimeout(request.Context(), 15*time.Second)
		var err error
		batch, err = runtime.events.Read(waitContext, key, after)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			batch, err = runtime.events.ReadAvailable(key, after)
			if err == nil {
				_, err = response.Write([]byte(": keep-alive\n\n"))
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func (runtime *localRuntime) sessionEventWebSocket(
	response http.ResponseWriter,
	request *http.Request,
	key sessionevents.Key,
	after uint64,
	batch sessionevents.Batch,
) {
	if !localHeaderTokenContains(request.Header.Get("Sec-WebSocket-Protocol"), localEventWebSocketProtocol) ||
		!localWebSocketOrigin(request.Header.Values("Origin")) {
		localhttp.WriteError(response, http.StatusForbidden, "event_websocket_denied")
		return
	}
	connection, err := websocket.Accept(response, request, &websocket.AcceptOptions{
		Subprotocols: []string{localEventWebSocketProtocol}, InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	streamContext := connection.CloseRead(request.Context())
	streamID := batch.StreamID
	for {
		if batch.StreamID != streamID {
			_ = connection.Close(websocket.StatusPolicyViolation, "event_stream_reset")
			return
		}
		for _, event := range batch.Events {
			document, _ := json.Marshal(event)
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
		batch, err = runtime.events.Read(streamContext, key, after)
		if err != nil {
			return
		}
	}
}

func localEventCursor(request *http.Request) (string, uint64, bool) {
	values := request.URL.Query()
	for key, entries := range values {
		if (key != "workspace_id" && key != "after" && key != "stream_id") || len(entries) != 1 {
			return "", 0, false
		}
	}
	workspaceID := values.Get("workspace_id")
	afterValues, lastIDs := values["after"], request.Header.Values("Last-Event-ID")
	if len(lastIDs) > 1 || (len(afterValues) == 1 && len(lastIDs) == 1 && lastIDs[0] != "") {
		return "", 0, false
	}
	raw := "0"
	if len(afterValues) == 1 {
		raw = afterValues[0]
	} else if len(lastIDs) == 1 && lastIDs[0] != "" {
		parts := strings.Split(lastIDs[0], ":")
		if len(parts) != 2 || values.Get("stream_id") != "" {
			return "", 0, false
		}
		values.Set("stream_id", parts[0])
		request.URL.RawQuery = values.Encode()
		raw = parts[1]
	}
	after, err := strconv.ParseUint(raw, 10, 64)
	identity := values.Get("stream_id")
	if identity != "" {
		if decoded, err := hex.DecodeString(identity); err != nil || len(decoded) != 16 || identity != strings.ToLower(identity) {
			return "", 0, false
		}
	}
	return workspaceID, after, err == nil && (after == 0 || identity != "")
}

func localWebSocketOrigin(values []string) bool {
	if len(values) != 1 {
		return false
	}
	origin, err := url.Parse(values[0])
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.User != nil ||
		origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return false
	}
	return origin.Hostname() == "127.0.0.1" || origin.Hostname() == "::1" || origin.Hostname() == "localhost"
}

func localHeaderTokenContains(value, expected string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.TrimSpace(token) == expected {
			return true
		}
	}
	return false
}
