package store

// messages.go holds every read and write of `messages`: one item per message
// routd has ever appended, inbound and outbound alike.
//
// THE PARTITION IS THE CHAT AND THE SORT KEY IS core.TS + KeySep + THE ID.
// Twenty-five reads filter on the chat and range on the time, and the four sort
// key inequalities on the interface were admitted for four of them. The largest
// chat partition measured is 10,688 rows on krons (2026-09-12), against 24,013
// in the table, so the partition is what keeps a history read off the other
// 55 chats.
//
// THE ID IS IN THE KEY BECAUSE THE INSTANT IS NOT UNIQUE. On krons 1,195 pairs
// carry an identical timestamp string and 245 of those pairs sit in ONE chat, so
// a time-only key would put two messages on one item and the second Put would
// erase the first. 10,488 pairs share a second inside one chat, which is what
// the key's fixed-width lead answers: the instant decides the order and the id
// only breaks a tie (objstore.OrderedLead).
//
// THE CURSOR IS A WHOLE KEY NOW. `chats.agent_cursor` held a bare 30-byte
// instant compared `> timestamp`, and under a composite key a bare instant sorts
// BELOW every message at it — so a message at exactly the cursor instant would
// be fed a second time, once per poll, for ever. The cursor holds the whole sort
// key and the poll loop reads `> cursor`, which is the same position it always
// meant. `arizuko migrate-hot` derives it as the greatest key at or before the
// stored instant, and that derivation is proven per chat against all three
// snapshots (messages_migrate_test.go).
//
// AND THE LOG IS APPEND-ORDERED. A live append takes the chat's next position,
// never one at or below the row the chat already ends on (appendInstant). The
// dispatch cursor is a high-water mark over this key, so a row written below it
// is a row no feed ever returns — which is what buried every steer re-injection
// behind a platform second and what dropped eleven of twelve documents of one
// Telegram upload. The archive's restore writes items directly and is NOT this
// door, so a history import still lands at the instants it carries.
//
// platform_id STAYS HERE and is not delivery state. One writer stamps it, once,
// when the adapter confirms delivery; no non-bot row on any of the three
// instances carries one; and MessageByID resolves a reply's parent BY it inside
// this partition. `status` and `delivery_attempts` are the mutable half and live
// in message_delivery.go, where `sent` is the absence of an item.
//
// ONE WRITE FANS OUT. PutMessage writes this item and the three read-shaped
// indexes in ONE batch, so an index can never hold a message the table does not
// or the other way round. The fan-out lives here, with the entity that owns the
// row, rather than in each index file: three writers would be three things that
// can forget.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/router"
)

// The attributes a message item carries. The chat, the instant and the id are
// the coordinate and are not among them: storing one twice is two facts that can
// disagree.
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
	// AttrMsgPlatformID is the platform's own id for a delivered row. It is
	// exported because the delivery sweep stamps it and the archive carries it.
	AttrMsgPlatformID = "platform_id"
)

// The ordered declaration is what makes a message key a position: every driver
// refuses a key whose instant is short, so it cannot sort by width before it
// sorts by time (BUGS L97).
func init() { objstore.OrderedLead(Messages, "", tsWidth) }

// MsgStamp assembles a message's position from a rendered instant and its id, so
// the live writer, the three indexes and the move that carries an existing row
// all build one shape.
func MsgStamp(ts, id string) string { return ts + KeySep + id }

// MsgCoord splits a position back into the instant and the id. A key that does
// not split names an item nobody wrote.
func MsgCoord(sortKey string) (ts, id string, ok bool) {
	return strings.Cut(sortKey, KeySep)
}

// requireMessageCursor refuses a cursor that is not a whole message position.
//
// objstore's ordered LEAD accepts a value of exactly the instant's width on
// purpose — a query BOUND is one, and `MessagesBefore` compares against a bare
// instant — so the entity is what has to refuse one as a stored CURSOR. A bare
// instant sorts BELOW every message at that instant, so `> cursor` hands the
// whole second back on every poll, for ever.
func requireMessageCursor(cursor string) error {
	if len(cursor) <= tsWidth || cursor[tsWidth] != KeySep[0] {
		return fmt.Errorf("%q is not a message position: a cursor carries the instant, the "+
			"separator and the id, and a bare instant sorts below every message at it, so the "+
			"poll loop would feed that instant again on every tick", cursor)
	}
	return nil
}

// MessageCursor is the position a consumer records after it has taken a message:
// the message's own key. A cursor that held only the instant would sort below
// every message at that instant and hand the whole second back on the next read.
func MessageCursor(m core.Message) string { return MsgStamp(core.TS(m.Timestamp), m.ID) }

// CursorInstant reads the instant out of a cursor, for the readers that ask when
// rather than where — the new-day notice and the idle-session check.
func CursorInstant(cursor string) (time.Time, bool) {
	ts, _, ok := MsgCoord(cursor)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	return t, err == nil
}

// msgStampAt is the bound that sorts at the START of an instant: below every
// message key at it, because every key carries the separator and an id after the
// instant. `timestamp < t` and `timestamp >= t` compare against this.
func msgStampAt(t time.Time) string { return core.TS(t) }

// msgStampPast is the bound that sorts just PAST an instant: above every message
// key at it, because the byte after KeySep is greater than KeySep. `timestamp >
// t` and `timestamp <= t` compare against this.
//
// It is derived from KeySep rather than written out, so the two cannot drift
// into a bound that includes or excludes a whole instant by one byte.
func msgStampPast(t time.Time) string { return core.TS(t) + string(KeySep[0]+1) }

// MessageKey is the coordinate of one message, built from the values a caller
// holds rather than from an id alone.
//
// A caller that has only an id has no coordinate, and every such caller in the
// tree holds the chat too — the reply parent is in the chat being replied to,
// the retry sweep holds the row it read, and an operator's dead-letter replay
// passes what the list it read rendered.
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
// value is LEFT OFF: absent is the only null this model has, so a reader that
// tests for a field's presence must not find an empty one.
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

// messageFrom reads one item back into the shape every caller renders.
//
// The instant comes out of the COORDINATE, because that is where it is stored.
// A key this cannot split still yields a message — the attributes are the truth
// and a zero time is visible — rather than dropping a row out of a feed.
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

// setFlag records a true flag and leaves a false one OFF the item, because
// absent is the only null this model has and a stored "0" is a flag every reader
// finds set.
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
// batch. routd is the only writer.
//
// A DUPLICATE ID IS A NO-OP, which is what the `INSERT OR IGNORE` it replaces
// meant: the first row written is authoritative and the log is append-only. The
// condition is on the message item, and a refusal takes the indexes with it, so
// a re-delivered message cannot double-count a folder.
func (s *Store) PutMessage(m core.Message) error {
	_, err := s.AppendMessage(m)
	return err
}

// AppendMessage is PutMessage answering the row AS STORED, which is what a
// caller that indexes, addresses or reports the row afterwards has to hold: the
// instant it carries is the one the append actually took, and appendInstant
// moves that instant when the caller's own would bury the row.
func (s *Store) AppendMessage(m core.Message) (core.Message, error) {
	return s.putMessage(context.Background(), m, core.MessageStatusSent)
}

// putMessage is PutMessage's body, answering the row as stored. The status is a
// parameter rather than a field of the message because it is not stored on the
// message: it decides whether a delivery item exists at all
// (message_delivery.go).
//
// `also` rides the message's own batch, so a caller whose write BELONGS with the
// append gets it atomically without a transaction — FireProactive's cooldown is
// the one caller, and the atomicity is what makes a crash before dispatch cost
// one missed turn rather than a double fire.
func (s *Store) putMessage(ctx context.Context, m core.Message, status string,
	also ...objstore.Write) (core.Message, error) {
	if m.Timestamp.IsZero() {
		// A stamped-here instant would be the caller's SILENTLY: the coordinate
		// is built from the message, so the caller would walk away holding a
		// message whose position does not exist, and every later address of it —
		// the enrich, the errored flag, the delivery stamp — would miss without
		// saying so. The writer stamps the instant; this refuses to invent one.
		return m, fmt.Errorf("message %s in %s carries no instant, and the instant is half its "+
			"position", m.ID, m.ChatJID)
	}
	// THE READ IS FOR THE STICKY PIN, and nothing else: an untopiced message
	// inherits the chat's pin, which is an attribute only a read answers.
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
	// THE APPEND TAKES THE CHAT'S NEXT POSITION, never one at or below the row
	// it already ends on.
	m.Timestamp, err = s.appendInstant(ctx, m)
	if err != nil {
		return m, err
	}
	key, err := messageKeyOf(m)
	if err != nil {
		return m, err
	}
	// ONE WRITE OPENS THE CHAT AND FLAGS IT. `chats` is the partition list the
	// poll loop and the archive both walk, so a message in a chat it does not
	// name would be a message nothing ever feeds and nothing ever exports; and a
	// real inbound makes the chat eligible for a proactive turn, which is a
	// property of the CHAT rather than a scan of its messages.
	//
	// A Merge is what makes them ONE and lets them ride the batch below. The pair
	// this replaces was a create-if-absent and a conditional Update on the SAME
	// item, so neither could join it — a batch may name an item once, and a
	// create-if-absent REFUSES on the chat's second message, which would take the
	// whole batch with it.
	// A chat that carries no real inbound is OPENED and nothing more: the item's
	// existence is the whole fact there.
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
		// No routed_to, so no folder feed entry and no counter to move: the
		// batch is the message and the feeds that take every row.
		_, err := s.TryWriteEntities(ctx, nil, writes...)
		return m, err
	}
	return m, s.countFolderMessage(ctx, m.RoutedTo, append(writes, folderWrite))
}

// appendInstant answers the instant a live append takes: the writer's own, or
// one nanosecond past the chat's newest row when the writer's own would put the
// new row at or below it.
//
// THE DISPATCH CURSOR IS A HIGH-WATER MARK, so a row written BELOW it is a row
// no feed ever returns: NewMessagesForChat reads `> cursor` and the cursor only
// moves forward. That made three separate bugs out of one mistake. Adapters
// stamp a message with the platform's whole second (routd's ingest truncates
// `time.Unix(sec, 0)`), and the re-injection a steer command writes carries the
// HOST clock — so a platform second three ahead of the host buried every
// `/root <cmd>`, `/new <text>` and `#topic <text>` re-injection behind the
// cursor the command itself had just set, with no ack and a leaked elevation
// (BUGS L102). And two rows of one second are ordered by their ids, which is
// arrival order for nothing: a row landing while the previous turn ran, at the
// same second as the batch tail but with a lesser id, sorted below the cursor
// the turn then set and was never fed again — one Telegram upload lost eleven of
// twelve documents (BUGS L103).
//
// ONE FLOOR ANSWERS BOTH, and it is the chat's own newest position rather than
// its cursor, because the cursor has not moved yet when a steer re-injects.
// The instant is the key's fixed-width LEAD, so one nanosecond past it sorts
// above every row at the floor whatever id follows.
//
// THE COST IS ONE KEY-ONLY READ of the partition this write already opens, and
// what is left is the window between that read and the write: two appends to one
// chat can still interleave. That window is microseconds against L103's, which
// was the whole duration of a turn.
//
// A CLAMPED ROW IS STILL THE CALLER'S ROW. Nothing is dropped and nothing is
// renamed — the id, the sender and the text are untouched and the instant moves
// by a nanosecond from a value the platform had already rounded to a second.
func (s *Store) appendInstant(ctx context.Context, m core.Message) (time.Time, error) {
	own := MsgStamp(core.TS(m.Timestamp), m.ID)
	floor, err := s.newestMessageKey(ctx, m.ChatJID)
	if err != nil {
		return time.Time{}, err
	}
	if floor == "" || own > floor {
		return m.Timestamp, nil
	}
	// A RE-DELIVERY IS NOT A LATE ARRIVAL. The append-only log dedups on the
	// message ITEM: the same id at the same instant is the same position, and the
	// Put's ItemAbsent condition turns the second one into a no-op. Moving that
	// row to a free position would defeat the only dedup ingest has and append the
	// message twice. The extra read costs nothing on the ordinary path, which
	// returned above.
	held, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: m.ChatJID, Project: []string{},
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: own},
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("read %s in %s: %w", own, m.ChatJID, err)
	}
	if len(held) > 0 {
		return m.Timestamp, nil
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
// reset the silence clock and never count as recent activity, because the
// trigger must not feed itself. It is declared here because the message writer
// is what decides a row is not real traffic, and routd's scan reads the same
// name.
const ProactiveSelfSender = "timed-proactive"

// isRealInbound reports whether a message is traffic a person sent. A bot row is
// the agent's own output and the proactive row is the trigger's.
func isRealInbound(m core.Message) bool {
	return !m.BotMsg && m.Sender != ProactiveSelfSender
}

// EnrichMessage replaces a message's content with the media-enriched body and
// clears its attachments, so the observed-context render on later turns sees the
// transcript rather than the raw refs.
//
// It takes the MESSAGE rather than an id, because an item is addressed by its
// coordinate and every caller holds the row it enriched.
func (s *Store) EnrichMessage(m core.Message, content string) error {
	key, err := messageKeyOf(m)
	if err != nil {
		return err
	}
	// A message nobody appended is nothing to enrich, and the Update says so by
	// refusing rather than creating a half-built row.
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
// first — the poll loop's feed.
//
// THE CURSOR IS A WHOLE MESSAGE KEY and the read is `> cursor`, so a message at
// exactly the cursor instant is consumed once. An empty cursor feeds the chat
// from the start, which is what a chat no turn has ever run on means.
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
// first. The read is newest-first and the caller wants the other order, so the
// page is taken from the end and then reversed.
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
// NEWEST first — the order the web transcript pages backwards in.
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

// webChatJID is the chat a folder's web panel writes into. groupfolder.JidFolder
// is the read half of the same grammar.
func webChatJID(folder string) string { return "web:" + folder }

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
// oldest first — the inverse of the inbound feed, for `arizuko send`.
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

// Topics answers a folder's web-chat threads, most recently active first.
//
// THE GROUPING IS THE READER'S, and that is the partition doing its job: the
// statement this replaces was a GROUP BY over the same chat's rows, so the rows
// examined are identical and only the place they are folded has moved. A web
// chat is one chat, which bounds it.
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
		// The preview is the alphabetically first content, which is what
		// MIN(content) answered, and the instant is the newest, which is what
		// MAX(timestamp) answered. The read arrives in key order, so the last
		// row of the partition is the newest.
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

// truncateRunes cuts a string to n characters, which is what SQLite's substr
// counts.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// messageAt reads one message of a chat by its id.
//
// THE ID IS THE TAIL OF THE KEY, so this is a partition read with the id
// compared in the reader rather than a point read. The statement it replaces was
// the same shape — `WHERE id=? AND chat_jid=?` has no index past the chat, so
// both examine the chat's rows — and the chat bounds it at 10,688 on krons.
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
// platform's id for it.
//
// THE CHAT BOUND IS NOT OPTIONAL. `platform_id` is a per-platform value, so an
// unscoped lookup resolves a reply id to a row in another chat and another
// folder, and the caller then copies that row's sender, text and topic into this
// message. The partition is what makes that impossible rather than checked.
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
	for _, it := range items {
		_, own, ok := MsgCoord(it.Key.Sort)
		if (ok && own == id) || it.Attrs[AttrMsgPlatformID] == id {
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

// RoutedToByMessageID answers the folder one message of a chat was routed to, so
// a reply to a bot row goes back to the child that authored it.
//
// IT IS CHAT-SCOPED, and the reply path is why that is not a narrowing: a reply
// names a message in the chat it is sent to, so the parent is in this partition
// by construction. The statement it replaces searched every chat, which is the
// cross-chat resolution MessageByID's own comment warns about.
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
//
// It takes the MESSAGES rather than ids, for MessageKey's reason: the caller
// holds the rows it dispatched.
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
// again — the operator's "retry this chat". It answers how many rows moved, so a
// caller can refuse to report success on zero.
//
// AN ENTITY HAS NO PREDICATE UPDATE, so the rows are read and then written one
// each. That is stricter than the statement it replaces, which could clear a row
// flagged after the decision to clear was made.
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

// DeleteErroredMessages drops a chat's errored rows, and the index entries that
// point at them, so the next inbound starts the chat from a clean batch.
//
// It answers the positions it deleted, because a caller holding an index
// DERIVED from these rows has no other way to learn they are gone: the rows the
// flag selects are read in here, and a deleted message that goes on answering
// searches reports content nobody can read back (routd/msgsearch).
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
// message a caller last saw.
//
// PAGING IS A KEY RANGE NOW. The statement it replaces read the page anchor's
// timestamp and then compared `(timestamp > ? OR (timestamp = ? AND id > ?))`,
// which is the composite key written out by hand; the key IS that comparison, so
// the second statement goes. An anchor this chat does not hold feeds from the
// start, which is what a caller catching up wants.
//
// chatJID binds the turn to its chat: a turn id alone is guessable and is not
// proof of ownership, so containment is the partition rather than a check in
// each caller.
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

// TurnHasBotReply reports whether the agent already delivered a bot row for one
// turn, so the result path does not send its prose a second time. Interim
// verb="status" rows are excluded: an auto-status must never count as the reply,
// or the agent's real answer is swallowed (spec 5/24).
func (s *Store) TurnHasBotReply(chatJID, turnID string) bool {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Messages, Partition: chatJID,
		Where: []objstore.Cmp{
			{Attr: attrMsgTurnID, Op: objstore.Eq, Value: turnID},
			{Attr: attrMsgBot, Op: objstore.Present},
			{Attr: attrMsgVerb, Op: objstore.Ne, Value: verbStatus},
		},
		Limit: 1,
	})
	return err == nil && len(items) > 0
}

// verbStatus is the interim ⏳ row's verb. submit_status edits that message in
// place rather than streaming a new notice (spec 5/24).
const verbStatus = "status"

// LastStatusPlatformID answers the platform id of the newest delivered status
// row of one turn, or empty when none landed yet.
func (s *Store) LastStatusPlatformID(chatJID, turnID string) string {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Messages, Partition: chatJID,
		Where: []objstore.Cmp{
			{Attr: attrMsgTurnID, Op: objstore.Eq, Value: turnID},
			{Attr: attrMsgVerb, Op: objstore.Eq, Value: verbStatus},
			{Attr: AttrMsgPlatformID, Op: objstore.Present},
		},
		Descending: true, Limit: 1,
	})
	if err != nil || len(items) == 0 {
		return ""
	}
	return items[0].Attrs[AttrMsgPlatformID]
}

// ThreadHasBotMessage reports whether the bot has posted in one (chat, topic).
// An empty topic is the main timeline and is not a thread.
func (s *Store) ThreadHasBotMessage(chatJID, topic string) bool {
	if topic == "" {
		return false
	}
	// The statement this replaces matched `topic=? OR turn_id=?`, because a
	// thread is named by either on the platforms that thread off a turn. A Cmp
	// list is a conjunction, so the disjunction is two reads and the first hit
	// answers.
	for _, c := range []objstore.Cmp{
		{Attr: attrMsgTopic, Op: objstore.Eq, Value: topic},
		{Attr: attrMsgTurnID, Op: objstore.Eq, Value: topic},
	} {
		items, err := s.Obj().Query(context.Background(), objstore.Query{
			Entity: Messages, Partition: chatJID,
			Where: []objstore.Cmp{c, {Attr: attrMsgBot, Op: objstore.Present}},
			Limit: 1,
		})
		if err == nil && len(items) > 0 {
			return true
		}
	}
	return false
}

// JIDRoutedToFolder reports whether a chat's default route target is folder or a
// descendant of it.
func (s *Store) JIDRoutedToFolder(jid, folder string) bool {
	target := s.DefaultFolderForJID(jid)
	if target == "" {
		return false
	}
	return groupfolder.Contains(folder, target)
}

// JIDRoutableToFolder reports whether folder (or a descendant) is the target of
// any route matching jid when the verb predicate is ignored. It recognises a
// sub-folder that handles jid only under a verb-scoped route, so a mention-only
// agent may reply to its own chat.
func (s *Store) JIDRoutableToFolder(jid, folder string) bool {
	msg := core.Message{ChatJID: jid}
	for _, r := range s.AllRoutes() {
		if !router.RouteMatchesIgnoreVerb(r, msg) {
			continue
		}
		if groupfolder.Contains(folder, core.ParseRouteTarget(r.Target).Folder) {
			return true
		}
	}
	return false
}

// newestMessageKey answers the position of the newest message a chat holds, or
// empty when it holds none.
//
// It is the `messages` half of chats.go's DeriveAgentCursors, and it answers a
// KEY rather than an instant because that is what the cursor holds now.
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

// CursorAtOrBefore answers the greatest message key of a chat at or before an
// instant, which is the cursor a stored instant becomes.
//
// IT IS THE MIGRATION'S DERIVATION AND IT IS EXACT. Every key at or before the
// instant is at or below this one, and every key after it is above, so the set
// the poll loop feeds is unchanged — proven per chat against all three snapshots
// (messages_migrate_test.go). An empty answer is a cursor with no message at or
// before it, which means the same as no cursor: feed from the start.
func (s *Store) CursorAtOrBefore(ctx context.Context, jid, instant string) (string, error) {
	if instant == "" {
		return "", nil
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: jid,
		Key:        objstore.KeyPredicate{Op: objstore.LtKey, Value: instant + string(KeySep[0]+1)},
		Descending: true, Limit: 1, Project: []string{},
	})
	if err != nil {
		return "", fmt.Errorf("derive %s's cursor at %s: %w", jid, instant, err)
	}
	if len(items) == 0 {
		return "", nil
	}
	return items[0].Key.Sort, nil
}

// --- the proactive scan's per-chat reads (spec 5/26) ---------------------
//
// Each is a bounded read of ONE chat, because the scan already holds the chat it
// is deciding about: it walks the eligible chats from `chats` and then asks these
// about each in turn. The statements they replace were the same shape.

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

// LastInbound answers the newest real inbound row of a chat — its body and its
// instant — which the positive signals read. ok=false when the chat has none.
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

// LatestSource answers which adapter delivered the newest sourced inbound row of
// a chat, so the Deliverer sends an outbound back the way the conversation came
// in. Empty when the chat has none.
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

// BotSpokeSince reports whether a chat carries a bot row at or after an instant
// — the BotQuiet veto.
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

// InboundCountSince counts a chat's real inbound rows at or after an instant —
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

// realInboundFilter is the "traffic a person sent" predicate, written once
// because four reads ask it and a fifth that spelled it differently would
// disagree about what the scan is looking at.
func realInboundFilter() []objstore.Cmp {
	return []objstore.Cmp{
		{Attr: attrMsgBot, Op: objstore.Absent},
		{Attr: attrMsgSender, Op: objstore.Ne, Value: ProactiveSelfSender},
	}
}

// FireProactive appends the synthetic inbound row AND sets the chat's proactive
// cooldown in one batch, then answers the row's id — which is the turn id.
//
// The caller dispatches the run only after this commits, so a crash before
// dispatch leaves the cooldown set: at worst one missed turn, never a
// double-fire. The cooldown rides the message's own batch, which is
// all-or-nothing, so the pair needs no transaction to be one commit.
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

// CountMessages answers how many messages every chat holds. `arizuko
// migrate-hot` reads it back to check that every row travelled, which is the one
// caller that wants a number over the whole history.
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
// row of any kind landed — the two numbers an errored-chat listing shows beside
// each chat.
//
// It is one partition read where the statement it replaces was two correlated
// subqueries over the same rows.
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
// and the first and last instant. It is the held-set page's candidate list.
//
// IT WALKS THE PARTITION LIST, which is `chats` — 136 items on krons — and reads
// each chat in turn. The statement it replaces was one GROUP BY over the whole
// table, so the rows examined are the same and only the place they are folded
// has moved.
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

// LastInboundMessage answers a chat's newest inbound human row — the message the
// loop resolved last, and the one a held-set reader asks resolve about again.
func (s *Store) LastInboundMessage(ctx context.Context, jid string) (core.Message, bool, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: jid,
		Where: []objstore.Cmp{
			{Attr: attrMsgFromMe, Op: objstore.Absent},
			{Attr: attrMsgBot, Op: objstore.Absent},
		},
		Descending: true, Limit: 1,
	})
	if err != nil {
		return core.Message{}, false, fmt.Errorf("read %s's newest inbound row: %w", jid, err)
	}
	if len(items) == 0 {
		return core.Message{}, false, nil
	}
	return messageFrom(items[0]), true, nil
}
