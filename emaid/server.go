package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/onvos/arizuko/chanlib"
)

type server struct {
	chanlib.NoFileSender
	cfg   config
	db    *sql.DB
	files *attachCache
}

func newServer(cfg config, db *sql.DB, files *attachCache) *server {
	return &server{cfg: cfg, db: db, files: files}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"email:"}, s)
	mux.HandleFunc("GET /files/", chanlib.Auth(s.cfg.ChannelSecret, s.handleFile))
	return mux
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	// path: /files/{uid}/{part}
	path := strings.TrimPrefix(r.URL.Path, "/files/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		chanlib.WriteErr(w, 400, "path must be /files/{uid}/{part}")
		return
	}
	uid, err1 := strconv.Atoi(parts[0])
	part, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		chanlib.WriteErr(w, 400, "uid and part must be integers")
		return
	}
	key := fmt.Sprintf("%d-%d", uid, part)
	a, ok := s.files.Get(key)
	if !ok {
		chanlib.WriteErr(w, 404, "attachment not found")
		return
	}
	if a.Mime != "" {
		w.Header().Set("Content-Type", a.Mime)
	}
	if a.Filename != "" {
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="%s"`, a.Filename))
	}
	w.Write(a.Data)
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
