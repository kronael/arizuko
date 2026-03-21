package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type mockRouter struct {
	mu           sync.Mutex
	registered   map[string]any
	messages     []inboundMsg
	chats        []map[string]any
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
		var msg inboundMsg
		json.NewDecoder(r.Body).Decode(&msg)
		m.mu.Lock()
		m.messages = append(m.messages, msg)
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /v1/chats", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.chats = append(m.chats, body)
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

	rc := newRouterClient(mr.srv.URL, "secret")
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

	rc := newRouterClient(mr.srv.URL, "wrong")
	_, err := rc.Register("telegram", "http://tg:9001", []string{"telegram:"}, nil)

	if err == nil {
		t.Fatal("expected error for bad secret")
	}
}

func TestRouterClientSendMessage(t *testing.T) {
	mr := newMockRouter("")
	defer mr.close()

	rc := newRouterClient(mr.srv.URL, "")
	rc.Token = "test-token"

	err := rc.SendMessage(inboundMsg{
		ID:         "123",
		ChatJID:    "telegram:-100123",
		Sender:     "telegram:456",
		SenderName: "Alice",
		Content:    "hello",
		Timestamp:  1709942400,
		IsGroup:    true,
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

func TestRouterClientSendChat(t *testing.T) {
	mr := newMockRouter("")
	defer mr.close()

	rc := newRouterClient(mr.srv.URL, "")
	rc.Token = "test-token"

	err := rc.SendChat("telegram:-100123", "Dev Chat", true)
	if err != nil {
		t.Fatal(err)
	}

	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.chats) != 1 {
		t.Fatalf("chats = %d", len(mr.chats))
	}
	if mr.chats[0]["name"] != "Dev Chat" {
		t.Errorf("name = %v", mr.chats[0]["name"])
	}
}

func TestRouterClientDeregister(t *testing.T) {
	mr := newMockRouter("")
	defer mr.close()

	rc := newRouterClient(mr.srv.URL, "")
	rc.Token = "test-token"

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

	rc := newRouterClient(mr.srv.URL, "")
	rc.Token = "bad-token"

	err := rc.SendMessage(inboundMsg{
		ChatJID: "telegram:123",
		Content: "hello",
	})

	if err == nil {
		t.Fatal("expected error for bad token")
	}
}
