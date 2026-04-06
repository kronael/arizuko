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
	return newServer(cfg, db, newAttachCache(100)), db
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

	if w.Code != 502 {
		t.Errorf("status = %d, want 502", w.Code)
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

func TestFileProxy(t *testing.T) {
	db := newTestDB(t)
	fc := newAttachCache(100)
	fc.Put("42-0", []byte("hello pdf"), "application/pdf", "doc.pdf")

	s := newServer(config{Name: "email"}, db, fc)

	req := httptest.NewRequest("GET", "/files/42/0", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Header().Get("Content-Type") != "application/pdf" {
		t.Errorf("content-type = %q", w.Header().Get("Content-Type"))
	}
	if w.Body.String() != "hello pdf" {
		t.Errorf("body = %q", w.Body.String())
	}
	if w.Header().Get("Content-Disposition") != `attachment; filename="doc.pdf"` {
		t.Errorf("disposition = %q", w.Header().Get("Content-Disposition"))
	}
}

func TestFileProxyNotFound(t *testing.T) {
	s, _ := testServer(t, "")
	req := httptest.NewRequest("GET", "/files/999/0", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestFileProxyBadPath(t *testing.T) {
	s, _ := testServer(t, "")
	req := httptest.NewRequest("GET", "/files/notanumber/0", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestFileProxyAuthRequired(t *testing.T) {
	db := newTestDB(t)
	fc := newAttachCache(100)
	fc.Put("1-0", []byte("data"), "text/plain", "file.txt")
	s := newServer(config{Name: "email", ChannelSecret: "secret123"}, db, fc)

	req := httptest.NewRequest("GET", "/files/1/0", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}
