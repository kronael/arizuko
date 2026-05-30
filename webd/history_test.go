package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// GET /chat/<token>/<topic>/messages returns prior messages for the token's
// JID + topic, oldest→newest, with bot role mapped to assistant.
func TestChatTokenHistory_OrderAndRoles(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")

	now := time.Now()
	// Insert out of chronological order to prove the handler sorts ascending.
	if err := st.PutMessage(core.Message{
		ID: "b1", ChatJID: "web:main", Content: "pong",
		Timestamp: now, Topic: "v1-t1", BotMsg: true,
	}); err != nil {
		t.Fatalf("put b1: %v", err)
	}
	if err := st.PutMessage(core.Message{
		ID: "u1", ChatJID: "web:main", Content: "ping",
		Timestamp: now.Add(-time.Second), Topic: "v1-t1", Name: "Alice",
	}); err != nil {
		t.Fatalf("put u1: %v", err)
	}
	// A message on a different topic must not leak in.
	if err := st.PutMessage(core.Message{
		ID: "x1", ChatJID: "web:main", Content: "other",
		Timestamp: now, Topic: "v1-other",
	}); err != nil {
		t.Fatalf("put x1: %v", err)
	}

	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/chat/" + tok + "/v1-t1/messages")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if origin := resp.Header.Get("Access-Control-Allow-Origin"); origin != "*" {
		t.Errorf("missing CORS header, got %q", origin)
	}

	var out []struct {
		ID, Role, Content, TS string
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(out), out)
	}
	if out[0].ID != "u1" || out[1].ID != "b1" {
		t.Errorf("order = %s,%s; want u1,b1 (oldest→newest)", out[0].ID, out[1].ID)
	}
	if out[0].Role != "user" || out[1].Role != "assistant" {
		t.Errorf("roles = %s,%s; want user,assistant", out[0].Role, out[1].Role)
	}
}

// Empty topic → empty list (not an error). The pattern requires a topic
// segment, so the empty-topic case is exercised at the handler level.
func TestChatTokenHistory_EmptyTopic(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")

	req := httptest.NewRequest("GET", "/chat/"+tok+"//messages", nil)
	req.SetPathValue("token", tok)
	req.SetPathValue("topic", "")
	w := httptest.NewRecorder()
	s.handleChatTokenHistory(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var out []json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("len = %d, want 0 for empty topic", len(out))
	}
}

// Unknown token → 404 like the sibling /chat handlers.
func TestChatTokenHistory_UnknownToken(t *testing.T) {
	s, _, _ := newTestServer(t)

	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/chat/nope/v1-t1/messages")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
