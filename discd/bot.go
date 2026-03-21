package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

type bot struct {
	session *discordgo.Session
	cfg     config
	rc      *routerClient
}

func newBot(cfg config) (*bot, error) {
	s, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		return nil, fmt.Errorf("discord session: %w", err)
	}
	s.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages |
		discordgo.IntentMessageContent
	return &bot{session: s, cfg: cfg}, nil
}

func (b *bot) start(rc *routerClient) error {
	b.rc = rc
	b.session.AddHandler(b.onMessage)
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("discord open: %w", err)
	}
	slog.Info("discord connected", "user", b.session.State.User.Username)
	return nil
}

func (b *bot) stop() {
	b.session.Close()
}

func (b *bot) onMessage(_ *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.Bot {
		return
	}

	jid := "discord:" + m.ChannelID
	ch, err := b.session.Channel(m.ChannelID)
	isGroup := err == nil && ch.Type == discordgo.ChannelTypeGuildText

	name := ""
	if isGroup && ch.Name != "" {
		name = ch.Name
	} else {
		name = m.Author.Username
	}
	b.rc.sendChat(jid, name, isGroup)

	content := m.Content
	for _, att := range m.Attachments {
		content += fmt.Sprintf(" [Attachment: %s]", att.Filename)
	}
	if content == "" {
		return
	}

	if b.cfg.AssistantName != "" && b.session.State.User != nil {
		mention := "<@" + b.session.State.User.ID + ">"
		mentionNick := "<@!" + b.session.State.User.ID + ">"
		if strings.Contains(content, mention) || strings.Contains(content, mentionNick) {
			content = strings.ReplaceAll(content, mention, "@"+b.cfg.AssistantName)
			content = strings.ReplaceAll(content, mentionNick, "@"+b.cfg.AssistantName)
		}
	}

	err = b.rc.sendMessage(inboundMsg{
		ID:         m.ID,
		ChatJID:    jid,
		Sender:     "discord:" + m.Author.ID,
		SenderName: m.Author.Username,
		Content:    content,
		Timestamp:  m.Timestamp.Unix(),
		IsGroup:    isGroup,
	})
	if err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
	}
}

func (b *bot) send(jid, text, replyTo string) (string, error) {
	chID := strings.TrimPrefix(jid, "discord:")
	chunks := chunk(text, 2000)
	var firstID string
	for i, c := range chunks {
		var (
			msg *discordgo.Message
			err error
		)
		for attempt := 0; attempt < 3; attempt++ {
			if replyTo != "" && i == 0 {
				ref := &discordgo.MessageReference{MessageID: replyTo}
				msg, err = b.session.ChannelMessageSendReply(chID, c, ref)
			} else {
				msg, err = b.session.ChannelMessageSend(chID, c)
			}
			if err == nil {
				break
			}
			errStr := err.Error()
			if strings.Contains(errStr, "429") || strings.Contains(errStr, "rate limit") {
				slog.Warn("discord rate limited, retrying", "attempt", attempt+1)
				time.Sleep(time.Second)
				continue
			}
			break
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

func (b *bot) sendFile(jid, path, name string) error {
	chID := strings.TrimPrefix(jid, "discord:")
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("discord open file: %w", err)
	}
	defer f.Close()
	if name == "" {
		name = filepath.Base(path)
	}
	_, err = b.session.ChannelFileSend(chID, name, f)
	if err != nil {
		return fmt.Errorf("discord sendfile: %w", err)
	}
	return nil
}

func (b *bot) typing(jid string, on bool) error {
	if !on {
		return nil
	}
	chID := strings.TrimPrefix(jid, "discord:")
	return b.session.ChannelTyping(chID)
}

func chunk(s string, max int) []string {
	if len(s) <= max {
		return []string{s}
	}
	var out []string
	for len(s) > 0 {
		end := max
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[:end])
		s = s[end:]
	}
	return out
}
