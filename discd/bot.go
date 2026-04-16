package main

import (
	"fmt"
	"log/slog"
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
	files   *fileCache
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
	if m.Author.Bot {
		return
	}
	jid := "discord:" + m.ChannelID
	content := m.Content
	var atts []chanlib.InboundAttachment
	for _, att := range m.Attachments {
		content += fmt.Sprintf(" [Attachment: %s]", att.Filename)
		atts = append(atts, chanlib.InboundAttachment{
			Mime:     att.ContentType,
			Filename: att.Filename,
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
	}
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
			es := err.Error()
			if !strings.Contains(es, "429") && !strings.Contains(es, "rate limit") {
				break
			}
			slog.Warn("discord rate limited, retrying", "attempt", attempt+1)
			time.Sleep(time.Second)
		}
		if err != nil {
			return "", fmt.Errorf("discord send: %w", err)
		}
		if firstID == "" && msg != nil {
			firstID = msg.ID
		}
	}
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

func (b *bot) sendTyping(jid string) {
	chID := strings.TrimPrefix(jid, "discord:")
	if err := b.session.ChannelTyping(chID); err != nil {
		slog.Warn("discord typing failed", "jid", jid, "err", err)
	}
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
