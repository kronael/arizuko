package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/matterbridge/telegram-bot-api/v6"

	"github.com/onvos/arizuko/chanlib"
)

type bot struct {
	api       *tgbotapi.BotAPI
	cfg       config
	cancel    context.CancelFunc
	mentionRe *regexp.Regexp
	typing    *chanlib.TypingRefresher
}

func newBot(cfg config) (*bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return nil, fmt.Errorf("telegram auth: %w", err)
	}
	slog.Info("telegram connected", "username", api.Self.UserName)
	b := &bot{api: api, cfg: cfg}
	b.typing = chanlib.NewTypingRefresher(4*time.Second, 10*time.Minute, b.sendTyping, nil)
	if cfg.AssistantName != "" {
		b.mentionRe = regexp.MustCompile(
			fmt.Sprintf(`(?i)^@%s\b`, regexp.QuoteMeta(cfg.AssistantName)))
	}
	return b, nil
}

func (b *bot) loadOffset() int {
	if b.cfg.StateFile == "" {
		return 0
	}
	data, err := os.ReadFile(b.cfg.StateFile)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return n
}

func (b *bot) saveOffset(offset int) {
	if b.cfg.StateFile == "" {
		return
	}
	os.WriteFile(b.cfg.StateFile, []byte(strconv.Itoa(offset)), 0o644)
}

func (b *bot) poll(ctx context.Context, rc *chanlib.RouterClient) {
	offset := b.loadOffset()
	uc := tgbotapi.NewUpdate(offset)
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
			b.saveOffset(u.UpdateID + 1)
		}
	}
}

func (b *bot) stop() {
	if b.cancel != nil {
		b.cancel()
	}
	b.typing.Stop()
	b.api.StopReceivingUpdates()
}

func (b *bot) handle(msg *tgbotapi.Message, rc *chanlib.RouterClient) {
	if msg.From != nil && msg.From.IsBot {
		return
	}
	jid := "telegram:" + strconv.FormatInt(msg.Chat.ID, 10)

	res := extractMedia(msg, b.cfg.ListenURL)
	content := res.content
	if content == "" {
		content = msg.Text
		if content == "" {
			content = msg.Caption
		}
	}
	if content == "" {
		return
	}

	if b.mentionRe != nil && b.api.Self.UserName != "" && msg.Entities != nil {
		mention := "@" + b.api.Self.UserName
		for _, e := range msg.Entities {
			if e.Type == "mention" && strings.EqualFold(entity(msg.Text, e), mention) && !b.mentionRe.MatchString(content) {
				content = "@" + b.cfg.AssistantName + " " + content
				break
			}
		}
	}

	topic := ""
	if msg.MessageThreadID != 0 {
		topic = strconv.Itoa(msg.MessageThreadID)
	}

	im := chanlib.InboundMsg{
		ID:          strconv.Itoa(msg.MessageID),
		ChatJID:     jid,
		Sender:      "telegram:" + userID(msg.From),
		SenderName:  userName(msg.From),
		Content:     content,
		Timestamp:   int64(msg.Date),
		Topic:       topic,
		Attachments: res.attachments,
	}
	if r := msg.ReplyToMessage; r != nil {
		im.ReplyTo = strconv.Itoa(r.MessageID)
		im.ReplyToSender = userName(r.From)
		im.ReplyToText = r.Text
		if im.ReplyToText == "" {
			im.ReplyToText = r.Caption
		}
	}
	err := rc.SendMessage(im)
	if err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
	}
}

func (b *bot) Send(req chanlib.SendRequest) (string, error) {
	id, err := parseChatID(req.ChatJID)
	if err != nil {
		return "", err
	}
	replyMsgID := 0
	if req.ReplyTo != "" {
		if n, err := strconv.ParseInt(req.ReplyTo, 10, 64); err == nil {
			replyMsgID = int(n)
		}
	}
	msgThreadID := 0
	if req.ThreadID != "" {
		if n, err := strconv.Atoi(req.ThreadID); err == nil {
			msgThreadID = n
		}
	}
	text := req.Content
	html := mdToHTML(text)
	var firstID string
	for _, c := range chanlib.Chunk(html, 4096) {
		m := tgbotapi.NewMessage(id, c)
		m.ParseMode = "HTML"
		if replyMsgID != 0 {
			m.ReplyToMessageID = replyMsgID
			replyMsgID = 0 // only first chunk replies
		}
		if msgThreadID != 0 {
			m.MessageThreadID = msgThreadID
		}
		sent, err := b.api.Send(m)
		if err != nil {
			var tgErr *tgbotapi.Error
			if errors.As(err, &tgErr) && tgErr.Code == 400 {
				for _, p := range chanlib.Chunk(text, 4096) {
					pm := tgbotapi.NewMessage(id, p)
					if msgThreadID != 0 {
						pm.MessageThreadID = msgThreadID
					}
					if s2, e2 := b.api.Send(pm); e2 != nil {
						return "", fmt.Errorf("telegram send: %w", e2)
					} else if firstID == "" {
						firstID = strconv.Itoa(s2.MessageID)
					}
				}
				return firstID, nil
			}
			return "", fmt.Errorf("telegram send: %w", err)
		}
		if firstID == "" {
			firstID = strconv.Itoa(sent.MessageID)
		}
	}
	return firstID, nil
}

func (b *bot) SendFile(jid, path, name, caption string) error {
	id, err := parseChatID(jid)
	if err != nil {
		return err
	}
	f := tgbotapi.FilePath(path)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(path))
	}
	var m tgbotapi.Chattable
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp":
		p := tgbotapi.NewPhoto(id, f)
		p.Caption = caption
		m = p
	case ".mp4", ".mov":
		v := tgbotapi.NewVideo(id, f)
		v.Caption = caption
		m = v
	case ".gif":
		a := tgbotapi.NewAnimation(id, f)
		a.Caption = caption
		m = a
	case ".mp3", ".ogg":
		a := tgbotapi.NewAudio(id, f)
		a.Caption = caption
		m = a
	default:
		d := tgbotapi.NewDocument(id, f)
		d.Caption = caption
		m = d
	}
	if _, err := b.api.Send(m); err != nil {
		return fmt.Errorf("telegram sendfile: %w", err)
	}
	return nil
}

func (b *bot) Typing(jid string, on bool) { b.typing.Set(jid, on) }

func (b *bot) sendTyping(jid string) {
	id, err := parseChatID(jid)
	if err != nil {
		return
	}
	if _, err := b.api.Request(tgbotapi.NewChatAction(id, tgbotapi.ChatTyping)); err != nil {
		slog.Warn("typing failed", "jid", jid, "err", err)
	}
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

type mediaResult struct {
	content     string
	attachments []chanlib.InboundAttachment
}

func extractMedia(msg *tgbotapi.Message, listenURL string) mediaResult {
	cap := ""
	if msg.Caption != "" {
		cap = " " + msg.Caption
	}
	att := func(fileID, mime, filename string, size int64) mediaResult {
		url := ""
		if listenURL != "" && fileID != "" {
			url = listenURL + "/files/" + fileID
		}
		return mediaResult{
			attachments: []chanlib.InboundAttachment{
				{Mime: mime, Filename: filename, URL: url, Size: size},
			},
		}
	}
	switch {
	case msg.Photo != nil:
		best := msg.Photo[len(msg.Photo)-1]
		r := att(best.FileID, "image/jpeg", best.FileID+".jpg", int64(best.FileSize))
		r.content = "[Photo]" + cap
		return r
	case msg.Video != nil:
		r := att(msg.Video.FileID, "video/mp4", msg.Video.FileID+".mp4", msg.Video.FileSize)
		r.content = "[Video]" + cap
		return r
	case msg.Voice != nil:
		r := att(msg.Voice.FileID, "audio/ogg", msg.Voice.FileID+".ogg", int64(msg.Voice.FileSize))
		r.content = "[Voice message]" + cap
		return r
	case msg.Audio != nil:
		fname := msg.Audio.FileName
		if fname == "" {
			fname = msg.Audio.FileID + ".mp3"
		}
		r := att(msg.Audio.FileID, "audio/mpeg", fname, msg.Audio.FileSize)
		r.content = "[Audio]" + cap
		return r
	case msg.Document != nil:
		n := msg.Document.FileName
		if n == "" {
			n = msg.Document.FileID
		}
		r := att(msg.Document.FileID, msg.Document.MimeType, n, msg.Document.FileSize)
		r.content = fmt.Sprintf("[Document: %s]%s", n, cap)
		return r
	case msg.Sticker != nil:
		return mediaResult{content: fmt.Sprintf("[Sticker %s]", msg.Sticker.Emoji)}
	case msg.Location != nil:
		return mediaResult{content: "[Location]"}
	case msg.Contact != nil:
		return mediaResult{content: "[Contact]"}
	}
	return mediaResult{}
}

var (
	reCodeBlock  = regexp.MustCompile("(?s)```(?:\\w*)\n?(.*?)```")
	reInlineCode = regexp.MustCompile("`([^`]+)`")
	reBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic     = regexp.MustCompile(`(^|[^*])\*([^*]+)\*([^*]|$)`)
	reHeader     = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
)

func mdToHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = reCodeBlock.ReplaceAllString(s, "<pre>$1</pre>")
	s = reInlineCode.ReplaceAllString(s, "<code>$1</code>")
	s = reBold.ReplaceAllString(s, "<b>$1</b>")
	s = reItalic.ReplaceAllString(s, "${1}<i>${2}</i>${3}")
	s = reHeader.ReplaceAllString(s, "<b>$1</b>")
	return s
}
