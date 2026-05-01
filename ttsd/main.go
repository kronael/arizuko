// ttsd is a thin OpenAI-compatible TTS proxy. By default it forwards
// /v1/audio/speech to a Kokoro-FastAPI container co-deployed in the
// arizuko compose graph (TTS_BACKEND_URL=http://kokoro:8880). Operators
// who run a different backend (Piper, Coqui, OpenAI cloud) override
// TTS_BACKEND_URL and skip the bundled Kokoro service.
//
// Why a wrapper at all: it pins arizuko's TTS contract to the OpenAI
// /v1/audio/speech shape and adds /health that returns 503 when the
// backend is down — matching the rest of the daemon healthcheck protocol.
//
// Deliberately lacks: auth (front it with proxyd if exposed), DB, admin
// UI, model management. Configuration via env only.
package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	addr := envOr("TTSD_ADDR", ":8880")
	backend := envOr("TTS_BACKEND_URL", "http://kokoro:8880")
	logLevel := envOr("LOG_LEVEL", "info")

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLevel(logLevel),
	})))

	beURL, err := url.Parse(backend)
	if err != nil {
		slog.Error("invalid TTS_BACKEND_URL", "url", backend, "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler(backend))
	mux.Handle("POST /v1/audio/speech", proxyHandler(beURL))
	// /v1/voices is convenient for `ttsd` to surface Kokoro's voice list.
	mux.Handle("GET /v1/voices", proxyHandler(beURL))

	srv := &http.Server{Addr: addr, Handler: mux, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second}
	go func() {
		slog.Info("ttsd listening", "addr", addr, "backend", backend)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	slog.Info("ttsd shut down")
}

// proxyHandler is a single-target reverse proxy with the same path
// (Director copies the request URL.Path verbatim into beURL).
func proxyHandler(beURL *url.URL) http.Handler {
	rp := httputil.NewSingleHostReverseProxy(beURL)
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		slog.Warn("backend proxy error", "err", err)
		http.Error(w, "tts backend unreachable", http.StatusBadGateway)
	}
	return rp
}

// healthHandler probes the backend's /health endpoint (Kokoro-FastAPI
// exposes one) and reports 503 if it's unreachable. Falls back to a
// HEAD probe when the backend lacks /health.
func healthHandler(backend string) http.HandlerFunc {
	client := &http.Client{Timeout: 3 * time.Second}
	return func(w http.ResponseWriter, _ *http.Request) {
		if backendUp(client, backend) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"status": "ok", "name": "ttsd"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{"status": "disconnected", "name": "ttsd"})
	}
}

func backendUp(client *http.Client, backend string) bool {
	resp, err := client.Get(strings.TrimRight(backend, "/") + "/health")
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 500 {
			return true
		}
	}
	// Some backends (raw Kokoro) don't expose /health; HEAD the root.
	req, err := http.NewRequest("HEAD", backend, nil)
	if err != nil {
		return false
	}
	resp, err = client.Do(req)
	if err != nil {
		return false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode < 500
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
