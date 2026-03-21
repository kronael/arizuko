package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func testBskyServer(t *testing.T, secret string) *server {
	t.Helper()
	cfg := config{Name: "bluesky", ChannelSecret: secret}
	// bc is nil; tests that exercise createPost require a mock HTTP server
	return newServer(cfg, nil)
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
