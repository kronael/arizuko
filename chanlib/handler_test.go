package chanlib

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

type mockBot struct {
	sendReq  SendRequest
	sendID   string
	sendErr  error
	fileJID  string
	fileName string
	fileErr  error
	typJID   string
	typOn    bool
}

func (m *mockBot) Send(req SendRequest) (string, error) {
	m.sendReq = req
	return m.sendID, m.sendErr
}

func (m *mockBot) SendFile(jid, _, name, _ string) error {
	m.fileJID = jid
	m.fileName = name
	return m.fileErr
}

func (m *mockBot) Typing(jid string, on bool) {
	m.typJID = jid
	m.typOn = on
}

func mux(bot *mockBot) http.Handler {
	return NewAdapterMux("test", "secret", []string{"test:"}, bot)
}

func TestHandlerSend(t *testing.T) {
	bot := &mockBot{sendID: "msg-1"}
	h := mux(bot)
	body, _ := json.Marshal(map[string]string{
		"chat_jid": "test:1", "content": "hi", "reply_to": "r1", "thread_id": "t1",
	})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["ok"] != true {
		t.Errorf("ok = %v", resp["ok"])
	}
	if resp["id"] != "msg-1" {
		t.Errorf("id = %v", resp["id"])
	}
	if bot.sendReq.ChatJID != "test:1" || bot.sendReq.Content != "hi" {
		t.Errorf("send req = %+v", bot.sendReq)
	}
	if bot.sendReq.ReplyTo != "r1" || bot.sendReq.ThreadID != "t1" {
		t.Errorf("send req = %+v", bot.sendReq)
	}
}

func TestHandlerSendNoID(t *testing.T) {
	bot := &mockBot{sendID: ""}
	h := mux(bot)
	body, _ := json.Marshal(map[string]string{"chat_jid": "test:1", "content": "hi"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["id"]; ok {
		t.Errorf("id should be absent, got %v", resp["id"])
	}
}

func TestHandlerSendMissing(t *testing.T) {
	bot := &mockBot{}
	h := mux(bot)
	body, _ := json.Marshal(map[string]string{"chat_jid": "test:1"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandlerSendError(t *testing.T) {
	bot := &mockBot{sendErr: errors.New("fail")}
	h := mux(bot)
	body, _ := json.Marshal(map[string]string{"chat_jid": "test:1", "content": "hi"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 502 {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}

func TestHandlerSendAuth(t *testing.T) {
	bot := &mockBot{}
	h := mux(bot)
	body, _ := json.Marshal(map[string]string{"chat_jid": "test:1", "content": "hi"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestHandlerSendFile(t *testing.T) {
	bot := &mockBot{}
	h := mux(bot)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("chat_jid", "test:1")
	mw.WriteField("filename", "pic.jpg")
	fw, _ := mw.CreateFormFile("file", "pic.jpg")
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
	if bot.fileJID != "test:1" || bot.fileName != "pic.jpg" {
		t.Errorf("file = %q %q", bot.fileJID, bot.fileName)
	}
}

func TestHandlerSendFileMissingJID(t *testing.T) {
	bot := &mockBot{}
	h := mux(bot)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("filename", "pic.jpg")
	fw, _ := mw.CreateFormFile("file", "pic.jpg")
	fw.Write([]byte("data"))
	mw.Close()

	req := httptest.NewRequest("POST", "/send-file", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandlerSendFileError(t *testing.T) {
	bot := &mockBot{fileErr: errors.New("boom")}
	h := mux(bot)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("chat_jid", "test:1")
	fw, _ := mw.CreateFormFile("file", "pic.jpg")
	fw.Write([]byte("data"))
	mw.Close()

	req := httptest.NewRequest("POST", "/send-file", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 502 {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}

func TestHandlerTyping(t *testing.T) {
	for _, tt := range []struct {
		name string
		body any
		code int
		jid  string
		on   bool
	}{
		{"on", map[string]any{"chat_jid": "test:1", "on": true}, 200, "test:1", true},
		{"off", map[string]any{"chat_jid": "test:1", "on": false}, 200, "test:1", false},
		{"missing_jid", map[string]any{"on": true}, 400, "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bot := &mockBot{}
			h := mux(bot)
			b, _ := json.Marshal(tt.body)
			req := httptest.NewRequest("POST", "/typing", bytes.NewReader(b))
			req.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tt.code {
				t.Fatalf("status = %d, want %d", w.Code, tt.code)
			}
			if tt.code == 200 && (bot.typJID != tt.jid || bot.typOn != tt.on) {
				t.Errorf("typing = %q %v, want %q %v", bot.typJID, bot.typOn, tt.jid, tt.on)
			}
		})
	}
}

func TestHandlerTypingInvalidJSON(t *testing.T) {
	bot := &mockBot{}
	h := mux(bot)
	req := httptest.NewRequest("POST", "/typing", bytes.NewReader([]byte("not json")))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandlerHealth(t *testing.T) {
	bot := &mockBot{}
	h := mux(bot)
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("status = %v", resp["status"])
	}
	if resp["name"] != "test" {
		t.Errorf("name = %v", resp["name"])
	}
}
