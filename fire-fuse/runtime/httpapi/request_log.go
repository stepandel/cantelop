package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

type requestLogResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *requestLogResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *requestLogResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// Unwrap lets http.ResponseController reach optional capabilities implemented
// by the underlying writer without exposing them through this wrapper.
func (w *requestLogResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func withRequestLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		loggedWriter := &requestLogResponseWriter{ResponseWriter: w}

		next.ServeHTTP(loggedWriter, r)

		status := loggedWriter.status
		if status == 0 {
			status = http.StatusOK
		}
		attributes := []any{
			"request_id", RequestID(r.Context()),
			"method", r.Method,
			"route", requestRoute(r),
			"status", status,
			"duration", time.Since(started),
		}
		if resourceID := r.PathValue("id"); requestIDPattern.MatchString(resourceID) {
			attributes = append(attributes, "resource_id", resourceID)
		}
		if class := httpErrorClass(status); class != "" {
			attributes = append(attributes, "error_class", class)
		}
		logger.InfoContext(r.Context(), "HTTP request completed", attributes...)
		if trace := warmuptrace.FromContext(r.Context()); trace != nil {
			snapshot := trace.Snapshot()
			logger.InfoContext(
				r.Context(),
				"sandbox warmup trace completed",
				"request_id", RequestID(r.Context()),
				"sandbox_id", snapshot.MachineID,
				"status", loggedWriter.status,
				"total_ns", snapshot.TotalNS,
				"steps", snapshot.Steps,
			)
		}
	})
}

func requestRoute(r *http.Request) string {
	if r.Pattern == "" {
		return "unmatched"
	}
	return r.Pattern
}

func httpErrorClass(status int) string {
	switch {
	case status >= 500:
		return "server_error"
	case status >= 400:
		return "client_error"
	default:
		return ""
	}
}
