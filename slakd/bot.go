package main

import (
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
)

// slackBase is the Slack Web API root. Overridable in tests via newBotWithBase.
const slackBase = "https://slack.com/api"

// signingWindow is the max permitted clock skew on inbound webhooks.
const signingWindow = 5 * time.Minute

// cacheTTL is the default user/conversation cache TTL (15 min per spec).
const defaultCacheTTL = 15 * time.Minute

type bot struct {
	chanlib.NoVoiceSender

	cfg   config
	api   string // Slack Web API base ("https://slack.com/api" in prod)
	http  *http.Client
	rc    *chanlib.RouterClient
	files *chanlib.URLCache
	users *ttlCache
	chans *ttlCache

	botUserID atomic.Value // string — set by authTest at startup
	teamID    atomic.Value // string — workspace ID

	connected     atomic.Bool
	lastInboundAt atomic.Int64
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
		cfg:   cfg,
		api:   base,
		http:  &http.Client{Timeout: 30 * time.Second},
		users: newTTLCache(cfg.CacheTTL),
		chans: newTTLCache(cfg.CacheTTL),
	}
	b.lastInboundAt.Store(time.Now().Unix())
	return b, nil
}

// start verifies the bot token via auth.test, stores bot_user_id + team_id,
// and marks the bot connected. Failure here aborts startup.
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
}

// verifySignature confirms the X-Slack-Signature header matches the body
// signed with the signing secret. Returns nil on success.
// Spec: signature is hex of HMAC-SHA256 over "v0:<ts>:<body>".
// Reject if |now - ts| > 5 min.
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

// handleEvent dispatches a verified Events API payload. URL verification
// returns the challenge; event_callback is processed inline.
func (b *bot) handleEvent(body []byte, w http.ResponseWriter) {
	// Use a small struct that captures the envelope shape we care about.
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
		return
	case "event_callback":
		// ack immediately, dispatch synchronously (we're fast); Slack
		// retries on non-2xx, which we do not want for handled events.
		w.WriteHeader(http.StatusOK)
		b.dispatch(env.TeamID, env.Event)
		return
	default:
		// Other envelope types (e.g. app_rate_limited) — ack and ignore.
		w.WriteHeader(http.StatusOK)
	}
}

// dispatch routes a single Slack event by its `type` field.
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
		// We handle message.channels / .groups / .im / .mpim under the
		// single Events API `message` type — channel_type discriminates.
		// Subtype "message_changed", "message_deleted", "bot_message" etc
		// are filtered: only top-level user messages flow inbound.
		if head.Subtype != "" && head.Subtype != "thread_broadcast" {
			return
		}
		b.handleMessage(teamID, raw)
	case "reaction_added":
		b.handleReaction(teamID, raw)
	case "reaction_removed":
		// v1: not emitted (per spec).
		return
	case "member_joined_channel":
		b.handleJoin(teamID, raw)
	case "file_shared":
		// Slack also delivers a `message` with files[]; we attach via
		// that path. file_shared without a message is rare; skip — the
		// follow-up message event carries the attachment.
		return
	default:
		// Unhandled event types are normal — Slack emits many; only the
		// subset declared in the manifest reaches us.
	}
}

type slackFile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Mimetype  string `json:"mimetype"`
	URLPriv   string `json:"url_private"`
	Size      int64  `json:"size"`
}

type slackMessage struct {
	Type        string      `json:"type"`
	Subtype     string      `json:"subtype"`
	User        string      `json:"user"`
	BotID       string      `json:"bot_id"`
	Text        string      `json:"text"`
	TS          string      `json:"ts"`
	ThreadTS    string      `json:"thread_ts"`
	Channel     string      `json:"channel"`
	ChannelType string      `json:"channel_type"`
	Files       []slackFile `json:"files"`
}

func (b *bot) handleMessage(teamID string, raw json.RawMessage) {
	var m slackMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		slog.Warn("slack: message decode failed", "err", err)
		return
	}
	// Skip our own messages: matches bot_user_id, OR bot_id is set with no user (app posts).
	if m.User != "" && m.User == b.BotUserID() {
		return
	}
	if m.User == "" && m.BotID != "" {
		return
	}
	if m.Channel == "" || m.TS == "" {
		return
	}

	kind, isIM := b.kindFor(m.Channel, m.ChannelType)
	jid := formatJID(teamIDFallback(teamID, b.TeamID()), kind, m.Channel)

	content := m.Text
	atts := b.attachmentsFor(m.Files, &content)
	if content == "" && len(atts) == 0 {
		return
	}

	topic := ""
	if m.ThreadTS != "" && m.ThreadTS != m.TS {
		topic = m.ThreadTS
	}

	isGroup := !isIM
	// Mention detection: text contains `<@bot_user_id>`. Slack's app_mention
	// fires alongside message.*; we derive Verb here per spec.
	verb := ""
	if isGroup && b.BotUserID() != "" && strings.Contains(m.Text, "<@"+b.BotUserID()+">") {
		verb = "mention"
	}

	senderName := b.userName(m.User)
	chatName := b.chatName(m.Channel)

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
		Type    string `json:"type"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	} `json:"item"`
	EventTS string `json:"event_ts"`
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
	kind, isIM := b.kindFor(r.Item.Channel, "")
	jid := formatJID(teamIDFallback(teamID, b.TeamID()), kind, r.Item.Channel)
	if err := b.rc.SendMessage(chanlib.InboundMsg{
		ID:         r.Item.TS + ":r:" + r.Reaction,
		ChatJID:    jid,
		Sender:     "slack:user/" + r.User,
		SenderName: b.userName(r.User),
		Content:    r.Reaction,
		Timestamp:  time.Now().Unix(),
		Verb:       chanlib.ClassifyEmoji(r.Reaction),
		ReplyTo:    r.Item.TS,
		Reaction:   r.Reaction,
		IsGroup:    !isIM,
		ChatName:   b.chatName(r.Item.Channel),
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
	kind, isIM := b.kindFor(j.Channel, "")
	jid := formatJID(teamIDFallback(teamID, b.TeamID()), kind, j.Channel)
	if err := b.rc.SendMessage(chanlib.InboundMsg{
		ID:         "join:" + j.User + ":" + j.EventTS,
		ChatJID:    jid,
		Sender:     "slack:user/" + j.User,
		SenderName: b.userName(j.User),
		Content:    "joined",
		Verb:       "join",
		Timestamp:  time.Now().Unix(),
		IsGroup:    !isIM,
		ChatName:   b.chatName(j.Channel),
	}); err != nil {
		slog.Error("deliver join failed", "jid", jid, "err", err)
		return
	}
	b.lastInboundAt.Store(time.Now().Unix())
}

// attachmentsFor folds Slack file blobs into chanlib attachments and appends
// "[Attachment: <name>]" markers to content. Matches discd.buildAttachments.
func (b *bot) attachmentsFor(files []slackFile, content *string) []chanlib.InboundAttachment {
	if len(files) == 0 {
		return nil
	}
	var atts []chanlib.InboundAttachment
	for _, f := range files {
		if f.URLPriv == "" {
			continue
		}
		name := f.Name
		if name == "" {
			name = "attachment"
		}
		*content += fmt.Sprintf(" [Attachment: %s]", name)
		u := f.URLPriv
		if b.cfg.ListenURL != "" && b.files != nil {
			u = fmt.Sprintf("%s/files/%s", b.cfg.ListenURL, b.files.Put(f.URLPriv))
		}
		atts = append(atts, chanlib.InboundAttachment{
			Mime: f.Mimetype, Filename: name, URL: u, Size: f.Size,
		})
	}
	return atts
}

// kindFor returns the JID kind segment + whether it's a 1:1 DM. Falls back
// to channel_type from the event when conversations.info has no entry.
func (b *bot) kindFor(channelID, channelType string) (kind string, isIM bool) {
	if v, ok := b.chans.get(channelID); ok {
		c := v.(*slackConvInfo)
		return chanKind(c.IsIM, c.IsMpim), c.IsIM
	}
	// Lazy lookup; on failure use channel_type from the event.
	info, err := b.conversationsInfo(channelID)
	if err == nil && info != nil {
		b.chans.put(channelID, info)
		return chanKind(info.IsIM, info.IsMpim), info.IsIM
	}
	switch channelType {
	case "im":
		return "dm", true
	case "mpim":
		return "group", false
	default:
		return "channel", false
	}
}

// userName resolves to a display name via users.info, cached.
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

// chatName resolves to "#channel" form via conversations.info, cached.
func (b *bot) chatName(channelID string) string {
	if v, ok := b.chans.get(channelID); ok {
		c := v.(*slackConvInfo)
		if c.IsIM {
			return ""
		}
		if c.Name != "" {
			return "#" + c.Name
		}
	}
	info, err := b.conversationsInfo(channelID)
	if err != nil || info == nil {
		return ""
	}
	b.chans.put(channelID, info)
	if info.IsIM {
		return ""
	}
	if info.Name != "" {
		return "#" + info.Name
	}
	return ""
}

// ===== Outbound BotHandler =====

func (b *bot) Send(req chanlib.SendRequest) (string, error) {
	parts, err := parseJID(req.ChatJID)
	if err != nil {
		return "", err
	}
	body := url.Values{}
	body.Set("channel", parts.id)
	body.Set("text", req.Content)
	threadTS := req.ThreadID
	if threadTS == "" {
		threadTS = req.ReplyTo
	}
	if threadTS != "" {
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
	return resp.TS, nil
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
	// Step 1: getUploadURLExternal
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
	// Step 2: PUT bytes to upload_url
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
	// Step 3: completeUploadExternal
	files, _ := json.Marshal([]map[string]string{{"id": get.FileID, "title": name}})
	complete := url.Values{}
	complete.Set("files", string(files))
	complete.Set("channel_id", parts.id)
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

func (b *bot) Typing(string, bool) {
	// Slack has no typing primitive for bots — RTM `typing` is user-only.
	// Silently skip; the agent caller treats this as a soft signal.
}

func (b *bot) Post(req chanlib.PostRequest) (string, error) {
	// Slack channels are the post surface — map to send (spec).
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
	form.Set("channel", parts.id)
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
	// Spec: dislike-via-like — emit reactions.add with thumbsdown.
	return b.Like(chanlib.LikeRequest{ChatJID: req.ChatJID, TargetID: req.TargetID, Reaction: "thumbsdown"})
}

func (b *bot) Delete(req chanlib.DeleteRequest) error {
	parts, err := parseJID(req.ChatJID)
	if err != nil {
		return err
	}
	form := url.Values{}
	form.Set("channel", parts.id)
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
	form.Set("channel", parts.id)
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
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
		UserID    string `json:"user_id"`
		BotID     string `json:"bot_id"`
		TeamID    string `json:"team_id"`
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
	if n := resp.User.Profile.DisplayName; n != "" {
		return n, nil
	}
	if n := resp.User.Profile.RealName; n != "" {
		return n, nil
	}
	if n := resp.User.RealName; n != "" {
		return n, nil
	}
	return resp.User.Name, nil
}

type slackConvInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IsIM    bool   `json:"is_im"`
	IsMpim  bool   `json:"is_mpim"`
	IsGroup bool   `json:"is_group"`
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

// postForm sends a urlencoded request with bearer token, decodes JSON into out.
// Respects Retry-After on 429; logs and returns the underlying status error.
func (b *bot) postForm(ctx context.Context, path string, form url.Values, out any) error {
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", b.api+path, strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+b.cfg.BotToken)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
		req.Header.Set("User-Agent", chanlib.UserAgent)
		resp, err := b.http.Do(req)
		if err != nil {
			return fmt.Errorf("slack %s: %w", path, err)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			ra := resp.Header.Get("Retry-After")
			d := parseRetryAfter(ra)
			slog.Warn("slack rate limited", "path", path, "retry_after", d, "attempt", attempt+1)
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
			}
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("slack %s: status %d", path, resp.StatusCode)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
	}
	return fmt.Errorf("slack %s: rate-limit exhausted", path)
}

func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return time.Second
	}
	if n, err := strconv.Atoi(h); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return time.Second
}

// parseSlackTS turns "1700000000.000200" into a unix seconds int64. On parse
// failure returns time.Now (events have a TS; this is only a safety net).
func parseSlackTS(ts string) int64 {
	if ts == "" {
		return time.Now().Unix()
	}
	dot := strings.IndexByte(ts, '.')
	s := ts
	if dot >= 0 {
		s = ts[:dot]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Now().Unix()
	}
	return n
}

// teamIDFallback prefers the per-event team_id; falls back to auth.test's.
func teamIDFallback(eventTeamID, authTeamID string) string {
	if eventTeamID != "" {
		return eventTeamID
	}
	return authTeamID
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
	if !ok || time.Now().After(e.exp) {
		return nil, false
	}
	return e.v, true
}

func (c *ttlCache) put(k string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[k] = ttlEntry{v: v, exp: time.Now().Add(c.ttl)}
}
