package routd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// fakeAdapter is a minimal platform adapter: it records /send hits and returns
// a platform id. Capabilities default to send_text so chanreg.Send proceeds.
type fakeAdapter struct {
	srv    *httptest.Server
	mu     sync.Mutex
	hits   int // /send + /document hits (messaging)
	reacts int // /like hits (reactions)
	last   struct{ jid, content, idemKey string }
}

func newFakeAdapter(t *testing.T, id string) *fakeAdapter {
	t.Helper()
	a := &fakeAdapter{}
	mux := http.NewServeMux()
	mux.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ChatJID string `json:"chat_jid"`
			Content string `json:"content"`
			IdemKey string `json:"idempotency_key"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		a.mu.Lock()
		a.hits++
		a.last.jid, a.last.content, a.last.idemKey = body.ChatJID, body.Content, body.IdemKey
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
	// Advertise every verb these tests drive. chanreg short-circuits an
	// unadvertised verb before it reaches the adapter (BUGS L94), so a fixture
	// that declares only send_text makes React fail on the CAPABILITY, hiding
	// whatever the test meant to assert. Production adapters declare these —
	// discd/main.go, teled/main.go, slakd/main.go all carry "like".
	caps := map[string]bool{"send_text": true, "post": true, "like": true}
	if _, err := reg.Register(name, url, prefixes, caps); err != nil {
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
	if a.last.idemKey != "out-1" {
		t.Fatalf("idempotency_key=%q want out-1 (threaded through to the adapter)", a.last.idemKey)
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

// TestDelivererResolvesCurrentEntry: the Deliverer caches no channel, so a
// re-registration that moves an adapter's URL takes effect on the very next
// send. Registry.Register REPLACES the Entry value, so a cached channel would
// have held the old URL and delivered to a dead address.
func TestDelivererResolvesCurrentEntry(t *testing.T) {
	old := newFakeAdapter(t, "from-old")
	moved := newFakeAdapter(t, "from-moved")
	reg := chanreg.New()
	registerAdapter(t, reg, "slakd", old.srv.URL, "slack:T1/")
	d := newChanDeliverer(reg, nil, nil)

	if _, err := d.Send("slack:T1/C/U", "first", "", "", "", "k1"); err != nil {
		t.Fatalf("send before move: %v", err)
	}
	// The adapter re-registers at a new URL (a restart on a new host/port).
	registerAdapter(t, reg, "slakd", moved.srv.URL, "slack:T1/")
	if _, err := d.Send("slack:T1/C/U", "second", "", "", "", "k2"); err != nil {
		t.Fatalf("send after move: %v", err)
	}
	if old.count() != 1 || moved.count() != 1 {
		t.Fatalf("stale entry served: old=%d moved=%d want 1,1", old.count(), moved.count())
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
	srv := NewServer(db, nil, NewChannelDeliverer(reg, nil, db.LatestSource), nil, 0, "")
	srv.SetChannelRegistry(reg)
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chanReq("POST", "/v1/channels/register", testChanSecret, map[string]any{
		"name": "slakd", "url": a.srv.URL,
		"jid_prefixes": []string{"slack:T1/"},
		"capabilities": map[string]bool{"send_text": true},
	}))
	if rec.Code != 200 {
		t.Fatalf("register status=%d body=%s", rec.Code, rec.Body.String())
	}

	db.PutTurnContext("t1", "demo", "", "slack:T1/C/U", "u1", "")
	srv.replyForTest(t, "t1", "slack:T1/C/U", "answer")
	if a.count() != 1 {
		t.Fatalf("adapter delivered=%d want 1", a.count())
	}
	if a.last.content != "answer" {
		t.Fatalf("delivered content=%q want answer", a.last.content)
	}
}

// F67 / spec 5/34-channel-protocol § "Outbound and adapter resolution" step 1: an explicit
// channel pin beats the latest inbound source AND the ForJID prefix scan. This
// is the disambiguation a caller reaches for when one prefix is served by two
// adapter accounts (primary `telegram` plus a second bot) — before this, the
// field decoded fine and was dropped on the floor, so the caller was silently
// overridden by exactly the fallback it named a channel to avoid.
func TestDelivererPinBeatsInboundSourceAndForJID(t *testing.T) {
	pinned := newFakeAdapter(t, "from-pinned")
	source := newFakeAdapter(t, "from-source")
	reg := chanreg.New()
	registerAdapter(t, reg, "aaa", pinned.srv.URL, "slack:T1/")
	registerAdapter(t, reg, "zzz", source.srv.URL, "slack:T1/")

	// lookupSource says "zzz" — the pin must still win.
	d := newChanDeliverer(reg, nil, func(string) string { return "zzz" })
	if _, err := d.SendVia("aaa", "slack:T1/C/U", "hi"); err != nil {
		t.Fatalf("SendVia: %v", err)
	}
	if pinned.count() != 1 || source.count() != 0 {
		t.Fatalf("pin ignored: pinned=%d source=%d want 1,0", pinned.count(), source.count())
	}

	// An empty pin falls back to the inbound source, unchanged.
	if _, err := d.SendVia("", "slack:T1/C/U", "hi again"); err != nil {
		t.Fatalf("SendVia unpinned: %v", err)
	}
	if source.count() != 1 {
		t.Fatalf("empty pin did not fall back to the inbound source (source=%d)", source.count())
	}
}

// The HTTP half of F67: POST /v1/outbound decodes `channel` and it must reach
// resolution. An unregistered pin is refused LOUDLY (400) rather than falling
// through — Registry.Resolve silently degrades an unknown name to ForJID, which
// would deliver through the very adapter the caller pinned away from.
func TestOutboundHonoursChannelPin(t *testing.T) {
	pinned := newFakeAdapter(t, "from-pinned")
	other := newFakeAdapter(t, "from-other")
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	reg := chanreg.New()
	// lookupSource pins the fallback to "telegram" so the unpinned resolution is
	// DETERMINISTIC: without this the ForJID prefix scan picks either adapter by
	// map order and the assertion below would pass by luck.
	deliver := NewChannelDeliverer(reg, nil, func(string) string { return "telegram" })
	registerAdapter(t, reg, "telegram", other.srv.URL, "telegram:")
	registerAdapter(t, reg, "telegram-rhias", pinned.srv.URL, "telegram:")

	srv := NewServer(db, nil, deliver, nil, 0, "")
	srv.SetChannelRegistry(reg)
	h := srv.Handler()

	// Pinned: the second bot account receives it, not the prefix-scan winner.
	rec := doJSON(t, h, "POST", "/v1/outbound", "",
		apiv1.OutboundRequest{JID: "telegram:user/9", Text: "pinned", Channel: "telegram-rhias"})
	if rec.Code != 200 {
		t.Fatalf("pinned outbound status=%d body=%s", rec.Code, rec.Body.String())
	}
	if pinned.count() != 1 || other.count() != 0 {
		t.Fatalf("pin ignored: pinned=%d other=%d want 1,0", pinned.count(), other.count())
	}

	// Unregistered pin: loud 400, and NOTHING is delivered.
	rec = doJSON(t, h, "POST", "/v1/outbound", "",
		apiv1.OutboundRequest{JID: "telegram:user/9", Text: "ghost", Channel: "telegram-gone"})
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "unknown_channel") {
		t.Fatalf("unregistered pin status=%d body=%s, want 400 unknown_channel", rec.Code, rec.Body.String())
	}
	if pinned.count() != 1 || other.count() != 0 {
		t.Fatalf("unregistered pin still delivered: pinned=%d other=%d", pinned.count(), other.count())
	}
}

// POST /v1/outbound is a message like any other: routd appends the row it
// delivers, so a daemon's send (onbod's greeting) reaches find_messages, the
// transcript and — when the platform refuses it — the dead_letters surface.
// Falsifiable: drop the PutMessage and the adapter still delivers while the
// messages table stays empty.
func TestOutboundAppendsMessagesRow(t *testing.T) {
	adapter := newFakeAdapter(t, "pid-out-1")
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := chanreg.New()
	// The inbound-source fallback knows telegram only, so a slack: jid has no
	// adapter to resolve to.
	deliver := NewChannelDeliverer(reg, nil, func(jid string) string {
		if strings.HasPrefix(jid, "telegram:") {
			return "telegram"
		}
		return ""
	})
	registerAdapter(t, reg, "telegram", adapter.srv.URL, "telegram:")
	srv := NewServer(db, nil, deliver, nil, 0, "")
	srv.SetChannelRegistry(reg)
	h := srv.Handler()

	rec := doJSON(t, h, "POST", "/v1/outbound", "",
		apiv1.OutboundRequest{JID: "telegram:user/9", Text: "welcome aboard"})
	if rec.Code != 200 {
		t.Fatalf("outbound status=%d body=%s", rec.Code, rec.Body.String())
	}
	sent := outboundRowOf(t, db, "telegram:user/9")
	if sent.Content != "welcome aboard" || sent.PlatformID != "pid-out-1" {
		t.Fatalf("row = (%q, %q), want (welcome aboard, pid-out-1)", sent.Content, sent.PlatformID)
	}
	if st := statusOf(t, db, sent.ID); st != core.MessageStatusSent {
		t.Fatalf("status = %q, want sent", st)
	}

	// No adapter owns slack: → the send is refused, the row is dead-lettered
	// (`failed`, never `pending`), and the sweep never re-sends it.
	rec = doJSON(t, h, "POST", "/v1/outbound", "",
		apiv1.OutboundRequest{JID: "slack:T/C/U", Text: "nobody home"})
	if rec.Code != 502 {
		t.Fatalf("refused outbound status=%d, want 502", rec.Code)
	}
	refused := outboundRowOf(t, db, "slack:T/C/U")
	status := statusOf(t, db, refused.ID)
	if status != core.MessageStatusFailed {
		t.Fatalf("refused outbound row status=%q, want failed", status)
	}
	if rows, _ := db.PendingOutbound(time.Now().Add(time.Hour), 10); len(rows) != 0 {
		t.Fatalf("dead-lettered outbound row is in the retry feed: %+v", rows)
	}
}

// outboundRowOf reads the one bot row a chat holds — the outbound the handler
// appended.
func outboundRowOf(t *testing.T, db *DB, jid string) core.Message {
	t.Helper()
	msgs, err := db.MessagesSince(jid, "")
	if err != nil {
		t.Fatal(err)
	}
	var out []core.Message
	for _, m := range msgs {
		if m.BotMsg && m.FromMe {
			out = append(out, m)
		}
	}
	if len(out) != 1 {
		t.Fatalf("%s holds %d outbound rows, want 1", jid, len(out))
	}
	return out[0]
}
