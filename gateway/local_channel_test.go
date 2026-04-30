package gateway

import (
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

func TestLocalChannelSend_EnqueuesAndStoresAsInbound(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	lc := NewLocalChannel(s)
	var enqueued []string
	lc.SetEnqueue(func(jid string) { enqueued = append(enqueued, jid) })

	id, err := lc.Send("local:child", "parent reply", "", "", "")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id == "" {
		t.Fatal("no message id returned")
	}

	// Message must be visible to MessagesSince (i.e. not a bot message),
	// so the child agent wakes up and can forward to the original user.
	msgs, err := s.MessagesSince("local:child", time.Time{}, "nobot")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range msgs {
		if m.Content == "parent reply" && !m.BotMsg && !m.FromMe {
			found = true
		}
	}
	if !found {
		t.Errorf("escalation reply not stored as inbound non-bot message: %+v", msgs)
	}

	if len(enqueued) != 1 || enqueued[0] != "local:child" {
		t.Errorf("enqueue not invoked for local recipient: %v", enqueued)
	}
}

func TestLocalChannelSend_RejectsNonLocal(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	lc := NewLocalChannel(s)
	if _, err := lc.Send("telegram:1", "x", "", "", ""); err == nil {
		t.Error("non-local jid should be rejected")
	}
}

var _ core.Channel = (*LocalChannel)(nil)
