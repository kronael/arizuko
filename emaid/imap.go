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
	"sync/atomic"
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

// attRegistry is a transitory in-memory map of "uid-attIdx" -> metadata.
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
	// connected reflects IMAP liveness: true between successful imapConnect
	// and the corresponding Logout/close, or after a successful poll cycle.
	connected     atomic.Bool
	lastInboundAt atomic.Int64
}

func newPoller(cfg config, db *sql.DB, reg *attRegistry) *poller {
	p := &poller{cfg: cfg, db: db, dialTLS: imapclient.DialTLS, reg: reg}
	p.lastInboundAt.Store(time.Now().Unix())
	return p
}

func (p *poller) isConnected() bool    { return p.connected.Load() }
func (p *poller) LastInboundAt() int64 { return p.lastInboundAt.Load() }

// imapConnect dials, logs in and selects INBOX. Caller must Logout.
func imapConnect(
	cfg config,
	dialTLS func(string, *imapclient.Options) (*imapclient.Client, error),
	opts *imapclient.Options,
) (*imapclient.Client, error) {
	c, err := dialTLS(cfg.IMAPHost+":"+cfg.IMAPPort, opts)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	if err := c.Login(cfg.Account, cfg.Password).Wait(); err != nil {
		c.Logout() //nolint:errcheck
		return nil, fmt.Errorf("login: %w", err)
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		c.Logout() //nolint:errcheck
		return nil, fmt.Errorf("select: %w", err)
	}
	return c, nil
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

	c, err := imapConnect(p.cfg, p.dialTLS, opts)
	if err != nil {
		return err
	}
	defer c.Logout()
	p.connected.Store(true)
	defer p.connected.Store(false)

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
	c, err := imapConnect(p.cfg, p.dialTLS, nil)
	if err != nil {
		p.connected.Store(false)
		return err
	}
	defer c.Logout()
	p.connected.Store(true)
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
		if p.cfg.StrictAuth && !authResultsPass(bodyRaw) {
			slog.Warn("dropping mail: auth-results fail", "uid", msg.UID, "from", fromAddr)
			// Mark seen so we don't re-process the same rejected message.
			c.Store(imap.UIDSetNum(msg.UID), &imap.StoreFlags{
				Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen},
			}, nil).Close() //nolint:errcheck
			return nil
		}
		body, atts = extractContent(bodyRaw, msg.UID, p.cfg.ListenURL, p.reg, p.cfg.MaxAttachment)
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

	if err := rc.SendMessage(chanlib.InboundMsg{
		ID:          msgID,
		ChatJID:     jid,
		Sender:      "email:" + fromAddr,
		SenderName:  fromName,
		Content:     content,
		Timestamp:   ts,
		Attachments: atts,
	}); err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
		return nil
	}
	p.lastInboundAt.Store(time.Now().Unix())

	// Pass UIDSet to Store — the library detects UIDSet and issues UID STORE.
	// Surface the error: if mark-seen fails, the next fetchUnseen will
	// re-deliver via NotFlag:FlagSeen. Router dedups on msgID, so duplicate
	// delivery is safe; silent loss of the mark-seen error is not (the old
	// slog.Warn dropped it and claimed success).
	cmd := c.Store(imap.UIDSetNum(msg.UID), &imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: []imap.Flag{imap.FlagSeen},
	}, nil)
	if err := cmd.Close(); err != nil {
		return fmt.Errorf("mark seen uid %d: %w", msg.UID, err)
	}

	return nil
}

// authResultsPass returns false if Authentication-Results indicates
// spf=fail / dkim=fail / dmarc=fail. Missing header → pass (many
// providers omit it). Only consulted when cfg.StrictAuth is set.
func authResultsPass(raw []byte) bool {
	limit := len(raw)
	if limit > 64*1024 {
		limit = 64 * 1024
	}
	head := strings.ToLower(string(raw[:limit]))
	if idx := strings.Index(head, "\r\n\r\n"); idx >= 0 {
		head = head[:idx]
	}
	for _, ln := range strings.Split(head, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "authentication-results:") {
			continue
		}
		if strings.Contains(ln, "spf=fail") ||
			strings.Contains(ln, "dkim=fail") ||
			strings.Contains(ln, "dmarc=fail") {
			return false
		}
	}
	return true
}

// extractContent walks the MIME tree. go-message's Reader flattens nested
// multiparts into a linear stream of leaf parts, so the IMAP BODY section
// number is the 1-indexed leaf position in that stream (works for simple
// single-level multipart/mixed; nested multiparts yield best-effort paths).
//
// raw is the full MIME bytes; passing them (rather than an io.Reader)
// keeps the fallback path correct — the previous implementation tried to
// re-read from an io.Reader that had already been advanced by CreateReader
// and returned a truncated tail instead of the whole body.
//
// maxBytes (>0) caps per-attachment reads via io.LimitReader to prevent
// a remote sender from forcing unbounded IMAP bandwidth + CPU use just to
// measure a multi-GB attachment.
func extractContent(
	raw []byte, uid imap.UID, listenURL string, reg *attRegistry, maxBytes int64,
) (string, []chanlib.InboundAttachment) {
	mr, err := gomessage.CreateReader(bytes.NewReader(raw))
	if err != nil {
		// Fallback from the ORIGINAL bytes, not the consumed reader.
		return string(raw), nil
	}
	var text string
	var atts []chanlib.InboundAttachment
	partCount := 0 // every leaf part (text + attachment) — IMAP BODY section.
	attIdx := 0    // monotonic attachment index for the public URL key.
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		partCount++
		switch h := p.Header.(type) {
		case *gomessage.InlineHeader:
			ct, _, _ := h.ContentType()
			if ct == "text/plain" && text == "" {
				var body io.Reader = p.Body
				if maxBytes > 0 {
					body = io.LimitReader(body, maxBytes+1)
				}
				b, _ := io.ReadAll(body)
				io.Copy(io.Discard, p.Body) //nolint:errcheck
				text = string(b)
			} else {
				io.Copy(io.Discard, p.Body) //nolint:errcheck
			}
		case *gomessage.AttachmentHeader:
			ct, _, _ := h.ContentType()
			fname, _ := h.Filename()
			if fname == "" {
				fname = fmt.Sprintf("attachment-%d", attIdx)
			}
			// Read body only to measure size -- data is NOT stored.
			var body io.Reader = p.Body
			if maxBytes > 0 {
				body = io.LimitReader(body, maxBytes+1)
			}
			n, _ := io.Copy(io.Discard, body)
			io.Copy(io.Discard, p.Body) //nolint:errcheck
			att := chanlib.InboundAttachment{
				Mime:     ct,
				Filename: fname,
				Size:     n,
			}
			if reg != nil && listenURL != "" {
				key := fmt.Sprintf("%d-%d", uid, attIdx)
				// IMAP BODY section = absolute 1-indexed leaf position.
				// The previous code used attIdx+1 here, which mismatched
				// whenever a text part appeared before the attachment.
				reg.put(key, attMeta{
					Mime:     ct,
					Filename: fname,
					Size:     n,
					Part:     []int{partCount},
				})
				att.URL = fmt.Sprintf("%s/files/%d/%d", listenURL, uid, attIdx)
			}
			atts = append(atts, att)
			attIdx++
		}
	}
	return text, atts
}
