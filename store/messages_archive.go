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

// undeliveredStatus reads which rows are not `sent`, so the export can stamp a
// status that is stored as an absence.
func (s *Store) undeliveredStatus(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for _, status := range []string{core.MessageStatusPending, core.MessageStatusFailed} {
		items, err := s.Obj().Query(ctx, objstore.Query{
			Entity: MessageDelivery, Partition: status, Project: []string{},
		})
		if err != nil {
			return nil, fmt.Errorf("read the %s deliveries: %w", status, err)
		}
		for _, it := range items {
			out[it.Key.Sort] = status
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
			if st, undelivered := status[it.Key.Sort]; undelivered {
				row.Status = st
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
	var batch []objstore.Write
	staged := 0
	folders := map[string]int{}
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, ferr := s.applyInChunks(ctx, "import messages", batch); ferr != nil {
			return ferr
		}
		imported += staged
		staged = 0
		batch = batch[:0]
		return nil
	}
	for {
		var m ArchiveMessageRow
		derr := dec.Decode(&m)
		if errors.Is(derr, io.EOF) {
			break
		}
		if derr != nil {
			return imported, chatJIDs, refused, fmt.Errorf("decode message: %w", derr)
		}
		writes, rerr := importedMessageWrites(m)
		if rerr != nil {
			refused = append(refused, UnorderableMessage{ID: m.ID, Timestamp: m.Timestamp})
			continue
		}
		batch = append(batch, writes...)
		staged++
		chatJIDs[m.ChatJID] = true
		if m.RoutedTo != "" {
			folders[m.RoutedTo]++
		}
		if staged >= batchSize {
			if ferr := flush(); ferr != nil {
				return imported, chatJIDs, refused, ferr
			}
		}
	}
	if ferr := flush(); ferr != nil {
		return imported, chatJIDs, refused, ferr
	}
	// The counts move ONCE per folder rather than once per row. A restore is not
	// the live path — nothing else writes while it runs — so the count that
	// matters is the one the whole archive leaves, and an Increment per row would
	// be one round trip per message for the same answer.
	for folder, n := range folders {
		if err := s.countFolderMessageBy(ctx, nil, folder, n, nil); err != nil {
			return imported, chatJIDs, refused, err
		}
	}
	return imported, chatJIDs, refused, nil
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
	t, err := time.Parse(time.RFC3339Nano, m.Timestamp)
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
	if w, owes := deliveryWrite(key, msg, m.Status); owes {
		writes = append(writes, w)
	}
	return writes, nil
}
