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
	"sync"
	"time"
	"unicode/utf16"

	tgbotapi "github.com/matterbridge/telegram-bot-api/v6"

	"github.com/onvos/arizuko/chanlib"
)

type bot struct {
	api       *tgbotapi.BotAPI
	cfg       config
	cancel    context.CancelFunc
	done      chan struct{}
	mentionRe *regexp.Regexp
	typing    *chanlib.TypingRefresher
	offsetMu  sync.Mutex
}

func newBot(cfg config) (*bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return nil, fmt.Errorf("telegram auth: %w", err)
	}
	slog.Info("telegram connected", "username", api.Self.UserName)
	b := &bot{api: api, cfg: cfg, done: make(chan struct{})}
	b.typing = chanlib.NewTypingRefresher(4*time.Second, 10*time.Minute, b.sendTyping, nil)
	if cfg.AssistantName != "" {
		b.mentionRe = regexp.MustCompile(
			fmt.Sprintf(`(?i)^@%s\b`, regexp.QuoteMeta(cfg.AssistantName)))
	}
	return b, nil
}

func (b *bot) loadOffset() int {
	data, _ := os.ReadFile(b.cfg.StateFile)
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n
}

// saveOffset writes atomically via temp + fsync + rename so a crash can't
// leave an empty or partial offset file.
func (b *bot) saveOffset(offset int) {
	if b.cfg.StateFile == "" {
		return
	}
	b.offsetMu.Lock()
	defer b.offsetMu.Unlock()
	tmp := b.cfg.StateFile + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		slog.Warn("offset open failed", "err", err)
		return
	}
	if _, err := f.WriteString(strconv.Itoa(offset)); err != nil {
		f.Close()
		os.Remove(tmp)
		slog.Warn("offset write failed", "err", err)
		return
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		slog.Warn("offset fsync failed", "err", err)
		return
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		slog.Warn("offset close failed", "err", err)
		return
	}
	if err := os.Rename(tmp, b.cfg.StateFile); err != nil {
		os.Remove(tmp)
		slog.Warn("offset rename failed", "err", err)
	}
}

func (b *bot) poll(ctx context.Context, rc *chanlib.RouterClient) {
	defer close(b.done)
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
			delivered := true
			if u.Message != nil {
				delivered = b.handle(u.Message, rc)
			}
			// Advance offset only after successful delivery so Telegram
			// redelivers on next long-poll if the router was unreachable.
			if delivered {
				b.saveOffset(u.UpdateID + 1)
			}
		}
	}
}

func (b *bot) stop() {
	if b.cancel != nil {
		b.cancel()
	}
	b.typing.Stop()
	b.api.StopReceivingUpdates()
	// Wait for poll goroutine to exit so any in-flight saveOffset completes.
	select {
	case <-b.done:
	case <-time.After(5 * time.Second):
		slog.Warn("poll goroutine did not exit within timeout")
	}
}

// handle processes a message and returns true iff the router accepted
// delivery (or the message was intentionally skipped and safe to ack).
func (b *bot) handle(msg *tgbotapi.Message, rc *chanlib.RouterClient) bool {
	if msg.From != nil && msg.From.IsBot {
		return true
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
		return true
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
	if err := rc.SendMessage(im); err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
		return false
	}
	slog.Debug("inbound", "chat_jid", jid, "sender_jid", im.Sender, "message_id", im.ID, "content_len", len(content))
	return true
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
	var firstID string
	for _, c := range chanlib.Chunk(mdToHTML(req.Content), 4096) {
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
				for _, p := range chanlib.Chunk(req.Content, 4096) {
					pm := tgbotapi.NewMessage(id, p)
					if msgThreadID != 0 {
						pm.MessageThreadID = msgThreadID
					}
					s2, e2 := b.api.Send(pm)
					if e2 != nil {
						return "", fmt.Errorf("telegram send: %w", e2)
					}
					if firstID == "" {
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
	slog.Debug("send", "chat_jid", req.ChatJID, "message_id", firstID, "source", "telegram")
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

// entity slices the substring addressed by a Telegram MessageEntity.
// Telegram documents Offset/Length as UTF-16 code units, so the string
// must be encoded to UTF-16 before indexing — otherwise emoji / non-BMP
// characters yield wrong ranges or panic on out-of-range.
func entity(text string, e tgbotapi.MessageEntity) string {
	u := utf16.Encode([]rune(text))
	if e.Offset < 0 || e.Offset > len(u) {
		return ""
	}
	end := e.Offset + e.Length
	if end > len(u) {
		end = len(u)
	}
	if end < e.Offset {
		return ""
	}
	return string(utf16.Decode(u[e.Offset:end]))
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
	att := func(content, fileID, mime, filename string, size int64) mediaResult {
		url := ""
		if listenURL != "" && fileID != "" {
			url = listenURL + "/files/" + fileID
		}
		return mediaResult{
			content:     content,
			attachments: []chanlib.InboundAttachment{{Mime: mime, Filename: filename, URL: url, Size: size}},
		}
	}
	switch {
	case msg.Photo != nil:
		p := msg.Photo[len(msg.Photo)-1]
		return att("[Photo]"+cap, p.FileID, "image/jpeg", p.FileID+".jpg", int64(p.FileSize))
	case msg.Video != nil:
		v := msg.Video
		return att("[Video]"+cap, v.FileID, "video/mp4", v.FileID+".mp4", v.FileSize)
	case msg.Voice != nil:
		v := msg.Voice
		return att("[Voice message]"+cap, v.FileID, "audio/ogg", v.FileID+".ogg", int64(v.FileSize))
	case msg.Audio != nil:
		a := msg.Audio
		fname := a.FileName
		if fname == "" {
			fname = a.FileID + ".mp3"
		}
		return att("[Audio]"+cap, a.FileID, "audio/mpeg", fname, a.FileSize)
	case msg.Document != nil:
		d := msg.Document
		n := d.FileName
		if n == "" {
			n = d.FileID
		}
		return att(fmt.Sprintf("[Document: %s]%s", n, cap), d.FileID, d.MimeType, n, d.FileSize)
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
