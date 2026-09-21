package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/kronael/arizuko/chanlib"
)

type bot struct {
	session       *discordgo.Session
	cfg           config
	rc            *chanlib.RouterClient
	typing        *chanlib.TypingRefresher
	files         *chanlib.URLCache
	lastInboundAt atomic.Int64
	// activeThread maps a parent-channel jid → the thread id its live
	// conversation is in, so the typing indicator shows in the thread where the
	// reply lands, not leaking to the channel root. Set/cleared on each inbound.
	activeThread sync.Map
}

func (b *bot) LastInboundAt() int64 { return b.lastInboundAt.Load() }

var _ chanlib.BotHandler = (*bot)(nil)

func newBot(cfg config) (*bot, error) {
	token := cfg.DiscordToken
	if !cfg.UserMode {
		token = "Bot " + token
	}
	s, err := discordgo.New(token)
	if err != nil {
		return nil, fmt.Errorf("discord session: %w", err)
	}
	if cfg.UserMode {
		// User-account IDENTIFY. The default discordgo Identify struct encodes
		// Intents=0 with no omitempty — the user gateway reads that as "intent
		// filtering with no intents", which collapses MESSAGE_CREATE delivery to
		// mentions + author-self only. Send a verbatim user-shaped payload via
		// RawIdentifyData so Discord pushes every channel message.
		raw, err := buildUserIdentify(cfg.DiscordToken)
		if err != nil {
			return nil, fmt.Errorf("build user identify: %w", err)
		}
		s.RawIdentifyData = raw
	} else {
		// Bot intents: guild/DM messages + reactions + message content.
		s.Identify.Intents = discordgo.IntentsGuildMessages |
			discordgo.IntentsDirectMessages |
			discordgo.IntentMessageContent |
			discordgo.IntentsGuildMessageReactions |
			discordgo.IntentsDirectMessageReactions
	}
	b := &bot{session: s, cfg: cfg}
	b.lastInboundAt.Store(time.Now().Unix())
	b.typing = chanlib.NewTypingRefresher(4*time.Second, chanlib.DefaultTypingMaxTTL, b.sendTyping, nil)
	return b, nil
}

func (b *bot) start(rc *chanlib.RouterClient) error {
	b.rc = rc
	b.session.AddHandler(b.onMessage)
	b.session.AddHandler(b.onReactionAdd)
	b.session.AddHandler(b.onPress)
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("discord open: %w", err)
	}
	slog.Info("discord connected", "user", b.session.State.User.Username)
	return nil
}

// buildUserIdentify produces the OP 2 `d` payload for user-account gateway sessions.
// Shape mirrors discord.py-self's gateway.identify(). Critically NO `intents` key —
// its presence flips the gateway to bot-filtering semantics and collapses
// MESSAGE_CREATE to mentions only.
func buildUserIdentify(token string) (json.RawMessage, error) {
	payload := map[string]any{
		"token": token,
		// Capabilities bits: 0,2,3,4,5,6,7,8,9,10,12,14 (discord.py-self
		// Capabilities.default; matches official client). = 22525.
		"capabilities": 22525,
		"properties": map[string]any{
			"os":                       "Mac OS X",
			"browser":                  "Chrome",
			"device":                   "",
			"system_locale":            "en-US",
			"browser_user_agent":       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36",
			"browser_version":          "136.0.0.0",
			"os_version":               "10.15.7",
			"referrer":                 "",
			"referring_domain":         "",
			"referrer_current":         "",
			"referring_domain_current": "",
			"release_channel":          "stable",
			"client_build_number":      397755,
			"client_event_source":      nil,
		},
		"presence": map[string]any{
			"status":     "online",
			"since":      0,
			"activities": []any{},
			"afk":        false,
		},
		"compress": false,
		"client_state": map[string]any{
			"guild_versions": map[string]any{},
		},
	}
	return json.Marshal(payload)
}

func (b *bot) stop() {
	b.typing.Stop()
	b.session.Close()
}

func (b *bot) isConnected() bool {
	return b.session != nil && b.session.DataReady
}

func (b *bot) channelName(id string) string {
	ch, err := b.session.State.Channel(id)
	if err == nil && ch.Name != "" {
		return "#" + ch.Name
	}
	return ""
}

// buildAttachments appends attachment text to content and returns chanlib attachments.
// Proxies through the local file cache when listenURL is set.
func buildAttachments(
	ctx context.Context, raw []*discordgo.MessageAttachment, content, listenURL string,
	cache *chanlib.URLCache,
) (string, []chanlib.InboundAttachment) {
	var atts []chanlib.InboundAttachment
	for _, att := range raw {
		if att == nil || att.URL == "" {
			continue
		}
		name := att.Filename
		if name == "" {
			name = "attachment"
		}
		content += fmt.Sprintf(" [Attachment: %s]", name)
		u := att.URL
		if listenURL != "" && cache != nil {
			id, err := cache.Put(ctx, att.URL)
			if err != nil {
				// The id still works in this process; only a restart loses it
				// (URLCache.Put, BUGS L163).
				slog.Error("attachment url not recorded", "id", id, "err", err)
			}
			u = fmt.Sprintf("%s/files/%s", listenURL, id)
		}
		atts = append(atts, chanlib.InboundAttachment{
			Mime: att.ContentType, Filename: name, URL: u,
		})
	}
	return content, atts
}

func (b *bot) onMessage(_ *discordgo.Session, m *discordgo.MessageCreate) {
	if m == nil || m.Author == nil || m.Author.Bot {
		return
	}
	// User-mode: Discord echoes our own MESSAGE_CREATE over the gateway.
	// Author.Bot is false for user accounts, so the line above doesn't catch
	// it. Drop self-authored messages to avoid an infinite retrigger loop.
	if self := b.session.State.User; self != nil && m.Author.ID == self.ID {
		return
	}
	// The gateway handler carries no context of its own.
	content, atts := buildAttachments(context.Background(), m.Attachments, m.Content,
		b.cfg.ListenURL, b.files)
	if content == "" {
		return
	}
	content = replaceMentions(content, b.cfg.AssistantName, b.session.State.User, m.Mentions)

	jid, topic, ch := b.chatAndTopic(m.GuildID, m.ChannelID)
	// Track where this chat's live conversation sits so typing shows in the
	// thread (topic), not the parent channel root.
	if topic != "" {
		b.activeThread.Store(jid, topic)
	} else {
		b.activeThread.Delete(jid)
	}
	chatName := b.channelName(m.ChannelID)
	isGroup := isGroupChannel(ch)

	// Guild @mentions use verb "mention"; other guild messages use the default
	// "message" verb. Route mode (target#observe) plus verb-specific match
	// rows control which verbs fire the agent (mention-only channels keep a
	// high-priority `verb=mention` trigger row + catch-all `#observe`).
	// DMs always use "message".
	verb := ""
	if isGroup && b.isMentioned(m) {
		verb = "mention"
	}
	var replyTo string
	if m.MessageReference != nil {
		replyTo = m.MessageReference.MessageID
	}

	if err := b.rc.SendMessage(chanlib.InboundMsg{
		ID:          m.ID,
		ChatJID:     jid,
		Sender:      "discord:user/" + m.Author.ID,
		SenderName:  m.Author.Username,
		Content:     content,
		Verb:        verb,
		Timestamp:   m.Timestamp.Unix(),
		Topic:       topic,
		ReplyTo:     replyTo,
		Attachments: atts,
		IsGroup:     isGroup,
		ChatName:    chatName,
	}); err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
		return
	}
	b.lastInboundAt.Store(time.Now().Unix())
	slog.Debug("inbound", "chat_jid", jid, "sender_jid", "discord:user/"+m.Author.ID, "message_id", m.ID, "content_len", len(content))
}

// onReactionAdd emits verb=like/dislike for each reaction; bot's own reactions
// skipped. Reactions targeting one of the bot's own messages are promoted to
// verb=mention (direct engagement) regardless of the emoji.
func (b *bot) onReactionAdd(_ *discordgo.Session, m *discordgo.MessageReactionAdd) {
	if m == nil || m.MessageReaction == nil {
		return
	}
	if b.session.State != nil && b.session.State.User != nil && m.UserID == b.session.State.User.ID {
		return
	}
	emoji := m.Emoji.Name
	if emoji == "" {
		return
	}
	jid := chatJID(m.GuildID, m.ChannelID)
	// Reply-to-bot promotion to verb=mention happens in gateway/api
	// (spec 5/L) — uniform across all adapters. Ship the raw emoji
	// classification; gateway upgrades when ReplyTo points at a bot row.
	verb := chanlib.ClassifyEmoji(emoji)
	senderName := ""
	if m.Member != nil && m.Member.User != nil {
		senderName = m.Member.User.Username
	}
	// Discord's MessageReaction event carries no thread/parent ID — the ChannelID
	// is already the thread channel when the message lives in a thread, so Topic
	// cannot be derived here. api.go Fix 1 (MessageByID lookup) handles inheritance.
	if err := b.rc.SendMessage(chanlib.InboundMsg{
		ID:         m.MessageID + ":r:" + emoji,
		ChatJID:    jid,
		Sender:     "discord:user/" + m.UserID,
		SenderName: senderName,
		Content:    emoji,
		Timestamp:  time.Now().Unix(),
		Verb:       verb,
		ReplyTo:    m.MessageID,
		Reaction:   emoji,
		IsGroup:    m.GuildID != "",
		ChatName:   b.channelName(m.ChannelID),
	}); err != nil {
		slog.Error("deliver reaction failed", "jid", jid, "err", err)
		return
	}
	b.lastInboundAt.Store(time.Now().Unix())
}

// chatJID renders the canonical Discord chat JID. guildID is empty for
// DMs (→ discord:dm/<channel>) and non-empty for guild channels and
// threads (→ discord:<guild>/<channel>).
func chatJID(guildID, channelID string) string {
	if guildID == "" {
		return "discord:dm/" + channelID
	}
	return "discord:" + guildID + "/" + channelID
}

// chanID extracts the platform-side channel ID from a JID. Accepts both
// the legacy `discord:<channel>` and the new
// `discord:<guild>/<channel>` / `discord:dm/<channel>` /
// `discord:_/<channel>` (legacy migrated rows without a known guild).
func chanID(jid string) string {
	rest := strings.TrimPrefix(jid, "discord:")
	if _, ch, ok := strings.Cut(rest, "/"); ok {
		return ch
	}
	return rest
}

// chatAndTopic places a message: a thread is a topic under its parent channel's
// jid, so routes, engagement and history see the whole conversation. ch is nil
// when the channel could not be resolved.
func (b *bot) chatAndTopic(guildID, channelID string) (jid, topic string, ch *discordgo.Channel) {
	jid = chatJID(guildID, channelID)
	ch, err := b.session.State.Channel(channelID)
	if err != nil {
		ch, err = b.session.Channel(channelID)
	}
	if err != nil {
		return jid, "", nil
	}
	if ch.Type == discordgo.ChannelTypeGuildPublicThread || ch.Type == discordgo.ChannelTypeGuildPrivateThread {
		topic = channelID
		if ch.ParentID != "" {
			jid = chatJID(guildID, ch.ParentID)
		}
	}
	return jid, topic, ch
}

// isGroupChannel: anything other than a 1:1 DM is multi-user (guild text,
// threads, group DMs). An unresolved channel counts as multi-user, so
// user-scope secrets stay closed by default.
func isGroupChannel(ch *discordgo.Channel) bool {
	return !(ch != nil && ch.Type == discordgo.ChannelTypeDM)
}

// buttons draws the options as one actions row, or nil when there are none.
// Discord allows five buttons in a row; the rest are dropped rather than
// failing the send.
func buttons(options []chanlib.Option) []discordgo.MessageComponent {
	if len(options) == 0 {
		return nil
	}
	row := discordgo.ActionsRow{}
	for _, o := range options[:min(len(options), 5)] {
		row.Components = append(row.Components, discordgo.Button{
			Label:    o.Label,
			Style:    discordgo.SecondaryButton,
			CustomID: o.Send(),
		})
	}
	return []discordgo.MessageComponent{row}
}

// onPress delivers a button press as the inbound message its CustomID would be
// if typed (chanlib.Option). Discord drops an interaction unacknowledged for
// three seconds, so the ack goes first and its failure does not stop the press.
func (b *bot) onPress(_ *discordgo.Session, i *discordgo.InteractionCreate) {
	if i == nil || i.Type != discordgo.InteractionMessageComponent {
		return
	}
	if err := b.session.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	}); err != nil {
		slog.Warn("discord interaction ack failed", "err", err)
	}
	user := i.User
	if i.Member != nil && i.Member.User != nil {
		user = i.Member.User
	}
	data := i.MessageComponentData().CustomID
	if user == nil || data == "" {
		return
	}
	jid, topic, ch := b.chatAndTopic(i.GuildID, i.ChannelID)
	promptID := ""
	if i.Message != nil {
		promptID = i.Message.ID
	}
	if err := b.rc.SendMessage(chanlib.InboundMsg{
		ID:         promptID + ":cb:" + i.ID,
		ChatJID:    jid,
		Sender:     "discord:user/" + user.ID,
		SenderName: user.Username,
		Content:    data,
		Timestamp:  time.Now().Unix(),
		Topic:      topic,
		ReplyTo:    promptID,
		IsGroup:    isGroupChannel(ch),
		ChatName:   b.channelName(i.ChannelID),
	}); err != nil {
		slog.Error("deliver press failed", "jid", jid, "err", err)
		return
	}
	b.lastInboundAt.Store(time.Now().Unix())
}

// neverFailOnReference is fail_if_not_exists=false in the pointer form the
// wire struct takes.
var neverFailOnReference = false

func (b *bot) Send(req chanlib.SendRequest) (string, error) {
	chID := chanID(req.ChatJID)
	replyTo := req.ReplyTo
	switch {
	case req.ThreadID != "":
		chID = req.ThreadID
	case req.ThreadRoot != "" && !strings.HasPrefix(req.ChatJID, "discord:dm/"):
		// Reply into a thread rooted on the trigger message: start one (or
		// reuse the existing one) and post there. The root lives in the
		// parent channel, so a reply-reference to it can't follow into the
		// thread — drop it (the thread itself already anchors the root).
		if thID, err := b.threadFrom(chID, req.ThreadRoot, req.Content); err == nil {
			chID = thID
			if replyTo == req.ThreadRoot {
				replyTo = ""
			}
		} else {
			slog.Warn("discord thread start failed; sending inline",
				"chat_jid", req.ChatJID, "root", req.ThreadRoot, "err", err)
		}
	}
	var firstID string
	chunks := chanlib.Chunk(req.Content, 2000)
	for i, c := range chunks {
		var msg *discordgo.Message
		var err error
		send := &discordgo.MessageSend{Content: c}
		// The buttons ride the LAST chunk so they sit under the whole prompt.
		if i == len(chunks)-1 {
			send.Components = buttons(req.Options)
		}
		if replyTo != "" && i == 0 {
			// Discord resolves a reference inside the destination channel only,
			// so a reply to a parent-channel message from inside its thread is
			// "Unknown message" (400, code 50035). That refused every such reply
			// on sloth, and the sweep's retry landed the text in the parent
			// channel. fail_if_not_exists=false posts a plain message in the
			// thread instead: the place is what the reader needs, the quote
			// banner is decoration.
			send.Reference = &discordgo.MessageReference{MessageID: replyTo, FailIfNotExists: &neverFailOnReference}
		}
		for attempt := range 3 {
			msg, err = b.session.ChannelMessageSendComplex(chID, send)
			if err == nil {
				break
			}
			if !isRateLimit(err) {
				break
			}
			slog.Warn("discord rate limited, retrying", "attempt", attempt+1, "max_attempts", 3, "err", err)
			time.Sleep(time.Second)
		}
		if err != nil {
			return "", classifySendErr(err, firstID)
		}
		if msg != nil {
			if firstID == "" {
				firstID = msg.ID
			}
		}
	}
	slog.Debug("send", "chat_jid", req.ChatJID, "message_id", firstID, "source", "discord")
	return firstID, nil
}

// threadFrom starts a public thread on rootID in channel chID and returns the
// thread channel id. A root that already has a thread reuses it — Discord
// assigns a message-rooted thread the SAME id as its root message.
func (b *bot) threadFrom(chID, rootID, content string) (string, error) {
	th, err := b.session.MessageThreadStartComplex(chID, rootID, &discordgo.ThreadStart{
		Name:                threadName(content),
		AutoArchiveDuration: 1440,
	})
	if err != nil {
		var rest *discordgo.RESTError
		if errors.As(err, &rest) && rest.Message != nil &&
			rest.Message.Code == discordgo.ErrCodeThreadAlreadyCreatedForThisMessage {
			return rootID, nil
		}
		return "", err
	}
	return th.ID, nil
}

// threadName derives a new thread's title from the reply's first line,
// clamped to Discord's 100-char limit.
func threadName(content string) string {
	name, _, _ := strings.Cut(strings.TrimSpace(content), "\n")
	if name == "" {
		return "thread"
	}
	if r := []rune(name); len(r) > 100 {
		return string(r[:100])
	}
	return name
}

// SendVoice attaches the synthesized audio as a Discord file. Discord
// has no native push-to-talk in chat, but inline audio attachments
// (.ogg/.opus) play with a built-in player, which is the closest
// equivalent. The agent picks send_voice when the persona is voice-first
// or the inbound was voice; we deliver a clickable audio bubble.
func (b *bot) SendVoice(jid, audioPath, caption, threadID string) (string, error) {
	chID := chanID(jid)
	if threadID != "" {
		chID = threadID
	}
	f, err := os.Open(audioPath)
	if err != nil {
		return "", fmt.Errorf("discord open voice: %w", err)
	}
	defer f.Close()
	name := filepath.Base(audioPath)
	msg, err := b.session.ChannelMessageSendComplex(chID, &discordgo.MessageSend{
		Content: caption,
		Files:   []*discordgo.File{{Name: name, ContentType: "audio/ogg", Reader: f}},
	})
	if err != nil {
		return "", fmt.Errorf("discord sendvoice: %w", err)
	}
	if msg != nil {
		return msg.ID, nil
	}
	return "", nil
}

func (b *bot) SendFile(jid, path, name, caption, _, threadID string) (string, error) {
	chID := chanID(jid)
	if threadID != "" {
		chID = threadID
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("discord open file: %w", err)
	}
	defer f.Close()
	if name == "" {
		name = filepath.Base(path)
	}
	// Discord renders images, videos, and audio inline based on
	// ContentType. Setting it explicitly here so the rich-media bubble
	// fires even when the upstream filename has an ambiguous/missing
	// extension (.bin, no ext, etc).
	msg, err := b.session.ChannelMessageSendComplex(chID, &discordgo.MessageSend{
		Content: caption,
		Files: []*discordgo.File{{
			Name:        name,
			ContentType: discordMimeFor(name),
			Reader:      f,
		}},
	})
	if err != nil {
		return "", fmt.Errorf("discord sendfile: %w", err)
	}
	if msg != nil {
		return msg.ID, nil
	}
	return "", nil
}

// discordMimeFor maps known extensions to MIME types so Discord's UI
// renders rich previews (image bubble, video player, audio player)
// instead of a blank attachment chip. Unknown → empty string ⇒ Discord
// picks application/octet-stream and the user sees a generic file.
func discordMimeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	case ".mp3":
		return "audio/mpeg"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".m4a":
		return "audio/mp4"
	case ".flac":
		return "audio/flac"
	case ".pdf":
		return "application/pdf"
	}
	return ""
}

func (b *bot) Typing(jid string, on bool) { b.typing.Set(jid, on) }

func (b *bot) Post(chanlib.PostRequest) (string, error) {
	return "", chanlib.Unsupported("post", "discord",
		"Discord has no broadcast-feed primitive. Use `send(jid=discord:<channel_id>, content=...)` — channels are the post surface.")
}

func (b *bot) Like(req chanlib.LikeRequest) error {
	emoji := req.Reaction
	if emoji == "" {
		emoji = "👍"
	}
	if err := b.session.MessageReactionAdd(chanID(req.ChatJID), req.TargetID, emoji); err != nil {
		return fmt.Errorf("discord like: %w", err)
	}
	return nil
}

func (b *bot) Delete(req chanlib.DeleteRequest) error {
	if err := b.session.ChannelMessageDelete(chanID(req.ChatJID), req.TargetID); err != nil {
		return fmt.Errorf("discord delete: %w", err)
	}
	return nil
}

func (b *bot) Forward(chanlib.ForwardRequest) (string, error) {
	return "", chanlib.Unsupported("forward", "discord",
		"Discord has no native forward. Use `send(jid=<target>, content=\"<quoted text>\\n\\n— from <source>\")` to relay manually.")
}

// Quote: same-channel reply with own commentary referencing the source.
func (b *bot) Quote(req chanlib.QuoteRequest) (string, error) {
	msg, err := b.session.ChannelMessageSendReply(
		chanID(req.ChatJID), req.Comment,
		&discordgo.MessageReference{MessageID: req.SourceMsgID},
	)
	if err != nil {
		return "", fmt.Errorf("discord quote: %w", err)
	}
	if msg == nil {
		return "", nil
	}
	return msg.ID, nil
}

func (b *bot) Repost(chanlib.RepostRequest) (string, error) {
	return "", chanlib.Unsupported("repost", "discord",
		"Discord has no repost. Use `send` to manually re-share content with attribution.")
}

func (b *bot) Dislike(chanlib.DislikeRequest) error {
	return chanlib.Unsupported("dislike", "discord",
		"Discord uses emoji reactions, not a downvote primitive. Use `like(target_id=..., emoji=\"👎\")` (or any negative emoji like 💩, 😡) to express disagreement.")
}

func (b *bot) Edit(req chanlib.EditRequest) error {
	if _, err := b.session.ChannelMessageEdit(chanID(req.ChatJID), req.TargetID, req.Content); err != nil {
		return fmt.Errorf("discord edit: %w", err)
	}
	return nil
}

func (b *bot) Pin(req chanlib.PinRequest) error {
	if err := b.session.ChannelMessagePin(chanID(req.ChatJID), req.TargetID); err != nil {
		return fmt.Errorf("discord pin: %w", err)
	}
	return nil
}

func (b *bot) Unpin(req chanlib.UnpinRequest) error {
	if req.All {
		return chanlib.Unsupported("unpin_all", "discord",
			"Discord has no bulk-unpin primitive. Call `unpin_message(target_id=...)` per pinned message id (fetch via channel.messages.pins on the Discord side).")
	}
	if err := b.session.ChannelMessageUnpin(chanID(req.ChatJID), req.TargetID); err != nil {
		return fmt.Errorf("discord unpin: %w", err)
	}
	return nil
}

// discordEpochMs is the Discord snowflake epoch (2015-01-01T00:00:00Z).
const discordEpochMs = 1420070400000

func (b *bot) FetchHistory(req chanlib.HistoryRequest) (chanlib.HistoryResponse, error) {
	chID := chanID(req.ChatJID)
	if chID == "" {
		return chanlib.HistoryResponse{}, fmt.Errorf("invalid chat_jid")
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var beforeID string
	if !req.Before.IsZero() {
		ms := max(req.Before.UnixMilli()-discordEpochMs, 0)
		beforeID = fmt.Sprintf("%d", ms<<22)
	}
	msgs, err := b.session.ChannelMessages(chID, limit, beforeID, "", "")
	if err != nil {
		return chanlib.HistoryResponse{}, fmt.Errorf("discord history: %w", err)
	}
	out := make([]chanlib.InboundMsg, 0, len(msgs))
	for _, m := range msgs {
		if m == nil || m.Author == nil {
			continue
		}
		content, atts := buildAttachments(context.Background(), m.Attachments, m.Content,
			b.cfg.ListenURL, b.files)
		if content == "" && len(atts) == 0 {
			continue
		}
		content = replaceMentions(content, b.cfg.AssistantName, b.session.State.User, m.Mentions)
		var replyTo string
		if m.MessageReference != nil {
			replyTo = m.MessageReference.MessageID
		}
		out = append(out, chanlib.InboundMsg{
			ID:          m.ID,
			ChatJID:     req.ChatJID,
			Sender:      "discord:user/" + m.Author.ID,
			SenderName:  m.Author.Username,
			Content:     content,
			Timestamp:   m.Timestamp.Unix(),
			ReplyTo:     replyTo,
			Attachments: atts,
			ChatName:    b.channelName(chID),
		})
	}
	return chanlib.HistoryResponse{Source: "platform", Messages: out}, nil
}

func (b *bot) sendTyping(jid string) bool {
	ch := chanID(jid)
	if th, ok := b.activeThread.Load(jid); ok {
		ch = th.(string) // type in the live thread, not the channel root
	}
	if err := b.session.ChannelTyping(ch); err != nil {
		slog.Warn("discord typing failed", "jid", jid, "err", err)
		return false
	}
	return true
}

// classifySendErr maps a failed chunk send onto chanlib's dedup taxonomy. A 429 that
// outlived the local retries proves Discord created nothing — but the dedup claim may
// be released only while NO chunk has landed; after a delivered chunk a re-post would
// duplicate it, so the claim stays held and routd dead-letters visibly. Everything
// else stays unclassified: a lost response may have sent the message.
func classifySendErr(err error, firstID string) error {
	if isRateLimit(err) && firstID == "" {
		return fmt.Errorf("discord send: %v: %w", err, chanlib.ErrNotDelivered)
	}
	if isClientRefusal(err) {
		// Discord refused the request as formed (a bad reference, a missing
		// channel, no permission); a retry cannot repair it. ErrInvalidRequest
		// maps to a 400 that carries Discord's own reason, so routd dead-letters
		// the row and the agent reads why, instead of the bare "status 502" that
		// the sweep retried ten times.
		return fmt.Errorf("%w: discord send: %v", chanlib.ErrInvalidRequest, err)
	}
	return fmt.Errorf("discord send: %w", err)
}

// isClientRefusal reports a Discord 4xx other than a rate limit.
func isClientRefusal(err error) bool {
	var rest *discordgo.RESTError
	if !errors.As(err, &rest) || rest.Response == nil {
		return false
	}
	code := rest.Response.StatusCode
	return code >= 400 && code < 500 && code != http.StatusTooManyRequests
}

// isRateLimit detects Discord 429 errors via typed discordgo errors with substring fallback.
func isRateLimit(err error) bool {
	if err == nil {
		return false
	}
	var rest *discordgo.RESTError
	if errors.As(err, &rest) {
		if rest.Response != nil && rest.Response.StatusCode == http.StatusTooManyRequests {
			return true
		}
	}
	var rl *discordgo.RateLimitError
	if errors.As(err, &rl) {
		return true
	}
	es := strings.ToLower(err.Error())
	return strings.Contains(es, "429") || strings.Contains(es, "rate limit")
}

// isMentioned returns true when the bot is in the Discord-populated
// Mentions list — i.e. the user typed `@<bot>` in body. Reply-to-bot
// promotion lives in gateway/api (spec 5/L), driven by ReplyTo + the
// stored is_bot_message column; no per-adapter ring buffer needed.
func (b *bot) isMentioned(m *discordgo.MessageCreate) bool {
	botID := b.session.State.User.ID
	for _, u := range m.Mentions {
		if u != nil && u.ID == botID {
			return true
		}
	}
	return false
}

// replaceMentions rewrites raw mention tokens to readable names: every
// mentioned user becomes @Username (resolved from the message's Mentions list,
// which Discord populates for every in-content mention). Unresolved tokens stay
// verbatim.
//
// The bot's OWN mention is DROPPED, not rewritten. Rendering it as
// @<assistant name> put an `@name` at the head of every mention-triggered
// message, and routd's prefix layer reads a leading `@name` as a delegation to
// the child group of that name: one sloth message produced 7 `@prefix: child
// group not found` warnings, and a child group actually named after the
// assistant would turn every plain mention into a silent delegation (BUGS
// L158). The `verb=mention` trigger already carries "the bot was addressed";
// the text does not have to.
//
// A message that is nothing BUT the mention falls back to the bare assistant
// name — routd rejects empty content with 400, so a bare ping must keep some
// text. With no assistant name configured the raw token stays, unchanged.
func replaceMentions(content, assistantName string, self *discordgo.User, mentions []*discordgo.User) string {
	if self != nil {
		if s := dropMention(content, self.ID); strings.TrimSpace(s) != "" {
			content = s
		} else if assistantName != "" {
			content = assistantName
		}
	}
	for _, u := range mentions {
		if u == nil || u.Username == "" {
			continue
		}
		at := "@" + u.Username
		content = strings.ReplaceAll(content, "<@"+u.ID+">", at)
		content = strings.ReplaceAll(content, "<@!"+u.ID+">", at)
	}
	return content
}

// dropMention removes one user's mention tokens together with a single adjacent
// space, so the removal leaves neither a doubled space nor a leading one. It
// touches only the token, never the surrounding formatting.
func dropMention(content, id string) string {
	for _, tok := range []string{"<@" + id + ">", "<@!" + id + ">"} {
		content = strings.ReplaceAll(content, tok+" ", "")
		content = strings.ReplaceAll(content, " "+tok, "")
		content = strings.ReplaceAll(content, tok, "")
	}
	return content
}
