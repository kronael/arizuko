package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/onvos/arizuko/chanlib"
)

type stubSender struct {
	chanlib.NoFileSender
	commented []string
	submitted []string
	err       error
}

func (s *stubSender) Send(req chanlib.SendRequest) (string, error) {
	if req.ReplyTo != "" {
		s.commented = append(s.commented, req.ReplyTo)
	} else {
		s.submitted = append(s.submitted, "x")
	}
	return "", s.err
}

func (s *stubSender) Typing(string, bool) {}

func testReditServer(t *testing.T, secret string) *server {
	t.Helper()
	cfg := config{Name: "reddit", ChannelSecret: secret}
	return newServer(cfg, &stubSender{})
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

func TestReditSendComment(t *testing.T) {
	stub := &stubSender{}
	s := newServer(config{Name: "reddit"}, stub)
	body, _ := json.Marshal(map[string]string{"chat_jid": "reddit:1", "content": "reply", "reply_to": "t3_abc"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if len(stub.commented) != 1 || stub.commented[0] != "t3_abc" {
		t.Errorf("comment not called with t3_abc: %v", stub.commented)
	}
	if len(stub.submitted) != 0 {
		t.Errorf("submit should not be called")
	}
}

func TestReditSendSubmit(t *testing.T) {
	stub := &stubSender{}
	s := newServer(config{Name: "reddit"}, stub)
	body, _ := json.Marshal(map[string]string{"chat_jid": "reddit:1", "content": "post"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if len(stub.submitted) != 1 {
		t.Errorf("submit not called: %v", stub.submitted)
	}
	if len(stub.commented) != 0 {
		t.Errorf("comment should not be called")
	}
}

func TestReditSendError(t *testing.T) {
	stub := &stubSender{err: errors.New("boom")}
	s := newServer(config{Name: "reddit"}, stub)
	body, _ := json.Marshal(map[string]string{"chat_jid": "reddit:1", "content": "hi"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 502 {
		t.Errorf("want 502, got %d", w.Code)
	}
}

func TestReditSendMalformedJSON(t *testing.T) {
	s := testReditServer(t, "")
	req := httptest.NewRequest("POST", "/send", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("want 400, got %d", w.Code)
	}
}
