package store

// chats.go holds every read and write of `chats`: one item per chat routd has
// ever seen, carrying the dispatch cursor, the errored flag and the two
// navigation pins.
//
// THE PARTITION IS OwnerRoutd AND THE SORT KEY IS THE JID. Every one of the
// eleven writes names one jid — the cursor advance, the errored flag, the
// multi-party flag and both sticky pins — and so do four of the seven reads,
// which is the whole hot path: GetAgentCursor runs once per inbound message and
// once more per dispatch decision.
//
// A CHAT HAS NO FOLDER, so there is no tenancy coordinate to partition by. The
// pins name one, but they are the chat's CURRENT navigation state and a chat
// outlives the folder it points at — folder_scope.go clears the pins on a group
// delete and keeps the row precisely because the cursor is the chat's own. Under
// a jid partition the three reads that name no jid would have no partition to
// name, and a query without one is refused by every driver.
//
// WHAT THE CONSTANT COSTS is those three, and each was already a scan on the
// engine this replaces (EXPLAIN QUERY PLAN, krons' routd.db, 2026-09-09):
// `SELECT COUNT(*) FROM chats WHERE errored=1` answers SCAN, `SELECT MIN(
// agent_cursor)` walks the jid autoindex because no index on agent_cursor
// exists, and the errored page reads the same rows. The move changes where the
// rows are dropped, not how many are examined. The partition is 136 items on
// krons, 53 on sloth and 45 on marinade (2026-09-09); it has no drainage, and
// what bounds it is the number of chats an instance has ever been addressed
// from, never its traffic.
//
// agent_cursor IS THE ONE ORDERED ATTRIBUTE. It is a copy of some message's
// timestamp compared back against `messages.timestamp` with `>`, so a value of
// another width sorts by width before it sorts by time and the poll loop skips
// the row for good — BUGS L97, and the reason migration 0044 exists. The
// declaration below refuses a short value at write time instead. The two flags
// are "1" or ABSENT rather than "1"/"0": absent is the only null this model has,
// and a false flag that is present is a value a tombstone test still finds.

import (
	"context"
	"fmt"
	"sort"

	"github.com/kronael/arizuko/objstore"
)

// The attributes a chat item carries. `jid` is not among them: it is the
// coordinate, and storing it twice is two facts that can disagree.
const (
	attrChatErrored = "errored"
	attrChatIsGroup = "is_group"
	// AttrChatAgentCursor is the dispatch watermark, and the two sticky ones are
	// the navigation pins. All three are exported: the move that carries these
	// rows has to name the cursor it must NOT re-render, and folder_scope.go
	// decides a purge from the group pin and clears both.
	AttrChatAgentCursor = "agent_cursor"
	AttrChatStickyGroup = "sticky_group"
	AttrChatStickyTopic = "sticky_topic"
)

// flagSet is what a true flag is stored as. False is the attribute's ABSENCE.
const flagSet = "1"

// The ordered declaration is what makes the dispatch cursor a cursor: every
// driver refuses a key of another width, so a short one cannot sort into the
// wrong place (BUGS L97).
func init() { objstore.OrderedAttr(Chats, AttrChatAgentCursor, tsWidth) }

// chatKey is the coordinate of one chat.
func chatKey(jid string) (objstore.Key, error) {
	if jid == "" {
		return objstore.Key{}, fmt.Errorf("chat row carries no jid, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: jid}, nil
}

// chatItem reads one chat, if it has ever been seen.
func (s *Store) chatItem(ctx context.Context, jid string) (objstore.Item, bool, error) {
	key, err := chatKey(jid)
	if err != nil {
		return objstore.Item{}, false, err
	}
	return s.itemAt(ctx, Chats, key)
}

// allChats reads the whole partition. The file header says what that costs and
// what bounds it.
func (s *Store) allChats(ctx context.Context) ([]objstore.Item, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: Chats, Partition: OwnerRoutd})
	if err != nil {
		return nil, fmt.Errorf("read the chats: %w", err)
	}
	return items, nil
}

// AgentCursor answers how far the dispatch feed has consumed a chat. Empty means
// no turn has ever run on it, which the poll loop reads as "feed from the start".
func (s *Store) AgentCursor(jid string) (string, error) {
	it, ok, err := s.chatItem(context.Background(), jid)
	if err != nil || !ok {
		return "", err
	}
	return it.Attrs[AttrChatAgentCursor], nil
}

// SetAgentCursor writes a chat's dispatch watermark, whatever it held.
//
// UNGUARDED on purpose, and it is the caller that makes that safe: the poll loop
// advances to the newest row of the batch it just fed and nothing else writes
// this attribute on that path. The guarded twin below is the restore path, where
// two cursors genuinely race.
func (s *Store) SetAgentCursor(jid, cursor string) error {
	key, err := chatKey(jid)
	if err != nil {
		return err
	}
	if _, err := s.upsertChat(context.Background(), key,
		map[string]string{AttrChatAgentCursor: cursor}); err != nil {
		return fmt.Errorf("set the agent cursor of %s: %w", jid, err)
	}
	return nil
}

// LowestAgentCursor answers the floor the poll loop feeds from: the lowest
// cursor any chat holds, or empty when none has advanced yet.
//
// The minimum is taken HERE because a query orders on the sort key and the sort
// key is the jid. That is the reader's work the constant partition buys, and the
// statement it replaces walked the same rows.
func (s *Store) LowestAgentCursor() (string, error) {
	items, err := s.allChats(context.Background())
	if err != nil {
		return "", err
	}
	low := ""
	for _, it := range items {
		cur := it.Attrs[AttrChatAgentCursor]
		if cur == "" {
			continue
		}
		if low == "" || cur < low {
			low = cur
		}
	}
	return low, nil
}

// CountChats answers how many chats the partition holds. `arizuko migrate-hot`
// reads it back to check that every row travelled.
func (s *Store) CountChats(ctx context.Context) (int, error) {
	items, err := s.allChats(ctx)
	return len(items), err
}

// MarkChatErrored records that a run on this chat failed outright. The flag
// surfaces in inspection; it does not block a re-feed, because the cursor
// advancing past the failed batch is the starvation guard.
func (s *Store) MarkChatErrored(jid string) error {
	key, err := chatKey(jid)
	if err != nil {
		return err
	}
	if _, err := s.upsertChat(context.Background(), key,
		map[string]string{attrChatErrored: flagSet}); err != nil {
		return fmt.Errorf("flag %s errored: %w", jid, err)
	}
	return nil
}

// ErroredChatJIDs answers which chats carry the errored flag, in jid order.
func (s *Store) ErroredChatJIDs() ([]string, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Chats, Partition: OwnerRoutd,
		Where: []objstore.Cmp{{Attr: attrChatErrored, Op: objstore.Eq, Value: flagSet}},
	})
	if err != nil {
		return nil, fmt.Errorf("read the errored chats: %w", err)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Key.Sort)
	}
	sort.Strings(out)
	return out, nil
}

// SetChatIsGroup records whether a chat is multi-party.
//
// False REMOVES the attribute rather than writing "0": absent is the only null
// this model has, so a flag that is present is a flag a reader finds set.
func (s *Store) SetChatIsGroup(jid string, isGroup bool) error {
	key, err := chatKey(jid)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if isGroup {
		if _, err := s.upsertChat(ctx, key, map[string]string{attrChatIsGroup: flagSet}); err != nil {
			return fmt.Errorf("record %s as a group chat: %w", jid, err)
		}
		return nil
	}
	if err := s.clearChatAttrs(ctx, key, attrChatIsGroup); err != nil {
		return fmt.Errorf("record %s as a direct chat: %w", jid, err)
	}
	return nil
}

// ChatIsGroup reports whether a chat is multi-party. A chat never seen is not.
func (s *Store) ChatIsGroup(jid string) (bool, error) {
	it, ok, err := s.chatItem(context.Background(), jid)
	if err != nil || !ok {
		return false, err
	}
	return it.Attrs[attrChatIsGroup] == flagSet, nil
}

// StickyPins answers the @group / #topic navigation pins a chat is steered by.
func (s *Store) StickyPins(jid string) (group, topic string, err error) {
	it, ok, err := s.chatItem(context.Background(), jid)
	if err != nil || !ok {
		return "", "", err
	}
	return it.Attrs[AttrChatStickyGroup], it.Attrs[AttrChatStickyTopic], nil
}

// SetStickyGroup pins the @group a chat's messages route to, or clears the pin
// when folder is empty.
func (s *Store) SetStickyGroup(jid, folder string) error {
	return s.setChatPin(jid, AttrChatStickyGroup, folder)
}

// SetStickyTopic pins the #topic a chat's messages land in, or clears the pin
// when topic is empty.
func (s *Store) SetStickyTopic(jid, topic string) error {
	return s.setChatPin(jid, AttrChatStickyTopic, topic)
}

// GetStickyTopic answers the #topic pin alone, which is what a message read
// stamps an untopiced row with.
func (s *Store) GetStickyTopic(jid string) string {
	_, topic, err := s.StickyPins(jid)
	if err != nil {
		return ""
	}
	return topic
}

// setChatPin writes one navigation pin, and an empty value CLEARS it — which is
// what the column path's NULL meant and what absent means here.
func (s *Store) setChatPin(jid, attr, value string) error {
	key, err := chatKey(jid)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if value == "" {
		if err := s.clearChatAttrs(ctx, key, attr); err != nil {
			return fmt.Errorf("clear %s on %s: %w", attr, jid, err)
		}
		return nil
	}
	if _, err := s.upsertChat(ctx, key, map[string]string{attr: value}); err != nil {
		return fmt.Errorf("pin %s=%q on %s: %w", attr, value, jid, err)
	}
	return nil
}

// upsertChat merges attrs into a chat, creating it when this is the first thing
// ever recorded about it.
//
// TWO SHAPES BECAUSE THE INTERFACE HAS TWO, and which one lands is decided by
// the first one's REFUSAL rather than by a read: a read and then a write is two
// decisions and the item can appear between them (the shape store/groups.go and
// store/pane_sessions.go already use). A Put would have erased every other
// attribute the chat holds, which on this entity means a sticky pin or the
// cursor.
func (s *Store) upsertChat(ctx context.Context, key objstore.Key,
	attrs map[string]string) (bool, error) {
	created, err := s.WriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Put, Entity: Chats, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil || created {
		return created, err
	}
	if _, err := s.WriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: Chats, Key: key, Attrs: attrs,
	}); err != nil {
		return false, err
	}
	return false, nil
}

// clearChatAttrs takes attributes away, and says nothing about a chat that was
// never seen: there is nothing to clear on an item that does not exist, and an
// Update refuses rather than creating one.
func (s *Store) clearChatAttrs(ctx context.Context, key objstore.Key, attrs ...string) error {
	_, err := s.WriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: Chats, Key: key, Remove: attrs,
	})
	return err
}

// DeriveAgentCursors moves each touched chat's cursor to its newest message, so
// a restored history is not fed to the agent as if it were unread (spec 5/8
// Finding 4). It answers how many cursors moved.
//
// NEVER BACKWARD, and that is the invariant this operation carries: a populated
// target's poll loop must not be walked back over messages it already consumed.
// The guard is the condition on the write, which is what the `WHERE
// excluded.agent_cursor > COALESCE(chats.agent_cursor,”)` it replaces was.
//
// ONE TRANSACTION IS GONE AND NOTHING RESTS ON IT. The statement this replaces
// wrapped every chat in one, but the writes are independent — one chat's cursor
// says nothing about another's — and each is idempotent because it only ever
// moves forward, so a run that dies leaves work a re-run finishes rather than a
// half-applied transaction.
func (s *Store) DeriveAgentCursors(ctx context.Context, touched map[string]bool) (int, error) {
	jids := make([]string, 0, len(touched))
	for jid := range touched {
		jids = append(jids, jid)
	}
	sort.Strings(jids)
	moved := 0
	for _, jid := range jids {
		newest, err := s.newestMessageAt(ctx, jid)
		if err != nil {
			return moved, err
		}
		if newest == "" {
			continue
		}
		key, err := chatKey(jid)
		if err != nil {
			return moved, err
		}
		did, err := s.advanceCursor(ctx, Chats, key, AttrChatAgentCursor, newest, nil)
		if err != nil {
			return moved, fmt.Errorf("derive the agent cursor of %s: %w", jid, err)
		}
		if did {
			moved++
		}
	}
	return moved, nil
}
