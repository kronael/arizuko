package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/onvos/arizuko/chanlib"
)

const historyLimitMax = 50

type server struct {
	chanlib.NoFileSender
	chanlib.NoSocial
	cfg           config
	db            *sql.DB
	reg           *attRegistry
	dialTLS       func(string, *imapclient.Options) (*imapclient.Client, error)
	isConnected   func() bool
	lastInboundAt func() int64
}

func newServer(cfg config, db *sql.DB, reg *attRegistry, isConnected func() bool, lastInboundAt func() int64) *server {
	return &server{cfg: cfg, db: db, reg: reg, dialTLS: imapclient.DialTLS, isConnected: isConnected, lastInboundAt: lastInboundAt}
}

func (s *server) handler() http.Handler {
	mux := chanlib.NewAdapterMux(s.cfg.Name, s.cfg.ChannelSecret, []string{"email:"}, s, s.isConnected, s.lastInboundAt)
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

// FetchHistory searches IMAP for messages in the requested thread before
// the given date. `Before` is date-only per IMAP spec. Limit clamps at 50.
// Returns `platform` source with the most recent `Limit` messages.
func (s *server) FetchHistory(req chanlib.HistoryRequest) (chanlib.HistoryResponse, error) {
	threadID := strings.TrimPrefix(req.ChatJID, "email:")
	var t emailThread
	row := s.db.QueryRow(
		`SELECT thread_id, from_address, root_msg_id FROM email_threads WHERE thread_id = ?`, threadID)
	if err := row.Scan(&t.ThreadID, &t.FromAddress, &t.RootMsgID); err != nil {
		return chanlib.HistoryResponse{}, fmt.Errorf("thread not found: %s", threadID)
	}

	limit := req.Limit
	if limit <= 0 || limit > historyLimitMax {
		limit = historyLimitMax
	}

	c, err := imapConnect(s.cfg, s.dialTLS, nil)
	if err != nil {
		return chanlib.HistoryResponse{}, fmt.Errorf("imap: %w", err)
	}
	defer c.Logout()

	// Match messages that belong to the thread: either the root itself, or any
	// reply whose References header contains the root Message-ID.
	rootHdr := "<" + t.RootMsgID + ">"
	criteria := &imap.SearchCriteria{
		Or: [][2]imap.SearchCriteria{{
			{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: rootHdr}}},
			{Header: []imap.SearchCriteriaHeaderField{{Key: "References", Value: t.RootMsgID}}},
		}},
	}
	if !req.Before.IsZero() {
		criteria.Before = req.Before
	}

	searchData, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return chanlib.HistoryResponse{}, fmt.Errorf("search: %w", err)
	}
	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return chanlib.HistoryResponse{Source: "platform", Messages: []chanlib.InboundMsg{}}, nil
	}
	// Keep the most recent `limit` UIDs.
	if len(uids) > limit {
		uids = uids[len(uids)-limit:]
	}

	fetchOpts := &imap.FetchOptions{
		Envelope:    true,
		BodySection: []*imap.FetchItemBodySection{{}},
		UID:         true,
	}
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), fetchOpts).Collect()
	if err != nil {
		return chanlib.HistoryResponse{}, fmt.Errorf("fetch: %w", err)
	}

	out := make([]chanlib.InboundMsg, 0, len(msgs))
	for _, m := range msgs {
		im, ok := fetchMsgToInbound(m, threadID, s.cfg.ListenURL, s.reg, s.cfg.MaxAttachment)
		if ok {
			out = append(out, im)
		}
	}
	return chanlib.HistoryResponse{Source: "platform", Messages: out}, nil
}

// fetchMsgToInbound converts an IMAP FetchMessageBuffer into an InboundMsg
// shape identical to the live poller's delivery. Factored out so history
// uses the same conversion as inbound (no drift).
func fetchMsgToInbound(
	msg *imapclient.FetchMessageBuffer,
	threadID, listenURL string,
	reg *attRegistry,
	maxBytes int64,
) (chanlib.InboundMsg, bool) {
	if msg.Envelope == nil {
		return chanlib.InboundMsg{}, false
	}
	env := msg.Envelope
	msgID := strings.Trim(env.MessageID, "<>")
	if msgID == "" {
		msgID = fmt.Sprintf("uid-%d", msg.UID)
	}
	var fromAddr, fromName string
	if len(env.From) > 0 {
		fromAddr = env.From[0].Addr()
		fromName = env.From[0].Name
		if fromName == "" {
			fromName = fromAddr
		}
	}
	bodyRaw := msg.FindBodySection(&imap.FetchItemBodySection{})
	body := ""
	var atts []chanlib.InboundAttachment
	if bodyRaw != nil {
		body, atts = extractContent(bodyRaw, msg.UID, listenURL, reg, maxBytes)
	}
	toAddr := ""
	if len(env.To) > 0 {
		toAddr = env.To[0].Addr()
	}
	content := fmt.Sprintf("From: %s <%s>\nSubject: %s\nDate: %s\nTo: %s\n\n%s",
		fromName, fromAddr, env.Subject, env.Date.Format(time.RFC1123Z), toAddr, body)
	for _, a := range atts {
		content += fmt.Sprintf(" [Attachment: %s]", a.Filename)
	}
	return chanlib.InboundMsg{
		ID:          msgID,
		ChatJID:     "email:" + threadID,
		Sender:      "email:" + fromAddr,
		SenderName:  fromName,
		Content:     content,
		Timestamp:   env.Date.Unix(),
		Attachments: atts,
	}, true
}
