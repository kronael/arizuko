package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

type server struct {
	cfg config
	db  *sql.DB
}

func newServer(cfg config, db *sql.DB) *server { return &server{cfg: cfg, db: db} }

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
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.Content == "" {
		writeErr(w, 400, "chat_jid and content required")
		return
	}

	threadID := strings.TrimPrefix(req.ChatJID, "email:")
	var thread emailThread
	row := s.db.QueryRow(`SELECT thread_id, from_address, root_msg_id FROM email_threads WHERE thread_id = ?`, threadID)
	if err := row.Scan(&thread.ThreadID, &thread.FromAddress, &thread.RootMsgID); err != nil {
		writeErr(w, 404, "thread not found")
		return
	}

	if err := sendReply(s.cfg, thread.FromAddress, thread.RootMsgID, req.Content); err != nil {
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
		"jid_prefixes": []string{"email:"},
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
