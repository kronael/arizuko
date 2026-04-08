package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/onvos/arizuko/chanlib"
)

type mockRouter struct {
	mu           sync.Mutex
	registered   map[string]any
	messages     []chanlib.InboundMsg
	deregistered bool
	srv          *httptest.Server
}

func newMockRouter(secret string) *mockRouter {
	m := &mockRouter{registered: make(map[string]any)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/channels/register", func(w http.ResponseWriter, r *http.Request) {
		if secret != "" && r.Header.Get("Authorization") != "Bearer "+secret {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "bad secret"})
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.registered = body
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "token": "test-token"})
	})
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "bad token"})
			return
		}
		var msg chanlib.InboundMsg
		json.NewDecoder(r.Body).Decode(&msg)
		m.mu.Lock()
		m.messages = append(m.messages, msg)
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /v1/channels/deregister", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.deregistered = true
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *mockRouter) close() { m.srv.Close() }

func TestRouterClientRegister(t *testing.T) {
	mr := newMockRouter("secret")
	defer mr.close()

	rc := chanlib.NewRouterClient(mr.srv.URL, "secret")
	token, err := rc.Register("telegram", "http://tg:9001",
		[]string{"telegram:"}, map[string]bool{"send_text": true})

	if err != nil {
		t.Fatal(err)
	}
	if token != "test-token" {
		t.Errorf("token = %q", token)
	}
	if mr.registered["name"] != "telegram" {
		t.Errorf("registered name = %v", mr.registered["name"])
	}
}

func TestRouterClientRegisterBadSecret(t *testing.T) {
	mr := newMockRouter("secret")
	defer mr.close()

	rc := chanlib.NewRouterClient(mr.srv.URL, "wrong")
	_, err := rc.Register("telegram", "http://tg:9001", []string{"telegram:"}, nil)

	if err == nil {
		t.Fatal("expected error for bad secret")
	}
}

func TestRouterClientSendMessage(t *testing.T) {
	mr := newMockRouter("")
	defer mr.close()

	rc := chanlib.NewRouterClient(mr.srv.URL, "")
	rc.SetToken("test-token")

	err := rc.SendMessage(chanlib.InboundMsg{
		ID:         "123",
		ChatJID:    "telegram:-100123",
		Sender:     "telegram:456",
		SenderName: "Alice",
		Content:    "hello",
		Timestamp:  1709942400,
	})

	if err != nil {
		t.Fatal(err)
	}
	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.messages) != 1 {
		t.Fatalf("messages = %d", len(mr.messages))
	}
	if mr.messages[0].Content != "hello" {
		t.Errorf("content = %q", mr.messages[0].Content)
	}
	if mr.messages[0].Sender != "telegram:456" {
		t.Errorf("sender = %q", mr.messages[0].Sender)
	}
}

func TestRouterClientDeregister(t *testing.T) {
	mr := newMockRouter("")
	defer mr.close()

	rc := chanlib.NewRouterClient(mr.srv.URL, "")
	rc.SetToken("test-token")

	if err := rc.Deregister(); err != nil {
		t.Fatal(err)
	}
	if !mr.deregistered {
		t.Error("expected deregistered")
	}
}

func TestRouterClientBadToken(t *testing.T) {
	mr := newMockRouter("")
	defer mr.close()

	rc := chanlib.NewRouterClient(mr.srv.URL, "")
	rc.SetToken("bad-token")

	err := rc.SendMessage(chanlib.InboundMsg{
		ChatJID: "telegram:123",
		Content: "hello",
	})

	if err == nil {
		t.Fatal("expected error for bad token")
	}
}
