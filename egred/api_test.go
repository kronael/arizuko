package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterUnregister(t *testing.T) {
	allow := NewAllowlist()
	api := NewAPI(allow)
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	body := `{"ip":"10.99.0.5","folder":"acme/eng","allowlist":["github.com","npmjs.org"]}`
	resp, err := http.Post(srv.URL+"/v1/register", "application/json", strings.NewReader(body))
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("register: %v %d", err, statusCodeOf(resp))
	}

	folder, list, ok := allow.Lookup("10.99.0.5")
	if !ok || folder != "acme/eng" || len(list) != 2 {
		t.Fatalf("lookup: %v %v %v", folder, list, ok)
	}

	resp, err = http.Get(srv.URL + "/v1/state")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(b, []byte(`"acme/eng"`)) {
		t.Errorf("state body missing folder: %s", b)
	}

	resp, err = http.Post(srv.URL+"/v1/unregister", "application/json",
		strings.NewReader(`{"ip":"10.99.0.5"}`))
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("unregister: %v %d", err, statusCodeOf(resp))
	}
	if _, _, ok := allow.Lookup("10.99.0.5"); ok {
		t.Errorf("post-unregister still present")
	}
}

func TestRegisterValidation(t *testing.T) {
	api := NewAPI(NewAllowlist())
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/v1/register", "application/json",
		strings.NewReader(`{"folder":"x"}`))
	if resp.StatusCode != 400 {
		t.Errorf("missing ip should 400, got %d", resp.StatusCode)
	}

	resp, _ = http.Get(srv.URL + "/v1/register")
	if resp.StatusCode != 405 {
		t.Errorf("GET should 405, got %d", resp.StatusCode)
	}
}

func TestHealth(t *testing.T) {
	api := NewAPI(NewAllowlist())
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("health: %v %d", err, statusCodeOf(resp))
	}
}

func statusCodeOf(r *http.Response) int {
	if r == nil {
		return 0
	}
	return r.StatusCode
}

// Compile-time guard: registerReq decodes a real body
var _ = func() bool {
	var req registerReq
	_ = json.Unmarshal([]byte(`{"ip":"x"}`), &req)
	return true
}()
