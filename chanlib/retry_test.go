package chanlib

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// shrink backoffs and cap to keep tests fast.
func shortBackoffs(t *testing.T) {
	t.Helper()
	prev := retryBackoffs
	prevCap := retryMaxRetryAfter
	retryBackoffs = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}
	retryMaxRetryAfter = 2 * time.Second
	t.Cleanup(func() {
		retryBackoffs = prev
		retryMaxRetryAfter = prevCap
	})
}

func TestDoWithRetry_503ThenOK(t *testing.T) {
	shortBackoffs(t)
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := DoWithRetry(srv.Client(), req)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("got %d want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("got %d requests want 2", got)
	}
}

func TestDoWithRetry_429WithRetryAfter(t *testing.T) {
	shortBackoffs(t)
	var n int32
	var firstAt, secondAt time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := atomic.AddInt32(&n, 1)
		if c == 1 {
			firstAt = time.Now()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		secondAt = time.Now()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := DoWithRetry(srv.Client(), req)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("got %d want 200", resp.StatusCode)
	}
	if secondAt.Sub(firstAt) < 800*time.Millisecond {
		t.Fatalf("did not honor Retry-After: waited %v", secondAt.Sub(firstAt))
	}
}

func TestDoWithRetry_500Exhausted(t *testing.T) {
	shortBackoffs(t)
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(500)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := DoWithRetry(srv.Client(), req)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Fatalf("got %d want 500", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&n); got != 3 {
		t.Fatalf("got %d requests want 3", got)
	}
}

func TestDoWithRetry_400NoRetry(t *testing.T) {
	shortBackoffs(t)
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(400)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := DoWithRetry(srv.Client(), req)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("got %d want 400", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("got %d requests want 1", got)
	}
}

func TestDoWithRetry_NetworkErrorRetried(t *testing.T) {
	shortBackoffs(t)
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("hijack unsupported")
			}
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := DoWithRetry(srv.Client(), req)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("got %d want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&n); got < 2 {
		t.Fatalf("got %d requests want >=2", got)
	}
}

func TestDoWithRetry_BodyRewound(t *testing.T) {
	shortBackoffs(t)
	var bodies []string
	var mu = make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu <- struct{}{}
		bodies = append(bodies, string(b))
		<-mu
		if len(bodies) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL, bytes.NewReader([]byte("hello")))
	resp, err := DoWithRetry(srv.Client(), req)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("got %d want 200", resp.StatusCode)
	}
	if len(bodies) != 2 || bodies[0] != "hello" || bodies[1] != "hello" {
		t.Fatalf("bodies = %v; want both 'hello'", bodies)
	}
}

// sanity: strings.NewReader body wraps cleanly.
func TestDoWithRetry_StringsBody(t *testing.T) {
	shortBackoffs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader("x"))
	resp, err := DoWithRetry(srv.Client(), req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
