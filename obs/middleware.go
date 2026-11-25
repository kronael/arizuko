package obs

// HTTP middleware: one wrapper that emits per-request metrics and a
// cross_daemon span. Mount it as the
// outermost handler on every HTTP-serving daemon. When metrics + traces are
// both off it still runs but does only a status-capturing wrap — microseconds,
// and arizuko is not latency-sensitive.

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// HTTPMiddleware wraps a daemon's mux to record arizuko_requests_total +
// arizuko_request_duration_seconds and open a cross_daemon span joined to the
// inbound trace. daemon names the metric/span owner. Mount BEFORE auth so the
// public /metrics and /health are measured too (they're cheap).
func HTTPMiddleware(daemon string) func(http.Handler) http.Handler {
	return httpMiddleware(daemon, false)
}

// PublicHTTPMiddleware starts a new trace for requests at an untrusted boundary.
func PublicHTTPMiddleware(daemon string) func(http.Handler) http.Handler {
	return httpMiddleware(daemon, true)
}

func httpMiddleware(daemon string, public bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ctx := r.Context()
			if public {
				ctx = trace.ContextWithSpanContext(ctx, trace.SpanContext{})
			} else {
				ctx = ExtractRequest(r)
			}
			ctx, end := StartSpan(ctx, "cross_daemon",
				"target", daemon, "method", r.Method, "path", r.URL.Path)
			r2 := r.WithContext(ctx)
			if public {
				r2.Header = r.Header.Clone()
				r2.Header.Del("traceparent")
				r2.Header.Del("tracestate")
			}
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r2)

			end(nil)
			// r2.Pattern is set by the mux during routing; fall back to a single
			// "unmatched" bucket on a 404 so unbounded paths can't explode the
			// label set.
			path := r2.Pattern
			if path == "" {
				path = "unmatched"
			}
			RecordRequest(daemon, r.Method, strconv.Itoa(sw.status), path,
				time.Since(start).Seconds())
		})
	}
}

// statusWriter captures the response status code for the requests_total label.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader && (code >= 200 || code == http.StatusSwitchingProtocols) {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

// Flush + Hijack forward to the wrapped writer so SSE (Flusher) and websocket
// upgrades (Hijacker) keep working through the middleware. A plain
// ResponseWriter that lacks them yields ErrNotSupported, exactly as if
// unwrapped.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		if !w.wroteHeader {
			w.WriteHeader(http.StatusOK)
		}
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}
