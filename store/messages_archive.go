package store

// messages_archive.go is `messages`' second reader: the whole history, which no
// manifest carries, exported and imported by `arizuko archive` (spec 5/8
// "Message history").
//
// IT IS A SECOND READER OF ONE ENTITY, not a second store. The cursor derivation
// is in chats.go rather than here because it is the second half of ONE
// operation: an import that did not park the poller makes the agent answer every
// restored message.
//
// THE CHATS ENTITY IS THE PARTITION LIST. An entity read names one partition, so
// "every message" is every chat, and `chats` is what holds them — 136 items on
// krons against 24,013 messages. `PutMessage` opens a chat item before it writes
// the first message of a chat precisely so that list can never be short; the
// poll loop rests on the same guarantee, and a chat missing from it would be a
// chat nothing ever feeds.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// ArchiveMessageRow is the FULL shape of one archived message: every attribute
// the item carries, its coordinate, and the delivery status that is stored as
// the ABSENCE of a delivery item. The archive is a backup rather than a filtered
// view, so it carries all of them verbatim.
type ArchiveMessageRow struct {
	ID            string `json:"id"`
	ChatJID       string `json:"chat_jid"`
	Sender        string `json:"sender"`
	SenderName    string `json:"sender_name,omitempty"`
	Content       string `json:"content"`
	Timestamp     string `json:"timestamp"`
	IsFromMe      bool   `json:"is_from_me"`
	IsBotMessage  bool   `json:"is_bot_message"`
	ForwardedFrom string `json:"forwarded_from,omitempty"`
	ReplyToID     string `json:"reply_to_id,omitempty"`
	ReplyToText   string `json:"reply_to_text,omitempty"`
	ReplyToSender string `json:"reply_to_sender,omitempty"`
	Topic         string `json:"topic,omitempty"`
	RoutedTo      string `json:"routed_to,omitempty"`
	Verb          string `json:"verb,omitempty"`
	Attachments   string `json:"attachments,omitempty"`
	Source        string `json:"source,omitempty"`
	IsObserved    bool   `json:"is_observed"`
	TurnID        string `json:"turn_id,omitempty"`
	Status        string `json:"status,omitempty"`
	PlatformID    string `json:"platform_id,omitempty"`
	ChatName      string `json:"chat_name,omitempty"`
	Errored       bool   `json:"errored"`
	LinkContext   string `json:"link_context,omitempty"`

	// DeliveryAttempts is how many sends this row has already cost. It belongs
	// to the delivery item rather than the message, and it is what the operator's
	// dead-letter page reports, so an archive that dropped it would restore a
	// queue whose every row claims never to have been tried.
	DeliveryAttempts int `json:"delivery_attempts,omitempty"`
}

// naiveDatetime is the layout of a hand-written timestamp with no zone. Two krons
// rows carry one (`2026-04-02 12:38:47`), inserted by hand in April.
const naiveDatetime = "2006-01-02 15:04:05"

// ReadMessageInstant parses a stored or archived timestamp into the instant a
// message position is built from.
//
// A NAIVE DATETIME IS READ AS UTC. An item has no position without an instant, so
// the choice for the two krons rows that carry one is to read them or to drop two
// messages. The instance writes UTC and has always written UTC, so the zone is
// not invented, it is the one the row was recorded in.
func ReadMessageInstant(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, naiveDatetime} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not an instant a message position can be built "+
		"from, so the row has no place in its chat's order", raw)
}

// defaultImportBatch bounds how many messages ride one import transaction — a
// multi-hour transfer must not hold the database's write lock for its whole
// duration (spec 5/8 "Message history").
const defaultImportBatch = 500

// UnorderableMessage names one archived row this import refused: its timestamp
// is not an instant, so there is no core.TS rendering of it and it cannot be
// ordered against a cursor. The id and the value as the archive carried it are
// both reported, because fixing the archive by hand is the operator's way back
// in.
type UnorderableMessage struct {
	ID        string
	Timestamp string
}

// undelivered is one row's delivery state: the status stored as the ABSENCE of a
// `sent` item, and the attempts already spent on it.
type undelivered struct {
	status   string
	attempts int
}

// undeliveredStatus reads which rows are not `sent`, so the export can stamp a
// state that is stored as an absence.
func (s *Store) undeliveredStatus(ctx context.Context) (map[string]undelivered, error) {
	out := map[string]undelivered{}
	for _, status := range []string{core.MessageStatusPending, core.MessageStatusFailed} {
		items, err := s.Obj().Query(ctx, objstore.Query{
			Entity: MessageDelivery, Partition: status,
		})
		if err != nil {
			return nil, fmt.Errorf("read the %s deliveries: %w", status, err)
		}
		for _, it := range items {
			n, err := parseCount(it.Attrs[attrDelAttempts])
			if err != nil {
				return nil, fmt.Errorf("%s's attempt count: %w", it.Key.Sort, err)
			}
			out[it.Key.Sort] = undelivered{status: status, attempts: n}
		}
	}
	return out, nil
}

// ExportMessagesJSONL streams every message to w as JSONL (repo convention: .jl,
// one JSON object per line), chat by chat and in position order inside each, so
// an unchanged database exports the same bytes twice. It answers the row count.
func (s *Store) ExportMessagesJSONL(ctx context.Context, w io.Writer) (int, error) {
	status, err := s.undeliveredStatus(ctx)
	if err != nil {
		return 0, err
	}
	jids, err := s.ChatJIDs(ctx)
	if err != nil {
		return 0, err
	}
	enc := json.NewEncoder(w)
	n := 0
	for _, jid := range jids {
		items, err := s.Obj().Query(ctx, objstore.Query{Entity: Messages, Partition: jid})
		if err != nil {
			return n, fmt.Errorf("read %s's messages: %w", jid, err)
		}
		for _, it := range items {
			row := archiveRowFrom(it)
			if st, owed := status[it.Key.Sort]; owed {
				row.Status, row.DeliveryAttempts = st.status, st.attempts
			} else {
				row.Status = core.MessageStatusSent
			}
			if err := enc.Encode(row); err != nil {
				return n, fmt.Errorf("encode message %s: %w", row.ID, err)
			}
			n++
		}
	}
	return n, nil
}

// archiveRowFrom renders one item as the archive's row.
func archiveRowFrom(it objstore.Item) ArchiveMessageRow {
	ts, id, _ := MsgCoord(it.Key.Sort)
	return ArchiveMessageRow{
		ID:            id,
		ChatJID:       it.Key.Partition,
		Sender:        it.Attrs[attrMsgSender],
		SenderName:    it.Attrs[attrMsgSenderName],
		Content:       it.Attrs[attrMsgContent],
		Timestamp:     ts,
		IsFromMe:      it.Attrs[attrMsgFromMe] == flagSet,
		IsBotMessage:  it.Attrs[attrMsgBot] == flagSet,
		ForwardedFrom: it.Attrs[attrMsgForwardedFrom],
		ReplyToID:     it.Attrs[attrMsgReplyToID],
		ReplyToText:   it.Attrs[attrMsgReplyToText],
		ReplyToSender: it.Attrs[attrMsgReplyToSender],
		Topic:         it.Attrs[attrMsgTopic],
		RoutedTo:      it.Attrs[attrMsgRoutedTo],
		Verb:          it.Attrs[attrMsgVerb],
		Attachments:   it.Attrs[attrMsgAttachments],
		Source:        it.Attrs[attrMsgSource],
		IsObserved:    it.Attrs[attrMsgObserved] == flagSet,
		TurnID:        it.Attrs[attrMsgTurnID],
		PlatformID:    it.Attrs[AttrMsgPlatformID],
		ChatName:      it.Attrs[attrMsgChatName],
		Errored:       it.Attrs[attrMsgErrored] == flagSet,
		LinkContext:   it.Attrs[attrMsgLinkContext],
	}
}

// ImportMessagesJSONL reads JSONL from r and appends every row, keyed on its own
// coordinate (spec 5/8 "Message history": "idempotent bulk append, not rebuild"
// — re-running the same archive, or restoring onto a target that already holds
// some of the same history, leaves the rows that are already there). It answers
// the number of rows read, the chats it touched for the caller to derive
// agent_cursor from, and the rows it refused.
//
// EVERY TIMESTAMP IS RE-RENDERED THROUGH core.TS on the way in. An archive is the
// one place a foreign string reaches a message's position, whose guarantee is
// that its string order is its time order, so this is where the value is brought
// into the layout rather than trusted (BUGS L219, L97).
//
// A row whose timestamp is not an instant is REFUSED — never written — and
// returned for the caller to report. Per row, not per archive: a restore is a
// sequence of committed phases over a streaming tar, so failing here cannot roll
// back the config and secrets already applied, and the stream is unbounded and
// batched, so it cannot be checked up front either. The import is idempotent, so
// correcting a named row and re-running completes the archive without
// duplicating what already landed.
func (s *Store) ImportMessagesJSONL(ctx context.Context, r io.Reader, batchSize int) (
	imported int, chatJIDs map[string]bool, refused []UnorderableMessage, err error) {
	if batchSize <= 0 {
		batchSize = defaultImportBatch
	}
	chatJIDs = map[string]bool{}
	dec := json.NewDecoder(r)
	var batch []ArchiveMessageRow
	for {
		var m ArchiveMessageRow
		derr := dec.Decode(&m)
		if errors.Is(derr, io.EOF) {
			break
		}
		if derr != nil {
			return imported, chatJIDs, refused, fmt.Errorf("decode message: %w", derr)
		}
		batch = append(batch, m)
		chatJIDs[m.ChatJID] = true
		if len(batch) < batchSize {
			continue
		}
		n, bad, berr := s.importMessageBatch(ctx, batch)
		imported, refused = imported+n, append(refused, bad...)
		if berr != nil {
			return imported, chatJIDs, refused, berr
		}
		batch = batch[:0]
	}
	n, bad, berr := s.importMessageBatch(ctx, batch)
	imported, refused = imported+n, append(refused, bad...)
	return imported, chatJIDs, refused, berr
}

// ImportMessageRows writes archived rows straight, for the caller that already
// holds them: `arizuko migrate-hot` reads the legacy table rather than a stream.
// It is the same writer, so a moved instance and a restored one carry the same
// items.
func (s *Store) importMessageRows(ctx context.Context, rows []ArchiveMessageRow) error {
	for start := 0; start < len(rows); start += defaultImportBatch {
		chunk := rows[start:min(start+defaultImportBatch, len(rows))]
		_, refused, err := s.importMessageBatch(ctx, chunk)
		if err != nil {
			return err
		}
		if len(refused) > 0 {
			return fmt.Errorf("%d message rows carry a timestamp no position can be built "+
				"from, starting with %s (%q)", len(refused), refused[0].ID, refused[0].Timestamp)
		}
	}
	return nil
}

// importMessageBatch renders one batch and writes it, answering how many landed
// and which rows it refused.
//
// IT READS WHAT IS ALREADY THERE FIRST, and that is what makes the import
// idempotent. A message write is a create-if-absent, a batch is all-or-nothing,
// and a refusal takes the whole chunk with it — so a re-run of the same archive,
// or a restore onto a target that already holds some of the same history, would
// fail on its first present row rather than skipping it. The rows already in the
// chat are read once per chat and the batch carries only what is new.
//
// The read and the write are two steps, and nothing can appear between them: a
// restore runs with the daemon that appends messages stopped. A live writer would
// make a present row reach the write as an unconditional Put of the same archived
// bytes, which is why the survivors keep no condition — the condition would turn
// one raced row into a lost batch of five hundred.
func (s *Store) importMessageBatch(ctx context.Context, rows []ArchiveMessageRow) (
	int, []UnorderableMessage, error) {
	var refused []UnorderableMessage
	rendered := make([]ArchiveMessageRow, 0, len(rows))
	chats := map[string]bool{}
	for _, m := range rows {
		if _, err := importedMessageWrites(m); err != nil {
			refused = append(refused, UnorderableMessage{ID: m.ID, Timestamp: m.Timestamp})
			continue
		}
		rendered = append(rendered, m)
		chats[m.ChatJID] = true
	}
	if len(rendered) == 0 {
		return 0, refused, nil
	}
	// The chat item is opened BEFORE the messages, because `chats` is the
	// partition list every whole-history reader walks: an import that filled the
	// messages and left the list short would restore a history the poll loop
	// never feeds and the next export cannot see.
	held, err := s.openChatsFor(ctx, chats)
	if err != nil {
		return 0, refused, err
	}
	var writes []objstore.Write
	folders := map[string]int{}
	staged := 0
	for _, m := range rendered {
		t, err := ReadMessageInstant(m.Timestamp)
		if err != nil {
			refused = append(refused, UnorderableMessage{ID: m.ID, Timestamp: m.Timestamp})
			continue
		}
		if held[m.ChatJID][MsgStamp(core.TS(t), m.ID)] {
			continue // already restored; the archive is an append, not a rebuild
		}
		w, err := importedMessageWrites(m)
		if err != nil {
			refused = append(refused, UnorderableMessage{ID: m.ID, Timestamp: m.Timestamp})
			continue
		}
		for i := range w {
			w[i].Cond = objstore.Cond{}
		}
		writes = append(writes, w...)
		staged++
		if m.RoutedTo != "" {
			folders[m.RoutedTo]++
		}
	}
	if staged == 0 {
		return 0, refused, nil
	}
	if _, err := s.applyInChunks(ctx, "import messages", writes); err != nil {
		return 0, refused, err
	}
	// The counts move ONCE per folder rather than once per row. A move and a
	// restore are not the live path — nothing else writes while they run — so the
	// number that matters is the one the whole batch leaves, and an Increment per
	// row would be one round trip per message for the same answer.
	for folder, n := range folders {
		if err := s.countFolderMessageBy(ctx, folder, n, nil); err != nil {
			return staged, refused, err
		}
	}
	return staged, refused, nil
}

// openChatsFor opens a chat item for every chat the batch names and answers the
// message positions each already holds.
//
// The positions are what the skip above rests on, and they are read per chat
// rather than per row: one query answers a whole chat.
func (s *Store) openChatsFor(ctx context.Context, chats map[string]bool) (
	map[string]map[string]bool, error) {
	held := make(map[string]map[string]bool, len(chats))
	jids := make([]string, 0, len(chats))
	for jid := range chats {
		jids = append(jids, jid)
	}
	sort.Strings(jids)
	for _, jid := range jids {
		key, err := chatKey(jid)
		if err != nil {
			return nil, err
		}
		// A refusal is a chat that is already open, which is the outcome this
		// wants; it must not take the messages with it, so it rides its own batch.
		if _, err := s.TryWriteEntities(ctx, nil, objstore.Write{
			Op: objstore.Put, Entity: Chats, Key: key, Attrs: map[string]string{},
			Cond: objstore.Cond{Item: objstore.ItemAbsent},
		}); err != nil {
			return nil, fmt.Errorf("open the chat %s: %w", jid, err)
		}
		items, err := s.Obj().Query(ctx, objstore.Query{
			Entity: Messages, Partition: jid, Project: []string{},
		})
		if err != nil {
			return nil, fmt.Errorf("read %s's restored messages: %w", jid, err)
		}
		at := make(map[string]bool, len(items))
		for _, it := range items {
			at[it.Key.Sort] = true
		}
		held[jid] = at
	}
	return held, nil
}

// importedMessageWrites renders one archived row as the items it becomes, or
// refuses it.
//
// It rebuilds every index the live writer would have written — the activity
// feed, the folder feed, and a delivery item for a row the archive says is not
// sent — because an import that carried only the messages would leave a restored
// instance whose ambient window, activity page and retry feed are empty while
// the history is whole. The folder COUNT is moved once per folder by the caller,
// which is the one thing a batch of Puts cannot carry.
func importedMessageWrites(m ArchiveMessageRow) ([]objstore.Write, error) {
	t, err := ReadMessageInstant(m.Timestamp)
	if err != nil {
		return nil, err
	}
	msg := core.Message{
		ID: m.ID, ChatJID: m.ChatJID, Sender: m.Sender, Name: m.SenderName,
		Content: m.Content, Timestamp: t, FromMe: m.IsFromMe, BotMsg: m.IsBotMessage,
		ForwardedFrom: m.ForwardedFrom, ReplyToID: m.ReplyToID, ReplyToText: m.ReplyToText,
		ReplyToSender: m.ReplyToSender, Topic: m.Topic, RoutedTo: m.RoutedTo, Verb: m.Verb,
		Attachments: m.Attachments, Source: m.Source, TurnID: m.TurnID,
		PlatformID: m.PlatformID, ChatName: m.ChatName, Errored: m.Errored,
		LinkContext: m.LinkContext,
	}
	key, err := messageKeyOf(msg)
	if err != nil {
		return nil, err
	}
	attrs := messageAttrs(msg)
	setFlag(attrs, attrMsgObserved, m.IsObserved)
	writes := []objstore.Write{
		{Op: objstore.Put, Entity: Messages, Key: key, Attrs: attrs,
			Cond: objstore.Cond{Item: objstore.ItemAbsent}},
		activityWrite(key, msg),
	}
	feed, counted, err := folderFeedWrite(key, msg)
	if err != nil {
		return nil, err
	}
	if counted {
		if m.IsObserved {
			feed.Attrs[attrFeedObserved] = flagSet
		}
		// The archive is appended onto whatever is already there, so the feed
		// entry takes the message's own condition rather than its own: a row the
		// target already holds must not double-count its folder.
		writes = append(writes, feed)
	}
	if w, owes := deliveryWrite(key, msg, m.Status, m.DeliveryAttempts); owes {
		writes = append(writes, w)
	}
	return writes, nil
}
