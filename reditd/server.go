package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

type server struct {
	cfg config
	rc  *redditClient
}

func newServer(cfg config, rc *redditClient) *server { return &server{cfg: cfg, rc: rc} }

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", s.auth(s.handleSend))
	mux.HandleFunc("POST /typing", s.auth(s.handleTyping))
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
	var err error
	if req.ReplyTo != "" {
		err = s.rc.comment(req.ReplyTo, req.Content)
	} else {
		err = s.rc.submit(req.Content)
	}
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) handleTyping(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"status": "ok", "name": s.cfg.Name,
		"jid_prefixes": []string{"reddit:"},
	})
}

func (s *server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.ChannelSecret != "" {
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if tok != s.cfg.ChannelSecret {
				writeErr(w, 401, "invalid secret")
				return
			}
		}
		next(w, r)
	}
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
