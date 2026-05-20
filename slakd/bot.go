package main

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/store"
)

const (
	slackBase       = "https://slack.com/api"
	signingWindow   = 5 * time.Minute
	defaultCacheTTL = 15 * time.Minute
)

// paneStore is the subset of *store.Store slakd needs for pane sessions.
// Kept narrow so tests can stub it without dragging the DB in.
type paneStore interface {
	UpsertPane(teamID, userID, threadTS, channelID string) error
	GetPaneByChannel(channelID string) (store.PaneSession, bool)
	SetPaneContext(teamID, userID, threadTS, contextJID string) error
	SetPaneStatusAt(teamID, userID, threadTS, ts string) error
}

type bot struct {
	chanlib.NoVoiceSender

	cfg     config
	api     string
	http    *http.Client
	rc      *chanlib.RouterClient
	files   *chanlib.URLCache
	users   *ttlCache
	chans   *ttlCache
	store   paneStore
	typing  *chanlib.TypingRefresher

	botUserID atomic.Value
	teamID    atomic.Value

	connected     atomic.Bool
	lastInboundAt atomic.Int64

	// pendingPrompts holds prompts the agent staged via MCP for the
	// next outbound on a pane (keyed by team/user/thread_ts). Consumed
	// — and cleared — on the first Send into that pane. Also a one-shot
	// title slot.
	paneOutMu       sync.Mutex
	pendingPrompts  map[string][]panePrompt
	pendingTitle    map[string]string
}

func (b *bot) isConnected() bool    { return b.connected.Load() }
func (b *bot) LastInboundAt() int64 { return b.lastInboundAt.Load() }

func (b *bot) BotUserID() string {
	if v, ok := b.botUserID.Load().(string); ok {
		return v
	}
	return ""
}

func (b *bot) TeamID() string {
	if v, ok := b.teamID.Load().(string); ok {
		return v
	}
	return ""
}

var _ chanlib.BotHandler = (*bot)(nil)

func newBot(cfg config) (*bot, error) {
	return newBotWithBase(cfg, slackBase)
}

func newBotWithBase(cfg config, base string) (*bot, error) {
	b := &bot{
		cfg:            cfg,
		api:            base,
		http:           &http.Client{Timeout: 30 * time.Second},
		users:          newTTLCache(cfg.CacheTTL),
		chans:          newTTLCache(cfg.CacheTTL),
		pendingPrompts: map[string][]panePrompt{},
		pendingTitle:   map[string]string{},
	}
	b.typing = chanlib.NewTypingRefresher(3*time.Second, chanlib.DefaultTypingMaxTTL, b.sendTypingChannel, nil)
	b.lastInboundAt.Store(time.Now().Unix())
	return b, nil
}

// sendTypingChannel calls conversations.typing for a regular (non-pane) Slack
// channel or DM. The indicator expires after ~5s on Slack's side, so
// TypingRefresher fires every 3s. Returns false on auth/permission errors to
// cancel the refresher.
func (b *bot) sendTypingChannel(jid string) bool {
	parts, err := parseJID(jid)
	if err != nil {
		return false
	}
	form := url.Values{}
	form.Set("channel", parts.ID)
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := b.postForm(context.Background(), "/conversations.typing", form, &resp); err != nil {
		slog.Debug("slack conversations.typing failed", "jid", jid, "err", err)
		return true // transient error — keep trying
	}
	if !resp.OK {
		// unknown_method / missing_scope = permanent; bot tokens can't use this
		// endpoint (user token required). Cancel refresher to stop log spam.
		if resp.Error == "unknown_method" || resp.Error == "missing_scope" || resp.Error == "not_allowed_token_type" {
			return false
		}
		slog.Warn("slack conversations.typing error", "jid", jid, "error", resp.Error)
	}
	return true
}

func (b *bot) start(rc *chanlib.RouterClient) error {
	b.rc = rc
	user, team, err := b.authTest(context.Background())
	if err != nil {
		return fmt.Errorf("slack auth.test: %w", err)
	}
	b.botUserID.Store(user)
	b.teamID.Store(team)
	b.connected.Store(true)
	slog.Info("slack connected", "bot_user_id", user, "team_id", team)
	return nil
}

func (b *bot) stop() {
	b.connected.Store(false)
	b.typing.Stop()
}

func verifySignature(secret, sigHeader, tsHeader string, body []byte, now time.Time) error {
	if secret == "" {
		return errors.New("signing secret not configured")
	}
	if sigHeader == "" || tsHeader == "" {
		return errors.New("missing signature headers")
	}
	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return fmt.Errorf("bad timestamp: %w", err)
	}
	skew := now.Unix() - ts
	if skew < 0 {
		skew = -skew
	}
	if skew > int64(signingWindow.Seconds()) {
		return fmt.Errorf("timestamp skew %ds exceeds %ds", skew, int64(signingWindow.Seconds()))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:"))
	mac.Write([]byte(tsHeader))
	mac.Write([]byte(":"))
	mac.Write(body)
	want := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(sigHeader)) != 1 {
		return errors.New("signature mismatch")
	}
	return nil
}

func (b *bot) handleEvent(body []byte, w http.ResponseWriter) {
	var env struct {
		Type      string          `json:"type"`
		Challenge string          `json:"challenge"`
		TeamID    string          `json:"team_id"`
		Event     json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		chanlib.WriteErr(w, 400, "invalid json")
		return
	}
	switch env.Type {
	case "url_verification":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(env.Challenge))
	case "event_callback":
		// ack first; Slack retries on non-2xx, which we don't want for handled events.
		w.WriteHeader(http.StatusOK)
		b.dispatch(env.TeamID, env.Event)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (b *bot) dispatch(teamID string, raw json.RawMessage) {
	var head struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		slog.Warn("slack: event decode failed", "err", err)
		return
	}
	switch head.Type {
	case "message":
		// thread_broadcast is a display copy of a thread reply that's
		// also surfaced in the parent channel — the original reply
		// already arrived as a regular message event, so dropping the
		// broadcast prevents duplicate inbound delivery.
		if head.Subtype == "thread_broadcast" {
			return
		}
		if head.Subtype != "" {
			return
		}
		b.handleMessage(teamID, raw)
	case "reaction_added":
		b.handleReaction(teamID, raw)
	case "member_joined_channel":
		b.handleJoin(teamID, raw)
	case "assistant_thread_started":
		b.handleAssistantThreadStarted(teamID, raw)
	case "assistant_thread_context_changed":
		b.handleAssistantThreadContextChanged(teamID, raw)
	}
}

type slackFile struct {
	Name     string `json:"name"`
	Mimetype string `json:"mimetype"`
	URLPriv  string `json:"url_private"`
	Size     int64  `json:"size"`
}

type slackMessage struct {
	User            string      `json:"user"`
	BotID           string      `json:"bot_id"`
	Text            string      `json:"text"`
	TS              string      `json:"ts"`
	ThreadTS        string      `json:"thread_ts"`
	Channel         string      `json:"channel"`
	ChannelType     string      `json:"channel_type"`
	Files           []slackFile `json:"files"`
	AssistantThread *struct {
		ActionToken string `json:"action_token"`
	} `json:"assistant_thread"`
}

func (b *bot) handleMessage(teamID string, raw json.RawMessage) {
	var m slackMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		slog.Warn("slack: message decode failed", "err", err)
		return
	}
	if m.User != "" && m.User == b.BotUserID() {
		return
	}
	if m.User == "" && m.BotID != "" {
		return
	}
	if m.Channel == "" || m.TS == "" {
		return
	}

	conv := b.convInfoFor(m.Channel, m.ChannelType)
	jid := chanlib.FormatSlackJID(cmp.Or(teamID, b.TeamID()), chanKind(conv.IsIM, conv.IsMpim), m.Channel)

	if m.AssistantThread != nil && m.AssistantThread.ActionToken != "" {
		root := cmp.Or(m.ThreadTS, m.TS)
		b.recordPane(cmp.Or(teamID, b.TeamID()), m.User, root, m.Channel)
	}

	content, atts := b.attachmentsFor(m.Text, m.Files)
	if content == "" && len(atts) == 0 {
		return
	}

	topic := ""
	if m.ThreadTS != "" && m.ThreadTS != m.TS {
		topic = m.ThreadTS
	}

	isGroup := !conv.IsIM
	verb := ""
	if isGroup && b.BotUserID() != "" && strings.Contains(m.Text, "<@"+b.BotUserID()+">") {
		verb = "mention"
	}

	senderName := b.userName(m.User)
	chatName := chatNameFrom(conv)

	if err := b.rc.SendMessage(chanlib.InboundMsg{
		ID:          m.TS,
		ChatJID:     jid,
		Sender:      "slack:user/" + m.User,
		SenderName:  senderName,
		Content:     content,
		Verb:        verb,
		Timestamp:   parseSlackTS(m.TS),
		Topic:       topic,
		Attachments: atts,
		IsGroup:     isGroup,
		ChatName:    chatName,
	}); err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
		return
	}
	b.lastInboundAt.Store(time.Now().Unix())
	slog.Debug("inbound", "chat_jid", jid, "sender_jid", "slack:user/"+m.User, "message_id", m.TS, "content_len", len(content))
}

type slackReaction struct {
	User     string `json:"user"`
	Reaction string `json:"reaction"`
	Item     struct {
		Channel  string `json:"channel"`
		TS       string `json:"ts"`
		ThreadTS string `json:"thread_ts"`
	} `json:"item"`
}

func (b *bot) handleReaction(teamID string, raw json.RawMessage) {
	var r slackReaction
	if err := json.Unmarshal(raw, &r); err != nil {
		slog.Warn("slack: reaction decode failed", "err", err)
		return
	}
	if r.User == "" || r.User == b.BotUserID() {
		return
	}
	if r.Item.Channel == "" || r.Item.TS == "" || r.Reaction == "" {
		return
	}
	conv := b.convInfoFor(r.Item.Channel, "")
	jid := chanlib.FormatSlackJID(cmp.Or(teamID, b.TeamID()), chanKind(conv.IsIM, conv.IsMpim), r.Item.Channel)
	if err := b.rc.SendMessage(chanlib.InboundMsg{
		ID:         r.Item.TS + ":r:" + r.Reaction,
		ChatJID:    jid,
		Sender:     "slack:user/" + r.User,
		SenderName: b.userName(r.User),
		Content:    r.Reaction,
		Timestamp:  time.Now().Unix(),
		Verb:       chanlib.ClassifyEmoji(r.Reaction),
		ReplyTo:    r.Item.TS,
		Topic:      r.Item.ThreadTS,
		Reaction:   r.Reaction,
		IsGroup:    !conv.IsIM,
		ChatName:   chatNameFrom(conv),
	}); err != nil {
		slog.Error("deliver reaction failed", "jid", jid, "err", err)
		return
	}
	b.lastInboundAt.Store(time.Now().Unix())
}

type slackJoin struct {
	User    string `json:"user"`
	Channel string `json:"channel"`
	EventTS string `json:"event_ts"`
}

func (b *bot) handleJoin(teamID string, raw json.RawMessage) {
	var j slackJoin
	if err := json.Unmarshal(raw, &j); err != nil {
		return
	}
	if j.User == "" || j.Channel == "" || j.User == b.BotUserID() {
		return
	}
	conv := b.convInfoFor(j.Channel, "")
	jid := chanlib.FormatSlackJID(cmp.Or(teamID, b.TeamID()), chanKind(conv.IsIM, conv.IsMpim), j.Channel)
	if err := b.rc.SendMessage(chanlib.InboundMsg{
		ID:         "join:" + j.User + ":" + j.EventTS,
		ChatJID:    jid,
		Sender:     "slack:user/" + j.User,
		SenderName: b.userName(j.User),
		Content:    "joined",
		Verb:       "join",
		Timestamp:  time.Now().Unix(),
		IsGroup:    !conv.IsIM,
		ChatName:   chatNameFrom(conv),
	}); err != nil {
		slog.Error("deliver join failed", "jid", jid, "err", err)
		return
	}
	b.lastInboundAt.Store(time.Now().Unix())
}

// ===== assistant pane (specs/6/D) =====

// defaultPanePrompts is the suggested-prompt set shown when a pane
// opens. Operator-overridable later via PERSONA.md (deferred).
var defaultPanePrompts = []panePrompt{
	{Title: "help", Message: "what can you do?"},
	{Title: "summarize", Message: "summarize my latest thread"},
	{Title: "research", Message: "research a topic for me"},
}

type panePrompt struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

type assistantThreadEvent struct {
	AssistantThread struct {
		UserID    string `json:"user_id"`
		ChannelID string `json:"channel_id"`
		ThreadTS  string `json:"thread_ts"`
		Context   struct {
			ChannelID string `json:"channel_id"`
			TeamID    string `json:"team_id"`
		} `json:"context"`
	} `json:"assistant_thread"`
}

// handleAssistantThreadStarted persists the pane row, sets the pane
// title + default suggested prompts, and synthesizes a pane_open
// inbound so gateway sees the open as a turn trigger.
func (b *bot) handleAssistantThreadStarted(teamID string, raw json.RawMessage) {
	var ev assistantThreadEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		slog.Warn("slack: assistant_thread_started decode failed", "err", err)
		return
	}
	at := ev.AssistantThread
	if at.UserID == "" || at.ChannelID == "" || at.ThreadTS == "" {
		slog.Warn("slack: assistant_thread_started missing fields", "user", at.UserID, "channel", at.ChannelID, "thread", at.ThreadTS)
		return
	}
	team := cmp.Or(teamID, b.TeamID())
	b.recordPane(team, at.UserID, at.ThreadTS, at.ChannelID)
	if ctx := at.Context.ChannelID; ctx != "" && b.store != nil {
		ctxTeam := cmp.Or(at.Context.TeamID, team)
		ctxJID := chanlib.FormatSlackJID(ctxTeam, "channel", ctx)
		_ = b.store.SetPaneContext(team, at.UserID, at.ThreadTS, ctxJID)
	}

	go b.setPaneTitle(at.ChannelID, at.ThreadTS, b.paneTitle())
	go b.setSuggestedPrompts(at.ChannelID, at.ThreadTS, defaultPanePrompts)

	// Synthetic inbound — gateway routes pane_open like any verb.
	// Content empty, sender = user_id; no Slack TS for the open event,
	// so synthesize an ID from thread_ts to keep uniqueness.
	conv := b.convInfoFor(at.ChannelID, "im")
	jid := chanlib.FormatSlackJID(team, chanKind(conv.IsIM, conv.IsMpim), at.ChannelID)
	if err := b.rc.SendMessage(chanlib.InboundMsg{
		ID:         "pane_open:" + at.ThreadTS,
		ChatJID:    jid,
		Sender:     "slack:user/" + at.UserID,
		SenderName: b.userName(at.UserID),
		Verb:       "pane_open",
		Timestamp:  time.Now().Unix(),
		IsGroup:    false,
		ChatName:   chatNameFrom(conv),
	}); err != nil {
		slog.Error("deliver pane_open failed", "jid", jid, "err", err)
	}
}

// handleAssistantThreadContextChanged updates the workspace channel
// the user is viewing while the pane is open. Does NOT synthesize a
// turn — context change alone isn't a user action.
func (b *bot) handleAssistantThreadContextChanged(teamID string, raw json.RawMessage) {
	var ev assistantThreadEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		slog.Warn("slack: assistant_thread_context_changed decode failed", "err", err)
		return
	}
	at := ev.AssistantThread
	if at.UserID == "" || at.ThreadTS == "" || b.store == nil {
		return
	}
	team := cmp.Or(teamID, b.TeamID())
	ctxJID := ""
	if ctx := at.Context.ChannelID; ctx != "" {
		ctxJID = chanlib.FormatSlackJID(cmp.Or(at.Context.TeamID, team), "channel", ctx)
	}
	if err := b.store.SetPaneContext(team, at.UserID, at.ThreadTS, ctxJID); err != nil {
		slog.Warn("slack: pane context update failed", "err", err)
	}
}

// paneTitle returns the pane title shown in Slack's sidebar. Format:
// "<assistant> — chat" when ASSISTANT_NAME is set; otherwise just "chat".
func (b *bot) paneTitle() string {
	if name := b.cfg.AssistantName; name != "" {
		return name + " — chat"
	}
	return "chat"
}

func (b *bot) setPaneTitle(channelID, threadTS, title string) {
	form := url.Values{}
	form.Set("channel_id", channelID)
	form.Set("thread_ts", threadTS)
	form.Set("title", title)
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := b.postForm(context.Background(), "/assistant.threads.setTitle", form, &resp); err != nil {
		slog.Debug("slack setTitle failed", "channel", channelID, "err", err)
		return
	}
	if !resp.OK {
		slog.Warn("slack setTitle non-ok", "err", resp.Error, "channel", channelID)
	}
}

func (b *bot) setSuggestedPrompts(channelID, threadTS string, prompts []panePrompt) {
	if len(prompts) == 0 {
		return
	}
	pj, err := json.Marshal(prompts)
	if err != nil {
		return
	}
	form := url.Values{}
	form.Set("channel_id", channelID)
	form.Set("thread_ts", threadTS)
	form.Set("prompts", string(pj))
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := b.postForm(context.Background(), "/assistant.threads.setSuggestedPrompts", form, &resp); err != nil {
		slog.Debug("slack setSuggestedPrompts failed", "channel", channelID, "err", err)
		return
	}
	if !resp.OK {
		slog.Warn("slack setSuggestedPrompts non-ok", "err", resp.Error, "channel", channelID)
	}
}

func (b *bot) attachmentsFor(content string, files []slackFile) (string, []chanlib.InboundAttachment) {
	var atts []chanlib.InboundAttachment
	for _, f := range files {
		if f.URLPriv == "" {
			continue
		}
		name := f.Name
		if name == "" {
			name = "attachment"
		}
		content += fmt.Sprintf(" [Attachment: %s]", name)
		u := f.URLPriv
		if b.cfg.ListenURL != "" && b.files != nil {
			u = fmt.Sprintf("%s/files/%s", b.cfg.ListenURL, b.files.Put(f.URLPriv))
		}
		atts = append(atts, chanlib.InboundAttachment{
			Mime: f.Mimetype, Filename: name, URL: u, Size: f.Size,
		})
	}
	return content, atts
}

// convInfoFor resolves conversation metadata (cached). On conversations.info
// failure, returns a synthetic info derived from the event's channel_type so
// the inbound still gets a sensible JID kind and IsGroup flag.
func (b *bot) convInfoFor(channelID, channelType string) *slackConvInfo {
	if v, ok := b.chans.get(channelID); ok {
		return v.(*slackConvInfo)
	}
	if info, err := b.conversationsInfo(channelID); err == nil && info != nil {
		b.chans.put(channelID, info)
		return info
	}
	switch channelType {
	case "im":
		return &slackConvInfo{IsIM: true}
	case "mpim":
		return &slackConvInfo{IsMpim: true}
	default:
		return &slackConvInfo{}
	}
}

func (b *bot) userName(userID string) string {
	if userID == "" {
		return ""
	}
	if v, ok := b.users.get(userID); ok {
		return v.(string)
	}
	name, err := b.usersInfo(userID)
	if err != nil || name == "" {
		return userID
	}
	b.users.put(userID, name)
	return name
}

func chatNameFrom(c *slackConvInfo) string {
	if c.IsIM || c.Name == "" {
		return ""
	}
	return "#" + c.Name
}

// ===== Outbound BotHandler =====

func (b *bot) Send(req chanlib.SendRequest) (string, error) {
	parts, err := parseJID(req.ChatJID)
	if err != nil {
		return "", err
	}
	body := url.Values{}
	body.Set("channel", parts.ID)
	body.Set("text", req.Content)
	if threadTS := cmp.Or(req.ThreadID, req.ReplyTo); threadTS != "" {
		body.Set("thread_ts", threadTS)
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		TS    string `json:"ts"`
	}
	if err := b.postForm(context.Background(), "/chat.postMessage", body, &resp); err != nil {
		return "", fmt.Errorf("slack send: %w", err)
	}
	if !resp.OK {
		return "", fmt.Errorf("slack send: %s", resp.Error)
	}
	slog.Debug("send", "chat_jid", req.ChatJID, "message_id", resp.TS, "source", "slack")
	b.applyPanePending(parts.ID)
	return resp.TS, nil
}

// applyPanePending fires any pending pane title / suggested-prompts
// calls staged via MCP for the pane bound to channelID. One-shot per
// outbound — drains the staged values. No-op when channel isn't a pane.
func (b *bot) applyPanePending(channelID string) {
	if b.store == nil {
		return
	}
	pane, ok := b.store.GetPaneByChannel(channelID)
	if !ok {
		return
	}
	key := paneKey(pane.TeamID, pane.UserID, pane.ThreadTS)
	b.paneOutMu.Lock()
	prompts := b.pendingPrompts[key]
	delete(b.pendingPrompts, key)
	title := b.pendingTitle[key]
	delete(b.pendingTitle, key)
	b.paneOutMu.Unlock()
	if title != "" {
		go b.setPaneTitle(pane.ChannelID, pane.ThreadTS, title)
	}
	if len(prompts) > 0 {
		go b.setSuggestedPrompts(pane.ChannelID, pane.ThreadTS, prompts)
	}
}

func paneKey(team, user, thread string) string { return team + "|" + user + "|" + thread }

// stagePanePromptsByJID looks up the pane bound to jid (DM channel)
// and stages prompts for the next Send. Returns error if jid doesn't
// map to an open pane (caller renders 404).
func (b *bot) stagePanePromptsByJID(jid string, prompts []panePrompt) error {
	pane, ok, err := b.paneByJID(jid)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("no open pane for jid")
	}
	b.setPanePending(pane.TeamID, pane.UserID, pane.ThreadTS, prompts, "")
	return nil
}

// stagePaneTitleByJID stages a one-shot pane title for the next Send.
func (b *bot) stagePaneTitleByJID(jid, title string) error {
	pane, ok, err := b.paneByJID(jid)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("no open pane for jid")
	}
	if title == "" {
		return errors.New("title required")
	}
	b.setPanePending(pane.TeamID, pane.UserID, pane.ThreadTS, nil, title)
	return nil
}

func (b *bot) paneByJID(jid string) (store.PaneSession, bool, error) {
	if b.store == nil {
		return store.PaneSession{}, false, errors.New("store not configured")
	}
	parts, err := parseJID(jid)
	if err != nil {
		return store.PaneSession{}, false, err
	}
	p, ok := b.store.GetPaneByChannel(parts.ID)
	return p, ok, nil
}

// setPanePending stages prompts and/or title to fire after the next
// Send into the given pane. Empty title leaves any prior staged title
// untouched (so prompts and title can be set independently). Replacing
// prompts with a non-nil empty slice clears them; nil leaves prior
// staging untouched.
func (b *bot) setPanePending(team, user, thread string, prompts []panePrompt, title string) {
	key := paneKey(team, user, thread)
	b.paneOutMu.Lock()
	defer b.paneOutMu.Unlock()
	if prompts != nil {
		b.pendingPrompts[key] = prompts
	}
	if title != "" {
		b.pendingTitle[key] = title
	}
}

func (b *bot) SendFile(jid, path, name, caption string) error {
	parts, err := parseJID(jid)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("slack open file: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("slack stat file: %w", err)
	}
	if name == "" {
		name = filepath.Base(path)
	}
	var get struct {
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
		UploadURL string `json:"upload_url"`
		FileID    string `json:"file_id"`
	}
	form := url.Values{}
	form.Set("filename", name)
	form.Set("length", strconv.FormatInt(st.Size(), 10))
	if err := b.postForm(context.Background(), "/files.getUploadURLExternal", form, &get); err != nil {
		return fmt.Errorf("slack upload url: %w", err)
	}
	if !get.OK {
		return fmt.Errorf("slack upload url: %s", get.Error)
	}
	req, err := http.NewRequestWithContext(context.Background(), "POST", get.UploadURL, f)
	if err != nil {
		return fmt.Errorf("slack upload req: %w", err)
	}
	req.ContentLength = st.Size()
	upResp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("slack upload: %w", err)
	}
	defer upResp.Body.Close()
	if upResp.StatusCode/100 != 2 {
		return fmt.Errorf("slack upload: status %d", upResp.StatusCode)
	}
	files, _ := json.Marshal([]map[string]string{{"id": get.FileID, "title": name}})
	complete := url.Values{}
	complete.Set("files", string(files))
	complete.Set("channel_id", parts.ID)
	if caption != "" {
		complete.Set("initial_comment", caption)
	}
	var done struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := b.postForm(context.Background(), "/files.completeUploadExternal", complete, &done); err != nil {
		return fmt.Errorf("slack complete upload: %w", err)
	}
	if !done.OK {
		return fmt.Errorf("slack complete upload: %s", done.Error)
	}
	return nil
}

// Typing surfaces a "thinking…" indicator in Slack. Two paths:
//   - Pane sessions (AI assistant): assistant.threads.setStatus, single shot.
//   - Regular DMs/channels: conversations.typing via TypingRefresher (3s refresh).
//
// Pane sessions are auto-detected from inbound assistant_thread.action_token
// payloads (see handleMessage).
func (b *bot) Typing(jid string, on bool) {
	pane, ok := b.lookupPane(jid)
	if !ok {
		b.typing.Set(jid, on)
		return
	}
	parts, err := parseJID(jid)
	if err != nil {
		return
	}
	status := ""
	if on {
		if name := b.cfg.AssistantName; name != "" {
			status = name + " is thinking…"
		} else {
			status = "thinking…"
		}
	}
	go func() {
		form := url.Values{}
		form.Set("channel_id", parts.ID)
		form.Set("thread_ts", pane.ThreadTS)
		form.Set("status", status)
		var resp struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := b.postForm(context.Background(), "/assistant.threads.setStatus", form, &resp); err != nil {
			slog.Debug("slack setStatus failed", "jid", jid, "err", err)
			return
		}
		if !resp.OK && resp.Error == "missing_scope" {
			slog.Warn("slack setStatus missing_scope (assistant:write required)", "jid", jid)
			return
		}
		if resp.OK && b.store != nil {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			_ = b.store.SetPaneStatusAt(pane.TeamID, pane.UserID, pane.ThreadTS, now)
		}
	}()
}

// recordPane persists a pane session triggered by an inbound carrying
// assistant_thread.action_token. teamID and userID are required for the
// PK; an empty user (rare — pane messages always have a user_id from
// Slack) skips persistence rather than write an unkeyable row.
func (b *bot) recordPane(teamID, userID, threadTS, channelID string) {
	if b.store == nil || teamID == "" || userID == "" || threadTS == "" || channelID == "" {
		return
	}
	if err := b.store.UpsertPane(teamID, userID, threadTS, channelID); err != nil {
		slog.Warn("slack: pane upsert failed", "channel", channelID, "err", err)
	}
}

// lookupPane reads the pane session by DM channel_id (extracted from
// the jid). Returns (zero, false) when the channel isn't a pane.
func (b *bot) lookupPane(jid string) (store.PaneSession, bool) {
	if b.store == nil {
		return store.PaneSession{}, false
	}
	parts, err := parseJID(jid)
	if err != nil {
		return store.PaneSession{}, false
	}
	return b.store.GetPaneByChannel(parts.ID)
}

func (b *bot) Post(req chanlib.PostRequest) (string, error) {
	return b.Send(chanlib.SendRequest{ChatJID: req.ChatJID, Content: req.Content})
}

func (b *bot) Like(req chanlib.LikeRequest) error {
	parts, err := parseJID(req.ChatJID)
	if err != nil {
		return err
	}
	emoji := strings.Trim(req.Reaction, ":")
	if emoji == "" {
		emoji = "thumbsup"
	}
	form := url.Values{}
	form.Set("channel", parts.ID)
	form.Set("name", emoji)
	form.Set("timestamp", req.TargetID)
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := b.postForm(context.Background(), "/reactions.add", form, &resp); err != nil {
		return fmt.Errorf("slack like: %w", err)
	}
	if !resp.OK && resp.Error != "already_reacted" {
		return fmt.Errorf("slack like: %s", resp.Error)
	}
	return nil
}

func (b *bot) Dislike(req chanlib.DislikeRequest) error {
	return b.Like(chanlib.LikeRequest{ChatJID: req.ChatJID, TargetID: req.TargetID, Reaction: "thumbsdown"})
}

func (b *bot) Delete(req chanlib.DeleteRequest) error {
	parts, err := parseJID(req.ChatJID)
	if err != nil {
		return err
	}
	form := url.Values{}
	form.Set("channel", parts.ID)
	form.Set("ts", req.TargetID)
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := b.postForm(context.Background(), "/chat.delete", form, &resp); err != nil {
		return fmt.Errorf("slack delete: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("slack delete: %s", resp.Error)
	}
	return nil
}

func (b *bot) Edit(req chanlib.EditRequest) error {
	parts, err := parseJID(req.ChatJID)
	if err != nil {
		return err
	}
	form := url.Values{}
	form.Set("channel", parts.ID)
	form.Set("ts", req.TargetID)
	form.Set("text", req.Content)
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := b.postForm(context.Background(), "/chat.update", form, &resp); err != nil {
		return fmt.Errorf("slack edit: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("slack edit: %s", resp.Error)
	}
	return nil
}

func (b *bot) Forward(chanlib.ForwardRequest) (string, error) {
	return "", chanlib.Unsupported("forward", "slack",
		"Slack has no forward primitive. Use `send(jid=<target>, content=\"<quoted text>\\n\\n— from <source>\")` to relay manually.")
}

func (b *bot) Quote(chanlib.QuoteRequest) (string, error) {
	return "", chanlib.Unsupported("quote", "slack",
		"Slack has no quote primitive. Use `send(jid=<chat>, content=\"<your take>\", reply_to=<source_ts>)` to thread under the source.")
}

func (b *bot) Repost(chanlib.RepostRequest) (string, error) {
	return "", chanlib.Unsupported("repost", "slack",
		"Slack has no repost. Use `send` to manually re-share content with attribution.")
}

// ===== Slack Web API helpers =====

func (b *bot) authTest(ctx context.Context) (userID, teamID string, err error) {
	var resp struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		UserID string `json:"user_id"`
		TeamID string `json:"team_id"`
	}
	if err := b.postForm(ctx, "/auth.test", url.Values{}, &resp); err != nil {
		return "", "", err
	}
	if !resp.OK {
		return "", "", fmt.Errorf("auth.test: %s", resp.Error)
	}
	return resp.UserID, resp.TeamID, nil
}

func (b *bot) usersInfo(userID string) (string, error) {
	var resp struct {
		OK   bool `json:"ok"`
		User struct {
			Name     string `json:"name"`
			RealName string `json:"real_name"`
			Profile  struct {
				DisplayName string `json:"display_name"`
				RealName    string `json:"real_name"`
			} `json:"profile"`
		} `json:"user"`
		Error string `json:"error"`
	}
	form := url.Values{}
	form.Set("user", userID)
	if err := b.postForm(context.Background(), "/users.info", form, &resp); err != nil {
		return "", err
	}
	if !resp.OK {
		return "", errors.New(resp.Error)
	}
	return cmp.Or(
		resp.User.Profile.DisplayName,
		resp.User.Profile.RealName,
		resp.User.RealName,
		resp.User.Name,
	), nil
}

type slackConvInfo struct {
	Name   string `json:"name"`
	IsIM   bool   `json:"is_im"`
	IsMpim bool   `json:"is_mpim"`
}

func (b *bot) conversationsInfo(channelID string) (*slackConvInfo, error) {
	var resp struct {
		OK      bool           `json:"ok"`
		Channel *slackConvInfo `json:"channel"`
		Error   string         `json:"error"`
	}
	form := url.Values{}
	form.Set("channel", channelID)
	if err := b.postForm(context.Background(), "/conversations.info", form, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, errors.New(resp.Error)
	}
	return resp.Channel, nil
}

func (b *bot) postForm(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, "POST", b.api+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.cfg.BotToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("User-Agent", chanlib.UserAgent)
	resp, err := chanlib.DoWithRetry(b.http, req)
	if err != nil {
		return fmt.Errorf("slack %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("slack %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

// parseSlackTS converts "1700000000.000200" to unix seconds; falls back to now on parse failure.
func parseSlackTS(ts string) int64 {
	if ts == "" {
		return time.Now().Unix()
	}
	s, _, _ := strings.Cut(ts, ".")
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Now().Unix()
	}
	return n
}

// ===== TTL cache =====

type ttlEntry struct {
	v   any
	exp time.Time
}

type ttlCache struct {
	mu  sync.Mutex
	m   map[string]ttlEntry
	ttl time.Duration
}

func newTTLCache(ttl time.Duration) *ttlCache {
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	return &ttlCache{m: map[string]ttlEntry{}, ttl: ttl}
}

func (c *ttlCache) get(k string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.exp) {
		delete(c.m, k)
		return nil, false
	}
	return e.v, true
}

func (c *ttlCache) put(k string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[k] = ttlEntry{v: v, exp: time.Now().Add(c.ttl)}
}
