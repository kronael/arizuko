package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onvos/arizuko/chanlib"
)

// TestBotHandler_Send drives linkd's comment-post path: ChatJID pointing at
// a post URN routes through /v2/socialActions/<urn>/comments.
func TestBotHandler_Send(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"comment-42"}`))
	}))
	defer srv.Close()

	lc := &linkClient{
		cfg:   config{APIBase: srv.URL},
		http:  srv.Client(),
		token: "tok",
		meURN: "urn:li:person:me",
		seen:  map[string]bool{},
	}

	id, err := lc.Send(chanlib.SendRequest{
		ChatJID: "linkedin:urn:li:activity:123",
		Content: "nice post!",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "comment-42" {
		t.Errorf("id = %q, want comment-42", id)
	}
	if !strings.Contains(gotPath, "/v2/socialActions/") || !strings.Contains(gotPath, "/comments") {
		t.Errorf("path = %q", gotPath)
	}
	msg, _ := gotBody["message"].(map[string]any)
	if msg["text"] != "nice post!" {
		t.Errorf("text = %v", msg["text"])
	}
	if gotBody["actor"] != "urn:li:person:me" {
		t.Errorf("actor = %v", gotBody["actor"])
	}
}
