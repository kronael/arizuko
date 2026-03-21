package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/onvos/arizuko/chanlib"
)

type server struct {
	cfg config
	bot *bot
}

func newServer(cfg config, b *bot) *server { return &server{cfg: cfg, bot: b} }

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", chanlib.Auth(s.cfg.ChannelSecret, s.handleSend))
	mux.HandleFunc("POST /send-file", chanlib.Auth(s.cfg.ChannelSecret, s.handleSendFile))
	mux.HandleFunc("POST /typing", chanlib.Auth(s.cfg.ChannelSecret, s.handleTyping))
	mux.HandleFunc("GET /health", s.handleHealth)
	return mux
}

func (s *server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatJID string `json:"chat_jid"`
		Content string `json:"content"`
		ReplyTo string `json:"reply_to"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.Content == "" {
		writeErr(w, 400, "chat_jid and content required")
		return
	}
	sentID, err := s.bot.send(req.ChatJID, req.Content, req.ReplyTo)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": sentID})
}

func (s *server) handleSendFile(w http.ResponseWriter, r *http.Request) {
	if r.ParseMultipartForm(50<<20) != nil {
		writeErr(w, 400, "invalid multipart")
		return
	}
	jid, name := r.FormValue("chat_jid"), r.FormValue("filename")
	if jid == "" {
		writeErr(w, 400, "chat_jid required")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "file required")
		return
	}
	defer file.Close()
	if name == "" {
		name = hdr.Filename
	}
	tmp, err := os.CreateTemp("", "tg-*"+filepath.Ext(name))
	if err != nil {
		writeErr(w, 500, "temp file failed")
		return
	}
	defer os.Remove(tmp.Name())
	io.Copy(tmp, file)
	tmp.Close()
	if err := s.bot.sendFile(jid, tmp.Name(), name); err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) handleTyping(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatJID string `json:"chat_jid"`
		On      bool   `json:"on"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	s.bot.typing(req.ChatJID, req.On)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"status": "ok", "name": s.cfg.Name,
		"jid_prefixes": []string{"telegram:"},
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}
