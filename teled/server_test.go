package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/onvos/arizuko/chanlib"
)

type stubSend struct{ JID, Content string }
type stubFile struct{ JID, Name string }

type stubBot struct {
	sent    []stubSend
	files   []stubFile
	typings []bool
}

func (b *stubBot) Send(req chanlib.SendRequest) (string, error) {
	b.sent = append(b.sent, stubSend{req.ChatJID, req.Content})
	return "", nil
}

func (b *stubBot) SendFile(jid, _, name, _ string) error {
	b.files = append(b.files, stubFile{jid, name})
	return nil
}

func (b *stubBot) Typing(_ string, on bool) { b.typings = append(b.typings, on) }

func stubHandler(secret string) (http.Handler, *stubBot) {
	sb := &stubBot{}
	return newServer(config{Name: "telegram", ChannelSecret: secret}, sb).handler(), sb
}

func testHandler(secret string) (http.Handler, *server) {
	s := newServer(config{Name: "telegram", ChannelSecret: secret}, &stubBot{})
	return s.handler(), s
}

func TestServerSend(t *testing.T) {
	h, sb := stubHandler("secret")
	body, _ := json.Marshal(map[string]string{"chat_jid": "telegram:123", "content": "hello"})
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
	body, _ := json.Marshal(map[string]string{"chat_jid": "telegram:123", "content": "hello"})
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
	body, _ := json.Marshal(map[string]string{"chat_jid": "telegram:123"})
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
	mw.WriteField("chat_jid", "telegram:123")
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
	body, _ := json.Marshal(map[string]any{"chat_jid": "telegram:123", "on": true})
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
	if resp["status"] != "ok" || resp["name"] != "telegram" {
		t.Errorf("health = %v", resp)
	}
}

func TestServerFileEmptyID(t *testing.T) {
	// /files/ with no file_id returns 400 (after auth)
	h, _ := testHandler("secret")
	req := httptest.NewRequest("GET", "/files/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("status = %d, want 400 for empty file_id", w.Code)
	}
}

func TestServerFileNoAuth(t *testing.T) {
	h, _ := testHandler("secret")
	req := httptest.NewRequest("GET", "/files/abc123", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("status = %d, want 401 for missing auth", w.Code)
	}
}

func TestServerNoSecret(t *testing.T) {
	h, sb := stubHandler("")
	body, _ := json.Marshal(map[string]string{"chat_jid": "telegram:123", "content": "hi"})
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
