package chanlib

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type SendRequest struct {
	ChatJID  string `json:"chat_jid"`
	Content  string `json:"content"`
	ReplyTo  string `json:"reply_to"`
	ThreadID string `json:"thread_id"`
}

// BotHandler is the interface adapters implement for outbound messaging.
// Send returns the sent message ID (may be ""); Typing is fire-and-forget.
type BotHandler interface {
	Send(req SendRequest) (string, error)
	SendFile(jid, path, name, caption string) error
	Typing(jid string, on bool)
}

// NoFileSender is an embeddable stub for adapters that don't support file sending.
type NoFileSender struct{}

func (NoFileSender) SendFile(_, _, _, _ string) error { return errSendFile }

var errSendFile = errors.New("send-file not supported")

func NewAdapterMux(name, secret string, prefixes []string, bot BotHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", Auth(secret, handleSend(bot)))
	mux.HandleFunc("POST /send-file", Auth(secret, handleSendFile(bot)))
	mux.HandleFunc("POST /typing", Auth(secret, handleTyping(bot)))
	mux.HandleFunc("GET /health", handleHealth(name, prefixes))
	return mux
}

func handleSend(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req SendRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.Content == "" {
			WriteErr(w, 400, "chat_jid and content required")
			return
		}
		id, err := bot.Send(req)
		if err != nil {
			WriteErr(w, 502, err.Error())
			return
		}
		resp := map[string]any{"ok": true}
		if id != "" {
			resp["id"] = id
		}
		WriteJSON(w, resp)
	}
}

func handleSendFile(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.ParseMultipartForm(50<<20) != nil {
			WriteErr(w, 400, "invalid multipart")
			return
		}
		jid, name, caption := r.FormValue("chat_jid"), r.FormValue("filename"), r.FormValue("caption")
		if jid == "" {
			WriteErr(w, 400, "chat_jid required")
			return
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			WriteErr(w, 400, "file required")
			return
		}
		defer file.Close()
		if name == "" {
			name = hdr.Filename
		}
		// Strip directory components and reject traversal tokens.
		// `name` is attacker-controlled (multipart form value / filename
		// header); without this, filepath.Join(dir, name) can escape
		// the temp dir.
		name = filepath.Base(filepath.Clean(name))
		if name == "" || name == "." || name == ".." || strings.Contains(name, "..") || strings.ContainsRune(name, os.PathSeparator) {
			WriteErr(w, 400, "invalid filename")
			return
		}
		dir, err := os.MkdirTemp("", "chan-")
		if err != nil {
			WriteErr(w, 500, "temp dir failed")
			return
		}
		defer os.RemoveAll(dir)
		localPath := filepath.Join(dir, name)
		tmp, err := os.Create(localPath)
		if err != nil {
			WriteErr(w, 500, "temp file failed")
			return
		}
		io.Copy(tmp, file)
		tmp.Close()
		if err := bot.SendFile(jid, localPath, name, caption); err != nil {
			WriteErr(w, 502, err.Error())
			return
		}
		WriteJSON(w, map[string]any{"ok": true})
	}
}

func handleTyping(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ChatJID string `json:"chat_jid"`
			On      bool   `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			slog.Warn("typing: decode failed", "err", err)
			WriteErr(w, 400, "invalid json body")
			return
		}
		if req.ChatJID == "" {
			WriteErr(w, 400, "chat_jid required")
			return
		}
		bot.Typing(req.ChatJID, req.On)
		WriteJSON(w, map[string]any{"ok": true})
	}
}

func handleHealth(name string, prefixes []string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, map[string]any{
			"status": "ok", "name": name, "jid_prefixes": prefixes,
		})
	}
}
