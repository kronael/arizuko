package main

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/onvos/arizuko/chanlib"
)

type creator interface {
	createPost(ctx context.Context, text, replyParentURI string) error
}

type server struct {
	cfg config
	bc  creator
}

func newServer(cfg config, bc creator) *server { return &server{cfg: cfg, bc: bc} }

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", chanlib.Auth(s.cfg.ChannelSecret, s.handleSend))
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
		chanlib.WriteErr(w, 400, "chat_jid and content required")
		return
	}
	if err := s.bc.createPost(context.Background(), req.Content, req.ReplyTo); err != nil {
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
		"jid_prefixes": []string{"bluesky:"},
	})
}
