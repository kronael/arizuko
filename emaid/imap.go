package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	gomessage "github.com/emersion/go-message/mail"

	"github.com/kronael/arizuko/auth/surrogate"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/emaid/emaistore"
)

var errIdleNotSupported = errors.New("IDLE not supported")

const maxRetryBackoff = 60 * time.Second

// attMeta holds attachment metadata recorded during MIME parsing.
// No data bytes are stored -- the file proxy re-fetches from IMAP on demand.
type attMeta struct {
	Mime     string
	Filename string
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
	st      *emaistore.Store
	dialTLS func(string, *imapclient.Options) (*imapclient.Client, error)
	reg     *attRegistry
	// connected reflects IMAP liveness: true between successful imapConnect
	// and the corresponding Logout/close, or after a successful poll cycle.
	connected     atomic.Bool
	lastInboundAt atomic.Int64
}

func newPoller(cfg config, st *emaistore.Store, reg *attRegistry) *poller {
	p := &poller{cfg: cfg, st: st, dialTLS: imapclient.DialTLS, reg: reg}
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
	if err := imapAuth(c, cfg); err != nil {
		c.Logout() //nolint:errcheck // the connection is going away either way
		if permanentAuthFailure(err) {
			return nil, fmt.Errorf("login: %w: %w", errAuthRejected, err)
		}
		return nil, fmt.Errorf("login: %w", err)
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		c.Logout() //nolint:errcheck // the connection is going away either way
		return nil, fmt.Errorf("select: %w", err)
	}
	return c, nil
}

// imapAuth logs in with the one credential checkAuth allowed: the password via
// LOGIN, or a refreshed access token via AUTHENTICATE XOAUTH2.
func imapAuth(c *imapclient.Client, cfg config) error {
	if cfg.RefreshToken != "" {
		token, err := cfg.accessToken(context.Background())
		if err != nil {
			return err
		}
		return c.Authenticate(xoauth2IMAP{cfg.Account, token})
	}
	return c.Login(cfg.Account, cfg.Password).Wait()
}

// errAuthRejected marks a credential the server refused. It ends the poll loop
// instead of feeding it: CLAUDE.md's rule is that only the transient is retried, and a
// wrong password answers the same way on every attempt.
var errAuthRejected = errors.New("imap: credential rejected")

// permanentAuthFailure reads the server's own response CODE rather than its prose.
// RFC 5530 gives AUTHENTICATIONFAILED and AUTHORIZATIONFAILED exactly for this, so a
// string match on the text would be guessing at what each server writes.
//
// Anything else — including a bare NO with no code — stays retryable, because a server
// that refuses a login during maintenance sends one of those too.
func permanentAuthFailure(err error) bool {
	if errors.Is(err, surrogate.ErrReconnect) {
		return true
	}
	var ie *imap.Error
	if !errors.As(err, &ie) {
		return false
	}
	switch ie.Code {
	case imap.ResponseCodeAuthenticationFailed, imap.ResponseCodeAuthorizationFailed:
		return true
	}
	return false
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
		// A rejected credential is not transient and the loop must not eat it. emaid
		// retried it every 60 s forever, at ERROR, so the one failure a person has to
		// act on looked exactly like a flapping network — and nothing ever told them
		// the password was wrong (BUGS `L62`).
		if errors.Is(err, errAuthRejected) {
			slog.Error("imap: the server rejected this account's credential; emaid is "+
				"stopping rather than retrying — fix EMAIL_PASSWORD (or the app password), or "+
				"replace a revoked EMAIL_REFRESH_TOKEN, and restart the instance",
				"account", p.cfg.Account, "host", p.cfg.IMAPHost, "err", err)
			return
		}
		slog.Error("imap idle error", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxRetryBackoff)
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
			idleCmd.Close() //nolint:errcheck // ending IDLE; the poll reconnects on any fault
			// idleCmd.Wait blocks on a server response after DONE is sent.
			// If TCP is alive but unresponsive the read never returns, so we
			// impose a 5-second backstop: close the connection to unblock it.
			waitDone := make(chan struct{})
			go func() {
				idleCmd.Wait() //nolint:errcheck // ending IDLE; the poll reconnects on any fault
				close(waitDone)
			}()
			select {
			case <-waitDone:
			case <-time.After(5 * time.Second):
				c.Close() //nolint:errcheck // the connection is going away either way
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
			backoff = min(backoff*2, maxRetryBackoff)
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
		BodySection: []*imap.FetchItemBodySection{{Peek: true}},
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

	var fromAddr string
	if len(env.From) > 0 {
		fromAddr = env.From[0].Addr()
	}

	var threadID, rootMsgID string
	// Try all In-Reply-To values; use first match (step 14).
	for _, irt := range env.InReplyTo {
		parentID := strings.Trim(irt, "<>")
		if t, ok := p.st.ThreadForMessage(parentID); ok {
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
	bodyRaw := msg.FindBodySection(&imap.FetchItemBodySection{})
	auth := classifyRaw(bodyRaw, p.cfg.Auth)
	if auth.State == "untrusted" && p.cfg.Auth.StrictAuth {
		slog.Warn("dropping mail: auth untrusted (strict)",
			"uid", msg.UID, "from", fromAddr, "reason", auth.Reason)
		// Surface the mark-seen error so the next poll doesn't re-drop
		// the same UID forever. Matches the non-drop path at line 388.
		markCmd := c.Store(imap.UIDSetNum(msg.UID), &imap.StoreFlags{
			Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen},
		}, nil)
		if err := markCmd.Close(); err != nil {
			return fmt.Errorf("mark seen (drop) uid %d: %w", msg.UID, err)
		}
		return nil
	}
	if err := p.st.LinkMessageToThread(msgID, threadID, fromAddr, rootMsgID); err != nil {
		return fmt.Errorf("record thread mapping for %s: %w", msgID, err)
	}
	inbound, _ := fetchMsgToInbound(msg, threadID, p.cfg.ListenURL, p.reg, p.cfg.MaxAttachment, p.cfg.Auth)
	if err := rc.SendMessage(inbound); err != nil {
		return fmt.Errorf("deliver %s: %w", inbound.ChatJID, err)
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

// renderEmailContent builds the agent-visible text for one email message.
func renderEmailContent(fromName, fromAddr, subject, date, toAddr, body string, atts []chanlib.InboundAttachment) string {
	var s strings.Builder
	fmt.Fprintf(&s, "From: %s <%s>\nSubject: %s\nDate: %s\nTo: %s\n\n%s",
		fromName, fromAddr, subject, date, toAddr, body)
	for _, a := range atts {
		fmt.Fprintf(&s, " [Attachment: %s]", a.Filename)
	}
	return s.String()
}

// classifyRaw extracts Authentication-Results + From headers from the
// raw RFC 5322 message and feeds them to Classify. Uses net/mail to
// handle line folding (RFC 5322 §2.2.3) correctly — substring scans of
// the raw bytes silently miss folded headers.
func classifyRaw(raw []byte, cfg AuthConfig) ClassifyResult {
	if raw == nil {
		return Classify(nil, "", cfg)
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		// Header-parse failure → treat as untrusted (fail-closed).
		return ClassifyResult{State: "untrusted", DMARC: "missing", Reason: "header parse: " + err.Error()}
	}
	ar := m.Header["Authentication-Results"]
	from := m.Header.Get("From")
	return Classify(ar, from, cfg)
}

func extractContent(
	raw []byte, uid imap.UID, listenURL string, reg *attRegistry, maxBytes int64,
) (string, []chanlib.InboundAttachment) {
	root, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		// Fallback from the ORIGINAL bytes, not the consumed reader.
		return string(raw), nil
	}
	var text string
	var atts []chanlib.InboundAttachment
	attIdx := 0
	// Keep the parts read before a malformed MIME part.
	_ = root.Walk(func(path []int, p *message.Entity, err error) error {
		if err != nil {
			return err
		}
		ct, _, _ := p.Header.ContentType()
		if strings.HasPrefix(ct, "multipart/") {
			return nil
		}
		disp, _, _ := p.Header.ContentDisposition()
		if disp == "inline" || (disp != "attachment" && strings.HasPrefix(ct, "text/")) {
			if ct == "text/plain" && text == "" {
				body := p.Body
				if maxBytes > 0 {
					body = io.LimitReader(body, maxBytes+1)
				}
				b, _ := io.ReadAll(body)
				io.Copy(io.Discard, p.Body) //nolint:errcheck // the part is skipped; a read error here is unactionable
				text = string(b)
			} else {
				io.Copy(io.Discard, p.Body) //nolint:errcheck // the part is skipped; a read error here is unactionable
			}
		} else {
			h := &gomessage.AttachmentHeader{Header: p.Header}
			fname, _ := h.Filename()
			if fname == "" {
				fname = fmt.Sprintf("attachment-%d", attIdx)
			}
			att := chanlib.InboundAttachment{
				Mime:     ct,
				Filename: fname,
			}
			if reg != nil && listenURL != "" {
				key := fmt.Sprintf("%d-%d", uid, attIdx)
				section := make([]int, len(path))
				for i, n := range path {
					section[i] = n + 1
				}
				if len(section) == 0 {
					section = []int{1}
				}
				reg.put(key, attMeta{
					Mime:     ct,
					Filename: fname,
					Part:     section,
				})
				att.URL = fmt.Sprintf("%s/files/%d/%d", listenURL, uid, attIdx)
			}
			atts = append(atts, att)
			attIdx++
		}
		return nil
	})
	return text, atts
}
