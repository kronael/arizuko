package store

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

func TestChatThreadRepliesPreservesRowAcrossReopen(t *testing.T) {
	ctx := context.Background()
	cfg := dyntest.Config(t)
	cfg.Prefix = dynstore.TestPrefix()
	h, err := dynstore.Dial(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.CreateEntity(ctx, Chats); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.DropEntity(ctx, Chats); err != nil {
			t.Error(err)
		}
	})
	s := NewOver(h)
	const jid = "discord:g/channel"
	cursor := MsgStamp(at(12), "message")
	if err := s.SetAgentCursor(jid, cursor); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStickyGroup(jid, "team"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStickyTopic(jid, "topic"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChatIsGroup(jid, true); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ChatThreadReplies("unseen"); err != nil || got {
		t.Fatalf("unseen = %v, %v", got, err)
	}
	for _, step := range []struct {
		value *bool
		want  bool
	}{
		{new(false), false}, {new(true), true}, {nil, true},
	} {
		var ws WriteSet
		if err := s.SetChatThreadRepliesInto(&ws, jid, step.value); err != nil {
			t.Fatal(err)
		}
		if ws.Len() != 1 {
			t.Fatalf("writes = %d", ws.Len())
		}
		if err := s.Apply(ctx, &ws); err != nil {
			t.Fatal(err)
		}
		reopened, err := dynstore.Dial(cfg)
		if err != nil {
			t.Fatal(err)
		}
		s = NewOver(reopened)
		got, err := s.ChatThreadReplies(jid)
		if err != nil || got != step.want {
			t.Fatalf("preference = %v, %v; want %v", got, err, step.want)
		}
		cur, err := s.AgentCursor(jid)
		if err != nil || cur != cursor {
			t.Fatalf("cursor = %q, %v", cur, err)
		}
		group, topic, err := s.StickyPins(jid)
		if err != nil || group != "team" || topic != "topic" {
			t.Fatalf("pins = %q, %q, %v", group, topic, err)
		}
		isGroup, err := s.ChatIsGroup(jid)
		if err != nil || !isGroup {
			t.Fatalf("group = %v, %v", isGroup, err)
		}
	}
	var bad WriteSet
	if err := s.SetChatThreadRepliesInto(&bad, "", nil); err == nil || bad.Len() != 0 {
		t.Fatal("empty jid accepted")
	}
	if err := s.WriteEntities(ctx, nil, objstore.Write{Op: objstore.Merge, Entity: Chats, Key: objstore.Key{Partition: OwnerRoutd, Sort: jid}, Attrs: map[string]string{attrChatThreadReplies: "invalid"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChatThreadReplies(jid); err == nil {
		t.Fatal("invalid preference accepted")
	}
}
