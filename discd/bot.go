package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/onvos/arizuko/chanlib"
)

type bot struct {
	session *discordgo.Session
	cfg     config
	rc      *chanlib.RouterClient
	typing  *chanlib.TypingRefresher
	files   *chanlib.URLCache
}

func newBot(cfg config) (*bot, error) {
	s, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		return nil, fmt.Errorf("discord session: %w", err)
	}
	s.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages |
		discordgo.IntentMessageContent
	b := &bot{session: s, cfg: cfg}
	b.typing = chanlib.NewTypingRefresher(8*time.Second, 10*time.Minute, b.sendTyping, nil)
	return b, nil
}

func (b *bot) start(rc *chanlib.RouterClient) error {
	b.rc = rc
	b.session.AddHandler(b.onMessage)
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

func (b *bot) onMessage(_ *discordgo.Session, m *discordgo.MessageCreate) {
	if m == nil || m.Author == nil || m.Author.Bot {
		return
	}
	jid := "discord:" + m.ChannelID
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

	if err := b.rc.SendMessage(chanlib.InboundMsg{
		ID:          m.ID,
		ChatJID:     jid,
		Sender:      "discord:" + m.Author.ID,
		SenderName:  m.Author.Username,
		Content:     content,
		Timestamp:   m.Timestamp.Unix(),
		Topic:       topic,
		Attachments: atts,
	}); err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
		return
	}
	slog.Debug("inbound", "chat_jid", jid, "sender_jid", "discord:"+m.Author.ID, "message_id", m.ID, "content_len", len(content))
}

func (b *bot) Send(req chanlib.SendRequest) (string, error) {
	chID := strings.TrimPrefix(req.ChatJID, "discord:")
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

func (b *bot) SendFile(jid, path, name, caption string) error {
	chID := strings.TrimPrefix(jid, "discord:")
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("discord open file: %w", err)
	}
	defer f.Close()
	if name == "" {
		name = filepath.Base(path)
	}
	_, err = b.session.ChannelMessageSendComplex(chID, &discordgo.MessageSend{
		Content: caption,
		Files:   []*discordgo.File{{Name: name, Reader: f}},
	})
	if err != nil {
		return fmt.Errorf("discord sendfile: %w", err)
	}
	return nil
}

func (b *bot) Typing(jid string, on bool) { b.typing.Set(jid, on) }

// discordEpochMs is the Discord snowflake epoch (2015-01-01T00:00:00Z).
const discordEpochMs = 1420070400000

// FetchHistory pulls recent messages from Discord for the given channel.
// Limit is clamped to [1, 100] per Discord API; Before is translated to a
// snowflake ID so the API returns messages strictly older than that time.
func (b *bot) FetchHistory(req chanlib.HistoryRequest) (chanlib.HistoryResponse, error) {
	chID := strings.TrimPrefix(req.ChatJID, "discord:")
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
			Sender:      "discord:" + m.Author.ID,
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
	chID := strings.TrimPrefix(jid, "discord:")
	if err := b.session.ChannelTyping(chID); err != nil {
		slog.Warn("discord typing failed", "jid", jid, "err", err)
	}
}

// isRateLimit returns true if err is a Discord 429 / rate-limit error.
// Prefers the typed discordgo errors; falls back to substring match.
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
