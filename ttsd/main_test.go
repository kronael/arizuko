package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHealthForwardsBackendUp asserts /health returns 200 when the
// backend's /health is reachable.
func TestHealthForwardsBackendUp(t *testing.T) {
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer be.Close()

	srv := httptest.NewServer(healthHandler(be.URL))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
}

// TestHealthForwardsBackendDown asserts /health returns 503 when the
// backend is unreachable.
func TestHealthForwardsBackendDown(t *testing.T) {
	srv := httptest.NewServer(healthHandler("http://127.0.0.1:1")) // nothing listens
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

// TestProxyForwardsSpeech asserts /v1/audio/speech proxies through to
// the backend, preserves request body + headers, and pipes the response.
func TestProxyForwardsSpeech(t *testing.T) {
	var gotPath, gotMethod, gotCT string
	var gotBody []byte
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "audio/ogg")
		w.Write([]byte("OggS-fake"))
	}))
	defer be.Close()
	beURL, _ := url.Parse(be.URL)

	srv := httptest.NewServer(proxyHandler(beURL))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/audio/speech", "application/json",
		strings.NewReader(`{"model":"kokoro","voice":"af_bella","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "OggS-fake" {
		t.Errorf("body = %q, want OggS-fake", body)
	}
	if gotMethod != "POST" || gotPath != "/v1/audio/speech" || gotCT != "application/json" {
		t.Errorf("backend got %s %s ct=%q (body=%s)", gotMethod, gotPath, gotCT, gotBody)
	}
	if !strings.Contains(string(gotBody), "af_bella") {
		t.Errorf("backend body did not include voice: %s", gotBody)
	}
}

// backendUp answers one question: is the backend listening and not erroring.
// Any non-5xx reply to GET /health settles it — including a 404, which means
// "no such endpoint" on a backend that is plainly alive. The HEAD fallback is
// therefore NOT for backends without /health (those already answer 404); it is
// for a backend whose /health itself 5xxs while the service still serves.
func TestBackendUpProbesHealthThenFallsBackToHEAD(t *testing.T) {
	client := &http.Client{Timeout: 2 * time.Second}

	// probe records which paths and methods were tried, so each case proves
	// WHICH probe decided it rather than only the verdict.
	cases := []struct {
		name     string
		health   int
		root     int
		want     bool
		wantHEAD bool
	}{
		{"health 200", http.StatusOK, http.StatusOK, true, false},
		{"health 404 — listening, no such endpoint", http.StatusNotFound, http.StatusInternalServerError, true, false},
		{"health 5xx, root serves", http.StatusInternalServerError, http.StatusOK, true, true},
		{"health 5xx, root 5xx", http.StatusInternalServerError, http.StatusInternalServerError, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sawHEAD bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "HEAD" {
					sawHEAD = true
					w.WriteHeader(c.root)
					return
				}
				if r.URL.Path == "/health" {
					w.WriteHeader(c.health)
					return
				}
				w.WriteHeader(c.root)
			}))
			defer srv.Close()

			if got := backendUp(client, srv.URL); got != c.want {
				t.Errorf("backendUp = %v, want %v", got, c.want)
			}
			if sawHEAD != c.wantHEAD {
				t.Errorf("HEAD fallback fired = %v, want %v", sawHEAD, c.wantHEAD)
			}
		})
	}

	if backendUp(client, "http://127.0.0.1:1") {
		t.Error("an unreachable backend read as up")
	}

	// A trailing slash must not produce //health, which some backends 404 —
	// and here that would still read as up, hiding the malformed probe.
	var probed string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probed = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if !backendUp(client, srv.URL+"/") {
		t.Error("a backend URL with a trailing slash read as down")
	}
	if probed != "/health" {
		t.Errorf("probed %q, want /health from a trailing-slash backend URL", probed)
	}
}

// The proxy's ErrorHandler turns an unreachable backend into a 502 with a
// readable body. Without it httputil answers a bare 502 and the operator sees
// nothing about which side failed.
func TestProxyAnswers502WhenTheBackendIsGone(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	beURL, err := url.Parse(dead.URL)
	if err != nil {
		t.Fatal(err)
	}
	dead.Close()

	rec := httptest.NewRecorder()
	proxyHandler(beURL).ServeHTTP(rec, httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader("{}")))

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "backend unreachable") {
		t.Errorf("body = %q, want it to name the backend", rec.Body.String())
	}
}

// main serves and shuts down on SIGTERM. CLAUDE.md requires graceful shutdown
// on SIGINT/SIGTERM for every daemon, and nothing was proving ttsd does it: a
// main that ignored the signal would be reaped by the container runtime's
// SIGKILL after its grace period, mid-response.
func TestMainServesAndShutsDownOnSIGTERM(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	lis.Close() // free the port; main binds it

	t.Setenv("TTSD_ADDR", addr)
	t.Setenv("TTS_BACKEND_URL", backend.URL)
	t.Setenv("ARIZUKO_INSTANCE", "test")

	done := make(chan struct{})
	go func() { main(); close(done) }()

	client := &http.Client{Timeout: 2 * time.Second}
	var up bool
	for i := 0; i < 100; i++ {
		resp, err := client.Get("http://" + addr + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				up = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !up {
		t.Fatalf("ttsd never answered /health on %s", addr)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("main did not return on SIGTERM; the container runtime would SIGKILL it")
	}

	// The listener is closed, so the port refuses.
	if resp, err := client.Get("http://" + addr + "/health"); err == nil {
		resp.Body.Close()
		t.Error("the server still answers after shutdown")
	}
}
