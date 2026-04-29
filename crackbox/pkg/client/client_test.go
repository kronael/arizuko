package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientAttachesAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		// Drain body so the HTTP/1.1 connection can be reused, not strictly required.
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, "s3cr3t")
	if err := c.Register("10.99.0.5", "x", []string{"github.com"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if want := "Bearer s3cr3t"; gotAuth != want {
		t.Errorf("auth header: got %q want %q", gotAuth, want)
	}

	gotAuth = ""
	if err := c.Unregister("10.99.0.5"); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if want := "Bearer s3cr3t"; gotAuth != want {
		t.Errorf("auth header on unregister: got %q want %q", gotAuth, want)
	}
}

func TestClientNoSecretSendsNoHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	if err := c.Register("10.99.0.5", "x", nil); err != nil {
		t.Fatalf("register: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("expected no Authorization header, got %q", gotAuth)
	}
}

// Sanity: state and health stay GET + no auth (the existing api_test
// covers server-side; this just makes sure the client constructor's
// secret doesn't accidentally leak onto reads).
func TestClientStateNoAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("state should not send auth, got %q", got)
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/state") {
			t.Errorf("path: %s", r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := New(srv.URL, "s3cr3t")
	if _, err := c.State(); err != nil {
		t.Fatalf("state: %v", err)
	}
}
