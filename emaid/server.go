package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"mime"
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

	data, err := s.fetchPart(imap.UID(uid), meta.Part, s.cfg.MaxAttachment)
	if err != nil {
		slog.Error("imap re-fetch failed", "uid", uid, "part", part, "err", err)
		chanlib.WriteErr(w, 502, "failed to fetch attachment from IMAP")
		return
	}

	w.Header().Set("Content-Type", meta.Mime)
	w.Header().Set("Content-Disposition", dispositionHeader(meta.Filename))
	w.Write(data) //nolint:errcheck
}

// dispositionHeader builds a safe Content-Disposition value.
// Strips CR/LF (header-injection vector), escapes quotes, and RFC 2047
// Q-encodes non-ASCII characters. ASCII-only filenames pass through
// unchanged so existing callers see "attachment; filename=\"doc.pdf\"".
func dispositionHeader(filename string) string {
	filename = strings.ReplaceAll(filename, "\r", "")
	filename = strings.ReplaceAll(filename, "\n", "")
	safe := strings.ReplaceAll(filename, `\`, `\\`)
	safe = strings.ReplaceAll(safe, `"`, `\"`)
	encoded := mime.QEncoding.Encode("utf-8", safe)
	return fmt.Sprintf(`attachment; filename="%s"`, encoded)
}

// fetchPart connects to IMAP and fetches a specific MIME part by UID.
// If maxBytes > 0, the returned data is truncated (not padded) to the cap
// to prevent a remote sender from ballooning this adapter's RSS via
// large attachments.
func (s *server) fetchPart(uid imap.UID, section []int, maxBytes int64) ([]byte, error) {
	c, err := imapConnect(s.cfg, s.dialTLS, nil)
	if err != nil {
		return nil, err
	}
	defer c.Logout()

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
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		data = data[:maxBytes]
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
