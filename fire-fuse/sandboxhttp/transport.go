// Package sandboxhttp owns the shared HTTP transport policy for private
// Sandbox runtime endpoints.
package sandboxhttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

const (
	defaultRuntimeReadyWaitTimeout   = 2 * time.Second
	defaultRuntimeReadyRetryInterval = 10 * time.Millisecond
)

// NewTransport returns a transport that never uses process proxy settings and
// briefly retries connection refusal while a newly activated runtime listens.
func NewTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dial := (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.DialContext = retryingDialContext(dial)
	return transport
}

func retryingDialContext(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		waitCtx, cancel := context.WithTimeout(ctx, defaultRuntimeReadyWaitTimeout)
		defer cancel()
		for {
			connection, err := dial(waitCtx, network, address)
			if err == nil || !errors.Is(err, syscall.ECONNREFUSED) {
				return connection, err
			}
			timer := time.NewTimer(defaultRuntimeReadyRetryInterval)
			select {
			case <-waitCtx.Done():
				timer.Stop()
				return nil, errors.Join(err, waitCtx.Err())
			case <-timer.C:
			}
		}
	}
}

// EndpointHeaders validates provider-owned endpoint headers before they cross
// the runtime trust boundary.
func EndpointHeaders(headers http.Header) (http.Header, error) {
	validated := make(http.Header, len(headers))
	for name, values := range headers {
		canonical := http.CanonicalHeaderKey(name)
		lower := strings.ToLower(canonical)
		if canonical == "" || lower == "host" || strings.HasPrefix(lower, "x-cantelop-") ||
			strings.HasPrefix(lower, "x-forwarded-") || lower == "connection" || lower == "upgrade" ||
			lower == "keep-alive" || lower == "proxy-authenticate" || lower == "proxy-authorization" ||
			lower == "te" || lower == "trailer" || lower == "transfer-encoding" {
			return nil, fmt.Errorf("runtime endpoint header %q is reserved", name)
		}
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") {
				return nil, fmt.Errorf("runtime endpoint header %q contains a line break", name)
			}
			validated.Add(canonical, value)
		}
	}
	return validated, nil
}
