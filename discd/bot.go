package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/onvos/arizuko/chanlib"
)

type bot struct {
	chanlib.NoSocial
	session       *discordgo.Session
	cfg           config
	rc            *chanlib.RouterClient
	typing        *chanlib.TypingRefresher
	files         *chanlib.URLCache
	lastInboundAt atomic.Int64
}

func (b *bot) LastInboundAt() int64 { return b.lastInboundAt.Load() }

var _ chanlib.BotHandler = (*bot)(nil)

func newBot(cfg config) (*bot, error) {
	s, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		return nil, fmt.Errorf("discord session: %w", err)
	}
	s.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages |
		discordgo.IntentMessageContent |
		discordgo.IntentsGuildMessageReactions |
		discordgo.IntentsDirectMessageReactions
	b := &bot{session: s, cfg: cfg}
	b.lastInboundAt.Store(time.Now().Unix())
	b.typing = chanlib.NewTypingRefresher(8*time.Second, 10*time.Minute, b.sendTyping, nil)
	return b, nil
}

func (b *bot) start(rc *chanlib.RouterClient) error {
	b.rc = rc
	b.session.AddHandler(b.onMessage)
	b.session.AddHandler(b.onReactionAdd)
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("discord open: %w", err)
	}
	slog.Info("discord connected", "user", b.session.State.User.Username)
	return nil
}

func (b *bot) stop() {
	b.typing.Stop()
	b.session.Close()
}

func (b *bot) isConnected() bool {
	return b.session != nil && b.session.DataReady
}

func (b *bot) onMessage(_ *discordgo.Session, m *discordgo.MessageCreate) {
	if m == nil || m.Author == nil || m.Author.Bot {
		return
	}
	jid := chatJID(m.GuildID, m.ChannelID)
	content := m.Content
	var atts []chanlib.InboundAttachment
	for _, att := range m.Attachments {
		if att == nil || att.URL == "" {
			continue
		}
		name := att.Filename
		if name == "" {
			name = "attachment"
		}
		content += fmt.Sprintf(" [Attachment: %s]", name)
		atts = append(atts, chanlib.InboundAttachment{
			Mime:     att.ContentType,
			Filename: name,
			URL:      fmt.Sprintf("%s/files/%s", b.cfg.ListenURL, b.files.Put(att.URL)),
			Size:     int64(att.Size),
		})
	}
	if content == "" {
		return
	}
	content = replaceMentions(content, b.cfg.AssistantName, b.session.State.User)

	topic := ""
	ch, err := b.session.State.Channel(m.ChannelID)
	if err != nil {
		ch, err = b.session.Channel(m.ChannelID)
	}
	if err == nil && (ch.Type == discordgo.ChannelTypeGuildPublicThread || ch.Type == discordgo.ChannelTypeGuildPrivateThread) {
		topic = m.ChannelID
	}
	// Anything other than a 1:1 DM is multi-user (guild text, threads,
	// group DMs). On lookup error fall back to multi-user; user-scope
	// secrets stay closed by default.
	isGroup := !(err == nil && ch.Type == discordgo.ChannelTypeDM)

	if err := b.rc.SendMessage(chanlib.InboundMsg{
		ID:          m.ID,
		ChatJID:     jid,
		Sender:      "discord:user/" + m.Author.ID,
		SenderName:  m.Author.Username,
		Content:     content,
		Timestamp:   m.Timestamp.Unix(),
		Topic:       topic,
		Attachments: atts,
		IsGroup:     isGroup,
	}); err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
		return
	}
	b.lastInboundAt.Store(time.Now().Unix())
	slog.Debug("inbound", "chat_jid", jid, "sender_jid", "discord:user/"+m.Author.ID, "message_id", m.ID, "content_len", len(content))
}

// onReactionAdd emits verb=like/dislike for each reaction; bot's own reactions skipped.
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
	verb := chanlib.ClassifyEmoji(emoji)
	senderName := ""
	if m.Member != nil && m.Member.User != nil {
		senderName = m.Member.User.Username
	}
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

func (b *bot) Send(req chanlib.SendRequest) (string, error) {
	chID := chanID(req.ChatJID)
	if req.ThreadID != "" {
		chID = req.ThreadID
	}
	var firstID string
	for i, c := range chanlib.Chunk(req.Content, 2000) {
		var msg *discordgo.Message
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if req.ReplyTo != "" && i == 0 {
				msg, err = b.session.ChannelMessageSendReply(chID, c, &discordgo.MessageReference{MessageID: req.ReplyTo})
			} else {
				msg, err = b.session.ChannelMessageSend(chID, c)
			}
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
			return "", fmt.Errorf("discord send: %w", err)
		}
		if firstID == "" && msg != nil {
			firstID = msg.ID
		}
	}
	slog.Debug("send", "chat_jid", req.ChatJID, "message_id", firstID, "source", "discord")
	return firstID, nil
}

// SendVoice attaches the synthesized audio as a Discord file. Discord
// has no native push-to-talk in chat, but inline audio attachments
// (.ogg/.opus) play with a built-in player, which is the closest
// equivalent. The agent picks send_voice when the persona is voice-first
// or the inbound was voice; we deliver a clickable audio bubble.
func (b *bot) SendVoice(jid, audioPath, caption string) (string, error) {
	chID := chanID(jid)
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

func (b *bot) SendFile(jid, path, name, caption string) error {
	chID := chanID(jid)
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("discord open file: %w", err)
	}
	defer f.Close()
	if name == "" {
		name = filepath.Base(path)
	}
	// Discord renders images, videos, and audio inline based on
	// ContentType. Setting it explicitly here so the rich-media bubble
	// fires even when the upstream filename has an ambiguous/missing
	// extension (.bin, no ext, etc).
	_, err = b.session.ChannelMessageSendComplex(chID, &discordgo.MessageSend{
		Content: caption,
		Files: []*discordgo.File{{
			Name:        name,
			ContentType: discordMimeFor(name),
			Reader:      f,
		}},
	})
	if err != nil {
		return fmt.Errorf("discord sendfile: %w", err)
	}
	return nil
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
		ms := req.Before.UnixMilli() - discordEpochMs
		if ms < 0 {
			ms = 0
		}
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
		content := m.Content
		var atts []chanlib.InboundAttachment
		for _, att := range m.Attachments {
			if att == nil || att.URL == "" {
				continue
			}
			name := att.Filename
			if name == "" {
				name = "attachment"
			}
			content += fmt.Sprintf(" [Attachment: %s]", name)
			url := att.URL
			if b.files != nil && b.cfg.ListenURL != "" {
				url = fmt.Sprintf("%s/files/%s", b.cfg.ListenURL, b.files.Put(att.URL))
			}
			atts = append(atts, chanlib.InboundAttachment{
				Mime: att.ContentType, Filename: name, URL: url, Size: int64(att.Size),
			})
		}
		if content == "" && len(atts) == 0 {
			continue
		}
		content = replaceMentions(content, b.cfg.AssistantName, b.session.State.User)
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
		})
	}
	return chanlib.HistoryResponse{Source: "platform", Messages: out}, nil
}

func (b *bot) sendTyping(jid string) {
	if err := b.session.ChannelTyping(chanID(jid)); err != nil {
		slog.Warn("discord typing failed", "jid", jid, "err", err)
	}
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

func replaceMentions(content, assistantName string, user *discordgo.User) string {
	if assistantName == "" || user == nil {
		return content
	}
	at := "@" + assistantName
	content = strings.ReplaceAll(content, "<@"+user.ID+">", at)
	content = strings.ReplaceAll(content, "<@!"+user.ID+">", at)
	return content
}
