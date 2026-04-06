package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomessage "github.com/emersion/go-message/mail"

	"github.com/onvos/arizuko/chanlib"
)

var errIdleNotSupported = errors.New("IDLE not supported")

// attMeta holds attachment metadata recorded during MIME parsing.
// No data bytes are stored -- the file proxy re-fetches from IMAP on demand.
type attMeta struct {
	Mime     string
	Filename string
	Size     int64
	Part     []int // IMAP BODY section path (1-indexed)
}

// attRegistry is a transitory in-memory map of "uid-partIdx" -> metadata.
type attRegistry struct {
	mu sync.RWMutex
	m  map[string]attMeta
}

func newAttRegistry() *attRegistry {
	return &attRegistry{m: make(map[string]attMeta)}
}

func (r *attRegistry) put(key string, meta attMeta) {
	r.mu.Lock()
	r.m[key] = meta
	r.mu.Unlock()
}

func (r *attRegistry) get(key string) (attMeta, bool) {
	r.mu.RLock()
	v, ok := r.m[key]
	r.mu.RUnlock()
	return v, ok
}

type poller struct {
	cfg     config
	db      *sql.DB
	dialTLS func(string, *imapclient.Options) (*imapclient.Client, error)
	reg     *attRegistry
}

func newPoller(cfg config, db *sql.DB, reg *attRegistry) *poller {
	return &poller{cfg: cfg, db: db, dialTLS: imapclient.DialTLS, reg: reg}
}

func (p *poller) run(ctx context.Context, rc *chanlib.RouterClient) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := p.runIdle(ctx, rc)
		if err == nil {
			return
		}
		if errors.Is(err, errIdleNotSupported) {
			slog.Info("imap: IDLE not supported, falling back to poll")
			p.runPoll(ctx, rc)
			return
		}
		slog.Error("imap idle error", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 60*time.Second)
	}
}

func (p *poller) runIdle(ctx context.Context, rc *chanlib.RouterClient) error {
	addr := p.cfg.IMAPHost + ":" + p.cfg.IMAPPort

	exists := make(chan struct{}, 1)
	opts := &imapclient.Options{
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				if data.NumMessages != nil {
					select {
					case exists <- struct{}{}:
					default:
					}
				}
			},
		},
	}

	c, err := p.dialTLS(addr, opts)
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

	if err := p.fetchUnseen(c, rc); err != nil {
		return fmt.Errorf("initial fetch: %w", err)
	}

	idleCmd, err := c.Idle()
	if err != nil {
		return errIdleNotSupported
	}

	idleTimer := time.NewTimer(28 * time.Minute)
	defer idleTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			idleCmd.Close() //nolint:errcheck
			// idleCmd.Wait blocks on a server response after DONE is sent.
			// If TCP is alive but unresponsive the read never returns, so we
			// impose a 5-second backstop: close the connection to unblock it.
			waitDone := make(chan struct{})
			go func() {
				idleCmd.Wait() //nolint:errcheck
				close(waitDone)
			}()
			select {
			case <-waitDone:
			case <-time.After(5 * time.Second):
				c.Close() //nolint:errcheck
				<-waitDone
			}
			return nil
		case <-exists:
		case <-idleTimer.C:
		}

		if err := idleCmd.Close(); err != nil {
			return fmt.Errorf("idle close: %w", err)
		}
		if err := idleCmd.Wait(); err != nil {
			return fmt.Errorf("idle wait: %w", err)
		}

		// Always fetch — timer wakeup is a safety net for silently dropped EXISTS.
		if _, err := c.Select("INBOX", nil).Wait(); err != nil {
			return fmt.Errorf("re-select: %w", err)
		}
		if err := p.fetchUnseen(c, rc); err != nil {
			return fmt.Errorf("fetch: %w", err)
		}

		idleCmd, err = c.Idle()
		if err != nil {
			return fmt.Errorf("re-idle: %w", err)
		}
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(28 * time.Minute)
	}
}

func (p *poller) runPoll(ctx context.Context, rc *chanlib.RouterClient) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
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

func (p *poller) poll(_ context.Context, rc *chanlib.RouterClient) error {
	addr := p.cfg.IMAPHost + ":" + p.cfg.IMAPPort
	c, err := p.dialTLS(addr, nil)
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

	return p.fetchUnseen(c, rc)
}

func (p *poller) fetchUnseen(c *imapclient.Client, rc *chanlib.RouterClient) error {
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
	rc *chanlib.RouterClient,
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
	var atts []chanlib.InboundAttachment
	if bodyRaw != nil {
		body, atts = extractContent(bytes.NewReader(bodyRaw), msg.UID, p.cfg.ListenURL, p.reg)
	}

	subject := env.Subject
	toAddr := ""
	if len(env.To) > 0 {
		toAddr = env.To[0].Addr()
	}
	ts := env.Date.Unix()
	content := fmt.Sprintf("From: %s <%s>\nSubject: %s\nDate: %s\nTo: %s\n\n%s",
		fromName, fromAddr, subject, env.Date.Format(time.RFC1123Z), toAddr, body)

	for _, a := range atts {
		content += fmt.Sprintf(" [Attachment: %s]", a.Filename)
	}

	jid := "email:" + threadID
	_ = rc.SendChat(jid, fromName+" ("+fromAddr+")", false)

	if err := rc.SendMessage(chanlib.InboundMsg{
		ID:          msgID,
		ChatJID:     jid,
		Sender:      "email:" + fromAddr,
		SenderName:  fromName,
		Content:     content,
		Timestamp:   ts,
		IsGroup:     false,
		Attachments: atts,
	}); err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
		return nil
	}

	// Pass UIDSet to Store — the library detects UIDSet and issues UID STORE.
	cmd := c.Store(imap.UIDSetNum(msg.UID), &imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: []imap.Flag{imap.FlagSeen},
	}, nil)
	if err := cmd.Close(); err != nil {
		slog.Warn("mark seen failed", "uid", msg.UID, "err", err)
	}

	return nil
}

func extractPlainText(r io.Reader) string {
	text, _ := extractContent(r, 0, "", nil)
	return text
}

func extractContent(r io.Reader, uid imap.UID, listenURL string, reg *attRegistry) (string, []chanlib.InboundAttachment) {
	mr, err := gomessage.CreateReader(r)
	if err != nil {
		b, _ := io.ReadAll(r)
		return string(b), nil
	}
	var text string
	var atts []chanlib.InboundAttachment
	partIdx := 0
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		switch h := p.Header.(type) {
		case *gomessage.InlineHeader:
			ct, _, _ := h.ContentType()
			if ct == "text/plain" && text == "" {
				b, _ := io.ReadAll(p.Body)
				text = string(b)
			}
		case *gomessage.AttachmentHeader:
			ct, _, _ := h.ContentType()
			fname, _ := h.Filename()
			if fname == "" {
				fname = fmt.Sprintf("attachment-%d", partIdx)
			}
			// Read body only to measure size -- data is NOT stored.
			n, _ := io.Copy(io.Discard, p.Body)
			att := chanlib.InboundAttachment{
				Mime:     ct,
				Filename: fname,
				Size:     n,
			}
			if reg != nil && listenURL != "" {
				key := fmt.Sprintf("%d-%d", uid, partIdx)
				// IMAP BODY section parts are 1-indexed.
				reg.put(key, attMeta{
					Mime:     ct,
					Filename: fname,
					Size:     n,
					Part:     []int{partIdx + 1},
				})
				att.URL = fmt.Sprintf("%s/files/%d/%d", listenURL, uid, partIdx)
			}
			atts = append(atts, att)
			partIdx++
		}
	}
	return text, atts
}
