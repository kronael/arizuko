package main

import (
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

type mastoClient struct {
	chanlib.NoFileSender
	cfg    config
	client *mastodon.Client
	me     *mastodon.Account
	http   *http.Client
	files  sync.Map // attachment ID → CDN URL
}

func newMastoClient(cfg config) (*mastoClient, error) {
	c := mastodon.NewClient(&mastodon.Config{
		Server:      cfg.InstanceURL,
		AccessToken: cfg.AccessToken,
	})
	me, err := c.GetAccountCurrentUser(context.Background())
	if err != nil {
		return nil, fmt.Errorf("mastodon auth: %w", err)
	}
	slog.Info("mastodon connected", "account", me.Acct)
	return &mastoClient{
		cfg: cfg, client: c, me: me,
		http: &http.Client{Timeout: 30 * time.Second},
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
	wsc := mc.client.NewWSClient()
	events, err := wsc.StreamingWSUser(ctx)
	if err != nil {
		return fmt.Errorf("streaming connect: %w", err)
	}
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
	msg := chanlib.InboundMsg{
		ChatJID:    jid,
		Sender:     jid,
		SenderName: name,
		Timestamp:  time.Now().Unix(),
	}

	switch n.Type {
	case "mention", "reply":
		if n.Status == nil {
			return
		}
		topic := ""
		if s, ok := n.Status.InReplyToID.(string); ok {
			topic = s
		}
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
	}
}

func (mc *mastoClient) Send(req chanlib.SendRequest) (string, error) {
	toot := &mastodon.Toot{Status: req.Content}
	if req.ReplyTo != "" {
		toot.InReplyToID = mastodon.ID(req.ReplyTo)
	}
	_, err := mc.client.PostStatus(context.Background(), toot)
	return "", err
}

func (mc *mastoClient) Typing(string, bool) {}

func (mc *mastoClient) extractAttachments(s *mastodon.Status) []chanlib.InboundAttachment {
	var atts []chanlib.InboundAttachment
	for _, a := range s.MediaAttachments {
		id := string(a.ID)
		mc.files.Store(id, a.URL)
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
	v, ok := mc.files.Load(id)
	if !ok {
		return "", false
	}
	return v.(string), true
}

func mediaMime(typ string) string {
	switch typ {
	case "image":
		return "image/jpeg"
	case "video", "gifv":
		return "video/mp4"
	case "audio":
		return "audio/mpeg"
	default:
		return "application/octet-stream"
	}
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
