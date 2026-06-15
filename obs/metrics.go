package obs

// Prometheus metrics for the nine families in spec 5/O. Recording is always
// safe: when METRICS_ENABLED is unset the collectors still exist but nothing
// scrapes them, so Record* calls are cheap atomic adds with no exporter cost.
// MetricsHandler returns the promhttp endpoint when enabled, a 404 stub when
// not — so a daemon can mount GET /metrics unconditionally.

import (
	"net/http"
	"os"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Bucket sets from spec 5/O § Histogram buckets.
var (
	bucketsTurn  = []float64{0.1, 0.5, 1, 5, 10, 30, 60, 120, 300, 600}
	bucketsModel = []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30}
	bucketsHTTP  = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5}
)

var (
	turnDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "arizuko_turn_duration_seconds",
		Help:    "Turn latency from claim to completion.",
		Buckets: bucketsTurn,
	}, []string{"folder", "outcome"})

	turnsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "arizuko_turns_total",
		Help: "Total turns processed.",
	}, []string{"folder", "outcome"})

	modelCallDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "arizuko_model_call_duration_seconds",
		Help:    "Anthropic API call latency.",
		Buckets: bucketsModel,
	}, []string{"model", "folder"})

	modelTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "arizuko_model_tokens_total",
		Help: "Tokens used (in/out/cache_read/cache_write).",
	}, []string{"model", "folder", "direction"})

	containerSpawns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "arizuko_container_spawns_total",
		Help: "Container spawn attempts.",
	}, []string{"folder", "outcome"})

	containerActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "arizuko_container_active",
		Help: "Currently running containers.",
	})

	containerDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "arizuko_container_duration_seconds",
		Help:    "Container run time.",
		Buckets: bucketsTurn,
	}, []string{"folder", "outcome"})

	requestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "arizuko_requests_total",
		Help: "HTTP requests by status code.",
	}, []string{"daemon", "method", "status"})

	requestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "arizuko_request_duration_seconds",
		Help:    "Request latency.",
		Buckets: bucketsHTTP,
	}, []string{"daemon", "method", "path"})

	circuitBreakerState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "arizuko_circuit_breaker_state",
		Help: "0=closed, 1=half-open, 2=open.",
	}, []string{"folder"})

	egressRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "arizuko_egress_requests_total",
		Help: "Proxied egress requests.",
	}, []string{"folder", "host", "status"})

	egressBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "arizuko_egress_bytes_total",
		Help: "Bytes proxied (in/out).",
	}, []string{"folder", "direction"})

	tokenMints = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "arizuko_token_mints_total",
		Help: "Tokens minted (access/refresh).",
	}, []string{"type"})

	tokenRefreshes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "arizuko_token_refreshes_total",
		Help: "Refresh attempts (success/revoked/expired).",
	}, []string{"outcome"})
)

// registry holds every collector. Built once, reused by MetricsHandler.
var (
	registry     *prometheus.Registry
	registryOnce sync.Once
)

func reg() *prometheus.Registry {
	registryOnce.Do(func() {
		registry = prometheus.NewRegistry()
		registry.MustRegister(
			turnDuration, turnsTotal,
			modelCallDuration, modelTokens,
			containerSpawns, containerActive, containerDuration,
			requestsTotal, requestDuration,
			circuitBreakerState,
			egressRequests, egressBytes,
			tokenMints, tokenRefreshes,
		)
	})
	return registry
}

// MetricsEnabled reports whether METRICS_ENABLED=true. Daemons gate the
// GET /metrics mount on it; recording helpers stay cheap when off.
func MetricsEnabled() bool {
	return os.Getenv("METRICS_ENABLED") == "true"
}

// MetricsHandler returns the Prometheus scrape endpoint when METRICS_ENABLED is
// true, else a 404 stub. Mount it BEFORE auth middleware (public endpoint).
func MetricsHandler() http.Handler {
	if !MetricsEnabled() {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "metrics disabled", http.StatusNotFound)
		})
	}
	return promhttp.HandlerFor(reg(), promhttp.HandlerOpts{})
}

// --- Recording helpers. One per instrumentation concern. ---

// RecordTurn records a turn's duration + count under (folder, outcome).
func RecordTurn(folder, outcome string, seconds float64) {
	turnDuration.WithLabelValues(folder, outcome).Observe(seconds)
	turnsTotal.WithLabelValues(folder, outcome).Inc()
}

// RecordModelCall records Anthropic call latency.
func RecordModelCall(model, folder string, seconds float64) {
	modelCallDuration.WithLabelValues(model, folder).Observe(seconds)
}

// RecordModelTokens adds n tokens for (model, folder, direction). direction is
// one of in/out/cache_read/cache_write.
func RecordModelTokens(model, folder, direction string, n int) {
	if n <= 0 {
		return
	}
	modelTokens.WithLabelValues(model, folder, direction).Add(float64(n))
}

// RecordContainerSpawn records a spawn attempt + its run time.
func RecordContainerSpawn(folder, outcome string, seconds float64) {
	containerSpawns.WithLabelValues(folder, outcome).Inc()
	containerDuration.WithLabelValues(folder, outcome).Observe(seconds)
}

// ContainerActiveInc / ContainerActiveDec bracket a live container.
func ContainerActiveInc() { containerActive.Inc() }
func ContainerActiveDec() { containerActive.Dec() }

// RecordRequest records an HTTP request's count + latency. Called by the
// HTTPMiddleware; path is the normalized route pattern, not the full URL.
func RecordRequest(daemon, method, status, path string, seconds float64) {
	requestsTotal.WithLabelValues(daemon, method, status).Inc()
	requestDuration.WithLabelValues(daemon, method, path).Observe(seconds)
}

// SetCircuitBreakerState sets the breaker gauge for a folder (0/1/2).
func SetCircuitBreakerState(folder string, state int) {
	circuitBreakerState.WithLabelValues(folder).Set(float64(state))
}

// RecordEgressRequest records a proxied egress request.
func RecordEgressRequest(folder, host, status string) {
	egressRequests.WithLabelValues(folder, host, status).Inc()
}

// RecordEgressBytes adds proxied bytes for (folder, direction in/out).
func RecordEgressBytes(folder, direction string, n int64) {
	if n <= 0 {
		return
	}
	egressBytes.WithLabelValues(folder, direction).Add(float64(n))
}

// RecordTokenMint counts a minted token by type (access/refresh).
func RecordTokenMint(typ string) {
	tokenMints.WithLabelValues(typ).Inc()
}

// RecordTokenRefresh counts a refresh attempt by outcome
// (success/revoked/expired/invalid).
func RecordTokenRefresh(outcome string) {
	tokenRefreshes.WithLabelValues(outcome).Inc()
}
