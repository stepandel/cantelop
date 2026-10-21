package sandboxhttp

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestEndpointHeadersRejectPlatformAndHopHeaders(t *testing.T) {
	for _, name := range []string{"Host", "X-Cantelop-App-ID", "X-Forwarded-For", "Connection", "Upgrade"} {
		if _, err := EndpointHeaders(http.Header{name: {"value"}}); err == nil {
			t.Errorf("EndpointHeaders accepted %q", name)
		}
	}
	headers, err := EndpointHeaders(http.Header{"X-Provider-Authorization": {"secret"}})
	if err != nil || headers.Get("X-Provider-Authorization") != "secret" {
		t.Fatalf("provider headers = %v, %v", headers, err)
	}
}

func TestTransportIgnoresProxyEnvironment(t *testing.T) {
	if transport := NewTransport(); transport.Proxy != nil {
		t.Fatal("Sandbox transport inherited an HTTP proxy")
	}
}

func TestRetryingDialContextWaitsForRuntimeListener(t *testing.T) {
	var attempts atomic.Int32
	server, client := net.Pipe()
	defer server.Close()
	dial := retryingDialContext(func(context.Context, string, string) (net.Conn, error) {
		if attempts.Add(1) < 3 {
			return nil, syscall.ECONNREFUSED
		}
		return client, nil
	})
	connection, err := dial(context.Background(), "tcp", "172.16.0.2:3000")
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
}
