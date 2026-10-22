package store

// chats.go holds every read and write of `chats`: one item per chat routd has
// ever seen, carrying the dispatch cursor, the errored flag and the two
// navigation pins.
//
// The partition is OwnerRoutd and the sort key the jid: a chat has no folder.
// The pins name one, but a chat outlives the folder it points at, so
// folder_scope.go clears the pins and keeps the row. The three reads that name
// no jid scan the partition: 136 items on krons, bounded by the chats an
// instance was ever addressed from, never by traffic.
//
// agent_cursor is the one ORDERED attribute and holds a WHOLE MESSAGE KEY,
// `core.TS + KeySep + id`: a bare instant sorts BELOW every message at it, so
// `> cursor` would hand back an answered message once per poll, for ever. The
// instant is fixed width and the id is not, so the declaration is an ordered
// LEAD (BUGS L97).
//
// The two flags are "1" or ABSENT: absent is the only null this model has, and
// a present false flag is a value a tombstone test still finds.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/kronael/arizuko/objstore"
)

// The attributes a chat item carries. `jid` is not among them: it is the
// coordinate.
const (
	attrChatErrored = "errored"
	attrChatIsGroup = "is_group"
	// Exported: the move that carries these rows names the cursor it must NOT
	// re-render, and folder_scope.go reads the group pin and clears both.
	AttrChatAgentCursor = "agent_cursor"
	AttrChatStickyGroup = "sticky_group"
	AttrChatStickyTopic = "sticky_topic"
	// attrChatHasInbound marks a chat that has ever carried a real inbound
	// message: none a bot sent, none the proactive trigger synthesised. The
	// proactive scan reads it instead of DISTINCT over every message.
	attrChatHasInbound = "has_inbound"
)

// flagSet is what a true flag is stored as. False is the attribute's ABSENCE.
const flagSet = "1"

// The ordered declaration is what makes the dispatch cursor a cursor: every
// driver refuses a value whose instant is short (BUGS L97).
func init() { objstore.OrderedLead(Chats, AttrChatAgentCursor, tsWidth) }

// chatKey is the coordinate of one chat.
func chatKey(jid string) (objstore.Key, error) {
	if jid == "" {
		return objstore.Key{}, fmt.Errorf("chat row carries no jid, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: jid}, nil
}

// chatItem reads one chat, if it has ever been seen.
func (s *Store) chatItem(ctx context.Context, jid string) (objstore.Item, bool, error) {
	return s.chatItemVia(ctx, jid)
}

// chatItemVia reads one chat by its jid.
func (s *Store) chatItemVia(ctx context.Context, jid string) (objstore.Item, bool, error) {
	key, err := chatKey(jid)
	if err != nil {
		return objstore.Item{}, false, err
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Chats, Partition: key.Partition,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil {
		return objstore.Item{}, false, fmt.Errorf("read the chat %s: %w", jid, err)
	}
	if len(items) == 0 {
		return objstore.Item{}, false, nil
	}
	return items[0], true, nil
}

// allChats reads the whole partition (file header).
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
// UNGUARDED on purpose: the poll loop advances to the newest row of the batch
// it just fed and nothing else writes this attribute on that path. The guarded
// twin is the restore path, where two cursors genuinely race.
func (s *Store) SetAgentCursor(jid, cursor string) error {
	if err := requireMessageCursor(cursor); err != nil {
		return fmt.Errorf("set the agent cursor of %s: %w", jid, err)
	}
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

// ChatJIDs answers every chat the instance has seen, in jid order. IT IS THE
// PARTITION LIST every whole-history reader walks: PutMessage opens a chat item
// before the first message of a chat, so no message sits in a partition this
// list does not name.
func (s *Store) ChatJIDs(ctx context.Context) ([]string, error) {
	items, err := s.allChats(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Key.Sort)
	}
	sort.Strings(out)
	return out, nil
}

// ProactiveChats answers the chats that have ever carried a real inbound
// message: the universe the proactive scan iterates before it filters by each
// group's mode.
func (s *Store) ProactiveChats(ctx context.Context) ([]string, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Chats, Partition: OwnerRoutd,
		Where:   []objstore.Cmp{{Attr: attrChatHasInbound, Op: objstore.Present}},
		Project: []string{},
	})
	if err != nil {
		return nil, fmt.Errorf("read the proactive chats: %w", err)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Key.Sort)
	}
	sort.Strings(out)
	return out, nil
}

// CountChats answers how many chats the partition holds.
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

// ClearChatErrored takes the flag away. It runs at the clean turn-boundary
// outcome (routd/dispatch.go), NOT at the cursor advance, which the failure
// path also takes. Without a clearer the flag is monotonic for the life of the
// instance (BUGS L304).
func (s *Store) ClearChatErrored(jid string) error {
	key, err := chatKey(jid)
	if err != nil {
		return err
	}
	if _, err := s.upsertChatClearing(context.Background(), key, nil, attrChatErrored); err != nil {
		return fmt.Errorf("clear the errored flag of %s: %w", jid, err)
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

// SetChatIsGroup records whether a chat is multi-party. False REMOVES the
// attribute rather than writing "0".
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
	// A direct chat still OPENS the item: one created here already says
	// "direct", and only an item that was already there has anything to clear.
	created, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Put, Entity: Chats, Key: key, Attrs: map[string]string{},
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil {
		return fmt.Errorf("record %s as a direct chat: %w", jid, err)
	}
	if created {
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

// setChatPin writes one navigation pin; an empty value CLEARS it.
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
// ever recorded about it. Which shape lands is decided by the create's
// REFUSAL, not by a read: a read-then-write is two decisions and the item can
// appear between them. A Put would erase the pins or the cursor.
func (s *Store) upsertChat(ctx context.Context, key objstore.Key,
	attrs map[string]string) (bool, error) {
	return s.upsertChatClearing(ctx, key, attrs)
}

// upsertChatClearing is upsertChat that also takes attributes AWAY. A create
// carries none of them by construction, so only the update half names them.
func (s *Store) upsertChatClearing(ctx context.Context, key objstore.Key,
	attrs map[string]string, remove ...string) (bool, error) {
	created, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Put, Entity: Chats, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil || created {
		return created, err
	}
	if err := s.WriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: Chats, Key: key, Attrs: attrs, Remove: remove,
	}); err != nil {
		return false, err
	}
	return false, nil
}

// clearChatAttrs takes attributes away, and says nothing about a chat that was
// never seen: an Update refuses rather than creating one.
func (s *Store) clearChatAttrs(ctx context.Context, key objstore.Key, attrs ...string) error {
	// A refusal is a chat nobody has seen, which is already clear.
	_, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: Chats, Key: key, Remove: attrs,
	})
	return err
}

// DeriveAgentCursors moves each touched chat's cursor to its newest message, so
// a restored history is not fed to the agent as if it were unread (spec 5/8
// Finding 4). It answers how many cursors moved.
//
// NEVER BACKWARD: the guard is the condition on the write. Each chat's write is
// independent and idempotent, so a run that dies leaves work a re-run
// finishes. ONE BAD CHAT DOES NOT STOP THE REST: the sweep runs every chat it
// can and reports the failures at the end, or the poll loop treats the whole
// remaining history as unread.
func (s *Store) DeriveAgentCursors(ctx context.Context, touched map[string]bool) (int, error) {
	jids := make([]string, 0, len(touched))
	for jid := range touched {
		jids = append(jids, jid)
	}
	sort.Strings(jids)
	moved := 0
	var failed []string
	for _, jid := range jids {
		newest, err := s.newestMessageKey(ctx, jid)
		if err != nil {
			return moved, err
		}
		if newest == "" {
			continue
		}
		key, err := chatKey(jid)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", jid, err))
			continue
		}
		did, err := s.advanceCursor(ctx, Chats, key, AttrChatAgentCursor, newest, nil)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", jid, err))
			continue
		}
		if did {
			moved++
		}
	}
	if len(failed) > 0 {
		return moved, fmt.Errorf("%d of %d chats kept no cursor, so the poll loop reads their "+
			"restored history as unread: %s", len(failed), len(jids), strings.Join(failed, "; "))
	}
	return moved, nil
}
