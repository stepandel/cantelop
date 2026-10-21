package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"sync/atomic"
	"time"
)

const RequestIDHeader = "X-Request-ID"

var (
	requestIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	requestIDFallback atomic.Uint64
)

type requestIDContextKey struct{}

// RequestID returns the validated request identifier stored by the HTTP
// middleware, or an empty string when the context did not originate there.
func RequestID(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(RequestIDHeader)
		if !requestIDPattern.MatchString(requestID) {
			requestID = newRequestID()
		}
		w.Header().Set(RequestIDHeader, requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newRequestID() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err == nil {
		return "req-" + hex.EncodeToString(random)
	}
	// The system CSPRNG failing should not leave a request untraceable. Time plus
	// a process-local sequence is a valid, non-secret emergency correlation ID.
	return fmt.Sprintf(
		"req-fallback-%x-%x",
		time.Now().UTC().UnixNano(),
		requestIDFallback.Add(1),
	)
}
