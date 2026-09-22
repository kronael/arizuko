package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A conforming component: openapi.json public, every advertised path mounted,
// everything but the public set gated.
func conformingComponent(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"openapi":"3.1.0","paths":{"/v1/widgets":{},"/health":{}}}`)) //nolint:errcheck // the test asserts on the response, not the write
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/widgets", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(mux)
}

func findResult(t *testing.T, rs []probeResult, name string) probeResult {
	t.Helper()
	for _, r := range rs {
		if r.name == name {
			return r
		}
	}
	t.Fatalf("no check named %q in %d results", name, len(rs))
	return probeResult{}
}

func TestProbePassesAConformingComponent(t *testing.T) {
	srv := conformingComponent(t)
	defer srv.Close()

	for _, r := range runProbe(srv.Client(), srv.URL, "", "") {
		if !r.ok {
			t.Errorf("conforming component failed %q: %s", r.name, r.why)
		}
	}
}

// The contract says openapi.json mounts BEFORE auth middleware. A component
// that gates it cannot be discovered at all, so this must fail loudly rather
// than report "no paths advertised".
func TestProbeCatchesGatedOpenAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	rs := runProbe(srv.Client(), srv.URL, "", "")
	got := findResult(t, rs, "openapi.json is public")
	if got.ok {
		t.Fatal("a 401 on openapi.json must fail the probe")
	}
}

// Drift in the direction the probe CAN see: the doc names a resource the mux
// does not serve. This is the failure that makes a published contract a lie.
func TestProbeCatchesAdvertisedButUnmounted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"openapi":"3.1.0","paths":{"/v1/ghosts":{}}}`)) //nolint:errcheck // the test asserts on the response, not the write
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rs := runProbe(srv.Client(), srv.URL, "", "")
	got := findResult(t, rs, "every advertised path is served")
	if got.ok {
		t.Fatal("a path in the doc that 404s must fail the probe")
	}
}

// The missing-gate case: a /v1 resource answering 200 with no credentials.
// This is the one a component author is most likely to ship by accident, and
// the reason the probe sends no Authorization header by default.
func TestProbeCatchesUngatedResource(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"openapi":"3.1.0","paths":{"/v1/secrets":{}}}`)) //nolint:errcheck // the test asserts on the response, not the write
	})
	mux.HandleFunc("/v1/secrets", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // no auth check at all
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rs := runProbe(srv.Client(), srv.URL, "", "")
	got := findResult(t, rs, "non-public paths require auth")
	if got.ok {
		t.Fatal("an unauthenticated 200 on /v1 must fail the probe")
	}
}

// /health and /metrics answering 200 unauthenticated is correct, not a gap.
// Without this the probe would fail every conforming daemon.
func TestProbeAllowsPublicHealth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"openapi":"3.1.0","paths":{"/health":{},"/metrics":{}}}`)) //nolint:errcheck // the test asserts on the response, not the write
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rs := runProbe(srv.Client(), srv.URL, "", "")
	got := findResult(t, rs, "non-public paths require auth")
	if !got.ok {
		t.Fatalf("health/metrics 200 must not be reported as an ungated resource: %s", got.why)
	}
}

// Containment: a token scoped to one folder naming another must be refused.
// The handler here honours the token but ignores the folder — the drift that
// cost a cross-tenant read (BUGS F33 family).
func TestProbeCatchesForeignFolderAccepted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"openapi":"3.1.0","paths":{"/v1/things":{}}}`)) //nolint:errcheck // the test asserts on the response, not the write
	})
	mux.HandleFunc("/v1/things", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK) // never binds ?folder= to the caller
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rs := runProbe(srv.Client(), srv.URL, "tok", "someone-elses-world")
	got := findResult(t, rs, "foreign folder is refused")
	if got.ok {
		t.Fatal("a handler that ignores ?folder= must fail the containment check")
	}
}

// dashd mounts only /dash/ pages and owns no /v1 resource, so it advertises an
// empty doc. The probe's first live run FAILED it; a component owes an honest
// doc, not a non-empty one. This pins the corrected reading.
func TestProbeAcceptsAComponentThatOwnsNoResource(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"openapi":"3.1.0","paths":{}}`)) //nolint:errcheck // the test asserts on the response, not the write
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, r := range runProbe(srv.Client(), srv.URL, "", "") {
		if !r.ok {
			t.Errorf("a UI-only component must pass, failed %q: %s", r.name, r.why)
		}
	}
}

func TestProbeReportsUnreachable(t *testing.T) {
	srv := conformingComponent(t)
	url := srv.URL
	srv.Close() // nothing listening now

	rs := runProbe(http.DefaultClient, url, "", "")
	if len(rs) != 1 || rs[0].ok {
		t.Fatalf("a dead component must produce exactly one failing check, got %+v", rs)
	}
}
