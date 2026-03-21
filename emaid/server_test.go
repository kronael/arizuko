package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func testServer(t *testing.T, secret string) (*server, *sql.DB) {
	t.Helper()
	db := newTestDB(t)
	cfg := config{Name: "email", ChannelSecret: secret}
	return newServer(cfg, db), db
}

func TestHandleSend_NoThread(t *testing.T) {
	s, _ := testServer(t, "")
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "email:notexist", "content": "hello",
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	// thread not in DB → 404
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleHealth(t *testing.T) {
	s, _ := testServer(t, "")
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" || resp["name"] != "email" {
		t.Errorf("health = %v", resp)
	}
}

func TestAuthRequired(t *testing.T) {
	s, _ := testServer(t, "mysecret")
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "email:tid", "content": "hello",
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// no Authorization header
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleTyping(t *testing.T) {
	s, _ := testServer(t, "")
	body, _ := json.Marshal(map[string]any{"chat_jid": "email:tid", "on": true})
	req := httptest.NewRequest("POST", "/typing", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["ok"] != true {
		t.Errorf("ok = %v", resp["ok"])
	}
}

func TestAuthPassthrough(t *testing.T) {
	s, db := testServer(t, "tok")
	upsertThread(db, "root@x.com", "abc123def456", "alice@x.com", "root@x.com")

	// /typing always ok with valid auth
	body, _ := json.Marshal(map[string]any{"on": false})
	req := httptest.NewRequest("POST", "/typing", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}
