package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// capRecorder stands in for the routd registration.
type capRecorder struct {
	calls int
	caps  map[string]bool
}

func (c *capRecorder) SetCaps(caps map[string]bool) error {
	c.calls++
	c.caps = caps
	return nil
}

// A token GitHub refuses gates ONE verb. Propagating it exits chanlib.Run,
// and a daemon that exits at boot cycles the whole compose project (BUGS
// L419), so gitd stays up on ingress and takes send_text back off the
// registration that already ran.
func TestVerifyToken_RefusedTokenDegradesToIngress(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer gh.Close()
	b := newBot(config{Token: "ghp_revoked", APIBase: gh.URL})
	rc := &capRecorder{}

	verifyToken(context.Background(), b, rc)

	if !b.isConnected() {
		t.Error("gitd stopped serving ingress over a credential that gates only send_text")
	}
	if rc.calls != 1 {
		t.Fatalf("SetCaps calls = %d, want 1: routd still routes send_text to gitd", rc.calls)
	}
	if rc.caps["send_text"] {
		t.Errorf("caps = %v, want send_text withdrawn", rc.caps)
	}
}

// A token GitHub accepts leaves the registration alone.
func TestVerifyToken_GoodTokenKeepsRegistration(t *testing.T) {
	gh, _, _ := fakeGitHub(t, http.StatusOK, `{}`)
	b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
	rc := &capRecorder{}

	verifyToken(context.Background(), b, rc)

	if rc.calls != 0 {
		t.Errorf("SetCaps calls = %d on a working token, want 0", rc.calls)
	}
	if b.Login() != "arizuko-bot" {
		t.Errorf("login = %q", b.Login())
	}
}
