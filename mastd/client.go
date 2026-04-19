package main

import (
	"container/list"
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-mastodon"

	"github.com/onvos/arizuko/chanlib"
)

// fileCache holds a bounded map of attachment ID → CDN URL. Evicts
// oldest entries once size exceeds maxSize so a long-running stream
// doesn't grow memory without bound.
type fileCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List // front=oldest, back=newest
	maxSize int
}

type fileEntry struct {
	id  string
	url string
}

func newFileCache(max int) *fileCache {
	if max <= 0 {
		max = 1000
	}
	return &fileCache{
		entries: map[string]*list.Element{},
		order:   list.New(),
		maxSize: max,
	}
}

func (fc *fileCache) Put(id, url string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if el, ok := fc.entries[id]; ok {
		el.Value.(*fileEntry).url = url
		fc.order.MoveToBack(el)
		return
	}
	el := fc.order.PushBack(&fileEntry{id: id, url: url})
	fc.entries[id] = el
	for fc.order.Len() > fc.maxSize {
		front := fc.order.Front()
		if front == nil {
			break
		}
		fc.order.Remove(front)
		delete(fc.entries, front.Value.(*fileEntry).id)
	}
}

func (fc *fileCache) Get(id string) (string, bool) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	el, ok := fc.entries[id]
	if !ok {
		return "", false
	}
	return el.Value.(*fileEntry).url, true
}

type mastoClient struct {
	chanlib.NoFileSender
	cfg    config
	client *mastodon.Client
	me     *mastodon.Account
	http   *http.Client
	files  *fileCache
}

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
	return &mastoClient{
		cfg: cfg, client: c, me: me,
		http:  &http.Client{Timeout: 30 * time.Second},
		files: newFileCache(cfg.FileCacheSize),
	}, nil
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
	acc := n.Account
	jid := "mastodon:" + string(acc.ID)
	name := acc.DisplayName
	if name == "" {
		name = acc.Acct
	}
	msg := chanlib.InboundMsg{ChatJID: jid, Sender: jid, SenderName: name, Timestamp: time.Now().Unix()}

	switch n.Type {
	case "mention", "reply":
		if n.Status == nil {
			return
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
	case "favourite":
		if n.Status == nil {
			return
		}
		msg.ID = "fav-" + string(n.Status.ID) + "-" + string(acc.ID)
		msg.Content = "❤️"
		msg.Verb = "react"
	case "reblog":
		if n.Status == nil {
			return
		}
		msg.ID = "reblog-" + string(n.Status.ID) + "-" + string(acc.ID)
		msg.Content = string(n.Status.ID)
		msg.Verb = "repost"
	case "follow":
		msg.ID = "follow-" + string(acc.ID)
		msg.Content = name + " followed you"
		msg.Verb = "follow"
	default:
		slog.Debug("mastodon: unhandled notification type", "type", n.Type)
		return
	}

	if err := rc.SendMessage(msg); err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
		return
	}
	slog.Debug("inbound", "chat_jid", jid, "sender_jid", msg.Sender, "message_id", msg.ID, "content_len", len(msg.Content), "verb", msg.Verb)
}

// FetchHistory returns notifications from the target account, newest-first
// platform order reversed to oldest-first. JID shape `mastodon:<account_id>`;
// we paginate `/api/v1/notifications` with max_id and filter by account.
// Mastodon servers have instance-dependent depth limits; we don't mark
// capped unless we detect truncation via empty pagination.
func (mc *mastoClient) FetchHistory(req chanlib.HistoryRequest) (chanlib.HistoryResponse, error) {
	jid := req.ChatJID
	if !strings.HasPrefix(jid, "mastodon:") {
		return chanlib.HistoryResponse{Source: "unsupported", Messages: []chanlib.InboundMsg{}}, nil
	}
	accountID := strings.TrimPrefix(jid, "mastodon:")
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
	// Reverse for oldest-first.
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

// notificationToMsg mirrors handleNotification but returns the message
// instead of delivering. Returns ok=false for types we skip.
func (mc *mastoClient) notificationToMsg(n *mastodon.Notification) (chanlib.InboundMsg, bool) {
	acc := n.Account
	jid := "mastodon:" + string(acc.ID)
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
	case "favourite":
		if n.Status == nil {
			return chanlib.InboundMsg{}, false
		}
		msg.ID = "fav-" + string(n.Status.ID) + "-" + string(acc.ID)
		msg.Content = "❤️"
		msg.Verb = "react"
	case "reblog":
		if n.Status == nil {
			return chanlib.InboundMsg{}, false
		}
		msg.ID = "reblog-" + string(n.Status.ID) + "-" + string(acc.ID)
		msg.Content = string(n.Status.ID)
		msg.Verb = "repost"
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
	_, err := mc.client.PostStatus(context.Background(), toot)
	if err != nil {
		return "", err
	}
	slog.Debug("send", "chat_jid", req.ChatJID, "source", "mastodon")
	return "", nil
}

func (mc *mastoClient) Typing(string, bool) {}

func (mc *mastoClient) extractAttachments(s *mastodon.Status) []chanlib.InboundAttachment {
	var atts []chanlib.InboundAttachment
	for _, a := range s.MediaAttachments {
		id := string(a.ID)
		mc.files.Put(id, a.URL)
		mime := mediaMime(a.Type)
		url := ""
		if mc.cfg.ListenURL != "" {
			url = mc.cfg.ListenURL + "/files/" + id
		}
		atts = append(atts, chanlib.InboundAttachment{
			Mime: mime, Filename: id + mimeExt(mime), URL: url,
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
