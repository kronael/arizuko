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
)

type mastoClient struct {
	cfg    config
	client *mastodon.Client
	me     *mastodon.Account
}

func newMastoClient(cfg config) (*mastoClient, error) {
	c := mastodon.NewClient(&mastodon.Config{
		Server:      cfg.InstanceURL,
		AccessToken: cfg.AccessToken,
	})
	ctx := context.Background()
	me, err := c.GetAccountCurrentUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("mastodon auth: %w", err)
	}
	slog.Info("mastodon connected", "account", me.Acct)
	return &mastoClient{cfg: cfg, client: c, me: me}, nil
}

func (mc *mastoClient) stream(ctx context.Context, rc *routerClient) {
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

func (mc *mastoClient) streamOnce(ctx context.Context, rc *routerClient) error {
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
				if ne.Notification.Type == "mention" {
					mc.handleMention(ne.Notification, rc)
				}
			}
		}
	}
}

func (mc *mastoClient) handleMention(n *mastodon.Notification, rc *routerClient) {
	if n.Status == nil {
		return
	}
	acc := n.Account
	jid := "mastodon:" + string(acc.ID)
	name := acc.DisplayName
	if name == "" {
		name = acc.Acct
	}
	_ = rc.sendChat(jid, name, false)

	content := stripHTML(n.Status.Content)
	err := rc.sendMessage(inboundMsg{
		ID:         string(n.Status.ID),
		ChatJID:    jid,
		Sender:     "mastodon:" + string(acc.ID),
		SenderName: name,
		Content:    content,
		Timestamp:  n.Status.CreatedAt.Unix(),
		IsGroup:    false,
	})
	if err != nil {
		slog.Error("deliver failed", "jid", jid, "err", err)
	}
}

func (mc *mastoClient) postStatus(ctx context.Context, text, replyToID string) error {
	toot := &mastodon.Toot{Status: text}
	if replyToID != "" {
		toot.InReplyToID = mastodon.ID(replyToID)
	}
	_, err := mc.client.PostStatus(ctx, toot)
	return err
}

var reTag = regexp.MustCompile(`<[^>]+>`)

func stripHTML(s string) string {
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	s = strings.ReplaceAll(s, "</p>", "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.TrimSpace(s)
}
