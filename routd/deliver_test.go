package routd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/kronael/arizuko/chanreg"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// fakeAdapter is a minimal platform adapter: it records /send hits and returns
// a platform id. Capabilities default to send_text so chanreg.Send proceeds.
type fakeAdapter struct {
	srv    *httptest.Server
	mu     sync.Mutex
	hits   int // /send + /document hits (messaging)
	reacts int // /like hits (reactions)
	last   struct{ jid, content, turnID string }
}

func newFakeAdapter(t *testing.T, id string) *fakeAdapter {
	t.Helper()
	a := &fakeAdapter{}
	mux := http.NewServeMux()
	mux.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ChatJID string `json:"chat_jid"`
			Content string `json:"content"`
			TurnID  string `json:"turn_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		a.mu.Lock()
		a.hits++
		a.last.jid, a.last.content, a.last.turnID = body.ChatJID, body.Content, body.TurnID
		a.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	})
	mux.HandleFunc("/like", func(w http.ResponseWriter, _ *http.Request) {
		a.mu.Lock()
		a.reacts++
		a.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	})
	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)
	return a
}

func (a *fakeAdapter) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hits
}

func (a *fakeAdapter) reactCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reacts
}

// registerAdapter registers an adapter into reg with send_text capability.
// CHANNEL_REGISTER_ALLOW_PUBLIC is set for the test process so 127.0.0.1
// httptest URLs pass SSRF validation.
func registerAdapter(t *testing.T, reg *chanreg.Registry, name, url string, prefixes ...string) {
	t.Helper()
	t.Setenv("CHANNEL_REGISTER_ALLOW_PUBLIC", "1")
	if _, err := reg.Register(name, url, prefixes, map[string]bool{"send_text": true}); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
}

// TestDelivererSendsToResolvedAdapter: a registered adapter owning the jid
// prefix receives the Send (ForJID resolution, no inbound source).
func TestDelivererSendsToResolvedAdapter(t *testing.T) {
	a := newFakeAdapter(t, "pid-1")
	reg := chanreg.New()
	registerAdapter(t, reg, "slakd", a.srv.URL, "slack:T1/")
	d := newChanDeliverer(reg, nil, nil) // nil lookupSource → ForJID only

	pid, err := d.Send("slack:T1/C/U", "hi", "", "", "", "out-1")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if pid != "pid-1" {
		t.Fatalf("platform id=%q want pid-1", pid)
	}
	if a.count() != 1 {
		t.Fatalf("adapter hits=%d want 1", a.count())
	}
	if a.last.turnID != "out-1" {
		t.Fatalf("turn_id=%q want out-1 (idempotency key threaded through)", a.last.turnID)
	}
}

// TestDelivererResolutionOrder: two adapters both own the prefix; the inbound
// source (lookupSource) wins over the ForJID prefix scan, matching gated's
// order (latest inbound source → registry Resolve/ForJID).
func TestDelivererResolutionOrder(t *testing.T) {
	first := newFakeAdapter(t, "from-first")
	source := newFakeAdapter(t, "from-source")
	reg := chanreg.New()
	// Both own the same prefix. "aaa" sorts first so a naive ForJID scan could
	// pick it; the source override must steer to "zzz".
	registerAdapter(t, reg, "aaa", first.srv.URL, "slack:T1/")
	registerAdapter(t, reg, "zzz", source.srv.URL, "slack:T1/")

	// lookupSource pins the jid to the "zzz" adapter (latest inbound source).
	d := newChanDeliverer(reg, nil, func(string) string { return "zzz" })
	if _, err := d.Send("slack:T1/C/U", "hi", "", "", "", "k"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if source.count() != 1 || first.count() != 0 {
		t.Fatalf("source resolution: source=%d first=%d want 1,0", source.count(), first.count())
	}
}

// TestDelivererNoChannel: an unowned jid yields an error (no adapter).
func TestDelivererNoChannel(t *testing.T) {
	reg := chanreg.New()
	d := newChanDeliverer(reg, nil, nil)
	if _, err := d.Send("telegram:42", "hi", "", "", "", "k"); err == nil {
		t.Fatal("send to unowned jid returned nil error")
	}
}

// TestDelivererDisabledChannel: a jid whose prefix is in SEND_DISABLED_CHANNELS
// is a silent no-op success — the adapter is never hit.
func TestDelivererDisabledChannel(t *testing.T) {
	a := newFakeAdapter(t, "pid")
	reg := chanreg.New()
	registerAdapter(t, reg, "slakd", a.srv.URL, "slack:T1/")
	d := newChanDeliverer(reg, []string{"slack"}, nil)

	pid, err := d.Send("slack:T1/C/U", "hi", "", "", "", "k")
	if err != nil || pid != "" {
		t.Fatalf("disabled send pid=%q err=%v want \"\",nil", pid, err)
	}
	if a.count() != 0 {
		t.Fatalf("disabled channel still delivered (hits=%d)", a.count())
	}
}

// TestDelivererDisabledAllowsReactions locks the "block messaging, allow
// reactions" contract: SEND_DISABLED_CHANNELS suppresses Send/Document/Post for a
// channel but NOT React — reactions stay live. (Matches gated, where canSendToJID
// gates messaging but socialDo/Like bypasses it; the operator policy is "reactions
// I'll allow, just messaging".)
func TestDelivererDisabledAllowsReactions(t *testing.T) {
	a := newFakeAdapter(t, "pid-1")
	reg := chanreg.New()
	registerAdapter(t, reg, "discd", a.srv.URL, "discord:")
	d := newChanDeliverer(reg, []string{"discord"}, nil)

	// messaging suppressed (silent no-op, adapter untouched)
	if pid, err := d.Send("discord:g/c", "hi", "", "", "", "k1"); pid != "" || err != nil {
		t.Fatalf("disabled Send pid=%q err=%v want \"\",nil", pid, err)
	}
	if _, err := d.Post("discord:g/c", "post", nil); err != nil {
		t.Fatalf("disabled Post err=%v want nil (no-op)", err)
	}
	if n := a.count(); n != 0 {
		t.Fatalf("disabled channel got a message (send/post hits=%d) — messaging must be blocked", n)
	}

	// reactions allowed (adapter IS hit even though the channel is send-disabled)
	if err := d.React("discord:g/c", "ts-1", "👍"); err != nil {
		t.Fatalf("React on disabled channel errored: %v", err)
	}
	if n := a.reactCount(); n != 1 {
		t.Fatalf("reaction suppressed (like hits=%d) want 1 — reactions must stay live", n)
	}
}

// TestDelivererReusesLiveChannel: the register hook records a live channel and
// resolve reuses it (so the retry outbox survives) instead of building fresh.
func TestDelivererReusesLiveChannel(t *testing.T) {
	a := newFakeAdapter(t, "pid")
	reg := chanreg.New()
	registerAdapter(t, reg, "slakd", a.srv.URL, "slack:T1/")
	d := newChanDeliverer(reg, nil, nil)

	live := chanreg.NewHTTPChannel(reg.Get("slakd"), func(context.Context) (string, error) { return "", nil })
	d.setLive("slakd", live)
	if got := d.resolve("slack:T1/C/U"); got != live {
		t.Fatal("resolve did not return the cached live channel")
	}
	d.dropLive("slakd")
	if got := d.resolve("slack:T1/C/U"); got == live {
		t.Fatal("resolve returned the dropped live channel")
	}
}

// TestServerWiresDelivererViaRegistry is the end-to-end channel→Deliverer path:
// register an adapter through the HTTP surface, then a turn reply resolves it
// and delivers (the flip-blocker: production must wire a non-nil Deliverer).
func TestServerWiresDelivererViaRegistry(t *testing.T) {
	t.Setenv("CHANNEL_REGISTER_ALLOW_PUBLIC", "1")
	a := newFakeAdapter(t, "1716.0042")
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	reg := chanreg.New()
	deliver, onRegister, onDeregister := NewChannelDeliverer(reg, nil, db.LatestSource)
	srv := NewServer(db, nil, deliver, nil, 0, "")
	srv.SetChannelRegistry(reg, onRegister, onDeregister)
	h := srv.Handler()

	// register the adapter via the HTTP surface.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chanReq("POST", "/v1/channels/register", testChanSecret, map[string]any{
		"name": "slakd", "url": a.srv.URL,
		"jid_prefixes": []string{"slack:T1/"},
		"capabilities": map[string]bool{"send_text": true},
	}))
	if rec.Code != 200 {
		t.Fatalf("register status=%d body=%s", rec.Code, rec.Body.String())
	}

	// a turn reply resolves the adapter and delivers.
	db.PutTurnContext("t1", "demo", "", "slack:T1/C/U", "u1", "")
	drec := doJSONKey(t, h, "POST", "/v1/turns/t1/reply", "k1",
		apiv1.ReplyRequest{JID: "slack:T1/C/U", Text: "answer"})
	if drec.Code != 200 {
		t.Fatalf("reply status=%d body=%s", drec.Code, drec.Body.String())
	}
	if a.count() != 1 {
		t.Fatalf("adapter delivered=%d want 1", a.count())
	}
	if a.last.content != "answer" {
		t.Fatalf("delivered content=%q want answer", a.last.content)
	}
	_ = onDeregister
}
