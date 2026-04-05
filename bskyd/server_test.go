package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/onvos/arizuko/chanlib"
)

type stubCreator struct {
	chanlib.NoFileSender
	err error
}

func (s *stubCreator) Send(chanlib.SendRequest) (string, error) { return "", s.err }
func (s *stubCreator) Typing(string, bool)                      {}

func testBskyServer(t *testing.T, secret string) *server {
	t.Helper()
	cfg := config{Name: "bluesky", ChannelSecret: secret}
	return newServer(cfg, &stubCreator{})
}

func TestBskyHealth(t *testing.T) {
	s := testBskyServer(t, "")
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("status field = %v", resp["status"])
	}
	if resp["name"] != "bluesky" {
		t.Errorf("name = %v, want bluesky", resp["name"])
	}
}

func TestBskyAuthRequired(t *testing.T) {
	s := testBskyServer(t, "secret123")
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "bluesky:did:plc:123", "content": "hello",
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestBskyAuthWrongToken(t *testing.T) {
	s := testBskyServer(t, "secret123")
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "bluesky:did:plc:123", "content": "hello",
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrongtoken")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestBskySend_MissingContent(t *testing.T) {
	s := testBskyServer(t, "")
	body, _ := json.Marshal(map[string]string{"chat_jid": "bluesky:did:plc:123"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestBskySend_MissingChatJID(t *testing.T) {
	s := testBskyServer(t, "")
	body, _ := json.Marshal(map[string]string{"content": "hello"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestBskyTyping(t *testing.T) {
	s := testBskyServer(t, "")
	req := httptest.NewRequest("POST", "/typing", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["ok"] != true {
		t.Errorf("ok = %v", resp["ok"])
	}
}

func TestBskyAuthNoSecret(t *testing.T) {
	// when ChannelSecret is empty, any request passes auth middleware
	s := testBskyServer(t, "")
	body, _ := json.Marshal(map[string]any{})
	req := httptest.NewRequest("POST", "/typing", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestBskySendError(t *testing.T) {
	s := newServer(config{Name: "bluesky"}, &stubCreator{err: errors.New("boom")})
	body, _ := json.Marshal(map[string]string{"chat_jid": "bluesky:1", "content": "hi"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 502 {
		t.Errorf("want 502, got %d", w.Code)
	}
}

func TestBskySendMalformedJSON(t *testing.T) {
	s := testBskyServer(t, "")
	req := httptest.NewRequest("POST", "/send", bytes.NewReader([]byte("{invalid")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("want 400, got %d", w.Code)
	}
}
