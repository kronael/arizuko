package obs

// HTTP middleware: one wrapper that emits per-request metrics and a
// cross_daemon span, and joins any inbound traceparent. Mount it as the
// outermost handler on every HTTP-serving daemon. When metrics + traces are
// both off it still runs but does only a status-capturing wrap — microseconds,
// and arizuko is not latency-sensitive.

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"time"
)

// HTTPMiddleware wraps a daemon's mux to record arizuko_requests_total +
// arizuko_request_duration_seconds and open a cross_daemon span joined to the
// inbound trace. daemon names the metric/span owner. Mount BEFORE auth so the
// public /metrics and /health are measured too (they're cheap).
func HTTPMiddleware(daemon string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			// Join the caller's trace (signed sibling hops carry traceparent;
			// trust-boundary ingress carries none → fresh root). WithContext
			// returns a NEW request; the mux stamps Pattern on THAT one, so read
			// it back from r2, not the original r.
			ctx, end := StartSpan(ExtractRequest(r), "cross_daemon",
				"target", daemon, "method", r.Method, "path", r.URL.Path)
			r2 := r.WithContext(ctx)
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

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
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
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}
