package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// capRecorder stands in for the routd registration.
type capRecorder struct {
	mu    sync.Mutex
	calls int
	caps  map[string]bool
}

func (c *capRecorder) SetCaps(caps map[string]bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.caps = caps
	return nil
}

func (c *capRecorder) read() (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.caps["send_text"]
}

// A token GitHub refuses gates ONE verb. Propagating it exits chanlib.Run,
// and a daemon that exits at boot cycles the whole compose project (BUGS
// L419), so gitd stays up on ingress and takes send_text back off the
// registration that already ran. Nothing asks again: only a new credential
// repairs a refusal.
func TestVerifyToken_RefusedTokenDegradesToIngress(t *testing.T) {
	var probes atomic.Int64
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer gh.Close()
	b := newBot(config{Token: "ghp_revoked", APIBase: gh.URL})
	rc := &capRecorder{}

	verifyToken(t.Context(), b, rc, time.Millisecond)

	if !b.isConnected() {
		t.Error("gitd stopped serving ingress over a credential that gates only send_text")
	}
	calls, sendText := rc.read()
	if calls != 1 {
		t.Fatalf("SetCaps calls = %d, want 1: routd still routes send_text to gitd", calls)
	}
	if sendText {
		t.Error("send_text was left advertised on a refused token")
	}
	// 100 ticks' worth of quiet: a re-ask loop would have fired many times.
	time.Sleep(100 * time.Millisecond)
	if n := probes.Load(); n != 1 {
		t.Errorf("/user probes = %d, want 1: a refusal is permanent and nothing retries it", n)
	}
}

// A token GitHub accepts leaves the registration alone.
func TestVerifyToken_GoodTokenKeepsRegistration(t *testing.T) {
	gh, _, _ := fakeGitHub(t, http.StatusOK, `{}`)
	b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
	rc := &capRecorder{}

	verifyToken(t.Context(), b, rc, time.Millisecond)

	if calls, _ := rc.read(); calls != 0 {
		t.Errorf("SetCaps calls = %d on a working token, want 0", calls)
	}
	if b.Login() != "arizuko-bot" {
		t.Errorf("login = %q", b.Login())
	}
}

// GitHub being unreachable is not a refused token. Degrading for the life of
// the process over one dropped connection left gitd unable to reply until
// someone restarted it, while /health answered 200 (BUGS L423). send_text
// still comes off while the login is unknown — without it gitd's own comments
// re-enter as human events — and goes back on the moment /user answers.
func TestVerifyToken_UnreachableGitHubRestoresSendText(t *testing.T) {
	var answering atomic.Bool
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !answering.Load() {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = w.Write([]byte(`{"login":"arizuko-bot"}`))
	}))
	defer gh.Close()
	b := newBot(config{Token: "ghp_test", APIBase: gh.URL})
	rc := &capRecorder{}

	verifyToken(t.Context(), b, rc, time.Millisecond)

	calls, sendText := rc.read()
	if calls != 1 || sendText {
		t.Fatalf("during the outage: SetCaps calls = %d, send_text = %v; want it withdrawn once", calls, sendText)
	}
	answering.Store(true)
	deadline := time.Now().Add(10 * time.Second)
	for {
		calls, sendText = rc.read()
		if sendText {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("send_text never came back: SetCaps calls = %d", calls)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if calls != 2 {
		t.Errorf("SetCaps calls = %d, want 2: one withdrawal and one restore", calls)
	}
	if b.Login() != "arizuko-bot" {
		t.Errorf("login = %q; gitd's own comments re-enter as human events", b.Login())
	}
}
