package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/chanreg"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

func setup(t *testing.T) (*Server, *chanreg.Registry, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	reg := chanreg.New("test-secret")
	srv := New(reg, s)
	return srv, reg, s
}

func postJSON(handler http.Handler, path string, body any, token string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func getJSON(handler http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func decodeResp(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRegisterChannel(t *testing.T) {
	srv, _, _ := setup(t)
	h := srv.Handler()

	w := postJSON(h, "/v1/channels/register", registerReq{
		Name:        "telegram",
		URL:         "http://tg:9001",
		JIDPrefixes: []string{"tg:"},
		Capabilities: map[string]bool{
			"send_text": true,
			"typing":    true,
		},
	}, "test-secret")

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	resp := decodeResp(t, w)
	if resp["ok"] != true {
		t.Errorf("ok = %v", resp["ok"])
	}
	if resp["token"] == nil || resp["token"] == "" {
		t.Error("expected token in response")
	}
}

func TestRegisterBadSecret(t *testing.T) {
	srv, _, _ := setup(t)
	h := srv.Handler()

	w := postJSON(h, "/v1/channels/register", registerReq{
		Name: "telegram", URL: "http://tg:9001", JIDPrefixes: []string{"tg:"},
	}, "wrong-secret")

	if w.Code != 401 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestRegisterMissingFields(t *testing.T) {
	srv, _, _ := setup(t)
	h := srv.Handler()

	w := postJSON(h, "/v1/channels/register", registerReq{
		Name: "telegram",
	}, "test-secret")

	if w.Code != 400 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestDeliverMessage(t *testing.T) {
	srv, reg, s := setup(t)
	h := srv.Handler()

	token, _ := reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	w := postJSON(h, "/v1/messages", messageReq{
		ChatJID:    "tg:123",
		Sender:     "tg:456",
		SenderName: "Alice",
		Content:    "hello world",
		Timestamp:  1709942400,
	}, token)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	msgs, _, err := s.NewMessages([]string{"tg:123"}, time.Time{}, "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if msgs[0].Content != "hello world" {
		t.Errorf("content = %q", msgs[0].Content)
	}
	if msgs[0].Name != "Alice" {
		t.Errorf("sender_name = %q", msgs[0].Name)
	}
}

func TestDeliverMessage_PersistsIsGroup(t *testing.T) {
	srv, reg, s := setup(t)
	h := srv.Handler()

	token, _ := reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	w := postJSON(h, "/v1/messages", messageReq{
		ChatJID: "tg:-100", Content: "hi", IsGroup: true,
	}, token)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !s.GetChatIsGroup("tg:-100") {
		t.Error("expected chats.is_group=1 after delivery with IsGroup=true")
	}

	// A subsequent inbound classified single-user must overwrite (last-wins).
	w = postJSON(h, "/v1/messages", messageReq{
		ChatJID: "tg:-100", Content: "hi again", IsGroup: false,
	}, token)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if s.GetChatIsGroup("tg:-100") {
		t.Error("expected last-wins flip back to single-user")
	}
}

func TestDeliverMessageBadToken(t *testing.T) {
	srv, _, _ := setup(t)
	h := srv.Handler()

	w := postJSON(h, "/v1/messages", messageReq{
		ChatJID: "tg:123", Content: "hello",
	}, "invalid-token")

	if w.Code != 401 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestDeliverMessageMissingFields(t *testing.T) {
	srv, reg, _ := setup(t)
	h := srv.Handler()

	token, _ := reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	w := postJSON(h, "/v1/messages", messageReq{
		ChatJID: "tg:123",
		// missing content
	}, token)

	if w.Code != 400 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestDeregister(t *testing.T) {
	srv, reg, _ := setup(t)
	h := srv.Handler()

	var deregistered string
	srv.OnDeregister(func(name string) { deregistered = name })

	token, _ := reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	w := postJSON(h, "/v1/channels/deregister", nil, token)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if deregistered != "tg" {
		t.Errorf("deregistered = %q", deregistered)
	}
	if reg.Get("tg") != nil {
		t.Error("expected channel removed from registry")
	}
}

func TestListChannels(t *testing.T) {
	srv, reg, _ := setup(t)
	h := srv.Handler()

	reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)
	reg.Register("dc", "http://dc:9002", []string{"dc:"}, nil)

	w := getJSON(h, "/v1/channels", "test-secret")
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	resp := decodeResp(t, w)
	channels := resp["channels"].([]any)
	if len(channels) != 2 {
		t.Errorf("channels = %d, want 2", len(channels))
	}
}

func TestHealth(t *testing.T) {
	srv, _, _ := setup(t)
	h := srv.Handler()

	w := getJSON(h, "/health", "")
	if w.Code != 200 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestRegisterCallbackCreatesChannel(t *testing.T) {
	srv, _, _ := setup(t)
	h := srv.Handler()

	var registeredName string
	srv.OnRegister(func(name string, ch *chanreg.HTTPChannel) {
		registeredName = name
	})

	w := postJSON(h, "/v1/channels/register", registerReq{
		Name:        "telegram",
		URL:         "http://tg:9001",
		JIDPrefixes: []string{"tg:"},
	}, "test-secret")

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if registeredName != "telegram" {
		t.Errorf("registered = %q", registeredName)
	}
}

func TestDeliverMessageWithAttachments(t *testing.T) {
	srv, reg, s := setup(t)
	h := srv.Handler()

	token, _ := reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	body := messageReq{
		ChatJID:    "tg:123",
		Sender:     "tg:456",
		SenderName: "Alice",
		Content:    "[Photo]",
		Timestamp:  1709942400,
		Attachments: []chanlib.InboundAttachment{
			{Mime: "image/jpeg", Filename: "photo.jpg", URL: "http://teled:9001/files/abc123", Size: 2048},
		},
	}
	w := postJSON(h, "/v1/messages", body, token)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	msgs, _, err := s.NewMessages([]string{"tg:123"}, time.Time{}, "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if msgs[0].Attachments == "" {
		t.Error("attachments should be stored, got empty")
	}
	if !strings.Contains(msgs[0].Attachments, "image/jpeg") {
		t.Errorf("attachments JSON missing mime type, got %q", msgs[0].Attachments)
	}
	if !strings.Contains(msgs[0].Attachments, "teled:9001") {
		t.Errorf("attachments JSON missing URL, got %q", msgs[0].Attachments)
	}
}

// fakeAdapter spins up an httptest.Server that records /send calls so
// outbound routing tests can assert which adapter URL was hit.
type fakeAdapter struct {
	name string
	srv  *httptest.Server
	mu   sync.Mutex
	hits []string
}

func newFakeAdapter(t *testing.T, name string) *fakeAdapter {
	t.Helper()
	a := &fakeAdapter{name: name}
	mux := http.NewServeMux()
	mux.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		a.mu.Lock()
		a.hits = append(a.hits, body["chat_jid"].(string))
		a.mu.Unlock()
		chanlib.WriteJSON(w, map[string]any{"ok": true, "id": "sent-1"})
	})
	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)
	return a
}

func (a *fakeAdapter) hitCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.hits)
}

func TestOutboundRoutesByChannelName(t *testing.T) {
	srv, reg, _ := setup(t)
	h := srv.Handler()

	primary := newFakeAdapter(t, "telegram")
	variant := newFakeAdapter(t, "telegram-REDACTED")
	reg.Register("telegram", primary.srv.URL, []string{"telegram:"},
		map[string]bool{"send_text": true})
	reg.Register("telegram-REDACTED", variant.srv.URL, []string{"telegram:"},
		map[string]bool{"send_text": true})

	// Explicit channel pin: caller knows which adapter received the message
	// and threads it back through /v1/outbound's "channel" field.
	w := postJSON(h, "/v1/outbound", map[string]string{
		"jid": "telegram:-123", "text": "hi", "channel": "telegram-REDACTED",
	}, "test-secret")
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if variant.hitCount() != 1 || primary.hitCount() != 0 {
		t.Errorf("pinned variant: primary=%d variant=%d, want 0/1",
			primary.hitCount(), variant.hitCount())
	}
}

func TestOutboundUsesLatestSource(t *testing.T) {
	srv, reg, s := setup(t)
	h := srv.Handler()

	primary := newFakeAdapter(t, "telegram")
	variant := newFakeAdapter(t, "telegram-REDACTED")
	reg.Register("telegram", primary.srv.URL, []string{"telegram:"},
		map[string]bool{"send_text": true})
	reg.Register("telegram-REDACTED", variant.srv.URL, []string{"telegram:"},
		map[string]bool{"send_text": true})

	// Inbound message via telegram-REDACTED is recorded with source.
	if err := s.PutMessage(core.Message{
		ID: "m1", ChatJID: "telegram:-123", Sender: "u",
		Content: "hi", Timestamp: time.Now(), Source: "telegram-REDACTED",
	}); err != nil {
		t.Fatal(err)
	}

	// No explicit channel → store.LatestSource returns the recorded adapter.
	w := postJSON(h, "/v1/outbound", map[string]string{
		"jid": "telegram:-123", "text": "hi",
	}, "test-secret")
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if variant.hitCount() != 1 || primary.hitCount() != 0 {
		t.Errorf("latest source: primary=%d variant=%d, want 0/1",
			primary.hitCount(), variant.hitCount())
	}
}

func TestOutboundUnknownChannelFallsBackToJID(t *testing.T) {
	srv, reg, _ := setup(t)
	h := srv.Handler()

	primary := newFakeAdapter(t, "telegram")
	reg.Register("telegram", primary.srv.URL, []string{"telegram:"},
		map[string]bool{"send_text": true})

	// Stale channel name (e.g. adapter was restarted) → Resolve falls back
	// to ForJID instead of failing outright.
	w := postJSON(h, "/v1/outbound", map[string]string{
		"jid": "telegram:-123", "text": "hi", "channel": "telegram-gone",
	}, "test-secret")
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if primary.hitCount() != 1 {
		t.Errorf("primary hits = %d, want 1", primary.hitCount())
	}
}

func TestOutboundNoMatchingAdapter(t *testing.T) {
	srv, reg, _ := setup(t)
	h := srv.Handler()

	primary := newFakeAdapter(t, "telegram")
	reg.Register("telegram", primary.srv.URL, []string{"telegram:"},
		map[string]bool{"send_text": true})

	w := postJSON(h, "/v1/outbound", map[string]string{
		"jid": "whatsapp:456", "text": "hi",
	}, "test-secret")
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestOutboundMissingFields(t *testing.T) {
	srv, _, _ := setup(t)
	h := srv.Handler()

	for _, body := range []map[string]string{
		{"jid": "tg:1"},           // missing text
		{"text": "hi"},            // missing jid
		{},                        // both missing
	} {
		w := postJSON(h, "/v1/outbound", body, "test-secret")
		if w.Code != 400 {
			t.Errorf("body=%v status=%d, want 400", body, w.Code)
		}
	}
}

func TestOutboundBadAuth(t *testing.T) {
	srv, _, _ := setup(t)
	h := srv.Handler()

	w := postJSON(h, "/v1/outbound", map[string]string{
		"jid": "tg:1", "text": "hi",
	}, "wrong-secret")
	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestNoSecretAllowsAll(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "store"))
	defer s.Close()

	reg := chanreg.New("") // no secret
	srv := New(reg, s)
	h := srv.Handler()

	w := postJSON(h, "/v1/channels/register", registerReq{
		Name:        "tg",
		URL:         "http://tg:9001",
		JIDPrefixes: []string{"tg:"},
	}, "")

	if w.Code != 200 {
		t.Errorf("status = %d with no secret", w.Code)
	}
}
