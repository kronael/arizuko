package obs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestHTTPMiddlewareTraceBoundary(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	oldTracer, oldTracesOn := tracer, tracesOn
	tracer, tracesOn = provider.Tracer("test"), true
	t.Cleanup(func() { tracer, tracesOn = oldTracer, oldTracesOn })

	headerParent := WithTurn(context.Background(), "test", "header")
	contextParent := WithTurn(context.Background(), "test", "context")
	for _, tc := range []struct {
		name   string
		public bool
	}{
		{"public_header", true},
		{"public_context", true},
		{"internal_header", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got trace.SpanContext
			var forwarded string
			middleware := HTTPMiddleware
			if tc.public {
				middleware = PublicHTTPMiddleware
			}
			h := middleware("testd")(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = trace.SpanContextFromContext(r.Context())
				forwarded = r.Header.Get("traceparent")
			}))
			req := httptest.NewRequest("GET", "/", nil)
			parent := headerParent
			if tc.name == "public_context" {
				parent = contextParent
				req = req.WithContext(parent)
			} else {
				InjectRequest(parent, req)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			parentID := trace.SpanContextFromContext(parent).TraceID()
			if !got.IsValid() || (got.TraceID() == parentID) == tc.public {
				t.Errorf("trace ID = %s, parent = %s, public = %t", got.TraceID(), parentID, tc.public)
			}
			if tc.name == "public_header" && forwarded != "" {
				t.Errorf("public traceparent forwarded: %s", forwarded)
			}
		})
	}
}

func TestStatusWriterFlushCommitsStatus(t *testing.T) {
	for _, status := range []int{0, http.StatusTeapot} {
		rec := httptest.NewRecorder()
		writer := &statusWriter{ResponseWriter: rec, status: http.StatusOK}
		if status != 0 {
			writer.WriteHeader(status)
		}
		writer.Flush()
		writer.WriteHeader(http.StatusInternalServerError)
		if writer.status != rec.Code {
			t.Errorf("recorded status = %d, response status = %d", writer.status, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	writer := &statusWriter{ResponseWriter: struct{ http.ResponseWriter }{rec}, status: http.StatusOK}
	writer.Flush()
	writer.WriteHeader(http.StatusTeapot)
	if writer.status != http.StatusTeapot || rec.Code != http.StatusTeapot {
		t.Errorf("unsupported flush committed status: recorded=%d response=%d", writer.status, rec.Code)
	}
}

func TestHTTPMiddleware_RecordsRequest(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "true")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := HTTPMiddleware("testd")(mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/ping", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", rec.Code)
	}

	scrape := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(scrape, httptest.NewRequest("GET", "/metrics", nil))
	body := scrape.Body.String()
	// The matched route pattern, not the raw path, bounds cardinality.
	if !strings.Contains(body, `daemon="testd"`) || !strings.Contains(body, `status="418"`) {
		t.Errorf("requests_total missing testd/418 labels:\n%s", body)
	}
	if !strings.Contains(body, "/v1/ping") {
		t.Errorf("request_duration missing route pattern label")
	}
}

func TestHTTPMiddleware_UnmatchedPath(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "true")
	mux := http.NewServeMux() // no routes → 404, empty r.Pattern
	h := HTTPMiddleware("testd")(mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/whatever/123", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	scrape := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(scrape, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(scrape.Body.String(), `path="unmatched"`) {
		t.Error("unmatched path not bucketed into 'unmatched'")
	}
}

type deadlineWriter struct {
	http.ResponseWriter
	deadline time.Time
}

func (w *deadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestStatusWriterUnwrapsDeadline(t *testing.T) {
	underlying := &deadlineWriter{ResponseWriter: httptest.NewRecorder()}
	writer := &statusWriter{ResponseWriter: underlying}
	deadline := time.Now().Add(time.Second)
	if err := http.NewResponseController(writer).SetWriteDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if !underlying.deadline.Equal(deadline) {
		t.Fatal("deadline did not reach underlying writer")
	}
}

type headerWriter struct {
	http.ResponseWriter
	codes []int
}

func (w *headerWriter) WriteHeader(code int) { w.codes = append(w.codes, code) }

func TestStatusWriterInformationalHeaders(t *testing.T) {
	for _, info := range []int{http.StatusContinue, http.StatusEarlyHints} {
		underlying := &headerWriter{ResponseWriter: httptest.NewRecorder()}
		writer := &statusWriter{ResponseWriter: underlying, status: http.StatusOK}
		writer.WriteHeader(info)
		writer.WriteHeader(http.StatusCreated)
		if len(underlying.codes) != 2 || underlying.codes[0] != info || underlying.codes[1] != http.StatusCreated {
			t.Errorf("forwarded statuses = %v", underlying.codes)
		}
		if writer.status != http.StatusCreated {
			t.Errorf("%d followed by 201 recorded %d", info, writer.status)
		}
	}
	underlying := &headerWriter{ResponseWriter: httptest.NewRecorder()}
	writer := &statusWriter{ResponseWriter: underlying, status: http.StatusOK}
	writer.WriteHeader(http.StatusSwitchingProtocols)
	writer.WriteHeader(http.StatusCreated)
	if writer.status != http.StatusSwitchingProtocols {
		t.Errorf("101 recorded %d", writer.status)
	}
}
