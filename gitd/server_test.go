package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/objstore/dynstore"
)

type secretMap map[string]string

func (m secretMap) GitHubSourceSecret(key string) (string, bool, error) {
	s, ok := m[key]
	return s, ok, nil
}

// fakeRoutd records every /v1/messages POST; status is what it answers.
type fakeRoutd struct {
	mu     sync.Mutex
	got    []chanlib.InboundMsg
	status int
}

func (f *fakeRoutd) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/messages" {
		http.NotFound(w, r)
		return
	}
	var m chanlib.InboundMsg
	_ = json.NewDecoder(r.Body).Decode(&m)
	f.mu.Lock()
	f.got = append(f.got, m)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"ok":false,"error":"refused"}`))
		return
	}
	_, _ = w.Write([]byte(`{"ok":true,"id":"` + m.ID + `"}`))
}

func (f *fakeRoutd) messages() []chanlib.InboundMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]chanlib.InboundMsg(nil), f.got...)
}

type testEnv struct {
	srv   *server
	h     http.Handler
	routd *fakeRoutd
	up    *httptest.Server
}

func newTestEnv(t *testing.T, secrets secretMap) *testEnv {
	t.Helper()
	routd := &fakeRoutd{}
	up := httptest.NewServer(routd)
	t.Cleanup(up.Close)
	rc := chanlib.NewRouterClient(up.URL)
	as, err := chanlib.OpenAdapterStore("gitd")
	if err != nil {
		t.Fatal(err)
	}
	// gitd's tables are named for its SERVICE, not for this test, so the body
	// drops them when it ends rather than leaving them as the next one's fixture.
	t.Cleanup(func() {
		dropGitdEntities()
		as.Close() //nolint:errcheck // the test is ending
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rc.RetainInbound(ctx, as.Chan)
	cfg := config{Name: "github", APIBase: "http://gh.invalid"}
	b := newBot(cfg)
	b.connected.Store(true)
	s := newServer(cfg, b, secrets, as.Chan, rc)
	s.now = func() time.Time { return fixedNow }
	return &testEnv{srv: s, h: s.handler(), routd: routd, up: up}
}

func (e *testEnv) deliver(t *testing.T, path, secret, event, delivery string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Hub-Signature-256", sign(secret, body))
	}
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", delivery)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return w
}

func TestWebhook_PushDelivered(t *testing.T) {
	e := newTestEnv(t, secretMap{"repo/kronael/arizuko": "s3cret"})
	w := e.deliver(t, "/github/repo/kronael/arizuko", "s3cret", "push", "11111111-aaaa", fixture(t, "push"))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	got := e.routd.messages()
	if len(got) != 1 {
		t.Fatalf("routd got %d messages", len(got))
	}
	m := got[0]
	if m.ChatJID != "github:kronael/arizuko" || m.Verb != "push" || m.ID != "gh-d11111111-aaaa" || m.Sender != "github:kronael" || m.Source != "" && m.Source != "github" {
		t.Errorf("message = %+v", m)
	}
	if !strings.Contains(w.Body.String(), `"jid":"github:kronael/arizuko"`) {
		t.Errorf("response body = %s", w.Body.String())
	}
	if e.srv.bot.LastInboundAt() != fixedNow.Unix() {
		t.Errorf("last inbound = %d", e.srv.bot.LastInboundAt())
	}
	if n, _ := chanlib.PendingInboundCount(t.Context(), e.srv.chanStore); n != 0 {
		t.Errorf("pending inbound = %d after a delivered event", n)
	}
}

func TestWebhook_RejectsBadSignature(t *testing.T) {
	e := newTestEnv(t, secretMap{"repo/kronael/arizuko": "s3cret"})
	body := fixture(t, "push")
	for name, w := range map[string]*httptest.ResponseRecorder{
		"wrong secret":   e.deliver(t, "/github/repo/kronael/arizuko", "other", "push", "d1", body),
		"missing header": e.deliver(t, "/github/repo/kronael/arizuko", "", "push", "d2", body),
	} {
		if w.Code != 401 {
			t.Errorf("%s: status=%d body=%s", name, w.Code, w.Body.String())
		}
	}
	forged := httptest.NewRequest("POST", "/github/repo/kronael/arizuko", bytes.NewReader(body))
	forged.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	forged.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, forged)
	if w.Code != 401 {
		t.Errorf("forged: status=%d", w.Code)
	}
	if n := len(e.routd.messages()); n != 0 {
		t.Fatalf("routd received %d messages from unsigned deliveries", n)
	}
	if e.srv.bot.LastInboundAt() != 0 {
		t.Error("a rejected delivery counted as inbound")
	}
}

func TestWebhook_UnknownSource(t *testing.T) {
	e := newTestEnv(t, secretMap{"repo/kronael/arizuko": "s3cret"})
	body := fixture(t, "push")
	for _, path := range []string{"/github/repo/kronael/crackbox", "/github/org/kronael", "/github/kronael/arizuko", "/github/repo", "/github/"} {
		if w := e.deliver(t, path, "s3cret", "push", "d", body); w.Code != 404 {
			t.Errorf("%s: status=%d", path, w.Code)
		}
	}
	if n := len(e.routd.messages()); n != 0 {
		t.Fatalf("routd received %d messages", n)
	}
}

func TestWebhook_SourceMismatchAndBots(t *testing.T) {
	e := newTestEnv(t, secretMap{"repo/kronael/crackbox": "s3cret", "app/arizuko-krons": "app-secret"})
	w := e.deliver(t, "/github/repo/kronael/crackbox", "s3cret", "issue_comment", "d1", fixture(t, "issue_comment_created"))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "outside the registered source") {
		t.Errorf("mismatch: status=%d body=%s", w.Code, w.Body.String())
	}
	w = e.deliver(t, "/github/app/arizuko-krons", "app-secret", "issue_comment", "d2", fixture(t, "issue_comment_bot"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"dropped":"bot"`) {
		t.Errorf("bot: status=%d body=%s", w.Code, w.Body.String())
	}
	e.srv.bot.login.Store("kronael")
	w = e.deliver(t, "/github/app/arizuko-krons", "app-secret", "issue_comment", "d3", fixture(t, "issue_comment_created"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"dropped":"self"`) {
		t.Errorf("self: status=%d body=%s", w.Code, w.Body.String())
	}
	w = e.deliver(t, "/github/app/arizuko-krons", "app-secret", "", "d4", fixture(t, "push"))
	if w.Code != 400 {
		t.Errorf("no event header: status=%d", w.Code)
	}
	if n := len(e.routd.messages()); n != 0 {
		t.Fatalf("routd received %d messages", n)
	}
}

func TestWebhook_TwoReposOneSecret(t *testing.T) {
	e := newTestEnv(t, secretMap{"app/arizuko-krons": "app-secret"})
	for _, f := range []string{"issue_comment_created", "issue_comment_created_crackbox"} {
		if w := e.deliver(t, "/github/app/arizuko-krons", "app-secret", "issue_comment", "d-"+f, fixture(t, f)); w.Code != 200 {
			t.Fatalf("%s: status=%d body=%s", f, w.Code, w.Body.String())
		}
	}
	got := e.routd.messages()
	if len(got) != 2 || got[0].ChatJID != "github:kronael/arizuko/pr/12" || got[1].ChatJID != "github:kronael/crackbox/issue/7" ||
		got[0].Topic != "pr/12" || got[1].Topic != "issue/7" {
		t.Fatalf("messages = %+v", got)
	}
}

func TestWebhook_RetainedWhenRoutdDown(t *testing.T) {
	e := newTestEnv(t, secretMap{"org/kronael": "s3cret"})
	e.up.Close()
	w := e.deliver(t, "/github/org/kronael", "s3cret", "push", "d-down", fixture(t, "push"))
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"retained":true`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if n, _ := chanlib.PendingInboundCount(t.Context(), e.srv.chanStore); n != 1 {
		t.Fatalf("pending inbound = %d, want 1", n)
	}
}

func TestWebhook_PermanentRefusalNotRetained(t *testing.T) {
	e := newTestEnv(t, secretMap{"org/kronael": "s3cret"})
	e.routd.status = 400
	w := e.deliver(t, "/github/org/kronael", "s3cret", "push", "d-refused", fixture(t, "push"))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "routd refused") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if n, _ := chanlib.PendingInboundCount(t.Context(), e.srv.chanStore); n != 0 {
		t.Fatalf("pending inbound = %d, want 0", n)
	}
}

// dropGitdEntities removes what OpenAdapterStore made, so nothing one body
// wrote outlives it — here or on the endpoint after the run.
func dropGitdEntities() {
	h, err := dynstore.FromEnv(dynstore.Prefix("gitd"))
	if err != nil {
		return
	}
	ctx := context.Background()
	left, err := h.Entities(ctx)
	if err != nil {
		return
	}
	for _, e := range left {
		_ = h.DropEntity(ctx, e)
	}
}
