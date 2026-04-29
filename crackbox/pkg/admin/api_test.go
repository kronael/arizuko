package admin

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterUnregister(t *testing.T) {
	reg := NewRegistry()
	api := NewAPI(reg)
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	body := `{"ip":"10.99.0.5","id":"acme/eng","allowlist":["github.com","npmjs.org"]}`
	resp, err := http.Post(srv.URL+"/v1/register", "application/json", strings.NewReader(body))
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("register: %v %d", err, statusCodeOf(resp))
	}

	id, list, ok := reg.Lookup("10.99.0.5")
	if !ok || id != "acme/eng" || len(list) != 2 {
		t.Fatalf("lookup: %v %v %v", id, list, ok)
	}

	resp, err = http.Get(srv.URL + "/v1/state")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(b, []byte(`"acme/eng"`)) {
		t.Errorf("state body missing id: %s", b)
	}

	resp, err = http.Post(srv.URL+"/v1/unregister", "application/json",
		strings.NewReader(`{"ip":"10.99.0.5"}`))
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("unregister: %v %d", err, statusCodeOf(resp))
	}
	if _, _, ok := reg.Lookup("10.99.0.5"); ok {
		t.Errorf("post-unregister still present")
	}
}

func TestRegisterValidation(t *testing.T) {
	api := NewAPI(NewRegistry())
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/v1/register", "application/json",
		strings.NewReader(`{"id":"x"}`))
	if resp.StatusCode != 400 {
		t.Errorf("missing ip should 400, got %d", resp.StatusCode)
	}

	resp, _ = http.Get(srv.URL + "/v1/register")
	if resp.StatusCode != 405 {
		t.Errorf("GET should 405, got %d", resp.StatusCode)
	}
}

func TestHealth(t *testing.T) {
	api := NewAPI(NewRegistry())
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("health: %v %d", err, statusCodeOf(resp))
	}
}

func TestRegistryAllow(t *testing.T) {
	r := NewRegistry()
	r.Set("10.99.0.5", "acme/eng", []string{"github.com", "anthropic.com"})

	if id, ok := r.Allow("10.99.0.5", "api.github.com"); !ok || id != "acme/eng" {
		t.Errorf("Allow allowed-host: ok=%v id=%q", ok, id)
	}
	if _, ok := r.Allow("10.99.0.5", "evil.com"); ok {
		t.Errorf("Allow denied-host should fail")
	}
	if _, ok := r.Allow("10.99.0.99", "github.com"); ok {
		t.Errorf("Allow unknown-IP should fail")
	}

	r.Remove("10.99.0.5")
	if _, _, ok := r.Lookup("10.99.0.5"); ok {
		t.Errorf("Remove did not clear")
	}
}

func statusCodeOf(r *http.Response) int {
	if r == nil {
		return 0
	}
	return r.StatusCode
}
