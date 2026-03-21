package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomessage "github.com/emersion/go-message/mail"
)

type poller struct {
	cfg config
	db  *sql.DB
}

func newPoller(cfg config, db *sql.DB) *poller {
	return &poller{cfg: cfg, db: db}
}

func (p *poller) run(ctx context.Context, rc *routerClient) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := p.poll(ctx, rc); err != nil {
			slog.Error("imap poll error", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 60*time.Second)
			continue
		}
		backoff = time.Second
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

func (p *poller) poll(ctx context.Context, rc *routerClient) error {
	addr := p.cfg.IMAPHost + ":" + p.cfg.IMAPPort
	c, err := imapclient.DialTLS(addr, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer c.Logout()

	if err := c.Login(p.cfg.Account, p.cfg.Password).Wait(); err != nil {
		return fmt.Errorf("login: %w", err)
	}

	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		return fmt.Errorf("select: %w", err)
	}

	criteria := &imap.SearchCriteria{
		NotFlag: []imap.Flag{imap.FlagSeen},
	}
	// Use UID search so IDs are stable across reconnects.
	searchData, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return nil
	}

	uidSet := imap.UIDSetNum(uids...)
	fetchOpts := &imap.FetchOptions{
		Envelope:    true,
		BodySection: []*imap.FetchItemBodySection{{}},
		UID:         true,
	}
	// Pass UIDSet to Fetch — the library detects UIDSet and issues UID FETCH.
	msgs, err := c.Fetch(uidSet, fetchOpts).Collect()
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	for _, msg := range msgs {
		if err := p.handleMsg(c, msg, rc); err != nil {
			slog.Error("handle msg failed", "err", err)
		}
	}
	return nil
}

func (p *poller) handleMsg(
	c *imapclient.Client,
	msg *imapclient.FetchMessageBuffer,
	rc *routerClient,
) error {
	if msg.Envelope == nil {
		return nil
	}
	env := msg.Envelope
	msgID := strings.Trim(env.MessageID, "<>")
	if msgID == "" {
		// Use UID-based fallback: stable across reconnects (step 14).
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

	var threadID, rootMsgID string
	// Try all In-Reply-To values; use first match (step 14).
	for _, irt := range env.InReplyTo {
		parentID := strings.Trim(irt, "<>")
		if t := getThreadByMsgID(p.db, parentID); t != nil {
			threadID = t.ThreadID
			rootMsgID = t.RootMsgID
			break
		}
	}
	if threadID == "" {
		rootMsgID = msgID
		h := sha256.Sum256([]byte(rootMsgID))
		threadID = fmt.Sprintf("%x", h[:6])
	}
	upsertThread(p.db, msgID, threadID, fromAddr, rootMsgID)

	bodyRaw := msg.FindBodySection(&imap.FetchItemBodySection{})
	body := ""
	if bodyRaw != nil {
		body = extractPlainText(bytes.NewReader(bodyRaw))
	}

	subject := env.Subject
	toAddr := ""
	if len(env.To) > 0 {
		toAddr = env.To[0].Addr()
	}
	ts := env.Date.Unix()
	content := fmt.Sprintf("From: %s <%s>\nSubject: %s\nDate: %s\nTo: %s\n\n%s",
		fromName, fromAddr, subject, env.Date.Format(time.RFC1123Z), toAddr, body)

	jid := "email:" + threadID
	_ = rc.SendChat(jid, fromName+" ("+fromAddr+")", false)

	if err := rc.SendMessage(inboundMsg{
		ID:         msgID,
		ChatJID:    jid,
		Sender:     "email:" + fromAddr,
		SenderName: fromName,
		Content:    content,
		Timestamp:  ts,
		IsGroup:    false,
	}); err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
	}

	// Pass UIDSet to Store — the library detects UIDSet and issues UID STORE.
	c.Store(imap.UIDSetNum(msg.UID), &imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: []imap.Flag{imap.FlagSeen},
	}, nil)

	return nil
}

func extractPlainText(r io.Reader) string {
	mr, err := gomessage.CreateReader(r)
	if err != nil {
		b, _ := io.ReadAll(r)
		return string(b)
	}
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		if ih, ok := p.Header.(*gomessage.InlineHeader); ok {
			ct, _, _ := ih.ContentType()
			if ct == "text/plain" {
				b, _ := io.ReadAll(p.Body)
				return string(b)
			}
		}
	}
	return ""
}
