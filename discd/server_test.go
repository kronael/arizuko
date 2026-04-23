package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

type stubBot struct {
	sent     []stubSend
	files    []stubFile
	typings  []bool
	postReq  chanlib.PostRequest
	postID   string
	postErr  error
	reactReq chanlib.ReactRequest
	reactErr error
	delReq   chanlib.DeleteRequest
	delErr   error
}

func (sb *stubBot) Post(r chanlib.PostRequest) (string, error) {
	sb.postReq = r
	return sb.postID, sb.postErr
}
func (sb *stubBot) React(r chanlib.ReactRequest) error {
	sb.reactReq = r
	return sb.reactErr
}
func (sb *stubBot) DeletePost(r chanlib.DeleteRequest) error {
	sb.delReq = r
	return sb.delErr
}

type stubSend struct{ JID, Content string }
type stubFile struct{ JID, Name string }

func (sb *stubBot) Send(req chanlib.SendRequest) (string, error) {
	sb.sent = append(sb.sent, stubSend{req.ChatJID, req.Content})
	return "stub-id", nil
}

func (sb *stubBot) SendFile(jid, _, name, _ string) error {
	sb.files = append(sb.files, stubFile{jid, name})
	return nil
}

func (sb *stubBot) Typing(_ string, on bool) {
	sb.typings = append(sb.typings, on)
}

func stubHandler(secret string) (http.Handler, *stubBot) {
	sb := &stubBot{}
	cfg := config{Name: "discord", ChannelSecret: secret}
	return newServer(cfg, sb, func() bool { return true }, func() int64 { return time.Now().Unix() }).handler(), sb
}

func TestServerPost(t *testing.T) {
	h, sb := stubHandler("secret")
	sb.postID = "msg-42"
	body, _ := json.Marshal(map[string]any{"chat_jid": "discord:123", "content": "hi"})
	req := httptest.NewRequest("POST", "/post", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if sb.postReq.ChatJID != "discord:123" || sb.postReq.Content != "hi" {
		t.Errorf("post req = %+v", sb.postReq)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["id"] != "msg-42" {
		t.Errorf("id = %v", resp["id"])
	}
}

func TestServerReact(t *testing.T) {
	h, sb := stubHandler("secret")
	body, _ := json.Marshal(map[string]any{
		"chat_jid": "discord:123", "target_id": "m1", "reaction": "🎉",
	})
	req := httptest.NewRequest("POST", "/react", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if sb.reactReq.TargetID != "m1" || sb.reactReq.Reaction != "🎉" {
		t.Errorf("react req = %+v", sb.reactReq)
	}
}

func TestServerDeletePost(t *testing.T) {
	h, sb := stubHandler("secret")
	body, _ := json.Marshal(map[string]any{"chat_jid": "discord:123", "target_id": "m1"})
	req := httptest.NewRequest("POST", "/delete-post", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if sb.delReq.TargetID != "m1" {
		t.Errorf("del req = %+v", sb.delReq)
	}
}

func TestServerSend(t *testing.T) {
	h, sb := stubHandler("secret")
	body, _ := json.Marshal(map[string]string{"chat_jid": "discord:123", "content": "hello"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if len(sb.sent) != 1 || sb.sent[0].Content != "hello" {
		t.Errorf("sent = %+v", sb.sent)
	}
}

func TestServerSendBadAuth(t *testing.T) {
	h, _ := stubHandler("secret")
	body, _ := json.Marshal(map[string]string{"chat_jid": "discord:123", "content": "hello"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestServerSendMissing(t *testing.T) {
	h, _ := stubHandler("")
	body, _ := json.Marshal(map[string]string{"chat_jid": "discord:123"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestServerSendFile(t *testing.T) {
	h, sb := stubHandler("secret")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("chat_jid", "discord:123")
	mw.WriteField("filename", "photo.jpg")
	fw, _ := mw.CreateFormFile("file", "photo.jpg")
	fw.Write([]byte("data"))
	mw.Close()

	req := httptest.NewRequest("POST", "/send-file", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if len(sb.files) != 1 || sb.files[0].Name != "photo.jpg" {
		t.Errorf("files = %+v", sb.files)
	}
}

func TestServerTyping(t *testing.T) {
	h, sb := stubHandler("")
	body, _ := json.Marshal(map[string]any{"chat_jid": "discord:123", "on": true})
	req := httptest.NewRequest("POST", "/typing", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if len(sb.typings) != 1 || !sb.typings[0] {
		t.Errorf("typings = %v", sb.typings)
	}
}

func TestServerHealth(t *testing.T) {
	h, _ := stubHandler("")
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" || resp["name"] != "discord" {
		t.Errorf("health = %v", resp)
	}
}

func TestServerHealthDisconnected(t *testing.T) {
	s := newServer(config{Name: "discord"}, &stubBot{}, func() bool { return false }, func() int64 { return time.Now().Unix() })
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

func TestServerNoSecret(t *testing.T) {
	h, sb := stubHandler("")
	body, _ := json.Marshal(map[string]string{"chat_jid": "discord:123", "content": "hi"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("status = %d", w.Code)
	}
	if len(sb.sent) != 1 {
		t.Errorf("sent = %d", len(sb.sent))
	}
}

func TestServerFileProxy(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("pngdata"))
	}))
	defer cdn.Close()

	srv := newServer(config{Name: "discord", ChannelSecret: "secret"}, &stubBot{}, func() bool { return true }, func() int64 { return time.Now().Unix() })
	id := srv.files.Put(cdn.URL + "/image.png")
	h := srv.handler()

	req := httptest.NewRequest("GET", "/files/"+id, nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Body.String() != "pngdata" {
		t.Errorf("body = %q", w.Body.String())
	}
	if w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("ct = %q", w.Header().Get("Content-Type"))
	}
}

func TestServerFileProxyNotFound(t *testing.T) {
	srv := newServer(config{Name: "discord", ChannelSecret: "secret"}, &stubBot{}, func() bool { return true }, func() int64 { return time.Now().Unix() })
	h := srv.handler()
	req := httptest.NewRequest("GET", "/files/missing", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestServerFileProxyAuth(t *testing.T) {
	srv := newServer(config{Name: "discord", ChannelSecret: "secret"}, &stubBot{}, func() bool { return true }, func() int64 { return time.Now().Unix() })
	srv.files.Put("http://example.com/img.jpg")
	h := srv.handler()
	req := httptest.NewRequest("GET", "/files/abc", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("status = %d", w.Code)
	}
}
