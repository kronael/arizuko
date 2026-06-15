package obs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsHandler_Disabled404(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "")
	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("disabled /metrics = %d, want 404", rec.Code)
	}
}

func TestMetricsHandler_EnabledExposesFamilies(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "true")

	// Record one sample per family so the collector emits a line.
	RecordTurn("solo/inbox", "success", 1.2)
	RecordModelCall("opus", "solo/inbox", 0.8)
	RecordModelTokens("opus", "solo/inbox", "in", 100)
	RecordContainerSpawn("solo/inbox", "success", 3.0)
	ContainerActiveInc()
	RecordRequest("routd", "POST", "200", "/v1/messages", 0.01)
	SetCircuitBreakerState("solo/inbox", 2)
	RecordEgressRequest("solo/inbox", "api.example.com", "200")
	RecordEgressBytes("solo/inbox", "out", 512)
	RecordTokenMint("access")
	RecordTokenRefresh("success")

	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("enabled /metrics = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	want := []string{
		"arizuko_turn_duration_seconds",
		"arizuko_turns_total",
		"arizuko_model_call_duration_seconds",
		"arizuko_model_tokens_total",
		"arizuko_container_spawns_total",
		"arizuko_container_active",
		"arizuko_container_duration_seconds",
		"arizuko_requests_total",
		"arizuko_request_duration_seconds",
		"arizuko_circuit_breaker_state",
		"arizuko_egress_requests_total",
		"arizuko_egress_bytes_total",
		"arizuko_token_mints_total",
		"arizuko_token_refreshes_total",
	}
	for _, name := range want {
		if !strings.Contains(body, name) {
			t.Errorf("/metrics missing family %q", name)
		}
	}
}

func TestRecordModelTokens_IgnoresNonPositive(t *testing.T) {
	// Should not panic or record; just exercises the guard.
	RecordModelTokens("opus", "f", "in", 0)
	RecordModelTokens("opus", "f", "in", -5)
	RecordEgressBytes("f", "in", 0)
}
