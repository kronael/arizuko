package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func testReditServer(t *testing.T, secret string) *server {
	t.Helper()
	cfg := config{Name: "reddit", ChannelSecret: secret}
	// rc is nil; tests that exercise comment/submit require a stub reddit client
	return newServer(cfg, nil)
}

func TestReditHealth(t *testing.T) {
	s := testReditServer(t, "")
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
	if resp["name"] != "reddit" {
		t.Errorf("name = %v, want reddit", resp["name"])
	}
}

func TestReditAuthRequired(t *testing.T) {
	s := testReditServer(t, "secret123")
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "reddit:alice", "content": "hello",
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestReditAuthWrongToken(t *testing.T) {
	s := testReditServer(t, "secret123")
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "reddit:alice", "content": "hello",
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

func TestReditSend_MissingContent(t *testing.T) {
	s := testReditServer(t, "")
	body, _ := json.Marshal(map[string]string{"chat_jid": "reddit:alice"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestReditSend_MissingChatJID(t *testing.T) {
	s := testReditServer(t, "")
	body, _ := json.Marshal(map[string]string{"content": "hello"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestReditTyping(t *testing.T) {
	s := testReditServer(t, "")
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

func TestReditAuthNoSecret(t *testing.T) {
	// when ChannelSecret is empty, any request passes auth middleware
	s := testReditServer(t, "")
	body, _ := json.Marshal(map[string]any{})
	req := httptest.NewRequest("POST", "/typing", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}
