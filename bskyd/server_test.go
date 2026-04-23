package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

type stubCreator struct {
	chanlib.NoFileSender
	err      error
	postID   string
	postErr  error
	postReq  chanlib.PostRequest
	reactReq chanlib.ReactRequest
	reactErr error
	delReq   chanlib.DeleteRequest
	delErr   error
}

func (s *stubCreator) Post(r chanlib.PostRequest) (string, error) {
	s.postReq = r
	return s.postID, s.postErr
}
func (s *stubCreator) React(r chanlib.ReactRequest) error {
	s.reactReq = r
	return s.reactErr
}
func (s *stubCreator) DeletePost(r chanlib.DeleteRequest) error {
	s.delReq = r
	return s.delErr
}

func (s *stubCreator) Send(chanlib.SendRequest) (string, error) { return "", s.err }
func (s *stubCreator) Typing(string, bool)                      {}

func testBskyServer(t *testing.T, secret string) *server {
	t.Helper()
	cfg := config{Name: "bluesky", ChannelSecret: secret}
	return newServer(cfg, &stubCreator{}, func() bool { return true }, func() int64 { return time.Now().Unix() })
}

func TestBskyHealth(t *testing.T) {
	s := testBskyServer(t, "")
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
	if resp["name"] != "bluesky" {
		t.Errorf("name = %v, want bluesky", resp["name"])
	}
}

func TestBskyHealthDisconnected(t *testing.T) {
	s := newServer(config{Name: "bluesky"}, &stubCreator{}, func() bool { return false }, func() int64 { return time.Now().Unix() })
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "disconnected" {
		t.Errorf("status = %v", resp["status"])
	}
}

func TestBskyAuthRequired(t *testing.T) {
	s := testBskyServer(t, "secret123")
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "bluesky:did:plc:123", "content": "hello",
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestBskyAuthWrongToken(t *testing.T) {
	s := testBskyServer(t, "secret123")
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "bluesky:did:plc:123", "content": "hello",
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

func TestBskySend_MissingContent(t *testing.T) {
	s := testBskyServer(t, "")
	body, _ := json.Marshal(map[string]string{"chat_jid": "bluesky:did:plc:123"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestBskySend_MissingChatJID(t *testing.T) {
	s := testBskyServer(t, "")
	body, _ := json.Marshal(map[string]string{"content": "hello"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestBskyTyping(t *testing.T) {
	s := testBskyServer(t, "")
	body, _ := json.Marshal(map[string]any{"chat_jid": "bluesky:did:plc:123", "on": true})
	req := httptest.NewRequest("POST", "/typing", bytes.NewReader(body))
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

func TestBskyAuthNoSecret(t *testing.T) {
	s := testBskyServer(t, "")
	body, _ := json.Marshal(map[string]any{"chat_jid": "bluesky:did:plc:123", "on": false})
	req := httptest.NewRequest("POST", "/typing", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestBskySendError(t *testing.T) {
	s := newServer(config{Name: "bluesky"}, &stubCreator{err: errors.New("boom")}, func() bool { return true }, func() int64 { return time.Now().Unix() })
	body, _ := json.Marshal(map[string]string{"chat_jid": "bluesky:1", "content": "hi"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 502 {
		t.Errorf("want 502, got %d", w.Code)
	}
}

func TestBskySendMalformedJSON(t *testing.T) {
	s := testBskyServer(t, "")
	req := httptest.NewRequest("POST", "/send", bytes.NewReader([]byte("{invalid")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestBskyFileProxy(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("did") != "did:plc:abc" || r.URL.Query().Get("cid") != "bafkrei123" {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("blob-data"))
	}))
	defer cdn.Close()

	s := newServer(config{Name: "bluesky", ChannelSecret: "sec", Service: cdn.URL}, &stubCreator{}, func() bool { return true }, func() int64 { return time.Now().Unix() })

	req := httptest.NewRequest("GET", "/files/did:plc:abc/bafkrei123", nil)
	req.Header.Set("Authorization", "Bearer sec")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("content-type = %q", w.Header().Get("Content-Type"))
	}
	body, _ := io.ReadAll(w.Body)
	if string(body) != "blob-data" {
		t.Errorf("body = %q", string(body))
	}
}

func TestBskyFileProxyMissingParts(t *testing.T) {
	cases := []struct {
		path string
		desc string
	}{
		{"/files/", "empty path"},
		{"/files/did:plc:abc", "missing cid"},
		{"/files/did:plc:abc/", "empty cid"},
	}
	for _, tc := range cases {
		s := newServer(config{Name: "bluesky"}, &stubCreator{}, func() bool { return true }, func() int64 { return time.Now().Unix() })
		req := httptest.NewRequest("GET", tc.path, nil)
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, req)
		if w.Code != 400 {
			t.Errorf("%s: status = %d, want 400", tc.desc, w.Code)
		}
	}
}

func TestBskyFileProxyNoAuth(t *testing.T) {
	s := newServer(config{Name: "bluesky", ChannelSecret: "sec"}, &stubCreator{}, func() bool { return true }, func() int64 { return time.Now().Unix() })
	req := httptest.NewRequest("GET", "/files/did:plc:abc/bafkrei123", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestBskyFileProxyCDNError(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer cdn.Close()

	s := newServer(config{Name: "bluesky", Service: cdn.URL}, &stubCreator{}, func() bool { return true }, func() int64 { return time.Now().Unix() })
	req := httptest.NewRequest("GET", "/files/did:plc:abc/bafkrei123", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 502 {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

func TestBskyPost(t *testing.T) {
	stub := &stubCreator{postID: "at://did:plc:me/app.bsky.feed.post/abc"}
	s := newServer(config{Name: "bluesky"}, stub, func() bool { return true }, func() int64 { return time.Now().Unix() })
	body, _ := json.Marshal(map[string]any{"chat_jid": "bluesky:did:plc:me", "content": "hello world"})
	req := httptest.NewRequest("POST", "/post", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if stub.postReq.Content != "hello world" {
		t.Errorf("post req = %+v", stub.postReq)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["id"] != "at://did:plc:me/app.bsky.feed.post/abc" {
		t.Errorf("id = %v", resp["id"])
	}
}

func TestBskyReact(t *testing.T) {
	stub := &stubCreator{}
	s := newServer(config{Name: "bluesky"}, stub, func() bool { return true }, func() int64 { return time.Now().Unix() })
	body, _ := json.Marshal(map[string]any{
		"chat_jid": "bluesky:did:plc:me", "target_id": "at://x/app.bsky.feed.post/y", "reaction": "like",
	})
	req := httptest.NewRequest("POST", "/react", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if stub.reactReq.TargetID != "at://x/app.bsky.feed.post/y" {
		t.Errorf("react req = %+v", stub.reactReq)
	}
}

func TestBskyDeletePost(t *testing.T) {
	stub := &stubCreator{}
	s := newServer(config{Name: "bluesky"}, stub, func() bool { return true }, func() int64 { return time.Now().Unix() })
	body, _ := json.Marshal(map[string]any{
		"chat_jid": "bluesky:did:plc:me", "target_id": "at://did:plc:me/app.bsky.feed.post/abc",
	})
	req := httptest.NewRequest("POST", "/delete-post", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if stub.delReq.TargetID != "at://did:plc:me/app.bsky.feed.post/abc" {
		t.Errorf("del req = %+v", stub.delReq)
	}
}

func TestBlobExt(t *testing.T) {
	cases := []struct {
		mime string
		want string
	}{
		{"image/jpeg", ".jpg"},
		{"image/png", ".png"},
		{"image/webp", ".webp"},
		{"image/gif", ".gif"},
		{"application/octet-stream", ".bin"},
	}
	for _, tc := range cases {
		got := blobExt(tc.mime)
		if got != tc.want {
			t.Errorf("blobExt(%q) = %q, want %q", tc.mime, got, tc.want)
		}
	}
}

func TestExtractAttachments(t *testing.T) {
	bc := &bskyClient{cfg: config{ListenURL: "http://bskyd:9005"}}
	n := notification{}
	n.Author.DID = "did:plc:abc"
	n.Record.Embed = &embedRecord{
		Type: "app.bsky.embed.images",
		Images: []embedImage{
			{
				Alt: "a photo",
				Image: blobRef{
					Ref:      blobRefLink{Link: "bafkrei123"},
					MimeType: "image/jpeg",
					Size:     5000,
				},
			},
			{
				Alt: "another",
				Image: blobRef{
					Ref:      blobRefLink{Link: "bafkrei456"},
					MimeType: "image/png",
					Size:     3000,
				},
			},
		},
	}

	atts := bc.extractAttachments(n)
	if len(atts) != 2 {
		t.Fatalf("got %d attachments, want 2", len(atts))
	}
	if atts[0].URL != "http://bskyd:9005/files/did:plc:abc/bafkrei123" {
		t.Errorf("att[0].URL = %q", atts[0].URL)
	}
	if atts[0].Mime != "image/jpeg" {
		t.Errorf("att[0].Mime = %q", atts[0].Mime)
	}
	if atts[0].Size != 5000 {
		t.Errorf("att[0].Size = %d", atts[0].Size)
	}
	if atts[0].Filename != "image_0.jpg" {
		t.Errorf("att[0].Filename = %q", atts[0].Filename)
	}
	if atts[1].URL != "http://bskyd:9005/files/did:plc:abc/bafkrei456" {
		t.Errorf("att[1].URL = %q", atts[1].URL)
	}
	if atts[1].Mime != "image/png" {
		t.Errorf("att[1].Mime = %q", atts[1].Mime)
	}
}

func TestExtractAttachments_NoEmbed(t *testing.T) {
	bc := &bskyClient{cfg: config{ListenURL: "http://bskyd:9005"}}
	n := notification{}
	atts := bc.extractAttachments(n)
	if atts != nil {
		t.Errorf("expected nil, got %v", atts)
	}
}

func TestExtractAttachments_WrongEmbedType(t *testing.T) {
	bc := &bskyClient{cfg: config{ListenURL: "http://bskyd:9005"}}
	n := notification{}
	n.Record.Embed = &embedRecord{Type: "app.bsky.embed.external"}
	atts := bc.extractAttachments(n)
	if atts != nil {
		t.Errorf("expected nil, got %v", atts)
	}
}
