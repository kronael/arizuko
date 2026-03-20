package chanreg

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPChannelOwns(t *testing.T) {
	e := &Entry{
		Name:        "telegram",
		JIDPrefixes: []string{"tg:", "telegram:"},
	}
	ch := NewHTTPChannel(e, "secret")

	if !ch.Owns("tg:123") {
		t.Error("expected to own tg:123")
	}
	if !ch.Owns("telegram:456") {
		t.Error("expected to own telegram:456")
	}
	if ch.Owns("dc:123") {
		t.Error("should not own dc:123")
	}
}

func TestHTTPChannelSend(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer srv.Close()

	e := &Entry{
		Name:         "tg",
		URL:          srv.URL,
		JIDPrefixes:  []string{"tg:"},
		Capabilities: map[string]bool{"send_text": true},
	}
	ch := NewHTTPChannel(e, "secret")

	if _, err := ch.Send("tg:123", "hello", ""); err != nil {
		t.Fatal(err)
	}
	if got["chat_jid"] != "tg:123" || got["content"] != "hello" {
		t.Errorf("got = %v", got)
	}
}

func TestHTTPChannelSendNoCapSkips(t *testing.T) {
	e := &Entry{
		Name:         "tg",
		URL:          "http://should-not-be-called",
		JIDPrefixes:  []string{"tg:"},
		Capabilities: map[string]bool{},
	}
	ch := NewHTTPChannel(e, "secret")

	// send_text not declared — should silently skip
	if _, err := ch.Send("tg:123", "hello", ""); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPChannelSendQueuesOnError(t *testing.T) {
	e := &Entry{
		Name:         "tg",
		URL:          "http://127.0.0.1:1", // unreachable
		JIDPrefixes:  []string{"tg:"},
		Capabilities: map[string]bool{"send_text": true},
	}
	ch := NewHTTPChannel(e, "secret")

	_, err := ch.Send("tg:123", "hello", "")
	if err == nil {
		t.Fatal("expected error for unreachable")
	}
	if ch.QueueLen() != 1 {
		t.Errorf("queue len = %d, want 1", ch.QueueLen())
	}
}

func TestHTTPChannelTypingFireAndForget(t *testing.T) {
	e := &Entry{
		Name:         "tg",
		URL:          "http://127.0.0.1:1", // unreachable
		JIDPrefixes:  []string{"tg:"},
		Capabilities: map[string]bool{"typing": true},
	}
	ch := NewHTTPChannel(e, "secret")

	// typing is fire-and-forget — no error returned
	if err := ch.Typing("tg:123", true); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPChannelHealthCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer srv.Close()

	e := &Entry{Name: "tg", URL: srv.URL}
	ch := NewHTTPChannel(e, "secret")

	if err := ch.HealthCheck(); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPChannelName(t *testing.T) {
	e := &Entry{Name: "telegram"}
	ch := NewHTTPChannel(e, "s")
	if ch.Name() != "telegram" {
		t.Errorf("name = %q", ch.Name())
	}
}

func TestHTTPChannelDrainOutbox(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer srv.Close()

	e := &Entry{
		Name:         "tg",
		URL:          srv.URL,
		JIDPrefixes:  []string{"tg:"},
		Capabilities: map[string]bool{"send_text": true},
	}
	ch := NewHTTPChannel(e, "secret")

	// manually enqueue
	ch.enqueue(outMsg{JID: "tg:1", Content: "a"})
	ch.enqueue(outMsg{JID: "tg:2", Content: "b"})

	ch.DrainOutbox()

	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if ch.QueueLen() != 0 {
		t.Errorf("queue len = %d after drain", ch.QueueLen())
	}
}
