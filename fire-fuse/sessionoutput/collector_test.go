package sessionoutput

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

func TestCollectorContinuouslyDrainsAcknowledgementBlockedOutput(t *testing.T) {
	const sandboxID = "sbx-22222222222222222222222222222222"
	const messageID = "msg_11111111111111111111111111111111"
	var mu sync.Mutex
	acknowledged := uint64(0)
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		status := http.StatusOK
		body := ""
		switch {
		case r.Method == http.MethodGet && r.URL.Path == messageprotocol.RuntimePath:
			body = fmt.Sprintf(`{"sandbox_id":%q,"protocol":2,"generation":1,"quiescent":false,"message_work":null,"activity":null,"observations":{"acknowledged":0,"earliest":1,"latest":0},"events":{"acknowledged":%d,"earliest":%d,"latest":%d}}`, sandboxID, acknowledged, acknowledged+1, acknowledged+1)
		case r.Method == http.MethodGet && r.URL.Path == messageprotocol.OutputPath:
			after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
			if after != acknowledged {
				return nil, fmt.Errorf("read after %d while acknowledged at %d", after, acknowledged)
			}
			body = fmt.Sprintf(`{"events":[{"cursor":%d,"message_id":%q,"event":{"delta":%d}}]}`, after+1, messageID, after+1)
		case r.Method == http.MethodPost && r.URL.Path == messageprotocol.OutputPath+"/ack":
			var body struct {
				Through uint64 `json:"through"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			acknowledged = body.Through
			status = http.StatusNoContent
		default:
			status = http.StatusNotFound
		}
		headers := make(http.Header)
		headers.Set(messageprotocol.SandboxHeader, sandboxID)
		headers.Set(messageprotocol.ProtocolHeader, "2")
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	route := session.Route{SandboxID: sandboxID, Endpoint: session.Endpoint{Scheme: "http", Authority: "sandbox.invalid"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	arrivals := make(chan time.Time, 5)
	done := make(chan error, 1)
	go func() {
		count := 0
		done <- NewCollector(transport).Collect(ctx, route, func(messageprotocol.OutputEvent) error {
			arrivals <- time.Now()
			count++
			if count == cap(arrivals) {
				cancel()
			}
			return nil
		})
	}()
	started := time.Now()
	for range cap(arrivals) {
		select {
		case <-arrivals:
		case <-time.After(time.Second):
			t.Fatal("continuous Session output collection stalled")
		}
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("five acknowledgement-blocked events took %s", elapsed)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("collector stopped with %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
