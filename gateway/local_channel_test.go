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
	s.PutGroup(core.Group{Folder: "child", Name: "Child"})

	lc := NewLocalChannel(s)
	var enqueued []string
	lc.SetEnqueue(func(jid string) { enqueued = append(enqueued, jid) })

	id, err := lc.Send("child", "parent reply", "", "", "")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id == "" {
		t.Fatal("no message id returned")
	}

	msgs, err := s.MessagesSince("child", time.Time{}, "nobot")
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

	if len(enqueued) != 1 || enqueued[0] != "child" {
		t.Errorf("enqueue not invoked for local recipient: %v", enqueued)
	}
}

func TestLocalChannelSend_RejectsExternalJID(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	lc := NewLocalChannel(s)
	if _, err := lc.Send("telegram:1", "x", "", "", ""); err == nil {
		t.Error("external jid should be rejected")
	}
}

func TestLocalChannelOwns_UnknownFolderRejected(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	lc := NewLocalChannel(s)
	if lc.Owns("does-not-exist") {
		t.Error("Owns should reject unregistered folder")
	}
}

var _ core.Channel = (*LocalChannel)(nil)
