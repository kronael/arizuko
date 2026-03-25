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

// testHandler builds an HTTP handler with a mock bot for testing.
// Uses the same server struct but bot methods are never called
// (telegram API not available), so we test the HTTP layer only.
// For bot integration we use e2e_test.go with the router mock.

func testHandler(secret string) (http.Handler, *server) {
	cfg := config{Name: "telegram", ChannelSecret: secret}
	// bot is nil — handlers will panic if they reach telegram API,
	// which is correct: server_test only tests HTTP validation/auth.
	s := &server{cfg: cfg, bot: nil}
	return s.handler(), s
}

// stubServer creates a handler that intercepts before calling bot methods.
type stubBot struct {
	sent    []stubSend
	files   []stubFile
	typings []bool
}

type stubSend struct{ JID, Content string }
type stubFile struct{ JID, Name string }

func stubHandler(secret string) (http.Handler, *stubBot) {
	sb := &stubBot{}
	cfg := config{Name: "telegram", ChannelSecret: secret}
	mux := http.NewServeMux()

	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if secret != "" {
				tok := r.Header.Get("Authorization")
				if tok != "Bearer "+secret {
					chanlib.WriteErr(w, 401, "invalid secret")
					return
				}
			}
			next(w, r)
		}
	}

	mux.HandleFunc("POST /send", auth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ChatJID string `json:"chat_jid"`
			Content string `json:"content"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.Content == "" {
			chanlib.WriteErr(w, 400, "chat_jid and content required")
			return
		}
		sb.sent = append(sb.sent, stubSend{req.ChatJID, req.Content})
		chanlib.WriteJSON(w, map[string]any{"ok": true})
	}))
	mux.HandleFunc("POST /send-file", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.ParseMultipartForm(50<<20) != nil {
			chanlib.WriteErr(w, 400, "invalid multipart")
			return
		}
		jid, name := r.FormValue("chat_jid"), r.FormValue("filename")
		if jid == "" {
			chanlib.WriteErr(w, 400, "chat_jid required")
			return
		}
		_, hdr, err := r.FormFile("file")
		if err != nil {
			chanlib.WriteErr(w, 400, "file required")
			return
		}
		if name == "" {
			name = hdr.Filename
		}
		sb.files = append(sb.files, stubFile{jid, name})
		chanlib.WriteJSON(w, map[string]any{"ok": true})
	}))
	mux.HandleFunc("POST /typing", auth(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ On bool `json:"on"` }
		json.NewDecoder(r.Body).Decode(&req)
		sb.typings = append(sb.typings, req.On)
		chanlib.WriteJSON(w, map[string]any{"ok": true})
	}))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		chanlib.WriteJSON(w, map[string]any{
			"status": "ok", "name": cfg.Name,
			"jid_prefixes": []string{"telegram:"},
		})
	})

	return mux, sb
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
