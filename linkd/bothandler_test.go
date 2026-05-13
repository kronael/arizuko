package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/chanlib"
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

func TestBotHandler_UnsupportedHints_Linkd(t *testing.T) {
	lc := &linkClient{}
	if _, err := lc.Forward(chanlib.ForwardRequest{}); !linkdHasHint(err) {
		t.Errorf("forward: missing hint err=%v", err)
	}
	if _, err := lc.Quote(chanlib.QuoteRequest{}); !linkdHasHint(err) {
		t.Errorf("quote: missing hint err=%v", err)
	}
	if err := lc.Dislike(chanlib.DislikeRequest{}); !linkdHasHint(err) {
		t.Errorf("dislike: missing hint err=%v", err)
	}
	if err := lc.Edit(chanlib.EditRequest{}); !linkdHasHint(err) {
		t.Errorf("edit: missing hint err=%v", err)
	}
}

func TestBotHandler_Post(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Header().Set("X-RestLi-Id", "urn:li:ugcPost:new1")
		w.WriteHeader(201)
	}))
	defer srv.Close()
	lc := &linkClient{
		cfg:   config{APIBase: srv.URL, AutoPublish: true},
		http:  srv.Client(),
		token: "tok",
		meURN: "urn:li:person:me",
	}
	id, err := lc.Post(chanlib.PostRequest{Content: "hello"})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if id != "urn:li:ugcPost:new1" {
		t.Errorf("id=%q", id)
	}
	if gotPath != "/v2/ugcPosts" {
		t.Errorf("path=%q", gotPath)
	}
	if gotBody["author"] != "urn:li:person:me" || gotBody["lifecycleState"] != "PUBLISHED" {
		t.Errorf("body=%v", gotBody)
	}
}

func TestBotHandler_Post_RefusesWithoutAutoPublish(t *testing.T) {
	lc := &linkClient{cfg: config{AutoPublish: false}, meURN: "urn:li:person:me"}
	if _, err := lc.Post(chanlib.PostRequest{Content: "x"}); err == nil {
		t.Error("expected refusal without AutoPublish")
	}
}

func TestBotHandler_Post_MediaUnsupported(t *testing.T) {
	lc := &linkClient{cfg: config{AutoPublish: true}, meURN: "urn:li:person:me"}
	_, err := lc.Post(chanlib.PostRequest{Content: "x", MediaPaths: []string{"/tmp/a.jpg"}})
	if !linkdHasHint(err) {
		t.Errorf("expected UnsupportedError with hint, got %v", err)
	}
}

func TestBotHandler_Like(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.WriteHeader(201)
	}))
	defer srv.Close()
	lc := &linkClient{
		cfg:   config{APIBase: srv.URL},
		http:  srv.Client(),
		token: "tok",
		meURN: "urn:li:person:me",
	}
	if err := lc.Like(chanlib.LikeRequest{TargetID: "urn:li:activity:777"}); err != nil {
		t.Fatalf("Like: %v", err)
	}
	if !strings.Contains(gotPath, "/v2/socialActions/") || !strings.Contains(gotPath, "/likes") {
		t.Errorf("path=%q", gotPath)
	}
	if gotBody["actor"] != "urn:li:person:me" || gotBody["object"] != "urn:li:activity:777" {
		t.Errorf("body=%v", gotBody)
	}
}

func TestBotHandler_Like_BadURN(t *testing.T) {
	lc := &linkClient{meURN: "urn:li:person:me"}
	if err := lc.Like(chanlib.LikeRequest{TargetID: "urn:li:person:other"}); err == nil {
		t.Error("expected error on non-post URN")
	}
}

func TestBotHandler_Delete(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.WriteHeader(204)
	}))
	defer srv.Close()
	lc := &linkClient{
		cfg:   config{APIBase: srv.URL},
		http:  srv.Client(),
		token: "tok",
		meURN: "urn:li:person:me",
	}
	if err := lc.Delete(chanlib.DeleteRequest{TargetID: "urn:li:ugcPost:42"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if gotMethod != "DELETE" {
		t.Errorf("method=%q", gotMethod)
	}
	if !strings.Contains(gotPath, "/v2/ugcPosts/") {
		t.Errorf("path=%q", gotPath)
	}
}

func TestBotHandler_Repost(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Header().Set("X-RestLi-Id", "urn:li:ugcPost:reshare1")
		w.WriteHeader(201)
	}))
	defer srv.Close()
	lc := &linkClient{
		cfg:   config{APIBase: srv.URL, AutoPublish: true},
		http:  srv.Client(),
		token: "tok",
		meURN: "urn:li:person:me",
	}
	id, err := lc.Repost(chanlib.RepostRequest{SourceMsgID: "urn:li:share:abc"})
	if err != nil {
		t.Fatalf("Repost: %v", err)
	}
	if id != "urn:li:ugcPost:reshare1" {
		t.Errorf("id=%q", id)
	}
	if gotPath != "/v2/ugcPosts" {
		t.Errorf("path=%q", gotPath)
	}
	if gotBody["referenceUgcPost"] != "urn:li:share:abc" {
		t.Errorf("referenceUgcPost=%v", gotBody["referenceUgcPost"])
	}
}

func TestBotHandler_Repost_BadURN(t *testing.T) {
	lc := &linkClient{cfg: config{AutoPublish: true}, meURN: "urn:li:person:me"}
	if _, err := lc.Repost(chanlib.RepostRequest{SourceMsgID: "urn:li:person:other"}); err == nil {
		t.Error("expected error on non-post URN")
	}
}

func TestBotHandler_Repost_RefusesWithoutAutoPublish(t *testing.T) {
	lc := &linkClient{cfg: config{AutoPublish: false}, meURN: "urn:li:person:me"}
	if _, err := lc.Repost(chanlib.RepostRequest{SourceMsgID: "urn:li:share:abc"}); err == nil {
		t.Error("expected refusal without AutoPublish")
	}
}

func linkdHasHint(err error) bool {
	if err == nil {
		return false
	}
	ue, ok := err.(*chanlib.UnsupportedError)
	return ok && ue.Hint != ""
}
