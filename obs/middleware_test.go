package obs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
