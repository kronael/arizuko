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

type stubPoster struct {
	chanlib.NoFileSender
	chanlib.NoVoiceSender
	chanlib.NoSocial
	err      error
	postReq  chanlib.PostRequest
	postID   string
	postErr  error
	reactReq chanlib.LikeRequest
	reactErr error
	delReq   chanlib.DeleteRequest
	delErr   error
}

func (s *stubPoster) Send(chanlib.SendRequest) (string, error) { return "", s.err }
func (s *stubPoster) Typing(string, bool)                      {}
func (s *stubPoster) Post(r chanlib.PostRequest) (string, error) {
	s.postReq = r
	return s.postID, s.postErr
}
func (s *stubPoster) Like(r chanlib.LikeRequest) error {
	s.reactReq = r
	return s.reactErr
}
func (s *stubPoster) Delete(r chanlib.DeleteRequest) error {
	s.delReq = r
	return s.delErr
}

type stubFiles struct{ urls map[string]string }

func (s *stubFiles) FileURL(id string) (string, bool) {
	u, ok := s.urls[id]
	return u, ok
}

func testMastServer(t *testing.T, secret string) *server {
	t.Helper()
	cfg := config{Name: "mastodon", ChannelSecret: secret}
	return newServer(cfg, &stubPoster{}, &stubFiles{}, func() bool { return true }, func() int64 { return time.Now().Unix() })
}

func TestMastPost(t *testing.T) {
	bot := &stubPoster{postID: "99"}
	s := newServer(config{Name: "mastodon"}, bot, &stubFiles{}, func() bool { return true }, func() int64 { return time.Now().Unix() })
	body, _ := json.Marshal(map[string]any{"chat_jid": "mastodon:1", "content": "toot"})
	req := httptest.NewRequest("POST", "/post", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if bot.postReq.Content != "toot" {
		t.Errorf("post req = %+v", bot.postReq)
	}
}

func TestMastLike(t *testing.T) {
	bot := &stubPoster{}
	s := newServer(config{Name: "mastodon"}, bot, &stubFiles{}, func() bool { return true }, func() int64 { return time.Now().Unix() })
	body, _ := json.Marshal(map[string]any{"chat_jid": "mastodon:1", "target_id": "t1"})
	req := httptest.NewRequest("POST", "/like", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if bot.reactReq.TargetID != "t1" {
		t.Errorf("like req = %+v", bot.reactReq)
	}
}

func TestMastDelete(t *testing.T) {
	bot := &stubPoster{}
	s := newServer(config{Name: "mastodon"}, bot, &stubFiles{}, func() bool { return true }, func() int64 { return time.Now().Unix() })
	body, _ := json.Marshal(map[string]any{"chat_jid": "mastodon:1", "target_id": "t1"})
	req := httptest.NewRequest("POST", "/delete", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if bot.delReq.TargetID != "t1" {
		t.Errorf("del req = %+v", bot.delReq)
	}
}

func TestMastHealth(t *testing.T) {
	s := testMastServer(t, "")
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
	if resp["name"] != "mastodon" {
		t.Errorf("name = %v, want mastodon", resp["name"])
	}
}

func TestMastHealthDisconnected(t *testing.T) {
	s := newServer(config{Name: "mastodon"}, &stubPoster{}, &stubFiles{}, func() bool { return false }, func() int64 { return time.Now().Unix() })
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

func TestMastAuthRequired(t *testing.T) {
	s := testMastServer(t, "secret123")
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "mastodon:123", "content": "hello",
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestMastAuthWrongToken(t *testing.T) {
	s := testMastServer(t, "secret123")
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "mastodon:123", "content": "hello",
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

func TestMastSend_MissingFields(t *testing.T) {
	s := testMastServer(t, "")
	body, _ := json.Marshal(map[string]string{"chat_jid": "mastodon:123"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestMastSend_MissingChatJID(t *testing.T) {
	s := testMastServer(t, "")
	body, _ := json.Marshal(map[string]string{"content": "hello"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestMastTyping(t *testing.T) {
	s := testMastServer(t, "")
	body, _ := json.Marshal(map[string]any{"chat_jid": "mastodon:123", "on": true})
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

func TestMastAuthNoSecret(t *testing.T) {
	s := testMastServer(t, "")
	body, _ := json.Marshal(map[string]any{"chat_jid": "mastodon:123", "on": false})
	req := httptest.NewRequest("POST", "/typing", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestMastSendError(t *testing.T) {
	s := newServer(config{Name: "mastodon"}, &stubPoster{err: errors.New("boom")}, &stubFiles{}, func() bool { return true }, func() int64 { return time.Now().Unix() })
	body, _ := json.Marshal(map[string]string{"chat_jid": "mastodon:1", "content": "hi"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 502 {
		t.Errorf("want 502, got %d", w.Code)
	}
}

func TestMastSendMalformedJSON(t *testing.T) {
	s := testMastServer(t, "")
	req := httptest.NewRequest("POST", "/send", bytes.NewReader([]byte("{invalid")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestMastFileProxy(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("fake-image-data"))
	}))
	defer cdn.Close()

	fr := &stubFiles{urls: map[string]string{"att123": cdn.URL + "/media/att123.jpg"}}
	s := newServer(config{Name: "mastodon", ChannelSecret: "sec"}, &stubPoster{}, fr, func() bool { return true }, func() int64 { return time.Now().Unix() })

	req := httptest.NewRequest("GET", "/files/att123", nil)
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
	if string(body) != "fake-image-data" {
		t.Errorf("body = %q", string(body))
	}
}

func TestMastFileProxyNotFound(t *testing.T) {
	fr := &stubFiles{urls: map[string]string{}}
	s := newServer(config{Name: "mastodon"}, &stubPoster{}, fr, func() bool { return true }, func() int64 { return time.Now().Unix() })

	req := httptest.NewRequest("GET", "/files/unknown", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestMastFileProxyNoAuth(t *testing.T) {
	fr := &stubFiles{urls: map[string]string{"att1": "http://cdn/x"}}
	s := newServer(config{Name: "mastodon", ChannelSecret: "sec"}, &stubPoster{}, fr, func() bool { return true }, func() int64 { return time.Now().Unix() })

	req := httptest.NewRequest("GET", "/files/att1", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestMastFileProxyEmptyID(t *testing.T) {
	s := newServer(config{Name: "mastodon"}, &stubPoster{}, &stubFiles{}, func() bool { return true }, func() int64 { return time.Now().Unix() })

	req := httptest.NewRequest("GET", "/files/", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestMastFileProxyCDNError(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer cdn.Close()

	fr := &stubFiles{urls: map[string]string{"att1": cdn.URL + "/x"}}
	s := newServer(config{Name: "mastodon"}, &stubPoster{}, fr, func() bool { return true }, func() int64 { return time.Now().Unix() })

	req := httptest.NewRequest("GET", "/files/att1", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != 502 {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

func TestExtractAttachments(t *testing.T) {
	mc := &mastoClient{cfg: config{ListenURL: "http://mastd:9004"}, files: chanlib.NewURLCache(10)}
	id := mc.files.Put("https://cdn.example.com/media/img1.jpg")
	u, ok := mc.FileURL(id)
	if !ok {
		t.Fatal("FileURL not found")
	}
	if u != "https://cdn.example.com/media/img1.jpg" {
		t.Errorf("url = %q", u)
	}
	_, ok = mc.FileURL("missing")
	if ok {
		t.Error("expected not found for missing key")
	}
}

func TestMediaMime(t *testing.T) {
	cases := []struct {
		typ  string
		want string
	}{
		{"image", "image/jpeg"},
		{"video", "video/mp4"},
		{"audio", "audio/mpeg"},
		{"gifv", "video/mp4"},
		{"unknown", "application/octet-stream"},
	}
	for _, tc := range cases {
		got := mediaMime(tc.typ)
		if got != tc.want {
			t.Errorf("mediaMime(%q) = %q, want %q", tc.typ, got, tc.want)
		}
	}
}

func TestMimeExt(t *testing.T) {
	cases := []struct {
		mime string
		want string
	}{
		{"image/jpeg", ".jpg"},
		{"video/mp4", ".mp4"},
		{"audio/mpeg", ".mp3"},
	}
	for _, tc := range cases {
		got := mimeExt(tc.mime)
		if got != tc.want {
			t.Errorf("mimeExt(%q) = %q, want %q", tc.mime, got, tc.want)
		}
	}
}
