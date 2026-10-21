package httpapi

import (
	"net/http"

	"github.com/stepandel/cantelop/fire-fuse/runtime/runtimeprovider"
)

// Test-only convenience constructors. Production wires the complete API through
// NewHandlerWithRuntimeProviderAndReadinessAndLogger.

// NewHandler builds an API without a readiness check. Its readiness endpoint
// reports unavailable until a check is supplied with NewHandlerWithReadiness.
func NewHandler(controlPlane any) http.Handler {
	return NewHandlerWithRuntimeProviderAndReadinessAndLogger(controlPlane, nil, nil, nil)
}

// NewHandlerWithRuntimeProvider builds an API with a provider-neutral Session service.
func NewHandlerWithRuntimeProvider(controlPlane any, runtimes runtimeprovider.Provider) http.Handler {
	return NewHandlerWithRuntimeProviderAndReadinessAndLogger(controlPlane, runtimes, nil, nil)
}

// NewHandlerWithReadiness builds the HTTP API with a dependency check.
func NewHandlerWithReadiness(controlPlane any, ready ReadinessCheck) http.Handler {
	return NewHandlerWithRuntimeProviderAndReadinessAndLogger(controlPlane, nil, ready, nil)
}
