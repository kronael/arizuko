package store

// messages.go holds every read and write of `messages`: one item per message
// routd has ever appended, inbound and outbound alike.
//
// The partition is the chat and the sort key `core.TS + KeySep + id`; the
// largest chat partition is 10,688 rows on krons against 24,013 in the table.
// The id is in the key because the instant is not unique: 245 pairs share one
// instant inside one chat on krons, and a time-only key would put two messages
// on one item. The fixed-width instant decides the order and the id breaks the
// tie (objstore.OrderedLead).
//
// THE CURSOR IS A WHOLE KEY: a bare instant sorts BELOW every message at it,
// so `> cursor` would feed a message at exactly the cursor instant once per
// poll, for ever (messages_migrate_test.go).
//
// THE LOG IS APPEND-ORDERED: a live append takes the chat's next position,
// never one at or below the row the chat already ends on (appendInstant). The
// archive's restore writes items directly and is NOT this door.
//
// platform_id stays here, because MessageByID resolves a reply's parent BY it
// inside this partition. `status` and `delivery_attempts` are the mutable half
// and live in message_delivery.go.
//
// ONE WRITE FANS OUT: PutMessage writes this item and the three read-shaped
// indexes in ONE batch, so an index can never hold a message the table does
// not. The fan-out lives with the entity that owns the row.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a message item carries. The chat, the instant and the id are
// the coordinate and are not among them.
const (
	attrMsgSender        = "sender"
	attrMsgSenderName    = "sender_name"
	attrMsgContent       = "content"
	attrMsgFromMe        = "is_from_me"
	attrMsgBot           = "is_bot_message"
	attrMsgForwardedFrom = "forwarded_from"
	attrMsgReplyToID     = "reply_to_id"
	attrMsgReplyToText   = "reply_to_text"
	attrMsgReplyToSender = "reply_to_sender"
	attrMsgTopic         = "topic"
	attrMsgRoutedTo      = "routed_to"
	attrMsgVerb          = "verb"
	attrMsgAttachments   = "attachments"
	attrMsgSource        = "source"
	attrMsgObserved      = "is_observed"
	attrMsgTurnID        = "turn_id"
	attrMsgChatName      = "chat_name"
	attrMsgErrored       = "errored"
	attrMsgLinkContext   = "link_context"
	// AttrMsgPlatformID is the platform's own id for a delivered row. Exported
	// because the delivery sweep stamps it and the archive carries it.
	AttrMsgPlatformID = "platform_id"
)

// The ordered declaration is what makes a message key a position: every driver
// refuses a key whose instant is short (BUGS L97).
func init() { objstore.OrderedLead(Messages, "", tsWidth) }

// MsgStamp assembles a message's position from a rendered instant and its id,
// so every writer of one builds one shape.
func MsgStamp(ts, id string) string { return ts + KeySep + id }

// MsgCoord splits a position back into the instant and the id. A key that does
// not split names an item nobody wrote.
func MsgCoord(sortKey string) (ts, id string, ok bool) {
	return strings.Cut(sortKey, KeySep)
}

// requireMessageCursor refuses a cursor that is not a whole message position.
// objstore's ordered LEAD accepts a value of exactly the instant's width on
// purpose (a query BOUND is one), so the entity is what refuses a bare instant
// as a stored CURSOR.
func requireMessageCursor(cursor string) error {
	if len(cursor) <= tsWidth || cursor[tsWidth] != KeySep[0] {
		return fmt.Errorf("%q is not a message position: a cursor carries the instant, the "+
			"separator and the id, and a bare instant sorts below every message at it, so the "+
			"poll loop would feed that instant again on every tick", cursor)
	}
	return nil
}

// MessageCursor is the position a consumer records after it has taken a
// message: the message's own key.
func MessageCursor(m core.Message) string { return MsgStamp(core.TS(m.Timestamp), m.ID) }

// CursorInstant reads the instant out of a cursor, for the readers that ask
// when rather than where.
func CursorInstant(cursor string) (time.Time, bool) {
	ts, _, ok := MsgCoord(cursor)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	return t, err == nil
}

// msgStampAt is the bound that sorts at the START of an instant: below every
// message key at it. `timestamp < t` and `timestamp >= t` compare against it.
func msgStampAt(t time.Time) string { return core.TS(t) }

// msgStampPast is the bound that sorts just PAST an instant: above every
// message key at it. `timestamp > t` and `timestamp <= t` compare against it.
// Derived from KeySep so the two cannot drift by one byte.
func msgStampPast(t time.Time) string { return core.TS(t) + string(KeySep[0]+1) }

// MessageKey is the coordinate of one message, built from the values a caller
// holds rather than from an id alone; every caller in the tree holds the chat
// too.
func MessageKey(chatJID, ts, id string) (objstore.Key, error) {
	if chatJID == "" {
		return objstore.Key{}, fmt.Errorf("message %q carries no chat, which is its "+
			"partition", id)
	}
	if len(ts) != tsWidth {
		return objstore.Key{}, fmt.Errorf("message %q carries the instant %q, which is %d "+
			"bytes and a position's instant is %d; the chat is read in time order, so a short "+
			"one sorts into the wrong place instead of failing (BUGS L97)", id, ts, len(ts), tsWidth)
	}
	if id == "" {
		return objstore.Key{}, fmt.Errorf("message at %s carries no id, and the id is what "+
			"keeps two messages of one instant apart", ts)
	}
	if strings.Contains(id, KeySep) {
		return objstore.Key{}, fmt.Errorf("message id %q carries the separator that divides "+
			"the two halves of its position", id)
	}
	return objstore.Key{Partition: chatJID, Sort: MsgStamp(ts, id)}, nil
}

// messageKeyOf is the coordinate of a message a caller already holds.
func messageKeyOf(m core.Message) (objstore.Key, error) {
	return MessageKey(m.ChatJID, core.TS(m.Timestamp), m.ID)
}

// messageAttrs renders one message as the attributes its item carries. An empty
// value is LEFT OFF: absent is the only null this model has.
func messageAttrs(m core.Message) map[string]string {
	attrs := map[string]string{}
	setAttr(attrs, attrMsgSender, m.Sender)
	setAttr(attrs, attrMsgSenderName, m.Name)
	setAttr(attrs, attrMsgContent, m.Content)
	setAttr(attrs, attrMsgForwardedFrom, m.ForwardedFrom)
	setAttr(attrs, attrMsgReplyToID, m.ReplyToID)
	setAttr(attrs, attrMsgReplyToText, m.ReplyToText)
	setAttr(attrs, attrMsgReplyToSender, m.ReplyToSender)
	setAttr(attrs, attrMsgTopic, m.Topic)
	setAttr(attrs, attrMsgRoutedTo, m.RoutedTo)
	setAttr(attrs, attrMsgVerb, defaultVerb(m.Verb))
	setAttr(attrs, attrMsgAttachments, m.Attachments)
	setAttr(attrs, attrMsgSource, m.Source)
	setAttr(attrs, attrMsgTurnID, m.TurnID)
	setAttr(attrs, attrMsgChatName, m.ChatName)
	setAttr(attrs, attrMsgLinkContext, m.LinkContext)
	setAttr(attrs, AttrMsgPlatformID, m.PlatformID)
	setFlag(attrs, attrMsgFromMe, m.FromMe)
	setFlag(attrs, attrMsgBot, m.BotMsg)
	setFlag(attrs, attrMsgErrored, m.Errored)
	return attrs
}

// messageFrom reads one item back. The instant comes out of the COORDINATE. A
// key this cannot split still yields a message with a visible zero time,
// rather than dropping a row out of a feed.
func messageFrom(it objstore.Item) core.Message {
	ts, id, _ := MsgCoord(it.Key.Sort)
	m := core.Message{
		ID:            id,
		ChatJID:       it.Key.Partition,
		Sender:        it.Attrs[attrMsgSender],
		Name:          it.Attrs[attrMsgSenderName],
		Content:       it.Attrs[attrMsgContent],
		ForwardedFrom: it.Attrs[attrMsgForwardedFrom],
		ReplyToID:     it.Attrs[attrMsgReplyToID],
		ReplyToText:   it.Attrs[attrMsgReplyToText],
		ReplyToSender: it.Attrs[attrMsgReplyToSender],
		Topic:         it.Attrs[attrMsgTopic],
		RoutedTo:      it.Attrs[attrMsgRoutedTo],
		Verb:          it.Attrs[attrMsgVerb],
		Attachments:   it.Attrs[attrMsgAttachments],
		Source:        it.Attrs[attrMsgSource],
		TurnID:        it.Attrs[attrMsgTurnID],
		ChatName:      it.Attrs[attrMsgChatName],
		LinkContext:   it.Attrs[attrMsgLinkContext],
		PlatformID:    it.Attrs[AttrMsgPlatformID],
		FromMe:        it.Attrs[attrMsgFromMe] == flagSet,
		BotMsg:        it.Attrs[attrMsgBot] == flagSet,
		Errored:       it.Attrs[attrMsgErrored] == flagSet,
	}
	m.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	return m
}

// messagesFrom reads a query's items back in the order the driver returned them.
func messagesFrom(items []objstore.Item) []core.Message {
	out := make([]core.Message, 0, len(items))
	for _, it := range items {
		out = append(out, messageFrom(it))
	}
	return out
}

// setFlag records a true flag and leaves a false one OFF the item: a stored
// "0" is a flag every reader finds set.
func setFlag(attrs map[string]string, name string, on bool) {
	if on {
		attrs[name] = flagSet
	}
}

// defaultVerb is the event type a message with none carries.
func defaultVerb(v string) string {
	if v == "" {
		return "message"
	}
	return v
}

// PutMessage appends one message and the index entries it belongs in, in ONE
// batch. routd is the only writer. A DUPLICATE ID IS A NO-OP: the first row is
// authoritative. The condition is on the message item, and a refusal takes the
// indexes with it, so a re-delivered message cannot double-count a folder.
func (s *Store) PutMessage(m core.Message) error {
	_, err := s.AppendMessage(m)
	return err
}

// AppendMessage is PutMessage answering the row AS STORED: appendInstant moves
// the instant when the caller's own would bury the row, and a caller that
// addresses the row afterwards has to hold the one the append took.
func (s *Store) AppendMessage(m core.Message) (core.Message, error) {
	return s.putMessage(context.Background(), m, core.MessageStatusSent)
}

// putMessage is PutMessage's body. The status is a parameter because it is not
// stored on the message: it decides whether a delivery item exists at all.
// `also` rides the message's own batch (FireProactive's cooldown).
func (s *Store) putMessage(ctx context.Context, m core.Message, status string,
	also ...objstore.Write) (core.Message, error) {
	if m.Timestamp.IsZero() {
		// The writer stamps the instant; this refuses to invent one, or the
		// caller walks away holding a message whose position does not exist.
		return m, fmt.Errorf("message %s in %s carries no instant, and the instant is half its "+
			"position", m.ID, m.ChatJID)
	}
	// THE READ IS FOR THE STICKY PIN, and nothing else.
	chat, _, err := s.chatItemVia(ctx, m.ChatJID)
	if err != nil {
		return m, err
	}
	if m.Topic == "" {
		m.Topic = chat.Attrs[AttrChatStickyTopic]
	}
	chatKey, err := chatKey(m.ChatJID)
	if err != nil {
		return m, err
	}
	m.Timestamp, err = s.appendInstant(ctx, m)
	if err != nil {
		return m, err
	}
	key, err := messageKeyOf(m)
	if err != nil {
		return m, err
	}
	// ONE Merge opens the chat and flags it: `chats` is the partition list the
	// poll loop and the archive walk, and a real inbound makes the chat
	// proactive-eligible. A Merge, because a create-if-absent REFUSES on the
	// chat's second message and would take the whole batch with it.
	chatFlags := map[string]string{}
	if isRealInbound(m) {
		chatFlags[attrChatHasInbound] = flagSet
	}
	writes := []objstore.Write{{
		Op: objstore.Merge, Entity: Chats, Key: chatKey, Attrs: chatFlags,
	}, {
		Op: objstore.Put, Entity: Messages, Key: key, Attrs: messageAttrs(m),
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}}
	writes = append(writes, activityWrite(key, m))
	if w, ok := deliveryWrite(key, m, status, 0); ok {
		writes = append(writes, w)
	}
	folderWrite, counted, err := folderFeedWrite(key, m)
	if err != nil {
		return m, err
	}
	writes = append(writes, also...)
	if !counted {
		// No routed_to: no folder feed entry and no counter to move.
		_, err := s.TryWriteEntities(ctx, nil, writes...)
		return m, err
	}
	return m, s.countFolderMessage(ctx, m.RoutedTo, append(writes, folderWrite))
}

// appendInstant answers the instant a live append takes: the writer's own, or
// one nanosecond past the chat's newest row when the writer's own would put
// the new row at or below it.
//
// THE DISPATCH CURSOR IS A HIGH-WATER MARK, so a row written BELOW it is never
// fed. A steer re-injection carrying the host clock landed behind a platform
// second three ahead, with no ack and a leaked elevation (BUGS L102); a row of
// the same second as the batch tail with a lesser id lost eleven of twelve
// documents (BUGS L103). One floor answers both, the chat's newest position
// rather than its cursor, because the cursor has not moved when a steer
// re-injects. The cost is one key-only read; the window left is microseconds.
// A clamped row is still the caller's row: only the instant moves, by a
// nanosecond from a value the platform had already rounded to a second.
func (s *Store) appendInstant(ctx context.Context, m core.Message) (time.Time, error) {
	own := MsgStamp(core.TS(m.Timestamp), m.ID)
	floor, err := s.newestMessageKey(ctx, m.ChatJID)
	if err != nil {
		return time.Time{}, err
	}
	if floor == "" || own > floor {
		return m.Timestamp, nil
	}
	// A redelivery keeps its stored position so the Put's ItemAbsent condition
	// refuses the whole batch, including its indexes.
	held, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: m.ChatJID, Project: []string{},
		Key: objstore.KeyPredicate{Op: objstore.GeKey, Value: msgStampAt(m.Timestamp)},
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("read %s in %s: %w", own, m.ChatJID, err)
	}
	for _, it := range held {
		if at, id, ok := MsgCoord(it.Key.Sort); ok && id == m.ID {
			return time.Parse(time.RFC3339Nano, at)
		}
	}
	at, _, ok := MsgCoord(floor)
	if !ok {
		return time.Time{}, fmt.Errorf("%s ends on %q, which is not a message position, so "+
			"there is no instant to append past", m.ChatJID, floor)
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s ends at %q, which is not an instant: %w",
			m.ChatJID, at, err)
	}
	return t.Add(time.Nanosecond), nil
}

// ProactiveSelfSender is the synthetic inbound's sender. Rows from it never
// reset the silence clock and never count as recent activity: the trigger must
// not feed itself. routd's scan reads the same name.
const ProactiveSelfSender = "timed-proactive"

// The senders arizuko writes on its own behalf. A person's sender is always
// platform-qualified; these are the platform addressing itself.
const (
	// SystemSender writes the auto-migrate prompt (routd/loop.go).
	SystemSender = "system"
	// EscalateSender and DelegateSender write the agent-to-agent hand-offs.
	EscalateSender = "escalate"
	DelegateSender = "delegate"
)

// SyntheticSenders is the whole set, declared once beside the writer. The
// answering read (routd/answering.go) is its reader: a migration storm's 55
// `system` prompts would otherwise be 55 people waiting.
var SyntheticSenders = []string{
	ProactiveSelfSender, SystemSender, EscalateSender, DelegateSender,
}

// isRealInbound reports whether a message is traffic a person sent.
func isRealInbound(m core.Message) bool {
	return !m.BotMsg && m.Sender != ProactiveSelfSender
}

// EnrichMessage replaces a message's content with the media-enriched body and
// clears its attachments, so the observed-context render on later turns sees
// the transcript rather than the raw refs.
func (s *Store) EnrichMessage(m core.Message, content string) error {
	key, err := messageKeyOf(m)
	if err != nil {
		return err
	}
	// A message nobody appended is nothing to enrich: the Update refuses
	// rather than creating a half-built row.
	_, err = s.TryWriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: Messages, Key: key,
		Attrs:  map[string]string{attrMsgContent: content},
		Remove: []string{attrMsgAttachments},
	})
	if err != nil {
		return fmt.Errorf("enrich %s: %w", m.ID, err)
	}
	return nil
}

// NewMessagesForChat answers a chat's messages after its own watermark, oldest
// first: the poll loop's feed. The cursor is a whole message key and the read
// is `> cursor`. An empty cursor feeds the chat from the start.
func (s *Store) NewMessagesForChat(ctx context.Context, jid, cursor string,
	limit int) ([]core.Message, error) {
	key := objstore.KeyPredicate{}
	if cursor != "" {
		key = objstore.KeyPredicate{Op: objstore.GtKey, Value: cursor}
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: jid, Key: key, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("read new messages in %s: %w", jid, err)
	}
	return messagesFrom(items), nil
}

// MessagesSince answers one chat's messages strictly after an instant, oldest
// first.
func (s *Store) MessagesSince(jid string, since time.Time) ([]core.Message, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Messages, Partition: jid,
		Key: objstore.KeyPredicate{Op: objstore.GeKey, Value: msgStampPast(since)},
	})
	if err != nil {
		return nil, fmt.Errorf("read %s since %s: %w", jid, core.TS(since), err)
	}
	return messagesFrom(items), nil
}

// MessagesBefore answers one chat's messages older than an instant, oldest
// first: the page is read newest-first from the end and then reversed.
func (s *Store) MessagesBefore(jid string, before time.Time, limit int) ([]core.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if before.IsZero() {
		before = time.Now()
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Messages, Partition: jid,
		Key:        objstore.KeyPredicate{Op: objstore.LtKey, Value: msgStampAt(before)},
		Descending: true, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("read %s before %s: %w", jid, core.TS(before), err)
	}
	return reversed(messagesFrom(items)), nil
}

// MessagesByThread answers one (chat, topic)'s messages older than an instant,
// NEWEST first: the order the web transcript pages backwards in.
func (s *Store) MessagesByThread(jid, topic string, before time.Time,
	limit int) ([]core.Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if before.IsZero() {
		before = time.Now()
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Messages, Partition: jid,
		Key:        objstore.KeyPredicate{Op: objstore.LtKey, Value: msgStampAt(before)},
		Where:      []objstore.Cmp{{Attr: attrMsgTopic, Op: objstore.Eq, Value: topic}},
		Descending: true, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("read %s#%s before %s: %w", jid, topic, core.TS(before), err)
	}
	return messagesFrom(items), nil
}

// webChatJID is the chat a folder's web panel writes into.
func webChatJID(folder string) string { return groupfolder.WebJID(folder) }

// MessagesByTopic is MessagesByThread on a folder's web chat.
func (s *Store) MessagesByTopic(folder, topic string, before time.Time,
	limit int) ([]core.Message, error) {
	return s.MessagesByThread(webChatJID(folder), topic, before, limit)
}

// MessagesSinceTopic answers one (chat, topic)'s messages strictly after an
// instant, oldest first.
func (s *Store) MessagesSinceTopic(jid, topic string, after time.Time,
	limit int) ([]core.Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Messages, Partition: jid,
		Key:   objstore.KeyPredicate{Op: objstore.GeKey, Value: msgStampPast(after)},
		Where: []objstore.Cmp{{Attr: attrMsgTopic, Op: objstore.Eq, Value: topic}},
		Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("read %s#%s since %s: %w", jid, topic, core.TS(after), err)
	}
	return messagesFrom(items), nil
}

// BotRepliesSince answers the agent's outbound rows on a chat after an instant,
// oldest first, for `arizuko send`.
func (s *Store) BotRepliesSince(jid string, since time.Time, limit int) ([]core.Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Messages, Partition: jid,
		Key:   objstore.KeyPredicate{Op: objstore.GeKey, Value: msgStampPast(since)},
		Where: []objstore.Cmp{{Attr: attrMsgBot, Op: objstore.Present}},
		Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("read %s's bot replies since %s: %w", jid, core.TS(since), err)
	}
	return messagesFrom(items), nil
}

// reversed flips a page read newest-first into chronological order.
func reversed(msgs []core.Message) []core.Message {
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs
}

// TopicSummary is one thread of a folder's web chat, as the topic list renders
// it.
type TopicSummary struct {
	ID           string
	Preview      string
	LastAt       time.Time
	MessageCount int
}

// topicPreviewChars is how much of a topic's opening line the list shows.
const topicPreviewChars = 80

// maxTopics bounds the topic list.
const maxTopics = 100

// Topics answers a folder's web-chat threads, most recently active first. The
// grouping is the reader's; a web chat is one chat, which bounds it.
func (s *Store) Topics(folder string) ([]TopicSummary, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Messages, Partition: webChatJID(folder),
	})
	if err != nil {
		return nil, fmt.Errorf("read %s's topics: %w", folder, err)
	}
	byTopic := map[string]*TopicSummary{}
	for _, it := range items {
		topic := it.Attrs[attrMsgTopic]
		if topic == "" {
			continue
		}
		t, seen := byTopic[topic]
		if !seen {
			t = &TopicSummary{ID: topic}
			byTopic[topic] = t
		}
		t.MessageCount++
		// The preview is the alphabetically first content; the read arrives in
		// key order, so the last row of the partition is the newest.
		if body := it.Attrs[attrMsgContent]; !seen || body < t.Preview {
			t.Preview = body
		}
		if ts, _, ok := MsgCoord(it.Key.Sort); ok {
			t.LastAt, _ = time.Parse(time.RFC3339Nano, ts)
		}
	}
	out := make([]TopicSummary, 0, len(byTopic))
	for _, t := range byTopic {
		t.Preview = truncateRunes(t.Preview, topicPreviewChars)
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	if len(out) > maxTopics {
		out = out[:maxTopics]
	}
	return out, nil
}

// truncateRunes cuts a string to n characters.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// messageAt reads one message of a chat by its id. The id is the TAIL of the
// key, so this is a partition read with the id compared in the reader; the
// chat bounds it at 10,688 on krons.
func (s *Store) messageAt(ctx context.Context, jid, id string) (objstore.Item, bool, error) {
	if jid == "" || id == "" {
		return objstore.Item{}, false, nil
	}
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: Messages, Partition: jid})
	if err != nil {
		return objstore.Item{}, false, fmt.Errorf("read %s in %s: %w", id, jid, err)
	}
	for _, it := range items {
		if _, got, ok := MsgCoord(it.Key.Sort); ok && got == id {
			return it, true, nil
		}
	}
	return objstore.Item{}, false, nil
}

// MessageByID answers one message of a chat named by its OWN id or by the
// platform's id for it. THE CHAT BOUND IS NOT OPTIONAL: `platform_id` is a
// per-platform value, so an unscoped lookup resolves a reply id to a row in
// another folder and the caller copies that row's sender, text and topic. The
// partition makes that impossible rather than checked.
func (s *Store) MessageByID(chatJID, id string) (core.Message, bool) {
	if chatJID == "" || id == "" {
		return core.Message{}, false
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Messages, Partition: chatJID,
	})
	if err != nil {
		return core.Message{}, false
	}
	bare := strings.TrimPrefix(id, chatJID+"/")
	for _, it := range items {
		_, own, ok := MsgCoord(it.Key.Sort)
		if ok && own == id {
			return messageFrom(it), true
		}
	}
	for _, it := range items {
		if p := it.Attrs[AttrMsgPlatformID]; p != "" && (p == id || p == bare) {
			return messageFrom(it), true
		}
	}
	return core.Message{}, false
}

// TopicByMessageID answers the topic of one message, so a reply to a threaded
// row forks lineage from the parent's topic rather than from the main one.
func (s *Store) TopicByMessageID(id, jid string) string {
	it, ok, err := s.messageAt(context.Background(), jid, id)
	if err != nil || !ok {
		return ""
	}
	return it.Attrs[attrMsgTopic]
}

// MessageTimestampByID answers when one message of a chat was appended.
func (s *Store) MessageTimestampByID(id, jid string) (time.Time, bool) {
	it, ok, err := s.messageAt(context.Background(), jid, id)
	if err != nil || !ok {
		return time.Time{}, false
	}
	ts, _, split := MsgCoord(it.Key.Sort)
	if !split {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	return t, err == nil
}

// RoutedToByMessageID answers the folder one message of a chat was routed to,
// so a reply to a bot row goes back to the child that authored it. Chat-scoped:
// a reply names a message in the chat it is sent to, so the parent is in this
// partition by construction (MessageByID says what an unscoped read costs).
func (s *Store) RoutedToByMessageID(chatJID, id string) string {
	m, ok := s.MessageByID(chatJID, id)
	if !ok {
		return ""
	}
	return m.RoutedTo
}

// MarkMessagesErrored flags a failed run's trigger rows so the circuit breaker
// can prune them. The rows stay visible to a later successful run until it
// trips.
func (s *Store) MarkMessagesErrored(ctx context.Context, msgs []core.Message) error {
	writes := make([]objstore.Write, 0, len(msgs))
	for _, m := range msgs {
		key, err := messageKeyOf(m)
		if err != nil {
			return err
		}
		writes = append(writes, objstore.Write{
			Op: objstore.Update, Entity: Messages, Key: key,
			Attrs: map[string]string{attrMsgErrored: flagSet},
		})
	}
	if len(writes) == 0 {
		return nil
	}
	_, err := s.applyInChunks(ctx, "flag messages errored", writes)
	return err
}

// erroredItems reads one chat's flagged rows.
func (s *Store) erroredItems(ctx context.Context, chatJID string) ([]objstore.Item, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: chatJID,
		Where: []objstore.Cmp{{Attr: attrMsgErrored, Op: objstore.Present}},
	})
	if err != nil {
		return nil, fmt.Errorf("read %s's errored messages: %w", chatJID, err)
	}
	return items, nil
}

// ClearErrored un-flags a chat's errored rows so the dispatch loop feeds them
// again: the operator's "retry this chat". It answers how many rows moved, so
// a caller can refuse to report success on zero. An entity has no predicate
// update, so the rows are read and then written one each.
func (s *Store) ClearErrored(ctx context.Context, chatJID string) (int64, error) {
	items, err := s.erroredItems(ctx, chatJID)
	if err != nil {
		return 0, err
	}
	writes := make([]objstore.Write, 0, len(items))
	for _, it := range items {
		writes = append(writes, objstore.Write{
			Op: objstore.Update, Entity: Messages, Key: it.Key,
			Remove: []string{attrMsgErrored},
		})
	}
	if len(writes) == 0 {
		return 0, nil
	}
	n, err := s.applyInChunks(ctx, "clear "+chatJID+"'s errored messages", writes)
	return int64(n), err
}

// DeleteErroredMessages drops a chat's errored rows and the index entries that
// point at them, so the next inbound starts the chat from a clean batch. It
// answers the positions it deleted, because a caller holding an index DERIVED
// from these rows has no other way to learn they are gone (routd/msgsearch).
func (s *Store) DeleteErroredMessages(ctx context.Context, chatJID string) ([]string, error) {
	items, err := s.erroredItems(ctx, chatJID)
	if err != nil {
		return nil, err
	}
	var writes []objstore.Write
	gone := make([]string, 0, len(items))
	for _, it := range items {
		writes = append(writes, objstore.Write{Op: objstore.Delete, Entity: Messages, Key: it.Key})
		writes = append(writes, indexDeletes(it)...)
		gone = append(gone, it.Key.Sort)
	}
	if len(writes) == 0 {
		return nil, nil
	}
	if _, err := s.applyInChunks(ctx, "delete "+chatJID+"'s errored messages", writes); err != nil {
		return nil, err
	}
	return gone, nil
}

// TurnFrames answers the bot rows one turn stamped, oldest first, paged by the
// message a caller last saw: the key IS the (timestamp, id) comparison. An
// anchor this chat does not hold feeds from the start. chatJID binds the turn
// to its chat: a turn id alone is guessable and is not proof of ownership, so
// containment is the partition rather than a check in each caller.
func (s *Store) TurnFrames(chatJID, turnID, afterID string, limit int) ([]core.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	if chatJID == "" {
		return nil, nil
	}
	ctx := context.Background()
	key := objstore.KeyPredicate{}
	if afterID != "" {
		if it, ok, err := s.messageAt(ctx, chatJID, afterID); err == nil && ok {
			key = objstore.KeyPredicate{Op: objstore.GtKey, Value: it.Key.Sort}
		}
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: chatJID, Key: key,
		Where: []objstore.Cmp{
			{Attr: attrMsgTurnID, Op: objstore.Eq, Value: turnID},
			{Attr: attrMsgBot, Op: objstore.Present},
		},
		Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("read turn %s's frames in %s: %w", turnID, chatJID, err)
	}
	return messagesFrom(items), nil
}

// TurnHasBotReply accepts sent rows and router-owned pending deliveries.
// Failed attempts and interim status rows cannot satisfy the turn's reply.
func (s *Store) TurnHasBotReply(chatJID, turnID string) bool {
	ctx := context.Background()
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: chatJID,
		Where: []objstore.Cmp{
			{Attr: attrMsgTurnID, Op: objstore.Eq, Value: turnID},
			{Attr: attrMsgBot, Op: objstore.Present},
			{Attr: attrMsgVerb, Op: objstore.Ne, Value: verbStatus},
			{Attr: attrMsgVerb, Op: objstore.Ne, Value: VerbStatusSummary},
		},
	})
	if err != nil {
		return false
	}
	for _, item := range items {
		_, failed, err := s.itemAt(ctx, MessageDelivery, deliveryKey(core.MessageStatusFailed, item.Key))
		if err != nil {
			return false
		}
		if !failed {
			return true
		}
	}
	return false
}

// verbStatus is the verb of a turn's progress row (routd/progress.go).
const verbStatus = "status"

const VerbStatusSummary = "status_summary"

// LastStatusRow answers the newest status row of one turn, delivered or not.
// A row whose first send is still owed has no platform id yet, and the caller
// has to find it all the same, or it would write a second row for the turn.
func (s *Store) LastStatusRow(chatJID, turnID string) (core.Message, bool) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Messages, Partition: chatJID,
		Where: []objstore.Cmp{
			{Attr: attrMsgTurnID, Op: objstore.Eq, Value: turnID},
			{Attr: attrMsgVerb, Op: objstore.Eq, Value: verbStatus},
		},
		Descending: true, Limit: 1,
	})
	if err != nil || len(items) == 0 {
		return core.Message{}, false
	}
	return messageFrom(items[0]), true
}

// SetMessageContent rewrites the content of a message the caller holds, and
// the feed preview cut from it, in one batch: a row that says one thing while
// its preview says another is two answers to "what was sent". A message
// nobody appended is refused.
func (s *Store) SetMessageContent(m core.Message, content string) error {
	key, err := messageKeyOf(m)
	if err != nil {
		return err
	}
	err = s.WriteEntities(context.Background(), nil,
		objstore.Write{
			Op: objstore.Update, Entity: Messages, Key: key,
			Attrs: map[string]string{attrMsgContent: content},
		},
		objstore.Write{
			Op: objstore.Update, Entity: MessageActivity, Key: activityKey(key),
			Attrs: map[string]string{attrActPreview: truncateRunes(content, activityPreviewChars)},
		})
	if err != nil {
		return fmt.Errorf("rewrite %s: %w", m.ID, err)
	}
	return nil
}

// newestMessageKey answers the position of the newest message a chat holds, or
// empty when it holds none. A KEY rather than an instant, because that is what
// the cursor holds.
func (s *Store) newestMessageKey(ctx context.Context, jid string) (string, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: jid, Descending: true, Limit: 1, Project: []string{},
	})
	if err != nil {
		return "", fmt.Errorf("newest message in %s: %w", jid, err)
	}
	if len(items) == 0 {
		return "", nil
	}
	return items[0].Key.Sort, nil
}

// --- the proactive scan's per-chat reads (spec 5/26) ---------------------
//
// Each is a bounded read of ONE chat, because the scan already holds the chat
// it is deciding about.

// LastInboundAt answers when a chat last carried a real inbound message, or the
// zero time when it never has. It is the silence-debounce clock.
func (s *Store) LastInboundAt(ctx context.Context, jid string) time.Time {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: jid,
		Where:      realInboundFilter(),
		Descending: true, Limit: 1, Project: []string{},
	})
	if err != nil || len(items) == 0 {
		return time.Time{}
	}
	ts, _, _ := MsgCoord(items[0].Key.Sort)
	t, _ := time.Parse(time.RFC3339Nano, ts)
	return t
}

// LastInbound answers the newest real inbound row of a chat, its body and its
// instant, which the positive signals read. ok=false when the chat has none.
func (s *Store) LastInbound(ctx context.Context, jid string) (string, time.Time, bool) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: jid,
		Where:      realInboundFilter(),
		Descending: true, Limit: 1,
	})
	if err != nil || len(items) == 0 {
		return "", time.Time{}, false
	}
	m := messageFrom(items[0])
	return m.Content, m.Timestamp, true
}

// LatestSource answers which adapter delivered the newest sourced inbound row
// of a chat, so the Deliverer sends an outbound back the way the conversation
// came in. Empty when the chat has none.
func (s *Store) LatestSource(ctx context.Context, jid string) string {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: jid,
		Where: []objstore.Cmp{
			{Attr: attrMsgBot, Op: objstore.Absent},
			{Attr: attrMsgSource, Op: objstore.Present},
		},
		Descending: true, Limit: 1, Project: []string{attrMsgSource},
	})
	if err != nil || len(items) == 0 {
		return ""
	}
	return items[0].Attrs[attrMsgSource]
}

// BotSpokeSince reports whether a chat carries a bot row at or after an
// instant: the BotQuiet veto.
func (s *Store) BotSpokeSince(ctx context.Context, jid string, since time.Time) bool {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: jid,
		Key:     objstore.KeyPredicate{Op: objstore.GeKey, Value: msgStampAt(since)},
		Where:   []objstore.Cmp{{Attr: attrMsgBot, Op: objstore.Present}},
		Limit:   1,
		Project: []string{},
	})
	return err == nil && len(items) > 0
}

// InboundCountSince counts a chat's real inbound rows at or after an instant:
// the RecentActivity floor.
func (s *Store) InboundCountSince(ctx context.Context, jid string, since time.Time) int {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: jid,
		Key:     objstore.KeyPredicate{Op: objstore.GeKey, Value: msgStampAt(since)},
		Where:   realInboundFilter(),
		Project: []string{},
	})
	if err != nil {
		return 0
	}
	return len(items)
}

// realInboundFilter is the "traffic a person sent" predicate, written once so
// the four reads that ask it cannot disagree.
func realInboundFilter() []objstore.Cmp {
	return []objstore.Cmp{
		{Attr: attrMsgBot, Op: objstore.Absent},
		{Attr: attrMsgSender, Op: objstore.Ne, Value: ProactiveSelfSender},
	}
}

// FireProactive appends the synthetic inbound row AND sets the chat's proactive
// cooldown in one batch, then answers the row's id, which is the turn id. The
// caller dispatches only after this commits, so a crash before dispatch leaves
// the cooldown set: at worst one missed turn, never a double fire.
func (s *Store) FireProactive(ctx context.Context, jid, id string) error {
	firedAt := time.Now()
	cooldown, err := proactiveFireWrite(jid, firedAt)
	if err != nil {
		return fmt.Errorf("fire a proactive turn on %s: %w", jid, err)
	}
	row := core.Message{
		ID: id, ChatJID: jid, Sender: ProactiveSelfSender, Timestamp: firedAt,
		Verb: "message",
	}
	if _, err := s.putMessage(ctx, row, core.MessageStatusSent, cooldown); err != nil {
		return fmt.Errorf("record the proactive trigger on %s: %w", jid, err)
	}
	return nil
}

// CountMessages answers how many messages every chat holds.
func (s *Store) CountMessages(ctx context.Context) (int, error) {
	jids, err := s.ChatJIDs(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, jid := range jids {
		items, err := s.Obj().Query(ctx, objstore.Query{
			Entity: Messages, Partition: jid, Project: []string{},
		})
		if err != nil {
			return 0, fmt.Errorf("count %s's messages: %w", jid, err)
		}
		n += len(items)
	}
	return n, nil
}

// ChatActivity answers how many inbound rows a chat holds and when its newest
// row of any kind landed: the two numbers an errored-chat listing shows beside
// each chat.
func (s *Store) ChatActivity(ctx context.Context, jid string) (int, time.Time, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: jid, Project: []string{attrMsgFromMe},
	})
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("read %s's activity: %w", jid, err)
	}
	inbound := 0
	for _, it := range items {
		if it.Attrs[attrMsgFromMe] != flagSet {
			inbound++
		}
	}
	var last time.Time
	if len(items) > 0 {
		ts, _, _ := MsgCoord(items[len(items)-1].Key.Sort)
		last, _ = time.Parse(time.RFC3339Nano, ts)
	}
	return inbound, last, nil
}

// InboundChatSummary answers every chat's inbound human traffic: how many rows,
// and the first and last instant. It is the held-set page's candidate list, and
// it walks the partition list, `chats` (136 items on krons).
func (s *Store) InboundChatSummary(ctx context.Context) ([]ChatSummary, error) {
	jids, err := s.ChatJIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ChatSummary, 0, len(jids))
	for _, jid := range jids {
		items, err := s.Obj().Query(ctx, objstore.Query{
			Entity: Messages, Partition: jid,
			Where: []objstore.Cmp{
				{Attr: attrMsgFromMe, Op: objstore.Absent},
				{Attr: attrMsgBot, Op: objstore.Absent},
			},
			Project: []string{},
		})
		if err != nil {
			return nil, fmt.Errorf("read %s's inbound rows: %w", jid, err)
		}
		if len(items) == 0 {
			continue
		}
		c := ChatSummary{ChatJID: jid, Count: len(items)}
		first, _, _ := MsgCoord(items[0].Key.Sort)
		last, _, _ := MsgCoord(items[len(items)-1].Key.Sort)
		c.FirstAt, _ = time.Parse(time.RFC3339Nano, first)
		c.LastAt, _ = time.Parse(time.RFC3339Nano, last)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out, nil
}

// ChatSummary is one chat's inbound traffic.
type ChatSummary struct {
	ChatJID string
	Count   int
	FirstAt time.Time
	LastAt  time.Time
}

// lastMessageWhere answers a chat's newest row matching `where`, and whether
// there is one; `what` names the row for the error a fault carries. ONE read
// for the three questions below, so the "no row is not a fault" contract
// cannot drift.
func (s *Store) lastMessageWhere(ctx context.Context, jid, what string,
	where []objstore.Cmp) (core.Message, bool, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: jid,
		Where: where, Descending: true, Limit: 1,
	})
	if err != nil {
		return core.Message{}, false, fmt.Errorf("read %s's newest %s row: %w", jid, what, err)
	}
	if len(items) == 0 {
		return core.Message{}, false, nil
	}
	return messageFrom(items[0]), true, nil
}

// inboundWhere selects the rows neither the agent nor the operator's own device
// wrote: the traffic the loop resolves.
func inboundWhere() []objstore.Cmp {
	return []objstore.Cmp{
		{Attr: attrMsgFromMe, Op: objstore.Absent},
		{Attr: attrMsgBot, Op: objstore.Absent},
	}
}

// LastMessage returns a chat's newest row, including future timestamps.
func (s *Store) LastMessage(ctx context.Context, jid string) (core.Message, bool, error) {
	return s.lastMessageWhere(ctx, jid, "message", nil)
}

// LastInboundMessage answers a chat's newest inbound human row: the message the
// loop resolved last, and the one a held-set reader asks resolve about again.
func (s *Store) LastInboundMessage(ctx context.Context, jid string) (core.Message, bool, error) {
	return s.lastMessageWhere(ctx, jid, "inbound", inboundWhere())
}

func (s *Store) ChatJIDsForSender(ctx context.Context, sender string) ([]string, error) {
	if sender == "" {
		return nil, nil
	}
	jids, err := s.ChatJIDs(ctx)
	if err != nil {
		return nil, err
	}
	where := append(inboundWhere(), objstore.Cmp{
		Attr: attrMsgSender, Op: objstore.Eq, Value: sender,
	})
	var chats []string
	for _, jid := range jids {
		_, found, err := s.lastMessageWhere(ctx, jid, "sender's inbound", where)
		if err != nil {
			return nil, err
		}
		if found {
			chats = append(chats, jid)
		}
	}
	return chats, nil
}

// LastHumanMessage answers a chat's newest row a PERSON wrote: inbound, and
// from none of the senders arizuko writes on its own behalf. Narrower than
// LastInboundMessage on purpose: the held set asks which message the loop
// resolved, and a `system` auto-migrate prompt is one; the answering read asks
// who is waiting, and that prompt is arizuko waiting on itself.
func (s *Store) LastHumanMessage(ctx context.Context, jid string) (core.Message, bool, error) {
	where := inboundWhere()
	for _, sender := range SyntheticSenders {
		where = append(where, objstore.Cmp{Attr: attrMsgSender, Op: objstore.Ne, Value: sender})
	}
	return s.lastMessageWhere(ctx, jid, "human", where)
}

// LastBotMessage answers a chat's newest row the agent wrote: the reply a
// waiting person is waiting for. Absent means this chat has never been
// answered, a different fact from an answer that arrived before the question.
func (s *Store) LastBotMessage(ctx context.Context, jid string) (core.Message, bool, error) {
	return s.lastMessageWhere(ctx, jid, "bot",
		[]objstore.Cmp{
			{Attr: attrMsgBot, Op: objstore.Present},
			{Attr: attrMsgVerb, Op: objstore.Ne, Value: verbStatus},
			{Attr: attrMsgVerb, Op: objstore.Ne, Value: VerbStatusSummary},
		})
}
