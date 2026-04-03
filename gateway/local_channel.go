package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

type LocalChannel struct {
	store *store.Store
	name  string
}

func NewLocalChannel(s *store.Store) *LocalChannel {
	return &LocalChannel{
		store: s,
		name:  "local",
	}
}

func (c *LocalChannel) Name() string {
	return c.name
}

func (c *LocalChannel) Connect(ctx context.Context) error {
	return nil
}

func (c *LocalChannel) Owns(jid string) bool {
	return strings.HasPrefix(jid, "local:")
}

func (c *LocalChannel) Send(jid, text, replyTo, threadID string) (string, error) {
	if !c.Owns(jid) {
		return "", fmt.Errorf("local channel does not own jid: %s", jid)
	}

	msgID := fmt.Sprintf("local-%d", time.Now().UnixNano())
	msg := core.Message{
		ID:        msgID,
		ChatJID:   jid,
		Sender:    "local",
		Name:      "local",
		Content:   text,
		Timestamp: time.Now(),
		FromMe:    false,
		BotMsg:    false,
		ReplyToID: replyTo,
		Topic:     threadID,
	}

	if err := c.store.PutMessage(msg); err != nil {
		return "", fmt.Errorf("store message: %w", err)
	}

	return msgID, nil
}

func (c *LocalChannel) SendFile(jid, path, name, caption string) error {
	return fmt.Errorf("local channel does not support file sending")
}

func (c *LocalChannel) Typing(jid string, on bool) error {
	return nil
}

func (c *LocalChannel) Disconnect() error {
	return nil
}
