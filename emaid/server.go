package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/onvos/arizuko/chanlib"
)

type server struct {
	chanlib.NoFileSender
	cfg config
	db  *sql.DB
}

func newServer(cfg config, db *sql.DB) *server { return &server{cfg: cfg, db: db} }

func (s *server) handler() http.Handler {
	return chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"email:"}, s)
}

func (s *server) Send(req chanlib.SendRequest) (string, error) {
	threadID := strings.TrimPrefix(req.ChatJID, "email:")
	var t emailThread
	row := s.db.QueryRow(
		`SELECT thread_id, from_address, root_msg_id FROM email_threads WHERE thread_id = ?`, threadID)
	if err := row.Scan(&t.ThreadID, &t.FromAddress, &t.RootMsgID); err != nil {
		return "", fmt.Errorf("thread not found")
	}
	return "", sendReply(s.cfg, t.FromAddress, t.RootMsgID, req.Content)
}

func (s *server) Typing(string, bool) {}
