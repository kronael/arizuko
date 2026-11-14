package store

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
	attrChatErrored       = "errored"
	attrChatIsGroup       = "is_group"
	attrChatThreadReplies = "thread_replies"
	// Exported: the move that carries these rows names the cursor it must NOT
	// re-render, and folder_scope.go reads the group pin and clears both.
	AttrChatAgentCursor = "agent_cursor"
	AttrChatStickyGroup = "sticky_group"
	AttrChatStickyTopic = "sticky_topic"
	attrChatInputFirst  = "input_first"
	attrChatInputLast   = "input_last"
)

const flagSet = "1"

func init() { objstore.OrderedLead(Chats, AttrChatAgentCursor, tsWidth) }

func chatKey(jid string) (objstore.Key, error) {
	if jid == "" {
		return objstore.Key{}, fmt.Errorf("chat row carries no jid, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: jid}, nil
}

func (s *Store) chatItem(ctx context.Context, jid string) (objstore.Item, bool, error) {
	return s.chatItemVia(ctx, jid)
}

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

func (s *Store) AdvanceAgentCursor(jid, cursor string) error {
	if err := requireMessageCursor(cursor); err != nil {
		return err
	}
	key, err := chatKey(jid)
	if err != nil {
		return err
	}

	_, err = s.advanceCursor(context.Background(), Chats, key, AttrChatAgentCursor, cursor, nil)
	return err
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

// InputCursorsBefore answers, jid to agent cursor, the chats whose unconsumed
// user rows may sort at or before `before`, a message cursor: every chat
// holding such a row is in the answer. Timestamp keys select candidates; the
// complete cursor resolves ties after hydration.
func (s *Store) InputCursorsBefore(ctx context.Context, before string) (map[string]string, error) {
	at, _, ok := MsgCoord(before)
	if !ok {
		return nil, fmt.Errorf("invalid input bound %q", before)
	}
	items, err := objstore.IndexItems(ctx, s.Obj(), "chats/input", objstore.KeyPredicate{Op: objstore.LtKey, Value: at + "\x7f"}, 0)
	if err != nil {
		return nil, fmt.Errorf("read the chats holding input: %w", err)
	}
	out := map[string]string{}
	for _, it := range items {
		cursor := it.Attrs[AttrChatAgentCursor]
		if it.Attrs[attrChatInputLast] <= cursor {
			continue
		}
		if max(it.Attrs[attrChatInputFirst], cursor) <= before {
			out[it.Key.Sort] = cursor
		}
	}
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
	if err := s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Merge, Entity: Chats, Key: key,
		Attrs: map[string]string{attrChatErrored: flagSet},
	}); err != nil {
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

// ChatThreadReplies returns the chat's preference or its group-chat default.
func (s *Store) ChatThreadReplies(jid string) (bool, error) {
	it, ok, err := s.chatItem(context.Background(), jid)
	if err != nil || !ok {
		return false, err
	}
	if value, set := it.Attrs[attrChatThreadReplies]; set {
		switch value {
		case "1":
			return true, nil
		case "0":
			return false, nil
		default:
			return false, fmt.Errorf("chat %s has invalid thread_replies %q", jid, value)
		}
	}
	return it.Attrs[attrChatIsGroup] == flagSet, nil
}

// SetChatThreadRepliesInto buffers a preference or clears it to use the default.
func (s *Store) SetChatThreadRepliesInto(ws *WriteSet, jid string, on *bool) error {
	key, err := chatKey(jid)
	if err != nil {
		return err
	}
	write := objstore.Write{Op: objstore.Merge, Entity: Chats, Key: key}
	if on == nil {
		write.Remove = []string{attrChatThreadReplies}
	} else if *on {
		write.Attrs = map[string]string{attrChatThreadReplies: "1"}
	} else {
		write.Attrs = map[string]string{attrChatThreadReplies: "0"}
	}
	ws.Add(write)
	return nil
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

func (s *Store) upsertChat(ctx context.Context, key objstore.Key,
	attrs map[string]string) (bool, error) {
	return s.upsertChatClearing(ctx, key, attrs)
}

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
