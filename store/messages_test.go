package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/conformance"
)

// A clamped message keeps its first content, position and indexes on redelivery.
func TestAppendMessageRedeliveryKeepsTheFirstRow(t *testing.T) {
	for _, folder := range []string{"", "team"} {
		t.Run("folder="+folder, func(t *testing.T) {
			s := openEntities(t)
			base := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
			seed := core.Message{ID: "floor", ChatJID: "chat", Timestamp: base}
			if err := s.PutMessage(seed); err != nil {
				t.Fatal(err)
			}
			late := core.Message{
				ID: "late", ChatJID: "chat", Timestamp: base.Add(-time.Hour),
				Content: "first", Sender: "person", Topic: "first", RoutedTo: folder,
			}
			first, err := s.putMessage(context.Background(), late, core.MessageStatusPending)
			if err != nil {
				t.Fatal(err)
			}
			if !first.Timestamp.After(seed.Timestamp) {
				t.Fatalf("late position = %s, want after %s", first.Timestamp, seed.Timestamp)
			}
			late.Content, late.Sender, late.Topic = "changed", "other", "other"
			if folder != "" {
				late.RoutedTo = "other"
			}
			for _, timestamp := range []time.Time{late.Timestamp, base.Add(time.Hour)} {
				late.Timestamp = timestamp
				got, err := s.putMessage(context.Background(), late, core.MessageStatusPending)
				if err != nil {
					t.Fatal(err)
				}
				assertFirstMessage(t, s, first, 2)
				if got.Content != first.Content || got.Topic != first.Topic ||
					!got.Timestamp.Equal(first.Timestamp) || got.RoutedTo != first.RoutedTo {
					t.Errorf("redelivery returned %+v, want authoritative %+v", got, first)
				}
			}
			late.ChatJID = "other-chat"
			got, err := s.AppendMessage(late)
			if err != nil || got.ChatJID != late.ChatJID || got.Content != late.Content {
				t.Fatalf("same ID in another chat = %+v, err=%v", got, err)
			}
			rows, err := s.NewMessagesForChat(context.Background(), late.ChatJID, "", 0)
			if err != nil || len(rows) != 1 || rows[0].ID != late.ID {
				t.Errorf("other chat = %+v, err=%v; want one row with ID %s", rows, err, late.ID)
			}
		})
	}
}

// A competing insert after the identity read forces a fresh identity and floor.
func TestAppendMessageSerializesCompetingInserts(t *testing.T) {
	for _, sameID := range []bool{false, true} {
		for _, folder := range []string{"", "team"} {
			t.Run(fmt.Sprintf("same-id=%t/folder=%s", sameID, folder),
				func(t *testing.T) {
					s := openEntities(t)
					base := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
					seed := core.Message{ID: "floor", ChatJID: "chat", Timestamp: base}
					if err := s.PutMessage(seed); err != nil {
						t.Fatal(err)
					}
					first := core.Message{
						ID: "first", ChatJID: "chat", Timestamp: base.Add(time.Hour),
						Content: "first", Sender: "person", Topic: "first", RoutedTo: folder,
					}
					second := first
					second.Timestamp = base.Add(-time.Hour)
					second.Content = "second"
					readAt := 1
					if !sameID {
						second.ID = "second"
						readAt = 2
					}
					fired := 0
					racer := NewOver(conformance.SlipAt(s.Obj(), Messages, readAt, func() {
						fired++
						if fired != readAt {
							return
						}
						var err error
						first, err = s.putMessage(context.Background(), first, core.MessageStatusPending)
						if err != nil {
							t.Fatal(err)
						}
					}))
					got, err := racer.putMessage(context.Background(), second, core.MessageStatusPending)
					if err != nil {
						t.Fatal(err)
					}
					if fired != readAt {
						t.Fatalf("interleave fired %d times, want %d", fired, readAt)
					}
					if sameID {
						assertFirstMessage(t, s, first, 2)
						if got.Content != first.Content || !got.Timestamp.Equal(first.Timestamp) {
							t.Errorf("competing redelivery returned %+v, want %+v", got, first)
						}
					} else {
						if !got.Timestamp.After(first.Timestamp) {
							t.Errorf("second position %s is not after competing floor %s", got.Timestamp, first.Timestamp)
						}
						rows, err := s.NewMessagesForChat(context.Background(), "chat", "", 0)
						if err != nil || len(rows) != 3 {
							t.Fatalf("messages = %+v, err=%v; want 3 rows", rows, err)
						}
						counts, err := s.FolderMessageCounts(context.Background(), []string{folder})
						if err != nil || (folder != "" && counts[folder] != 2) {
							t.Errorf("folder counts = %v, err=%v; want 2", counts, err)
						}
					}
				})
		}
	}
}

// A first append racing a redelivery commits once with or without a chat row.
func TestAppendMessageSerializesTheFirstInsert(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing-chat=%t", exists), func(t *testing.T) {
			s := openEntities(t)
			if exists {
				if err := s.SetStickyTopic("chat", "sticky"); err != nil {
					t.Fatal(err)
				}
			}
			first := core.Message{
				ID: "first", ChatJID: "chat", Content: "first", RoutedTo: "team",
				Timestamp: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC),
			}
			second := first
			second.Content = "second"
			second.Timestamp = first.Timestamp.Add(time.Hour)
			fired := 0
			racer := NewOver(conformance.SlipAt(s.Obj(), Messages, 1, func() {
				fired++
				var err error
				first, err = s.putMessage(context.Background(), first, core.MessageStatusPending)
				if err != nil {
					t.Fatal(err)
				}
			}))
			got, err := racer.putMessage(context.Background(), second, core.MessageStatusPending)
			if err != nil || fired != 1 {
				t.Fatalf("append err=%v, interleave fired %d times; want one successful interleave", err, fired)
			}
			assertFirstMessage(t, s, first, 1)
			if got.Content != first.Content || got.Topic != first.Topic || !got.Timestamp.Equal(first.Timestamp) {
				t.Errorf("competing first append returned %+v, want %+v", got, first)
			}
		})
	}
}

func assertFirstMessage(t *testing.T, s *Store, first core.Message, total int) {
	t.Helper()
	ctx := context.Background()
	rows, err := s.NewMessagesForChat(ctx, first.ChatJID, "", 0)
	if err != nil || len(rows) != total {
		t.Fatalf("messages = %+v, err=%v; want %d rows", rows, err, total)
	}
	n := 0
	for _, row := range rows {
		if row.ID == first.ID {
			n++
			if row.Content != first.Content || row.Sender != first.Sender ||
				row.Topic != first.Topic || row.RoutedTo != first.RoutedTo ||
				!row.Timestamp.Equal(first.Timestamp) {
				t.Errorf("stored message = %+v, want %+v", row, first)
			}
		}
	}
	if n != 1 {
		t.Errorf("message %s has %d rows, want 1", first.ID, n)
	}
	for _, q := range []objstore.Query{
		{Entity: MessageActivity, Partition: activityPartition},
		{Entity: MessageDelivery, Partition: core.MessageStatusPending},
		{Entity: MessageFolders, Partition: "team"},
		{Entity: MessageFolders, Partition: "other"},
	} {
		items, err := s.Obj().Query(ctx, q)
		want := 0
		if q.Entity == MessageActivity {
			want = total
		} else if q.Entity == MessageDelivery || q.Partition == first.RoutedTo {
			want = 1
		}
		if err != nil || len(items) != want {
			t.Errorf("%s/%s has %d rows, err=%v; want %d", q.Entity, q.Partition, len(items), err, want)
		}
		for _, item := range items {
			if _, id, ok := MsgCoord(item.Key.Sort); ok && id == first.ID {
				if item.Key.Sort != MessageCursor(first) {
					t.Errorf("index position = %s, want %s", item.Key.Sort, MessageCursor(first))
				}
				if q.Entity == MessageActivity && item.Attrs[attrActPreview] != first.Content {
					t.Errorf("activity content = %q, want %q", item.Attrs[attrActPreview], first.Content)
				}
			}
		}
	}
	counts, err := s.FolderMessageCounts(ctx, []string{"team", "other"})
	want := 0
	if first.RoutedTo != "" {
		want = 1
	}
	if err != nil || counts["team"] != want || counts["other"] != 0 {
		t.Errorf("folder counts = %v, err=%v; want team=%d, other=0", counts, err, want)
	}
}

// BotRepliesSince returns only the agent's outbound rows after a cutoff — the
// inverse of MessagesSince. Used by `arizuko send` operator-direct mode to show
// the agent's reply to a CLI-injected message.
func TestBotRepliesSince(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	base := time.Now().Truncate(time.Second)

	mustPut := func(m core.Message) {
		if err := s.PutMessage(m); err != nil {
			t.Fatalf("put %s: %v", m.ID, err)
		}
	}
	// The rows are appended in the order they happened, because an append takes
	// the chat's next position and an out-of-order fixture would be re-stamped
	// onto the tail (store.appendInstant).
	// bot reply BEFORE the cutoff — excluded
	mustPut(core.Message{ID: "old", ChatJID: "web:krons", Sender: "bot", Content: "stale", Timestamp: base.Add(-time.Hour), BotMsg: true})
	// operator inbound (not a bot row) — must be excluded
	mustPut(core.Message{ID: "in", ChatJID: "web:krons", Sender: "operator", Content: "draft tweets", Timestamp: base, Verb: "mention"})
	// agent replies (bot rows) after the cutoff — included, oldest first
	mustPut(core.Message{ID: "b1", ChatJID: "web:krons", Sender: "bot", Content: "tweet 1", Timestamp: base.Add(2 * time.Second), BotMsg: true})
	mustPut(core.Message{ID: "b2", ChatJID: "web:krons", Sender: "bot", Content: "tweet 2", Timestamp: base.Add(3 * time.Second), BotMsg: true})
	// bot reply on a different jid — excluded
	mustPut(core.Message{ID: "other", ChatJID: "web:happy", Sender: "bot", Content: "elsewhere", Timestamp: base.Add(2 * time.Second), BotMsg: true})

	got, err := s.BotRepliesSince("web:krons", base, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("replies = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].Content != "tweet 1" || got[1].Content != "tweet 2" {
		t.Errorf("order/content = %q, %q", got[0].Content, got[1].Content)
	}
}
