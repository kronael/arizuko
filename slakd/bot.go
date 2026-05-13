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
)

const (
	slackBase       = "https://slack.com/api"
	signingWindow   = 5 * time.Minute
	defaultCacheTTL = 15 * time.Minute
)

type bot struct {
	chanlib.NoVoiceSender

	cfg   config
	api   string
	http  *http.Client
	rc    *chanlib.RouterClient
	files *chanlib.URLCache
	users *ttlCache
	chans *ttlCache

	botUserID atomic.Value
	teamID    atomic.Value

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
		if head.Subtype != "" && head.Subtype != "thread_broadcast" {
			return
		}
		b.handleMessage(teamID, raw)
	case "reaction_added":
		b.handleReaction(teamID, raw)
	case "member_joined_channel":
		b.handleJoin(teamID, raw)
	}
}

type slackFile struct {
	Name     string `json:"name"`
	Mimetype string `json:"mimetype"`
	URLPriv  string `json:"url_private"`
	Size     int64  `json:"size"`
}

type slackMessage struct {
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
	jid := formatJID(cmp.Or(teamID, b.TeamID()), chanKind(conv.IsIM, conv.IsMpim), m.Channel)

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
		Channel string `json:"channel"`
		TS      string `json:"ts"`
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
	jid := formatJID(cmp.Or(teamID, b.TeamID()), chanKind(conv.IsIM, conv.IsMpim), r.Item.Channel)
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
	jid := formatJID(cmp.Or(teamID, b.TeamID()), chanKind(conv.IsIM, conv.IsMpim), j.Channel)
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
	body.Set("channel", parts.id)
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

// Typing is a no-op: Slack has no bot-side typing primitive.
func (b *bot) Typing(string, bool) {}

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
