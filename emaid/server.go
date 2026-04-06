package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/onvos/arizuko/chanlib"
)

type server struct {
	chanlib.NoFileSender
	cfg     config
	db      *sql.DB
	reg     *attRegistry
	dialTLS func(string, *imapclient.Options) (*imapclient.Client, error)
}

func newServer(cfg config, db *sql.DB, reg *attRegistry) *server {
	return &server{cfg: cfg, db: db, reg: reg, dialTLS: imapclient.DialTLS}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"email:"}, s)
	mux.HandleFunc("GET /files/", chanlib.Auth(s.cfg.ChannelSecret, s.handleFile))
	return mux
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/files/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		chanlib.WriteErr(w, 400, "path must be /files/{uid}/{part}")
		return
	}
	uid, err1 := strconv.ParseUint(parts[0], 10, 32)
	part, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		chanlib.WriteErr(w, 400, "uid and part must be integers")
		return
	}

	key := fmt.Sprintf("%d-%d", uid, part)
	meta, ok := s.reg.get(key)
	if !ok {
		chanlib.WriteErr(w, 404, "attachment not found")
		return
	}

	data, err := s.fetchPart(imap.UID(uid), meta.Part)
	if err != nil {
		slog.Error("imap re-fetch failed", "uid", uid, "part", part, "err", err)
		chanlib.WriteErr(w, 502, "failed to fetch attachment from IMAP")
		return
	}

	w.Header().Set("Content-Type", meta.Mime)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, meta.Filename))
	w.Write(data) //nolint:errcheck
}

// fetchPart connects to IMAP and fetches a specific MIME part by UID.
func (s *server) fetchPart(uid imap.UID, section []int) ([]byte, error) {
	addr := s.cfg.IMAPHost + ":" + s.cfg.IMAPPort
	c, err := s.dialTLS(addr, nil)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer c.Logout()

	if err := c.Login(s.cfg.Account, s.cfg.Password).Wait(); err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		return nil, fmt.Errorf("select: %w", err)
	}

	fetchOpts := &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{
			{Specifier: imap.PartSpecifierNone, Part: section},
		},
	}
	msgs, err := c.Fetch(imap.UIDSetNum(uid), fetchOpts).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("no message for uid %d", uid)
	}

	data := msgs[0].FindBodySection(&imap.FetchItemBodySection{Part: section})
	if data == nil {
		return nil, fmt.Errorf("no body section %v for uid %d", section, uid)
	}
	return data, nil
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
