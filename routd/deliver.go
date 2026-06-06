package routd

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
)

// chanDeliverer is routd's production Deliverer: it resolves the owning adapter
// for a jid through the channel registry and fans the outbound call out over
// chanreg.HTTPChannel.
//
// Live-channel reuse: the per-adapter HTTPChannel carries a retry outbox.
// Register/deregister hooks keep `live` in sync so a resolved send reuses the
// adapter's channel (and its queued backlog) instead of a throwaway. A cache
// miss falls back to a fresh channel built from the registry entry.
type chanDeliverer struct {
	reg           *chanreg.Registry
	disabledChans []string // SEND_DISABLED_CHANNELS jid-prefixes (the part before ':')

	// lookupSource maps a jid to the adapter name that delivered its newest
	// inbound (DB.LatestSource). Held as a func so the Deliverer doesn't import
	// the DB type — the cmd injects it. nil in tests that register adapters
	// directly: resolution falls through to ForJID.
	lookupSource func(jid string) string

	mu   sync.RWMutex
	live map[string]*chanreg.HTTPChannel // keyed by adapter name
}

func newChanDeliverer(reg *chanreg.Registry, disabled []string, lookupSource func(string) string) *chanDeliverer {
	return &chanDeliverer{
		reg:           reg,
		disabledChans: disabled,
		lookupSource:  lookupSource,
		live:          map[string]*chanreg.HTTPChannel{},
	}
}

// NewChannelDeliverer is the cmd entry point: it builds the production
// chanreg-backed Deliverer plus the register/deregister hooks that keep its
// live-channel cache in sync. The caller passes the same Deliverer to
// NewServer and LoopConfig.Deliver, and the hooks to Server.SetChannelRegistry.
// lookupSource is DB.LatestSource (the inbound-source resolver).
func NewChannelDeliverer(reg *chanreg.Registry, disabled []string, lookupSource func(string) string) (
	deliver Deliverer,
	onRegister func(name string, ch *chanreg.HTTPChannel),
	onDeregister func(name string)) {
	d := newChanDeliverer(reg, disabled, lookupSource)
	return d, d.setLive, d.dropLive
}

// setLive records the live HTTPChannel for name (register hook) and drains any
// backlog queued while the adapter was gone.
func (d *chanDeliverer) setLive(name string, ch *chanreg.HTTPChannel) {
	d.mu.Lock()
	d.live[name] = ch
	d.mu.Unlock()
	ch.DrainOutbox()
}

// dropLive forgets the live channel for name (deregister hook).
func (d *chanDeliverer) dropLive(name string) {
	d.mu.Lock()
	delete(d.live, name)
	d.mu.Unlock()
}

// resolve picks the channel for jid: latest inbound source for the jid →
// registry Resolve (name lookup, ForJID prefix fallback). Returns nil when no
// adapter owns the jid. Reuses the live (outbox-bearing) channel when one is
// cached, else builds a fresh one from the entry.
func (d *chanDeliverer) resolve(jid string) *chanreg.HTTPChannel {
	name := d.latestSource(jid)
	entry := d.reg.Resolve(name, jid)
	if entry == nil {
		return nil
	}
	d.mu.RLock()
	ch := d.live[entry.Name]
	d.mu.RUnlock()
	if ch != nil {
		return ch
	}
	return chanreg.NewHTTPChannel(entry, d.reg.Secret())
}

func (d *chanDeliverer) latestSource(jid string) string {
	if d.lookupSource == nil {
		return ""
	}
	return d.lookupSource(jid)
}

// disabled reports whether jid's channel prefix is in SEND_DISABLED_CHANNELS.
// A disabled send is a silent no-op success.
func (d *chanDeliverer) disabled(jid string) bool {
	prefix, _, _ := strings.Cut(jid, ":")
	for _, x := range d.disabledChans {
		if strings.EqualFold(x, prefix) {
			return true
		}
	}
	return false
}

func (d *chanDeliverer) Send(jid, text, replyToID, threadID, idempotencyKey string) (string, error) {
	if d.disabled(jid) {
		return "", nil
	}
	ch := d.resolve(jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Send(jid, text, replyToID, threadID, idempotencyKey)
}

func (d *chanDeliverer) Document(jid, path, name, caption, replyToID, idempotencyKey string) (string, error) {
	if d.disabled(jid) {
		return "", nil
	}
	ch := d.resolve(jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.SendFile(jid, path, name, caption, replyToID, "")
}

func (d *chanDeliverer) SendVoice(jid, audioPath, caption, threadID string) (string, error) {
	if d.disabled(jid) {
		return "", nil
	}
	ch := d.resolve(jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.SendVoice(jid, audioPath, caption, threadID)
}

func (d *chanDeliverer) React(jid, platformID, reaction string) error {
	ch := d.resolve(jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Like(context.Background(), jid, platformID, reaction)
}

func (d *chanDeliverer) Edit(jid, platformID, content string) error {
	ch := d.resolve(jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Edit(context.Background(), jid, platformID, content)
}

func (d *chanDeliverer) Delete(jid, platformID string) error {
	ch := d.resolve(jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Delete(context.Background(), jid, platformID)
}

func (d *chanDeliverer) Pin(jid, platformID string) error {
	ch := d.resolve(jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Pin(context.Background(), jid, platformID)
}

func (d *chanDeliverer) Unpin(jid, platformID string, all bool) error {
	ch := d.resolve(jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Unpin(context.Background(), jid, platformID, all)
}

func (d *chanDeliverer) Post(jid, content string, mediaPaths []string) (string, error) {
	if d.disabled(jid) {
		return "", nil
	}
	ch := d.resolve(jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Post(context.Background(), jid, content, mediaPaths)
}

func (d *chanDeliverer) Forward(sourceMsgID, targetJID, comment string) (string, error) {
	ch := d.resolve(targetJID)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", targetJID)
	}
	return ch.Forward(context.Background(), sourceMsgID, targetJID, comment)
}

func (d *chanDeliverer) Quote(jid, sourceMsgID, comment string) (string, error) {
	ch := d.resolve(jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Quote(context.Background(), jid, sourceMsgID, comment)
}

func (d *chanDeliverer) Repost(jid, sourceMsgID string) (string, error) {
	ch := d.resolve(jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Repost(context.Background(), jid, sourceMsgID)
}

func (d *chanDeliverer) Dislike(jid, platformID string) error {
	ch := d.resolve(jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Dislike(context.Background(), jid, platformID)
}

func (d *chanDeliverer) SetSuggestions(jid string, prompts []core.PanePrompt) error {
	ch := d.resolve(jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.SetSuggestions(context.Background(), jid, prompts)
}

func (d *chanDeliverer) SetName(jid, title string) error {
	ch := d.resolve(jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.SetName(context.Background(), jid, title)
}

// FetchHistory resolves the owning adapter for jid and proxies to its GET
// /v1/history (the fetch_history platform-truth source). No adapter / no
// fetch_history cap → error; the caller (Server.fetchPlatformHistory) then
// falls back to the local DB.
func (d *chanDeliverer) FetchHistory(jid string, before time.Time, limit int) ([]byte, error) {
	ch := d.resolve(jid)
	if ch == nil {
		return nil, fmt.Errorf("no channel for jid %s", jid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return ch.FetchHistory(ctx, jid, before, limit)
}

// RoundDone posts the turn-closed notice to the web adapter owning the folder's
// web: chat so the /chat SSE client stops waiting. A folder with no web channel
// (most chats) is a silent no-op.
func (d *chanDeliverer) RoundDone(folder, turnID, status, errMsg string) error {
	ch := d.resolve("web:" + folder)
	if ch == nil {
		return nil
	}
	return ch.PostRoundDone(folder, turnID, status, errMsg)
}
