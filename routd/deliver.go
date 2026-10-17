package routd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
)

// chanDeliverer is routd's production Deliverer: it resolves the owning
// adapter through the channel registry and fans out over chanreg.HTTPChannel.
// It caches no channel: a nil Transport pools connections already, and
// Registry.Register REPLACES the Entry value, so a cached channel would keep
// the old URL.
type chanDeliverer struct {
	reg           *chanreg.Registry
	disabledChans []string // SEND_DISABLED_CHANNELS jid-prefixes (the part before ':')

	// Held as a func so the Deliverer does not import the DB type; nil in
	// tests falls through to ForJID.
	lookupSource func(jid string) string
}

func newChanDeliverer(reg *chanreg.Registry, disabled []string, lookupSource func(string) string) *chanDeliverer {
	return &chanDeliverer{
		reg:           reg,
		disabledChans: disabled,
		lookupSource:  lookupSource,
	}
}

// NewChannelDeliverer is the cmd entry point; lookupSource is DB.LatestSource.
func NewChannelDeliverer(reg *chanreg.Registry, disabled []string, lookupSource func(string) string) Deliverer {
	return newChanDeliverer(reg, disabled, lookupSource)
}

// resolve picks the channel for jid in the order spec 5/34-channel-protocol fixes: an explicit
// pin (only POST /v1/outbound carries one) → the latest inbound source for the
// jid → the registry's prefix owner. nil when no adapter owns the jid.
func (d *chanDeliverer) resolveEntry(channel, jid string) *chanreg.Entry {
	name := channel
	if name == "" {
		name = d.latestSource(jid)
	}
	return d.reg.Resolve(name, jid)
}

func (d *chanDeliverer) resolve(channel, jid string) *chanreg.HTTPChannel {
	entry := d.resolveEntry(channel, jid)
	if entry == nil {
		return nil
	}
	return chanreg.NewHTTPChannel(entry, d.reg.Bearer())
}

func (d *chanDeliverer) latestSource(jid string) string {
	if d.lookupSource == nil {
		return ""
	}
	return d.lookupSource(jid)
}

// disabled reports whether jid's channel prefix is in SEND_DISABLED_CHANNELS;
// a disabled send is a silent no-op success.
func (d *chanDeliverer) disabled(jid string) bool {
	prefix, _, _ := strings.Cut(jid, ":")
	for _, x := range d.disabledChans {
		if strings.EqualFold(x, prefix) {
			return true
		}
	}
	return false
}

func (d *chanDeliverer) Send(jid, text, replyToID, threadID, threadRoot, idempotencyKey string) (string, error) {
	return d.send("", jid, text, nil, replyToID, threadID, threadRoot, idempotencyKey)
}

func (d *chanDeliverer) SendVia(channel, jid, text string) (string, error) {
	return d.send(channel, jid, text, nil, "", "", "", "")
}

func (d *chanDeliverer) SendOptions(jid, text string, options []chanlib.Option, threadID, idempotencyKey string) (string, error) {
	return d.send("", jid, text, options, "", threadID, "", idempotencyKey)
}

// send is the one text-egress body; Send, SendVia and SendOptions differ only
// in the pin and the options.
func (d *chanDeliverer) send(channel, jid, text string, options []chanlib.Option,
	replyToID, threadID, threadRoot, idempotencyKey string) (string, error) {
	if d.disabled(jid) {
		return "", nil
	}
	ch := d.resolve(channel, jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.SendOptionsCtx(context.Background(), jid, text, options, replyToID, threadID, threadRoot, idempotencyKey)
}

func (d *chanDeliverer) Document(jid, path, name, caption, replyToID, threadID, idempotencyKey string) (string, error) {
	if d.disabled(jid) {
		return "", nil
	}
	ch := d.resolve("", jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.SendFile(jid, path, name, caption, replyToID, threadID, idempotencyKey)
}

func (d *chanDeliverer) SendVoice(jid, audioPath, caption, threadID string) (string, error) {
	if d.disabled(jid) {
		return "", nil
	}
	ch := d.resolve("", jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.SendVoice(jid, audioPath, caption, threadID)
}

func (d *chanDeliverer) React(jid, platformID, reaction string) error {
	ch := d.resolve("", jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Like(context.Background(), jid, platformID, reaction)
}

func (d *chanDeliverer) Edit(jid, platformID, content string) error {
	ch := d.resolve("", jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Edit(context.Background(), jid, platformID, content)
}

func (d *chanDeliverer) CanEdit(jid string) bool {
	if d.disabled(jid) {
		return false
	}
	entry := d.resolveEntry("", jid)
	return entry != nil && entry.HasCap("edit")
}

func (d *chanDeliverer) Delete(jid, platformID string) error {
	ch := d.resolve("", jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Delete(context.Background(), jid, platformID)
}

func (d *chanDeliverer) Pin(jid, platformID string) error {
	ch := d.resolve("", jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Pin(context.Background(), jid, platformID)
}

func (d *chanDeliverer) Unpin(jid, platformID string, all bool) error {
	ch := d.resolve("", jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Unpin(context.Background(), jid, platformID, all)
}

func (d *chanDeliverer) Typing(jid string, on bool) error {
	if d.disabled(jid) {
		return nil
	}
	ch := d.resolve("", jid)
	if ch == nil {
		return nil // best-effort; never fail the turn
	}
	return ch.Typing(jid, on)
}

func (d *chanDeliverer) Post(jid, content string, mediaPaths []string) (string, error) {
	if d.disabled(jid) {
		return "", nil
	}
	ch := d.resolve("", jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Post(context.Background(), jid, content, mediaPaths)
}

func (d *chanDeliverer) Forward(sourceMsgID, targetJID, comment string) (string, error) {
	ch := d.resolve("", targetJID)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", targetJID)
	}
	return ch.Forward(context.Background(), sourceMsgID, targetJID, comment)
}

func (d *chanDeliverer) Quote(jid, sourceMsgID, comment string) (string, error) {
	ch := d.resolve("", jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Quote(context.Background(), jid, sourceMsgID, comment)
}

func (d *chanDeliverer) Repost(jid, sourceMsgID string) (string, error) {
	ch := d.resolve("", jid)
	if ch == nil {
		return "", fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Repost(context.Background(), jid, sourceMsgID)
}

func (d *chanDeliverer) Dislike(jid, platformID string) error {
	ch := d.resolve("", jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.Dislike(context.Background(), jid, platformID)
}

func (d *chanDeliverer) SetSuggestions(jid string, prompts []core.PanePrompt) error {
	ch := d.resolve("", jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.SetSuggestions(context.Background(), jid, prompts)
}

func (d *chanDeliverer) SetName(jid, title string) error {
	ch := d.resolve("", jid)
	if ch == nil {
		return fmt.Errorf("no channel for jid %s", jid)
	}
	return ch.SetName(context.Background(), jid, title)
}

// FetchHistory proxies to the owning adapter's GET /v1/history; no adapter or
// no fetch_history cap → error, and the caller falls back to the local DB.
func (d *chanDeliverer) FetchHistory(jid string, before time.Time, limit int) ([]byte, error) {
	ch := d.resolve("", jid)
	if ch == nil {
		return nil, fmt.Errorf("no channel for jid %s", jid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return ch.FetchHistory(ctx, jid, before, limit)
}

// RoundDone posts the turn-closed notice to the adapter owning chatJID. The
// JID travels verbatim — webd's hub keys on it as-is (BUGS L30). An adapter
// without /v1/round_done is a no-op via PostRoundDone's 404 rule; a chat with
// no adapter is a silent no-op here.
func (d *chanDeliverer) RoundDone(chatJID, turnID, status, errMsg string) error {
	ch := d.resolve("", chatJID)
	if ch == nil {
		return nil
	}
	return ch.PostRoundDone(chatJID, turnID, status, errMsg)
}
