package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type bot struct {
	api    *tgbotapi.BotAPI
	cfg    config
	cancel context.CancelFunc
}

func newBot(cfg config) (*bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return nil, fmt.Errorf("telegram auth: %w", err)
	}
	slog.Info("telegram connected", "username", api.Self.UserName)
	return &bot{api: api, cfg: cfg}, nil
}

func (b *bot) poll(ctx context.Context, rc *routerClient) {
	uc := tgbotapi.NewUpdate(0)
	uc.Timeout = 30
	ch := b.api.GetUpdatesChan(uc)
	ctx, b.cancel = context.WithCancel(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-ch:
			if !ok {
				return
			}
			if u.Message != nil {
				b.handle(u.Message, rc)
			}
		}
	}
}

func (b *bot) stop() {
	if b.cancel != nil {
		b.cancel()
	}
	b.api.StopReceivingUpdates()
}

func (b *bot) handle(msg *tgbotapi.Message, rc *routerClient) {
	if msg.From != nil && msg.From.IsBot {
		return
	}
	jid := "telegram:" + strconv.FormatInt(msg.Chat.ID, 10)
	isGroup := msg.Chat.IsGroup() || msg.Chat.IsSuperGroup()

	name := msg.Chat.Title
	if name == "" {
		name = userName(msg.From)
	}
	rc.sendChat(jid, name, isGroup)

	content := mediaText(msg)
	if content == "" {
		content = msg.Text
		if content == "" {
			content = msg.Caption
		}
	}
	if content == "" {
		return
	}

	if b.cfg.AssistantName != "" && b.api.Self.UserName != "" && msg.Entities != nil {
		re := regexp.MustCompile(fmt.Sprintf(`(?i)^@%s\b`, regexp.QuoteMeta(b.cfg.AssistantName)))
		mention := "@" + b.api.Self.UserName
		for _, e := range msg.Entities {
			if e.Type == "mention" && strings.EqualFold(entity(msg.Text, e), mention) && !re.MatchString(content) {
				content = "@" + b.cfg.AssistantName + " " + content
				break
			}
		}
	}

	err := rc.sendMessage(inboundMsg{
		ID:         strconv.Itoa(msg.MessageID),
		ChatJID:    jid,
		Sender:     "telegram:" + userID(msg.From),
		SenderName: userName(msg.From),
		Content:    content,
		Timestamp:  int64(msg.Date),
		IsGroup:    isGroup,
	})
	if err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
	}
}

func (b *bot) send(jid, text, replyTo string) error {
	id, err := parseChatID(jid)
	if err != nil {
		return err
	}
	replyMsgID := 0
	if replyTo != "" {
		if n, err := strconv.ParseInt(replyTo, 10, 64); err == nil {
			replyMsgID = int(n)
		}
	}
	html := mdToHTML(text)
	for _, c := range chunk(html, 4096) {
		m := tgbotapi.NewMessage(id, c)
		m.ParseMode = "HTML"
		if replyMsgID != 0 {
			m.ReplyToMessageID = replyMsgID
		}
		if _, err := b.api.Send(m); err != nil {
			if strings.Contains(err.Error(), "400") {
				for _, p := range chunk(text, 4096) {
					pm := tgbotapi.NewMessage(id, p)
					if _, e2 := b.api.Send(pm); e2 != nil {
						return fmt.Errorf("telegram send: %w", e2)
					}
				}
				return nil
			}
			return fmt.Errorf("telegram send: %w", err)
		}
	}
	return nil
}

func (b *bot) sendFile(jid, path, name string) error {
	id, err := parseChatID(jid)
	if err != nil {
		return err
	}
	f := tgbotapi.FilePath(path)
	ext := strings.ToLower(filepath.Ext(path))
	var m tgbotapi.Chattable
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp":
		p := tgbotapi.NewPhoto(id, f); p.Caption = name; m = p
	case ".mp4", ".mov":
		v := tgbotapi.NewVideo(id, f); v.Caption = name; m = v
	case ".gif":
		a := tgbotapi.NewAnimation(id, f); a.Caption = name; m = a
	case ".mp3", ".ogg":
		a := tgbotapi.NewAudio(id, f); a.Caption = name; m = a
	default:
		d := tgbotapi.NewDocument(id, f); d.Caption = name; m = d
	}
	if _, err := b.api.Send(m); err != nil {
		return fmt.Errorf("telegram sendfile: %w", err)
	}
	return nil
}

func (b *bot) typing(jid string, on bool) error {
	if !on {
		return nil
	}
	id, err := parseChatID(jid)
	if err != nil {
		return err
	}
	b.api.Send(tgbotapi.NewChatAction(id, tgbotapi.ChatTyping))
	return nil
}

func parseChatID(jid string) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(jid, "telegram:"), 10, 64)
}

func userName(u *tgbotapi.User) string {
	if u == nil {
		return "unknown"
	}
	n := u.FirstName
	if u.LastName != "" {
		n += " " + u.LastName
	}
	if n == "" {
		return strconv.FormatInt(u.ID, 10)
	}
	return n
}

func userID(u *tgbotapi.User) string {
	if u == nil {
		return ""
	}
	return strconv.FormatInt(u.ID, 10)
}

func entity(text string, e tgbotapi.MessageEntity) string {
	r := []rune(text)
	end := e.Offset + e.Length
	if end > len(r) {
		end = len(r)
	}
	return string(r[e.Offset:end])
}

func mediaText(msg *tgbotapi.Message) string {
	cap := ""
	if msg.Caption != "" {
		cap = " " + msg.Caption
	}
	switch {
	case msg.Photo != nil:
		return "[Photo]" + cap
	case msg.Video != nil:
		return "[Video]" + cap
	case msg.Voice != nil:
		return "[Voice message]" + cap
	case msg.Audio != nil:
		return "[Audio]" + cap
	case msg.Document != nil:
		n := "file"
		if msg.Document.FileName != "" {
			n = msg.Document.FileName
		}
		return fmt.Sprintf("[Document: %s]%s", n, cap)
	case msg.Sticker != nil:
		return fmt.Sprintf("[Sticker %s]", msg.Sticker.Emoji)
	case msg.Location != nil:
		return "[Location]"
	case msg.Contact != nil:
		return "[Contact]"
	}
	return ""
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

var (
	reCodeBlock  = regexp.MustCompile("(?s)```(?:\\w*)\n?(.*?)```")
	reInlineCode = regexp.MustCompile("`([^`]+)`")
	reBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic     = regexp.MustCompile(`(^|[^*])\*([^*]+)\*([^*]|$)`)
	reHeader     = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
)

func mdToHTML(s string) string {
	s = reCodeBlock.ReplaceAllString(s, "<pre>$1</pre>")
	s = reInlineCode.ReplaceAllString(s, "<code>$1</code>")
	s = reBold.ReplaceAllString(s, "<b>$1</b>")
	s = reItalic.ReplaceAllString(s, "${1}<i>${2}</i>${3}")
	s = reHeader.ReplaceAllString(s, "<b>$1</b>")
	return s
}
