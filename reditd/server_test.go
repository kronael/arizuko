package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

type stubSender struct {
	chanlib.NoFileSender
	chanlib.NoSocial
	commented []string
	submitted []string
	err       error
	postReq   chanlib.PostRequest
	postID    string
	postErr   error
	delReq    chanlib.DeleteRequest
	delErr    error
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
func (s *stubSender) Post(r chanlib.PostRequest) (string, error) {
	s.postReq = r
	return s.postID, s.postErr
}
func (s *stubSender) Like(chanlib.LikeRequest) error { return chanlib.ErrUnsupported }
func (s *stubSender) Delete(r chanlib.DeleteRequest) error {
	s.delReq = r
	return s.delErr
}

func testReditServer(t *testing.T, secret string) *server {
	t.Helper()
	cfg := config{Name: "reddit", ChannelSecret: secret}
	s := newServer(cfg, &stubSender{}, chanlib.NewURLCache(100), func() bool { return true }, func() int64 { return time.Now().Unix() })
	s.safeFetch = nil
	return s
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

func TestReditHealthDisconnected(t *testing.T) {
	s := newServer(config{Name: "reddit"}, &stubSender{}, chanlib.NewURLCache(100), func() bool { return false }, func() int64 { return time.Now().Unix() })
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
	body, _ := json.Marshal(map[string]any{"chat_jid": "reddit:alice", "on": true})
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

func TestReditAuthNoSecret(t *testing.T) {
	// when ChannelSecret is empty, any request passes auth middleware
	s := testReditServer(t, "")
	body, _ := json.Marshal(map[string]any{"chat_jid": "reddit:alice", "on": false})
	req := httptest.NewRequest("POST", "/typing", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestReditSendComment(t *testing.T) {
	stub := &stubSender{}
	s := newServer(config{Name: "reddit"}, stub, chanlib.NewURLCache(100), func() bool { return true }, func() int64 { return time.Now().Unix() })
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
	s := newServer(config{Name: "reddit"}, stub, chanlib.NewURLCache(100), func() bool { return true }, func() int64 { return time.Now().Unix() })
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
	s := newServer(config{Name: "reddit"}, stub, chanlib.NewURLCache(100), func() bool { return true }, func() int64 { return time.Now().Unix() })
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

func TestReditFileProxy(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("fakejpeg"))
	}))
	defer cdn.Close()

	fc := chanlib.NewURLCache(100)
	id := fc.Put(cdn.URL + "/image.jpg")
	s := newServer(config{Name: "reddit"}, &stubSender{}, fc, func() bool { return true }, func() int64 { return time.Now().Unix() })
	s.safeFetch = nil

	req := httptest.NewRequest("GET", "/files/"+id, nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("content-type = %q", w.Header().Get("Content-Type"))
	}
	if w.Body.String() != "fakejpeg" {
		t.Errorf("body = %q", w.Body.String())
	}
}

func TestReditFileProxyNotFound(t *testing.T) {
	s := testReditServer(t, "")
	req := httptest.NewRequest("GET", "/files/nonexistent", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestReditFileProxyAuthRequired(t *testing.T) {
	fc := chanlib.NewURLCache(100)
	fc.Put("https://example.com/img.jpg")
	s := newServer(config{Name: "reddit", ChannelSecret: "secret123"}, &stubSender{}, fc, func() bool { return true }, func() int64 { return time.Now().Unix() })

	req := httptest.NewRequest("GET", "/files/someid", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestReditPost(t *testing.T) {
	stub := &stubSender{postID: "t3_abc"}
	s := newServer(config{Name: "reddit"}, stub, chanlib.NewURLCache(100), func() bool { return true }, func() int64 { return time.Now().Unix() })
	body, _ := json.Marshal(map[string]any{"chat_jid": "reddit:sub", "content": "body"})
	req := httptest.NewRequest("POST", "/post", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if stub.postReq.Content != "body" {
		t.Errorf("post req = %+v", stub.postReq)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["id"] != "t3_abc" {
		t.Errorf("id = %v", resp["id"])
	}
}

func TestReditDelete(t *testing.T) {
	stub := &stubSender{}
	s := newServer(config{Name: "reddit"}, stub, chanlib.NewURLCache(100), func() bool { return true }, func() int64 { return time.Now().Unix() })
	body, _ := json.Marshal(map[string]any{"chat_jid": "reddit:sub", "target_id": "t3_abc"})
	req := httptest.NewRequest("POST", "/delete", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if stub.delReq.TargetID != "t3_abc" {
		t.Errorf("del req = %+v", stub.delReq)
	}
}

func TestReditLikeUnsupported(t *testing.T) {
	s := newServer(config{Name: "reddit"}, &stubSender{}, chanlib.NewURLCache(100), func() bool { return true }, func() int64 { return time.Now().Unix() })
	body, _ := json.Marshal(map[string]any{"chat_jid": "reddit:sub", "target_id": "t3_abc", "reaction": "👍"})
	req := httptest.NewRequest("POST", "/like", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 501 {
		t.Errorf("status = %d, want 501", w.Code)
	}
}

// TestReditFileProxy_SSRFBlock verifies that disallowed URLs are rejected even
// when the file cache happens to contain one (defense-in-depth).
func TestReditFileProxy_SSRFBlock(t *testing.T) {
	fc := chanlib.NewURLCache(100)
	id := fc.Put("http://169.254.169.254/latest/meta-data/")
	s := newServer(config{Name: "reddit"}, &stubSender{}, fc, func() bool { return true }, func() int64 { return time.Now().Unix() })
	// default safeFetch = isSafeFetchURL must reject
	req := httptest.NewRequest("GET", "/files/"+id, nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("status = %d, want 400 (SSRF blocked)", w.Code)
	}
}
