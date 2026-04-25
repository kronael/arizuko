package chanreg

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/onvos/arizuko/chanlib"
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

	if _, err := ch.Send("tg:123", "hello", "", ""); err != nil {
		t.Fatal(err)
	}
	if got["chat_jid"] != "tg:123" || got["content"] != "hello" {
		t.Errorf("got = %v", got)
	}
}

func TestHTTPChannelSendNoCapErrors(t *testing.T) {
	e := &Entry{
		Name:         "tg",
		URL:          "http://should-not-be-called",
		JIDPrefixes:  []string{"tg:"},
		Capabilities: map[string]bool{},
	}
	ch := NewHTTPChannel(e, "secret")

	// send_text not declared — must return error so caller knows the send was skipped
	_, err := ch.Send("tg:123", "hello", "", "")
	if err == nil {
		t.Fatal("expected error for missing send_text capability, got nil")
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

	_, err := ch.Send("tg:123", "hello", "", "")
	if err == nil {
		t.Fatal("expected error for unreachable")
	}
	if ch.QueueLen() != 1 {
		t.Errorf("queue len = %d, want 1", ch.QueueLen())
	}
}

func TestHTTPChannelTypingPostsBody(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/typing" {
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
		Capabilities: map[string]bool{"typing": true},
	}
	ch := NewHTTPChannel(e, "secret")

	if err := ch.Typing("tg:123", true); err != nil {
		t.Fatal(err)
	}
	if got["chat_jid"] != "tg:123" || got["on"] != true {
		t.Errorf("typing body = %v", got)
	}

	// Also test on=false
	if err := ch.Typing("tg:123", false); err != nil {
		t.Fatal(err)
	}
	if got["chat_jid"] != "tg:123" || got["on"] != false {
		t.Errorf("typing off body = %v", got)
	}
}

func TestHTTPChannelTypingNoCap(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer srv.Close()

	for _, caps := range []map[string]bool{{"send_text": true}, nil} {
		e := &Entry{
			Name: "tg", URL: srv.URL, JIDPrefixes: []string{"tg:"},
			Capabilities: caps,
		}
		ch := NewHTTPChannel(e, "secret")
		if err := ch.Typing("tg:123", true); err != nil {
			t.Fatal(err)
		}
		if called {
			t.Error("typing should not POST when cap missing")
		}
	}
}

func TestHTTPChannelTypingSwallowsFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	for _, tt := range []struct {
		name string
		url  string
	}{
		{"unreachable", "http://127.0.0.1:1"},
		{"non_2xx", srv.URL},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := &Entry{
				Name: "tg", URL: tt.url, JIDPrefixes: []string{"tg:"},
				Capabilities: map[string]bool{"typing": true},
			}
			ch := NewHTTPChannel(e, "secret")
			if err := ch.Typing("tg:123", true); err != nil {
				t.Fatal(err)
			}
		})
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

// TestHTTPChannelPost501JSON: 501 with structured body decodes into
// *chanlib.UnsupportedError carrying hint.
func TestHTTPChannelPost501JSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(501)
		w.Write([]byte(`{"ok":false,"error":"unsupported","tool":"post","platform":"x","hint":"use send"}`))
	}))
	defer srv.Close()
	e := &Entry{Name: "x", URL: srv.URL, JIDPrefixes: []string{"x:"}}
	ch := NewHTTPChannel(e, "secret")
	_, err := ch.Post(context.Background(), "x:1", "hi", nil)
	var ue *chanlib.UnsupportedError
	if !errors.As(err, &ue) {
		t.Fatalf("want *UnsupportedError, got %T: %v", err, err)
	}
	if ue.Hint != "use send" {
		t.Errorf("hint = %q", ue.Hint)
	}
	// Legacy errors.Is must still chain.
	if !errors.Is(err, ErrUnsupported) {
		t.Error("errors.Is(err, ErrUnsupported) must be true")
	}
}

// TestHTTPChannelPost501Plain: 501 with empty body falls back to plain ErrUnsupported.
func TestHTTPChannelPost501Plain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(501)
	}))
	defer srv.Close()
	e := &Entry{Name: "x", URL: srv.URL, JIDPrefixes: []string{"x:"}}
	ch := NewHTTPChannel(e, "secret")
	_, err := ch.Post(context.Background(), "x:1", "hi", nil)
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("want ErrUnsupported, got %v", err)
	}
}
