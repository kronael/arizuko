package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

// Uses bskyMock from xrpc_test.go (same package).

func TestBotHandler_Send(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}
	if _, err := bc.Send(chanlib.SendRequest{ChatJID: "bluesky:x", Content: "hello"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.createRecords) != 1 {
		t.Fatalf("createRecords = %d", len(m.createRecords))
	}
	if m.createRecords[0]["collection"] != "app.bsky.feed.post" {
		t.Errorf("collection = %v", m.createRecords[0]["collection"])
	}
}

func TestBotHandler_Post(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}
	id, err := bc.Post(chanlib.PostRequest{ChatJID: "bluesky:x", Content: "a new post"})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if !strings.HasPrefix(id, "at://") {
		t.Errorf("id = %q, want at:// URI", id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.createRecords) != 1 {
		t.Fatalf("createRecords = %d", len(m.createRecords))
	}
	rec := m.createRecords[0]["record"].(map[string]any)
	if rec["text"] != "a new post" {
		t.Errorf("text = %v", rec["text"])
	}
}

func TestBotHandler_Like(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}
	targetURI := "at://did:plc:other/app.bsky.feed.post/rkeyXYZ"
	if err := bc.Like(chanlib.LikeRequest{
		ChatJID: "bluesky:x", TargetID: targetURI, Reaction: "👍",
	}); err != nil {
		t.Fatalf("Like: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Like creates a feed.like record.
	if len(m.createRecords) != 1 {
		t.Fatalf("createRecords = %d", len(m.createRecords))
	}
	if m.createRecords[0]["collection"] != "app.bsky.feed.like" {
		t.Errorf("collection = %v", m.createRecords[0]["collection"])
	}
}

func TestBotHandler_DeletePost(t *testing.T) {
	// Use a dedicated server so we can register deleteRecord.
	var deleteHits int32
	var gotBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/xrpc/com.atproto.server.createSession", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(session{DID: "did:plc:me", AccessJwt: "a", RefreshJwt: "r"})
	})
	mux.HandleFunc("/xrpc/com.atproto.repo.deleteRecord", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&deleteHits, 1)
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bc := &bskyClient{
		cfg:  config{Service: srv.URL, Identifier: "u", Password: "p", DataDir: t.TempDir()},
		http: &http.Client{Timeout: 5 * time.Second},
	}
	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}
	targetURI := "at://did:plc:me/app.bsky.feed.post/rkeyDEL"
	if err := bc.DeletePost(chanlib.DeleteRequest{
		ChatJID: "bluesky:me", TargetID: targetURI,
	}); err != nil {
		t.Fatalf("DeletePost: %v", err)
	}
	if atomic.LoadInt32(&deleteHits) != 1 {
		t.Errorf("deleteHits = %d", atomic.LoadInt32(&deleteHits))
	}
	if gotBody["rkey"] != "rkeyDEL" {
		t.Errorf("rkey = %v", gotBody["rkey"])
	}
	if gotBody["collection"] != "app.bsky.feed.post" {
		t.Errorf("collection = %v", gotBody["collection"])
	}
}
