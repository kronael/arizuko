package chanlib

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
	NoSocial
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
	return NewAdapterMux("test", "secret", []string{"test:"}, bot,
		func() bool { return true }, func() int64 { return time.Now().Unix() })
}

func muxConn(bot *mockBot, connected func() bool) http.Handler {
	return NewAdapterMux("test", "secret", []string{"test:"}, bot, connected,
		func() int64 { return time.Now().Unix() })
}

func muxFull(bot *mockBot, connected func() bool, lastInbound func() int64) http.Handler {
	return NewAdapterMux("test", "secret", []string{"test:"}, bot, connected, lastInbound)
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
	for _, tt := range []struct {
		name      string
		connected bool
		wantCode  int
		wantStat  string
	}{
		{"connected", true, 200, "ok"},
		{"disconnected", false, 503, "disconnected"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bot := &mockBot{}
			connected := tt.connected
			h := muxConn(bot, func() bool { return connected })
			req := httptest.NewRequest("GET", "/health", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantCode)
			}
			var resp map[string]any
			json.NewDecoder(w.Body).Decode(&resp)
			if resp["status"] != tt.wantStat {
				t.Errorf("status = %v, want %v", resp["status"], tt.wantStat)
			}
			if resp["name"] != "test" {
				t.Errorf("name = %v", resp["name"])
			}
		})
	}
}

func TestHandlerPanicsOnNilIsConnected(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when isConnected is nil")
		}
	}()
	NewAdapterMux("test", "secret", []string{"test:"}, &mockBot{}, nil,
		func() int64 { return 0 })
}

func TestHandlerPanicsOnNilLastInboundAt(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when lastInboundAt is nil")
		}
	}()
	NewAdapterMux("test", "secret", []string{"test:"}, &mockBot{},
		func() bool { return true }, nil)
}

func TestHandlerHealthStale(t *testing.T) {
	bot := &mockBot{}
	old := time.Now().Add(-10 * time.Minute).Unix()
	h := muxFull(bot, func() bool { return true }, func() int64 { return old })
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "stale" {
		t.Errorf("status = %v, want stale", resp["status"])
	}
	if _, ok := resp["stale_seconds"]; !ok {
		t.Errorf("stale_seconds missing")
	}
}

func TestHandlerHealthDisconnectedBeatsStale(t *testing.T) {
	bot := &mockBot{}
	old := time.Now().Add(-10 * time.Minute).Unix()
	h := muxFull(bot, func() bool { return false }, func() int64 { return old })
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "disconnected" {
		t.Errorf("status = %v, want disconnected", resp["status"])
	}
}

// TestHandlerPostUnsupportedStructured asserts that when bot.Post returns
// *UnsupportedError, the 501 body carries tool/platform/hint.
func TestHandlerPostUnsupportedStructured(t *testing.T) {
	b := &mockBotPost{err: Unsupported("post", "test", "use send instead")}
	h := mux(&mockBot{}) // need a fresh mux pointing at b
	_ = h
	mux2 := NewAdapterMux("test", "secret", []string{"test:"}, b,
		func() bool { return true }, func() int64 { return time.Now().Unix() })
	body, _ := json.Marshal(map[string]string{"chat_jid": "test:1", "content": "x"})
	req := httptest.NewRequest("POST", "/post", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	mux2.ServeHTTP(w, req)
	if w.Code != 501 {
		t.Fatalf("status = %d, want 501", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["tool"] != "post" || resp["platform"] != "test" || resp["hint"] != "use send instead" {
		t.Errorf("body = %v", resp)
	}
}

// TestHandlerPostUnsupportedPlain asserts that plain ErrUnsupported still
// produces the legacy {"ok":false,"error":"unsupported"} body.
func TestHandlerPostUnsupportedPlain(t *testing.T) {
	b := &mockBotPost{err: ErrUnsupported}
	mux2 := NewAdapterMux("test", "secret", []string{"test:"}, b,
		func() bool { return true }, func() int64 { return time.Now().Unix() })
	body, _ := json.Marshal(map[string]string{"chat_jid": "test:1", "content": "x"})
	req := httptest.NewRequest("POST", "/post", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	mux2.ServeHTTP(w, req)
	if w.Code != 501 {
		t.Fatalf("status = %d, want 501", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "unsupported" {
		t.Errorf("body = %v", resp)
	}
	if _, has := resp["hint"]; has {
		t.Errorf("plain unsupported should not carry hint, got %v", resp)
	}
}

// TestUnsupportedErrorIs verifies errors.Is chain through structured value.
func TestUnsupportedErrorIs(t *testing.T) {
	e := Unsupported("forward", "mastodon", "no native primitive")
	if !errors.Is(e, ErrUnsupported) {
		t.Error("structured UnsupportedError must satisfy errors.Is(ErrUnsupported)")
	}
}

type mockBotPost struct {
	NoFileSender
	NoSocial
	err error
}

func (m *mockBotPost) Send(SendRequest) (string, error)    { return "", nil }
func (m *mockBotPost) Typing(string, bool)                 {}
func (m *mockBotPost) Post(PostRequest) (string, error)    { return "", m.err }
