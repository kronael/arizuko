package main

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/mattn/go-mastodon"

	"github.com/onvos/arizuko/chanlib"
)

type mastoClient struct {
	chanlib.NoFileSender
	cfg    config
	client *mastodon.Client
	me     *mastodon.Account
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
	return &mastoClient{cfg: cfg, client: c, me: me}, nil
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
	_ = rc.SendChat(jid, name, false)

	switch n.Type {
	case "mention", "reply":
		if n.Status == nil {
			return
		}
		topic := ""
		if n.Status.InReplyToID != nil {
			if replyID, ok := n.Status.InReplyToID.(string); ok {
				topic = replyID
			}
		}
		verb := "message"
		if n.Type == "reply" || topic != "" {
			verb = "reply"
		}
		err := rc.SendMessage(chanlib.InboundMsg{
			ID:         string(n.Status.ID),
			ChatJID:    jid,
			Sender:     "mastodon:" + string(acc.ID),
			SenderName: name,
			Content:    stripHTML(n.Status.Content),
			Timestamp:  n.Status.CreatedAt.Unix(),
			Topic:      topic,
			Verb:       verb,
		})
		if err != nil {
			slog.Error("deliver failed", "jid", jid, "err", err)
		}
	case "favourite":
		if n.Status == nil {
			return
		}
		emoji := "❤️"
		err := rc.SendMessage(chanlib.InboundMsg{
			ID:         "fav-" + string(n.Status.ID) + "-" + string(acc.ID),
			ChatJID:    jid,
			Sender:     "mastodon:" + string(acc.ID),
			SenderName: name,
			Content:    emoji,
			Timestamp:  time.Now().Unix(),
			Verb:       "react",
		})
		if err != nil {
			slog.Error("deliver failed", "jid", jid, "err", err)
		}
	case "reblog":
		if n.Status == nil {
			return
		}
		err := rc.SendMessage(chanlib.InboundMsg{
			ID:         "reblog-" + string(n.Status.ID) + "-" + string(acc.ID),
			ChatJID:    jid,
			Sender:     "mastodon:" + string(acc.ID),
			SenderName: name,
			Content:    string(n.Status.ID),
			Timestamp:  time.Now().Unix(),
			Verb:       "repost",
		})
		if err != nil {
			slog.Error("deliver failed", "jid", jid, "err", err)
		}
	case "follow":
		err := rc.SendMessage(chanlib.InboundMsg{
			ID:         "follow-" + string(acc.ID),
			ChatJID:    jid,
			Sender:     "mastodon:" + string(acc.ID),
			SenderName: name,
			Content:    name + " followed you",
			Timestamp:  time.Now().Unix(),
			Verb:       "follow",
		})
		if err != nil {
			slog.Error("deliver failed", "jid", jid, "err", err)
		}
	default:
		slog.Debug("mastodon: unhandled notification type", "type", n.Type)
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

var reTag = regexp.MustCompile(`<[^>]+>`)

func stripHTML(s string) string {
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	s = strings.ReplaceAll(s, "</p>", "\n")
	s = reTag.ReplaceAllString(s, "")
	return strings.TrimSpace(html.UnescapeString(s))
}
