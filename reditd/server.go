package main

import (
	"encoding/json"
	"net/http"

	"github.com/onvos/arizuko/chanlib"
)

type server struct {
	cfg config
	rc  *redditClient
}

func newServer(cfg config, rc *redditClient) *server { return &server{cfg: cfg, rc: rc} }

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", chanlib.Auth(s.cfg.ChannelSecret, s.handleSend))
	mux.HandleFunc("POST /typing", chanlib.Auth(s.cfg.ChannelSecret, s.handleTyping))
	mux.HandleFunc("GET /health", s.handleHealth)
	return mux
}

func (s *server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatJID  string `json:"chat_jid"`
		Content  string `json:"content"`
		ReplyTo  string `json:"reply_to"`
		ThreadID string `json:"thread_id"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.Content == "" {
		chanlib.WriteErr(w, 400, "chat_jid and content required")
		return
	}
	var err error
	if req.ReplyTo != "" {
		err = s.rc.comment(req.ReplyTo, req.Content)
	} else {
		err = s.rc.submit(req.Content)
	}
	if err != nil {
		chanlib.WriteErr(w, 502, err.Error())
		return
	}
	chanlib.WriteJSON(w, map[string]any{"ok": true})
}

func (s *server) handleTyping(w http.ResponseWriter, _ *http.Request) {
	chanlib.WriteJSON(w, map[string]any{"ok": true})
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	chanlib.WriteJSON(w, map[string]any{
		"status": "ok", "name": s.cfg.Name,
		"jid_prefixes": []string{"reddit:"},
	})
}
