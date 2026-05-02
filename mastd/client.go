package main

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mattn/go-mastodon"

	"github.com/onvos/arizuko/chanlib"
)

type mastoClient struct {
	chanlib.NoFileSender
	chanlib.NoVoiceSender
	chanlib.NoSocial
	cfg           config
	client        *mastodon.Client
	me            *mastodon.Account
	http          *http.Client
	files         *chanlib.URLCache
	streaming     atomic.Bool
	lastInboundAt atomic.Int64
}

func (mc *mastoClient) isConnected() bool    { return mc.streaming.Load() }
func (mc *mastoClient) LastInboundAt() int64 { return mc.lastInboundAt.Load() }

func newMastoClient(cfg config) (*mastoClient, error) {
	c := mastodon.NewClient(&mastodon.Config{
		Server:      cfg.InstanceURL,
		AccessToken: cfg.AccessToken,
	})
	c.UserAgent = chanlib.UserAgent
	me, err := c.GetAccountCurrentUser(context.Background())
	if err != nil {
		return nil, fmt.Errorf("mastodon auth: %w", err)
	}
	slog.Info("mastodon connected", "account", me.Acct)
	mc := &mastoClient{
		cfg: cfg, client: c, me: me,
		http:  &http.Client{Timeout: 30 * time.Second},
		files: chanlib.NewURLCache(cfg.FileCacheSize),
	}
	mc.lastInboundAt.Store(time.Now().Unix())
	return mc, nil
}

func (mc *mastoClient) stream(ctx context.Context, rc *chanlib.RouterClient) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := mc.streamOnce(ctx, rc); err != nil {
			slog.Error("streaming error", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 60*time.Second)
			continue
		}
		backoff = time.Second
	}
}

func (mc *mastoClient) streamOnce(ctx context.Context, rc *chanlib.RouterClient) error {
	// Derived context we cancel on return so the ws library tears down the
	// conn and the events goroutine can't leak past this call.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wsc := mc.client.NewWSClient()
	events, err := wsc.StreamingWSUser(sctx)
	if err != nil {
		return fmt.Errorf("streaming connect: %w", err)
	}
	mc.streaming.Store(true)
	defer mc.streaming.Store(false)
	// Drain events on exit so the library's sender goroutine can finish.
	defer func() {
		go func() {
			for range events {
			}
		}()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				return fmt.Errorf("stream closed")
			}
			if ne, ok := ev.(*mastodon.NotificationEvent); ok {
				mc.handleNotification(ne.Notification, rc)
			}
		}
	}
}

func (mc *mastoClient) handleNotification(n *mastodon.Notification, rc *chanlib.RouterClient) {
	msg, ok := mc.notificationToMsg(n)
	if !ok {
		slog.Debug("mastodon: unhandled notification type", "type", n.Type)
		return
	}
	if err := rc.SendMessage(msg); err != nil {
		slog.Error("deliver failed", "jid", msg.ChatJID, "err", err)
		return
	}
	mc.lastInboundAt.Store(time.Now().Unix())
	slog.Debug("inbound", "chat_jid", msg.ChatJID, "sender_jid", msg.Sender, "message_id", msg.ID, "content_len", len(msg.Content), "verb", msg.Verb)
}

func (mc *mastoClient) FetchHistory(req chanlib.HistoryRequest) (chanlib.HistoryResponse, error) {
	jid := req.ChatJID
	// Accept both legacy `mastodon:<id>` and new `mastodon:account/<id>`.
	var accountID string
	switch {
	case strings.HasPrefix(jid, "mastodon:account/"):
		accountID = strings.TrimPrefix(jid, "mastodon:account/")
	case strings.HasPrefix(jid, "mastodon:"):
		accountID = strings.TrimPrefix(jid, "mastodon:")
	default:
		return chanlib.HistoryResponse{Source: "unsupported", Messages: []chanlib.InboundMsg{}}, nil
	}
	if accountID == "" {
		return chanlib.HistoryResponse{Source: "unsupported", Messages: []chanlib.InboundMsg{}}, nil
	}
	limit := req.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	pg := &mastodon.Pagination{Limit: int64(limit)}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	notes, err := mc.client.GetNotifications(ctx, pg)
	if err != nil {
		return chanlib.HistoryResponse{}, fmt.Errorf("get notifications: %w", err)
	}
	before := req.Before
	out := make([]chanlib.InboundMsg, 0, len(notes))
	for i := len(notes) - 1; i >= 0; i-- {
		n := notes[i]
		if string(n.Account.ID) != accountID {
			continue
		}
		msg, ok := mc.notificationToMsg(n)
		if !ok {
			continue
		}
		if !before.IsZero() && msg.Timestamp >= before.Unix() {
			continue
		}
		out = append(out, msg)
	}
	return chanlib.HistoryResponse{Source: "platform", Messages: out}, nil
}

func (mc *mastoClient) notificationToMsg(n *mastodon.Notification) (chanlib.InboundMsg, bool) {
	acc := n.Account
	jid := "mastodon:account/" + string(acc.ID)
	name := acc.DisplayName
	if name == "" {
		name = acc.Acct
	}
	msg := chanlib.InboundMsg{ChatJID: jid, Sender: jid, SenderName: name, Timestamp: time.Now().Unix()}
	switch n.Type {
	case "mention", "reply":
		if n.Status == nil {
			return chanlib.InboundMsg{}, false
		}
		topic, _ := n.Status.InReplyToID.(string)
		verb := "message"
		if n.Type == "reply" || topic != "" {
			verb = "reply"
		}
		content := stripHTML(n.Status.Content)
		for _, att := range n.Status.MediaAttachments {
			content += fmt.Sprintf(" [%s: %s]", att.Type, att.Description)
		}
		msg.ID = string(n.Status.ID)
		msg.Content = content
		msg.Timestamp = n.Status.CreatedAt.Unix()
		msg.Topic = topic
		msg.Verb = verb
		msg.Attachments = mc.extractAttachments(n.Status)
		// visibility=direct → DM; everything else (public, unlisted,
		// private/followers-only) is multi-actor and treated as group.
		msg.IsGroup = n.Status.Visibility != "direct"
	case "favourite":
		if n.Status == nil {
			return chanlib.InboundMsg{}, false
		}
		msg.ID = "fav-" + string(n.Status.ID) + "-" + string(acc.ID)
		msg.Content = "❤️"
		msg.Verb = "like"
		msg.IsGroup = n.Status.Visibility != "direct"
	case "reblog":
		if n.Status == nil {
			return chanlib.InboundMsg{}, false
		}
		msg.ID = "reblog-" + string(n.Status.ID) + "-" + string(acc.ID)
		msg.Content = string(n.Status.ID)
		msg.Verb = "repost"
		msg.IsGroup = n.Status.Visibility != "direct"
	case "follow":
		msg.ID = "follow-" + string(acc.ID)
		msg.Content = name + " followed you"
		msg.Verb = "follow"
	default:
		return chanlib.InboundMsg{}, false
	}
	return msg, true
}

func (mc *mastoClient) Send(req chanlib.SendRequest) (string, error) {
	toot := &mastodon.Toot{Status: req.Content}
	if req.ReplyTo != "" {
		toot.InReplyToID = mastodon.ID(req.ReplyTo)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := mc.client.PostStatus(ctx, toot)
	if err != nil {
		return "", err
	}
	slog.Debug("send", "chat_jid", req.ChatJID, "source", "mastodon", "id", st.ID)
	return string(st.ID), nil
}

func (mc *mastoClient) Typing(string, bool) {}

func (mc *mastoClient) Post(req chanlib.PostRequest) (string, error) {
	if len(req.MediaPaths) > 0 {
		return "", fmt.Errorf("mastodon post: media upload not implemented")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	status, err := mc.client.PostStatus(ctx, &mastodon.Toot{Status: req.Content})
	if err != nil {
		return "", fmt.Errorf("mastodon post: %w", err)
	}
	slog.Debug("post", "source", "mastodon", "id", status.ID)
	return string(status.ID), nil
}

func (mc *mastoClient) Like(req chanlib.LikeRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := mc.client.Favourite(ctx, mastodon.ID(req.TargetID)); err != nil {
		return fmt.Errorf("mastodon favourite: %w", err)
	}
	return nil
}

func (mc *mastoClient) Delete(req chanlib.DeleteRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := mc.client.DeleteStatus(ctx, mastodon.ID(req.TargetID)); err != nil {
		return fmt.Errorf("mastodon delete: %w", err)
	}
	return nil
}

func (mc *mastoClient) Forward(chanlib.ForwardRequest) (string, error) {
	return "", chanlib.Unsupported("forward", "mastodon",
		"Mastodon has no forward primitive. Use `repost(source_msg_id=...)` to amplify, or `post(content=\"<commentary>\\n\\n<permalink>\")` to relay with text.")
}

func (mc *mastoClient) Quote(chanlib.QuoteRequest) (string, error) {
	return "", chanlib.Unsupported("quote", "mastodon",
		"Mastodon has no quote primitive. Use `post(content=\"<your take>\\n\\nvia: <permalink>\")` to share with commentary, or `repost(source_msg_id=...)` to amplify without commentary.")
}

func (mc *mastoClient) Repost(req chanlib.RepostRequest) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := mc.client.Reblog(ctx, mastodon.ID(req.SourceMsgID))
	if err != nil {
		return "", fmt.Errorf("mastodon repost: %w", err)
	}
	if st == nil {
		return "", nil
	}
	return string(st.ID), nil
}

func (mc *mastoClient) Dislike(chanlib.DislikeRequest) error {
	return chanlib.Unsupported("dislike", "mastodon",
		"Mastodon has no native downvote. Consider `reply` with a textual rebuttal instead of a sentiment signal.")
}

func (mc *mastoClient) Edit(req chanlib.EditRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	toot := &mastodon.Toot{Status: req.Content}
	if _, err := mc.client.UpdateStatus(ctx, toot, mastodon.ID(req.TargetID)); err != nil {
		return fmt.Errorf("mastodon edit: %w", err)
	}
	return nil
}

func (mc *mastoClient) SendFile(_, _, _, _ string) error {
	return chanlib.Unsupported("send_file", "mastodon",
		"Mastodon media upload (POST /api/v2/media + media_ids on toot) is not wired up yet. Inline a URL in `post(content=...)` for now.")
}

func (mc *mastoClient) extractAttachments(s *mastodon.Status) []chanlib.InboundAttachment {
	var atts []chanlib.InboundAttachment
	for _, a := range s.MediaAttachments {
		id := mc.files.Put(a.URL)
		mime := mediaMime(a.Type)
		url := ""
		if mc.cfg.ListenURL != "" {
			url = mc.cfg.ListenURL + "/files/" + id
		}
		atts = append(atts, chanlib.InboundAttachment{
			Mime: mime, Filename: string(a.ID) + mimeExt(mime), URL: url,
		})
	}
	return atts
}

func (mc *mastoClient) FileURL(id string) (string, bool) {
	return mc.files.Get(id)
}

func mediaMime(typ string) string {
	switch typ {
	case "image":
		return "image/jpeg"
	case "video", "gifv":
		return "video/mp4"
	case "audio":
		return "audio/mpeg"
	}
	return "application/octet-stream"
}

func mimeExt(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "video/mp4":
		return ".mp4"
	case "audio/mpeg":
		return ".mp3"
	}
	return ""
}

var reTag = regexp.MustCompile(`<[^>]+>`)

func stripHTML(s string) string {
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	s = strings.ReplaceAll(s, "</p>", "\n")
	s = reTag.ReplaceAllString(s, "")
	return strings.TrimSpace(html.UnescapeString(s))
}
