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
			if backoff < 60*time.Second {
				backoff *= 2
				if backoff > 60*time.Second {
					backoff = 60 * time.Second
				}
			}
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
	searchData, err := c.Search(criteria, nil).Wait()
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	nums := searchData.AllSeqNums()
	if len(nums) == 0 {
		return nil
	}

	seqSet := imap.SeqSetNum(nums...)
	fetchOpts := &imap.FetchOptions{
		Envelope:    true,
		BodySection: []*imap.FetchItemBodySection{{}},
	}
	msgs, err := c.Fetch(seqSet, fetchOpts).Collect()
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	for i, msg := range msgs {
		if err := p.handleMsg(ctx, c, msg, nums[i], rc); err != nil {
			slog.Error("handle msg failed", "err", err)
		}
	}
	return nil
}

func (p *poller) handleMsg(
	ctx context.Context,
	c *imapclient.Client,
	msg *imapclient.FetchMessageBuffer,
	seqNum uint32,
	rc *routerClient,
) error {
	if msg.Envelope == nil {
		return nil
	}
	env := msg.Envelope
	msgID := strings.Trim(env.MessageID, "<>")
	if msgID == "" {
		msgID = fmt.Sprintf("noid-%d", msg.SeqNum)
	}

	var fromAddr, fromName string
	if len(env.From) > 0 {
		fromAddr = env.From[0].Addr()
		fromName = env.From[0].Name
		if fromName == "" {
			fromName = fromAddr
		}
	}

	// Determine thread
	var threadID, rootMsgID string
	if len(env.InReplyTo) > 0 {
		parentID := strings.Trim(env.InReplyTo[0], "<>")
		if t := getThreadByMsgID(p.db, parentID); t != nil {
			threadID = t.ThreadID
			rootMsgID = t.RootMsgID
		}
	}
	if threadID == "" {
		rootMsgID = msgID
		h := sha256.Sum256([]byte(rootMsgID))
		threadID = fmt.Sprintf("%x", h[:6])
	}
	storeThread(p.db, msgID, threadID, fromAddr, rootMsgID)

	// Get body
	bodyRaw := msg.FindBodySection(&imap.FetchItemBodySection{})
	body := ""
	if bodyRaw != nil {
		body = extractPlainText(bytes.NewReader(bodyRaw))
	}

	// Build content
	subject := env.Subject
	toAddr := ""
	if len(env.To) > 0 {
		toAddr = env.To[0].Addr()
	}
	ts := env.Date.Unix()
	content := fmt.Sprintf("From: %s <%s>\nSubject: %s\nDate: %s\nTo: %s\n\n%s",
		fromName, fromAddr, subject, env.Date.Format(time.RFC1123Z), toAddr, body)

	jid := "email:" + threadID
	_ = rc.sendChat(jid, fromName+" ("+fromAddr+")", false)

	if err := rc.sendMessage(inboundMsg{
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

	// Mark seen
	c.Store(imap.SeqSetNum(seqNum), &imap.StoreFlags{
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
