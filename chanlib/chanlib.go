// Package chanlib provides shared primitives for channel adapter daemons.
package chanlib

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/obs"
)

// maxRouterResponseBytes caps decoded router responses to guard against OOM.
const maxRouterResponseBytes = 10 << 20

// UserAgent names arizuko to a third party (Reddit requires one). It carries
// NO version: a frozen number told GitHub something false, and a second copy
// of the version is a second thing to keep in step.
const UserAgent = "arizuko (+https://github.com/kronael/arizuko)"

// MaxAdapterJSONBody caps inbound JSON bodies at adapters.
const MaxAdapterJSONBody = 1 << 20

type InboundAttachment struct {
	Mime     string `json:"mime"`
	Filename string `json:"filename"`
	URL      string `json:"url"`
	Data     string `json:"data,omitempty"` // base64 inline content (whapd)
}

type InboundMsg struct {
	ID            string              `json:"id"`
	ChatJID       string              `json:"chat_jid"`
	Sender        string              `json:"sender"`
	SenderName    string              `json:"sender_name"`
	Content       string              `json:"content"`
	Timestamp     int64               `json:"timestamp"`
	Topic         string              `json:"topic,omitempty"`
	Verb          string              `json:"verb,omitempty"`
	ReplyTo       string              `json:"reply_to,omitempty"`
	ReplyToText   string              `json:"reply_to_text,omitempty"`
	ReplyToSender string              `json:"reply_to_sender,omitempty"`
	Attachments   []InboundAttachment `json:"attachments,omitempty"`
	// Reaction carries the source emoji when Verb is "like" or "dislike"
	// triggered by an inbound reaction event. Empty for content messages.
	Reaction string `json:"reaction,omitempty"`
	// IsGroup classifies the chat: true for group chats / channels / public
	// threads, false for DMs; routd upserts it onto chats.is_group to pick the
	// reply shape. IT GOES ON THE WIRE EVEN WHEN FALSE: an adapter's false is
	// the answer "a DM", which has to take a wrong `true` back off a chat, and
	// omitting it would spell a DM the way silence is spelled (routd/api/v1
	// § Message.IsGroup).
	IsGroup bool `json:"is_group"`
	// ChatName is the human-readable name of the source channel or group;
	// empty for DMs.
	ChatName string `json:"chat_name,omitempty"`
	// Source is the adapter's CHANNEL_NAME; routd persists it so a reply
	// resolves back to the ORIGINATING account — multi-account adapters share
	// one service:<daemon> JWT, so auth alone cannot disambiguate them.
	Source string `json:"source,omitempty"`
	// LinkContext is the route token's context snapshotted at ingest by webd
	// (spec 5/W § link context); prompt-build renders it as <link-context>.
	LinkContext string `json:"link_context,omitempty"`
}

type RouterClient struct {
	url, token string
	client     *http.Client

	// svcToken is the adapter's service:<daemon> ES256 JWT source, the
	// credential for EVERY authenticated routd call; nil → local dev, no bearer.
	svcToken func(context.Context) (string, error)

	// Saved for transparent re-register on 401.
	regName     string
	regURL      string
	regPrefixes []string
	regCaps     map[string]bool
	regMu       sync.Mutex

	// pending holds the retained inbound; nil until RetainInbound wires it.
	pending *chanstore.Store
}

func NewRouterClient(url string) *RouterClient {
	return &RouterClient{url: url, client: &http.Client{Timeout: 10 * time.Second}}
}

// SetServiceToken installs the adapter's service-token source (spec 5/1);
// nil is the local-dev path.
func (r *RouterClient) SetServiceToken(src func(context.Context) (string, error)) {
	r.regMu.Lock()
	r.svcToken = src
	r.regMu.Unlock()
}

// bearer returns the service token when a source is wired, else "". An
// exchange error returns "" so the call surfaces a 401 rather than silently
// presenting a stale secret.
func (r *RouterClient) bearer() string {
	r.regMu.Lock()
	src := r.svcToken
	r.regMu.Unlock()
	if src == nil {
		return ""
	}
	tok, err := src(context.Background())
	if err != nil {
		slog.Error("service-token exchange failed", "err", err)
		return ""
	}
	return tok
}

func (r *RouterClient) SetToken(t string) {
	r.regMu.Lock()
	r.token = t
	r.regMu.Unlock()
}

func (r *RouterClient) getToken() string {
	r.regMu.Lock()
	defer r.regMu.Unlock()
	return r.token
}

// Token returns the bearer adapters present on JWT-gated side calls to routd
// beyond message delivery (slakd's POST /v1/pane).
func (r *RouterClient) Token() string { return r.bearer() }

func (r *RouterClient) Register(name, url string, prefixes []string, caps map[string]bool) (string, error) {
	slog.Info("registering channel", "name", name, "url", url)
	var resp struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
		Error string `json:"error"`
	}
	err := r.Post("/v1/channels/register", map[string]any{
		"name": name, "url": url, "jid_prefixes": prefixes, "capabilities": caps,
	}, r.bearer(), &resp)
	if err != nil {
		slog.Error("channel registration failed", "name", name, "err", err)
		return "", err
	}
	if !resp.OK {
		slog.Error("channel registration rejected", "name", name, "reason", resp.Error)
		return "", fmt.Errorf("register: %s", resp.Error)
	}
	r.regMu.Lock()
	r.token = resp.Token
	r.regName = name
	r.regURL = url
	r.regPrefixes = prefixes
	r.regCaps = caps
	r.regMu.Unlock()
	slog.Info("channel registered", "name", name)
	return resp.Token, nil
}

// savedRegistration returns the parameters of the last successful Register.
// An empty name means Register never ran.
func (r *RouterClient) savedRegistration() (name, url string, prefixes []string, caps map[string]bool) {
	r.regMu.Lock()
	defer r.regMu.Unlock()
	return r.regName, r.regURL, r.regPrefixes, r.regCaps
}

func (r *RouterClient) reregister() error {
	name, url, prefixes, caps := r.savedRegistration()
	if name == "" {
		return fmt.Errorf("re-register: no saved params (Register was never called)")
	}
	slog.Warn("re-registering channel after 401", "name", name)
	_, err := r.Register(name, url, prefixes, caps)
	return err
}

// heartbeatEvery paces the registration refresh.
const heartbeatEvery = 3 * time.Minute

// Heartbeat re-registers with the saved parameters until ctx ends. EVERY
// daemon that registers a channel must run one: routd auto-deregisters after
// 3 failed health probes, and JWT auth and registration are decoupled, so a
// deregistered adapter keeps receiving inbound while its OUTBOUND silently
// fails — how the `web` channel disappeared for a whole daemon lifetime. With
// no saved registration it does nothing.
func (r *RouterClient) Heartbeat(ctx context.Context) {
	r.heartbeat(ctx, heartbeatEvery)
}

func (r *RouterClient) heartbeat(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			name, url, prefixes, caps := r.savedRegistration()
			if name == "" {
				continue
			}
			if _, err := r.Register(name, url, prefixes, caps); err != nil {
				slog.Warn("channel heartbeat failed", "name", name, "err", err)
			}
		}
	}
}

func (r *RouterClient) Deregister() error {
	var resp struct{ OK bool }
	return r.Post("/v1/channels/deregister", nil, r.getToken(), &resp)
}

// SendMessage delivers one inbound to routd. With RetainInbound wired, a
// transient failure is ALSO retained for the sweep (spec 5/34-channel-protocol, BUGS L25). The
// error still returns: an adapter that gates a platform cursor on delivery
// must keep seeing the failure.
func (r *RouterClient) SendMessage(msg InboundMsg) error {
	err := r.postInbound(msg)
	if err != nil {
		r.retainInbound(msg, err)
	}
	return err
}

// postInbound is the bare delivery: two attempts, re-registering once on a
// monolith 401. It is the ONLY place an InboundMsg goes on the wire — the live
// path and the retention sweep both call it, so neither can drift.
func (r *RouterClient) postInbound(msg InboundMsg) error {
	if msg.Source == "" {
		r.regMu.Lock()
		msg.Source = r.regName
		r.regMu.Unlock()
	}
	var last error
	for attempt := range 2 {
		if attempt > 0 {
			slog.Warn("send retry", "err", last)
			// A 401 on the monolith path means routd lost our registration token.
			// With a service-token source the JWT is independent of registration,
			// so re-registering would not fix it — just back off.
			if isAuthErr(last) && !r.usingServiceToken() {
				if rerr := r.reregister(); rerr != nil {
					slog.Error("re-register failed", "err", rerr)
					return last
				}
			} else {
				time.Sleep(500 * time.Millisecond)
			}
		}
		var resp struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := r.Post("/v1/messages", msg, r.bearer(), &resp); err != nil {
			last = err
			continue
		}
		if resp.OK {
			return nil
		}
		last = fmt.Errorf("deliver: %s", resp.Error)
	}
	return last
}

func isAuthErr(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == http.StatusUnauthorized
}

func (r *RouterClient) usingServiceToken() bool {
	r.regMu.Lock()
	defer r.regMu.Unlock()
	return r.svcToken != nil
}

// StatusError is a non-200 answer from routd, carrying the code so callers
// classify by number: retention must tell a restarting routd from one that
// rejected the payload, and a substring test cannot.
type StatusError struct {
	Path string
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("router %s: status %d", e.Path, e.Code)
}

func (r *RouterClient) Post(path string, body any, auth string, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequest("POST", r.url+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	obs.InjectRequest(req.Context(), req)
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("router %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &StatusError{Path: path, Code: resp.StatusCode}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxRouterResponseBytes)).Decode(out)
}

func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func WriteErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

func Chunk(s string, max int) []string {
	if len(s) <= max {
		return []string{s}
	}
	var out []string
	var cur []byte
	for _, r := range s {
		enc := []byte(string(r))
		if len(cur)+len(enc) > max && len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
		cur = append(cur, enc...)
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

func EnvOr(k, v string) string {
	if e := os.Getenv(k); e != "" {
		return e
	}
	return v
}

func EnvInt(k string, fallback int) int {
	s := os.Getenv(k)
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

// ShortHash returns 4-byte hex of sha256(s) for logging sensitive strings.
// Empty input returns "" so callers can skip the log field.
func ShortHash(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

func MustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		slog.Error("required env var missing", "key", k)
		os.Exit(1)
	}
	return v
}

// EnvDur reads a Go duration ("20m", "90s"). Unset, unparseable or
// non-positive → fallback.
func EnvDur(k string, fallback time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

func EnvBytes(k string, def int64) int64 {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// ProxyFile streams src to w, copying Content-Type, capped at max bytes
// (defaults to 20 MiB). Caller owns src.Body.
func ProxyFile(w http.ResponseWriter, src *http.Response, max int64) {
	if src.StatusCode != 200 {
		WriteErr(w, 502, "upstream fetch failed")
		return
	}
	if max <= 0 {
		max = 20 * 1024 * 1024
	}
	if ct := src.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// client disconnect mid-download; the header is already committed
	_, _ = io.Copy(w, io.LimitReader(src.Body, max))
}
