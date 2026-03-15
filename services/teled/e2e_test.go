package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestE2EOutboundFlow(t *testing.T) {
	h, sb := stubHandler("secret")

	body, _ := json.Marshal(map[string]any{
		"chat_jid": "telegram:-1001234567890",
		"content":  "Hello from the agent",
		"format":   "markdown",
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if len(sb.sent) != 1 || sb.sent[0].JID != "telegram:-1001234567890" {
		t.Errorf("sent = %+v", sb.sent)
	}
}

func TestE2EInboundFlow(t *testing.T) {
	mr := newMockRouter("")
	defer mr.close()

	rc := newRouterClient(mr.srv.URL, "")
	rc.token = "test-token"

	err := rc.sendMessage(inboundMsg{
		ID: "42", ChatJID: "telegram:-1001234567890",
		Sender: "telegram:12345", SenderName: "Alice",
		Content: "Hello from telegram", Timestamp: 1709942400, IsGroup: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.messages) != 1 || mr.messages[0].Content != "Hello from telegram" {
		t.Errorf("messages = %+v", mr.messages)
	}
	if mr.messages[0].Sender != "telegram:12345" {
		t.Errorf("sender = %q", mr.messages[0].Sender)
	}
}

func TestE2ERegistrationAndDeregistration(t *testing.T) {
	mr := newMockRouter("secret")
	defer mr.close()

	rc := newRouterClient(mr.srv.URL, "secret")
	token, err := rc.register("telegram", "http://telegram:9001",
		[]string{"telegram:"}, map[string]bool{"send_text": true})
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("expected token")
	}

	rc.token = token
	if err := rc.sendMessage(inboundMsg{ChatJID: "telegram:123", Content: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := rc.deregister(); err != nil {
		t.Fatal(err)
	}
	mr.mu.Lock()
	if !mr.deregistered {
		t.Error("expected deregistered")
	}
	mr.mu.Unlock()
}

func TestE2EHealth(t *testing.T) {
	h, _ := stubHandler("")
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" || resp["name"] != "telegram" {
		t.Errorf("health = %v", resp)
	}
}

func TestE2EChatMetadata(t *testing.T) {
	mr := newMockRouter("")
	defer mr.close()

	rc := newRouterClient(mr.srv.URL, "")
	rc.token = "test-token"
	if err := rc.sendChat("telegram:-100123", "Dev Chat", true); err != nil {
		t.Fatal(err)
	}

	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.chats) != 1 || mr.chats[0]["name"] != "Dev Chat" {
		t.Errorf("chats = %+v", mr.chats)
	}
}

func TestE2EMultipleMessages(t *testing.T) {
	mr := newMockRouter("")
	defer mr.close()

	rc := newRouterClient(mr.srv.URL, "")
	rc.token = "test-token"
	for i := 0; i < 5; i++ {
		rc.sendMessage(inboundMsg{ChatJID: "telegram:123", Content: "msg"})
	}
	mr.mu.Lock()
	if len(mr.messages) != 5 {
		t.Errorf("messages = %d, want 5", len(mr.messages))
	}
	mr.mu.Unlock()
}
