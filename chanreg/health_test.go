package chanreg

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckAll_HealthyAdapter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	r := New("s")
	r.Register("tg", srv.URL, []string{"tg:"}, nil)

	// Override the package-level health client so it hits our test server
	old := healthClient
	healthClient = srv.Client()
	defer func() { healthClient = old }()

	r.checkAll()

	e := r.Get("tg")
	if e == nil {
		t.Fatal("adapter should still be registered")
	}
	if e.HealthFails != 0 {
		t.Errorf("health fails = %d, want 0", e.HealthFails)
	}
}

func TestCheckAll_BadURL_DeregistersAfterMaxFails(t *testing.T) {
	// Server that always returns 500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	r := New("s")
	r.Register("bad", srv.URL, []string{"bad:"}, nil)

	old := healthClient
	healthClient = srv.Client()
	defer func() { healthClient = old }()

	// First check: fails=1
	r.checkAll()
	e := r.Get("bad")
	if e == nil {
		t.Fatal("adapter should still be registered after 1 fail")
	}
	if e.HealthFails != 1 {
		t.Errorf("health fails = %d, want 1", e.HealthFails)
	}

	// Second check: fails=2
	r.checkAll()
	e = r.Get("bad")
	if e == nil {
		t.Fatal("adapter should still be registered after 2 fails")
	}
	if e.HealthFails != 2 {
		t.Errorf("health fails = %d, want 2", e.HealthFails)
	}

	// Third check: fails=3 → deregistered
	r.checkAll()
	e = r.Get("bad")
	if e != nil {
		t.Errorf("adapter should be deregistered after %d fails", maxHealthFails)
	}
}

func TestCheckAll_FailThenRecover(t *testing.T) {
	healthy := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if healthy {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()

	r := New("s")
	r.Register("flaky", srv.URL, []string{"flaky:"}, nil)

	old := healthClient
	healthClient = srv.Client()
	defer func() { healthClient = old }()

	// Fail twice
	healthy = false
	r.checkAll()
	r.checkAll()
	e := r.Get("flaky")
	if e.HealthFails != 2 {
		t.Fatalf("health fails = %d, want 2", e.HealthFails)
	}

	// Recover — health succeeds, counter resets
	healthy = true
	r.checkAll()
	e = r.Get("flaky")
	if e == nil {
		t.Fatal("adapter should still be registered")
	}
	if e.HealthFails != 0 {
		t.Errorf("health fails = %d after recovery, want 0", e.HealthFails)
	}
}

func TestCheckAll_UnreachableURL(t *testing.T) {
	r := New("s")
	r.Register("dead", "http://127.0.0.1:1", []string{"dead:"}, nil)

	// Use default client — connection to port 1 will fail
	for i := 0; i < maxHealthFails; i++ {
		r.checkAll()
	}

	e := r.Get("dead")
	if e != nil {
		t.Error("adapter should be deregistered after max fails with unreachable URL")
	}
}

func TestCheckAll_EmptyRegistry(t *testing.T) {
	r := New("s")
	// Should not panic on empty registry
	r.checkAll()
}

func TestHealthPing_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	err := healthPing(srv.Client(), srv.URL)
	if err != nil {
		t.Errorf("healthPing returned error for healthy server: %v", err)
	}
}

func TestHealthPing_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()

	err := healthPing(srv.Client(), srv.URL)
	if err == nil {
		t.Error("healthPing should return error for non-200 status")
	}
}
