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
	store   *store.Store
	enqueue func(jid string)
}

func NewLocalChannel(s *store.Store) *LocalChannel {
	return &LocalChannel{store: s}
}

// SetEnqueue wires a callback so local: messages land as queued input
// for the target group (wakes escalation recipients).
func (c *LocalChannel) SetEnqueue(fn func(jid string)) {
	c.enqueue = fn
}

func (c *LocalChannel) Name() string                    { return "local" }
func (c *LocalChannel) Connect(context.Context) error   { return nil }
func (c *LocalChannel) Disconnect() error               { return nil }
func (c *LocalChannel) Typing(string, bool) error       { return nil }
func (c *LocalChannel) Owns(jid string) bool            { return strings.HasPrefix(jid, "local:") }

func (c *LocalChannel) SendFile(_, _, _, _ string) error {
	return fmt.Errorf("local channel does not support file sending")
}

func (c *LocalChannel) Send(jid, text, replyTo, threadID string) (string, error) {
	if !c.Owns(jid) {
		return "", fmt.Errorf("local channel does not own jid: %s", jid)
	}
	msgID := core.MsgID("local")
	err := c.store.PutMessage(core.Message{
		ID:        msgID,
		ChatJID:   jid,
		Sender:    "local",
		Name:      "local",
		Content:   text,
		Timestamp: time.Now(),
		ReplyToID: replyTo,
		Topic:     threadID,
	})
	if err != nil {
		return "", fmt.Errorf("store message: %w", err)
	}
	// Wake the local recipient so escalation replies flow back to the
	// originating child group for onward delivery to the user.
	if c.enqueue != nil {
		c.enqueue(jid)
	}
	return msgID, nil
}
