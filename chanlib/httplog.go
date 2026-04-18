package chanlib

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// StatusWriter wraps http.ResponseWriter to capture the response status code,
// delegating Flush (SSE) and Hijack (websockets) to the underlying writer.
type StatusWriter struct {
	http.ResponseWriter
	Code int
}

func (sw *StatusWriter) WriteHeader(code int) {
	sw.Code = code
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *StatusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (sw *StatusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := sw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("ResponseWriter does not implement http.Hijacker")
	}
	return h.Hijack()
}

// LogMiddleware logs method, path, status, duration, and X-User-Sub per request.
// Edge daemons that need extra fields (peer, host) should implement their own.
func LogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &StatusWriter{ResponseWriter: w, Code: 200}
		next.ServeHTTP(sw, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", sw.Code, "dur", time.Since(start).String(),
			"sub", r.Header.Get("X-User-Sub"))
	})
}
