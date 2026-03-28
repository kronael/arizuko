package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

type stubPoster struct{ err error }

func (s *stubPoster) postStatus(_ context.Context, _, _ string) error { return s.err }

func testMastServer(t *testing.T, secret string) *server {
	t.Helper()
	cfg := config{Name: "mastodon", ChannelSecret: secret}
	return newServer(cfg, nil)
}

func TestMastHealth(t *testing.T) {
	s := testMastServer(t, "")
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
	if resp["name"] != "mastodon" {
		t.Errorf("name = %v, want mastodon", resp["name"])
	}
}

func TestMastAuthRequired(t *testing.T) {
	s := testMastServer(t, "secret123")
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "mastodon:123", "content": "hello",
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestMastAuthWrongToken(t *testing.T) {
	s := testMastServer(t, "secret123")
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "mastodon:123", "content": "hello",
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

func TestMastSend_MissingFields(t *testing.T) {
	s := testMastServer(t, "")
	// missing content → 400 (validation happens before mc is called)
	body, _ := json.Marshal(map[string]string{"chat_jid": "mastodon:123"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestMastSend_MissingChatJID(t *testing.T) {
	s := testMastServer(t, "")
	body, _ := json.Marshal(map[string]string{"content": "hello"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestMastTyping(t *testing.T) {
	s := testMastServer(t, "")
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

func TestMastAuthNoSecret(t *testing.T) {
	// when ChannelSecret is empty, any request passes through
	s := testMastServer(t, "")
	body, _ := json.Marshal(map[string]string{"content": "hello"})
	req := httptest.NewRequest("POST", "/typing", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	// typing handler always returns 200
	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestMastSendError(t *testing.T) {
	s := newServer(config{Name: "mastodon"}, &stubPoster{err: errors.New("boom")})
	body, _ := json.Marshal(map[string]string{"chat_jid": "mastodon:1", "content": "hi"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 502 {
		t.Errorf("want 502, got %d", w.Code)
	}
}

func TestMastSendMalformedJSON(t *testing.T) {
	s := testMastServer(t, "")
	req := httptest.NewRequest("POST", "/send", bytes.NewReader([]byte("{invalid")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("want 400, got %d", w.Code)
	}
}
