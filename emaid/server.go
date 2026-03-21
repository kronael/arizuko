package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/onvos/arizuko/chanlib"
)

type server struct {
	cfg config
	db  *sql.DB
}

func newServer(cfg config, db *sql.DB) *server { return &server{cfg: cfg, db: db} }

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
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.Content == "" {
		chanlib.WriteErr(w, 400, "chat_jid and content required")
		return
	}

	threadID := strings.TrimPrefix(req.ChatJID, "email:")
	var thread emailThread
	row := s.db.QueryRow(`SELECT thread_id, from_address, root_msg_id FROM email_threads WHERE thread_id = ?`, threadID)
	if err := row.Scan(&thread.ThreadID, &thread.FromAddress, &thread.RootMsgID); err != nil {
		chanlib.WriteErr(w, 404, "thread not found")
		return
	}

	if err := sendReply(s.cfg, thread.FromAddress, thread.RootMsgID, req.Content); err != nil {
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
		"jid_prefixes": []string{"email:"},
	})
}
