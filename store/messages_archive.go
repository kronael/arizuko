package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// ArchiveMessageRow is the FULL shape of one archived message: every attribute,
// its coordinate, and the delivery status that is stored as the ABSENCE of a
// delivery item. The archive is a backup, not a filtered view.
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
	Title         string `json:"title,omitempty"`
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

	// DeliveryAttempts belongs to the delivery item, and it is what the
	// operator's dead-letter page reports: an archive that dropped it would
	// restore a queue whose every row claims never to have been tried.
	DeliveryAttempts int               `json:"delivery_attempts,omitempty"`
	Delivery         map[string]string `json:"delivery,omitempty"`
	AttachmentRetry  map[string]string `json:"attachment_retry,omitempty"`
}

var deliveryWorkAttrs = []string{"operation", "queued_at", "next_attempt_at", "expires_at", "last_error"}

// naiveDatetime is the layout of a hand-written timestamp with no zone. Two
// krons rows carry one (`2026-04-02 12:38:47`).
const naiveDatetime = "2006-01-02 15:04:05"

// ReadMessageInstant parses a stored or archived timestamp into the instant a
// message position is built from. A NAIVE DATETIME IS READ AS UTC: the choice
// for the two krons rows is to read them or drop two messages, and the instance
// has always written UTC.
func ReadMessageInstant(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, naiveDatetime} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not an instant a message position can be built "+
		"from, so the row has no place in its chat's order", raw)
}

const defaultImportBatch = 500

// UnorderableMessage names one archived row this import refused: its timestamp
// is not an instant, so it cannot be ordered against a cursor. The id and the
// value as the archive carried it are both reported, because fixing the
// archive by hand is the operator's way back in.
type UnorderableMessage struct {
	ID        string
	Timestamp string
}

type undelivered struct {
	status   string
	attempts int
	work     map[string]string
}

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
			work := map[string]string{}
			for _, name := range deliveryWorkAttrs {
				setAttr(work, name, it.Attrs[name])
			}
			out[it.Key.Sort] = undelivered{status: status, attempts: n, work: work}
		}
	}
	return out, nil
}

// ExportMessagesJSONL streams every message to w as JSONL (repo convention:
// .jl), chat by chat and in position order inside each, so an unchanged
// database exports the same bytes twice. It answers the row count.
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
				row.Delivery = st.work
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

func archiveRowFrom(it objstore.Item) ArchiveMessageRow {
	ts, id, _ := MsgCoord(it.Key.Sort)
	retryAttrs := map[string]string{}
	for _, name := range attachmentAttrs {
		if value, ok := it.Attrs[name]; ok {
			retryAttrs[name] = value
		}
	}
	return ArchiveMessageRow{
		AttachmentRetry: retryAttrs,
		ID:              id,
		ChatJID:         it.Key.Partition,
		Sender:          it.Attrs[attrMsgSender],
		SenderName:      it.Attrs[attrMsgSenderName],
		Content:         it.Attrs[attrMsgContent],
		Timestamp:       ts,
		IsFromMe:        it.Attrs[attrMsgFromMe] == flagSet,
		IsBotMessage:    it.Attrs[attrMsgBot] == flagSet,
		ForwardedFrom:   it.Attrs[attrMsgForwardedFrom],
		ReplyToID:       it.Attrs[attrMsgReplyToID],
		ReplyToText:     it.Attrs[attrMsgReplyToText],
		ReplyToSender:   it.Attrs[attrMsgReplyToSender],
		Topic:           it.Attrs[attrMsgTopic],
		Title:           it.Attrs[attrMsgTitle],
		RoutedTo:        it.Attrs[attrMsgRoutedTo],
		Verb:            it.Attrs[attrMsgVerb],
		Attachments:     it.Attrs[attrMsgAttachments],
		Source:          it.Attrs[attrMsgSource],
		IsObserved:      it.Attrs[attrMsgObserved] == flagSet,
		TurnID:          it.Attrs[attrMsgTurnID],
		PlatformID:      it.Attrs[AttrMsgPlatformID],
		ChatName:        it.Attrs[attrMsgChatName],
		Errored:         it.Attrs[attrMsgErrored] == flagSet,
		LinkContext:     it.Attrs[attrMsgLinkContext],
	}
}

// ImportMessagesJSONL reads JSONL from r and appends every row, keyed on its
// own coordinate: an idempotent bulk append, not a rebuild (spec 5/8 "Message
// history"). It answers the rows read, the chats touched (for the caller to
// derive agent_cursor from), and the rows it refused.
//
// EVERY TIMESTAMP IS RE-RENDERED THROUGH core.TS on the way in: an archive is
// the one place a foreign string reaches a message's position (BUGS L219,
// L97). A row whose timestamp is not an instant is REFUSED, never written, and
// returned per row: a restore is a sequence of committed phases over a
// streaming tar, so failing here can neither roll back the config already
// applied nor check the stream up front. Correcting a named row and re-running
// completes the archive without duplicates.
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
		if m.Status != "" {
			if err := validateDeliveryStatus(m.ID, m.Status); err != nil {
				return imported, chatJIDs, refused, err
			}
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

	held, err := s.openChatsFor(ctx, chats)
	if err != nil {
		return 0, refused, err
	}
	staged := 0
	for _, m := range rendered {
		t, err := ReadMessageInstant(m.Timestamp)
		if err != nil {
			refused = append(refused, UnorderableMessage{ID: m.ID, Timestamp: m.Timestamp})
			continue
		}
		stamp := MsgStamp(core.TS(t), m.ID)
		if held[m.ChatJID][stamp] {
			continue
		}
		w, err := importedMessageWrites(m)
		if err != nil {
			refused = append(refused, UnorderableMessage{ID: m.ID, Timestamp: m.Timestamp})
			continue
		}
		var committed bool
		if m.RoutedTo != "" {
			committed, err = s.tryCountFolderMessageBy(ctx, m.RoutedTo, 1, w)
		} else {
			committed, err = s.TryWriteEntities(ctx, nil, w...)
		}
		if err != nil {
			return staged, refused, fmt.Errorf("import message %s: %w", m.ID, err)
		}
		if !committed {
			return staged, refused, fmt.Errorf("import message %s: %w", m.ID, ErrRefused)
		}
		held[m.ChatJID][stamp] = true
		staged++
	}
	return staged, refused, nil
}

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
		// A refusal is a chat that is already open; it must not take the
		// messages with it, so it rides its own batch.
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

func importedMessageWrites(m ArchiveMessageRow) ([]objstore.Write, error) {
	t, err := ReadMessageInstant(m.Timestamp)
	if err != nil {
		return nil, err
	}
	msg := core.Message{
		ID: m.ID, ChatJID: m.ChatJID, Sender: m.Sender, Name: m.SenderName,
		Content: m.Content, Timestamp: t, FromMe: m.IsFromMe, BotMsg: m.IsBotMessage,
		ForwardedFrom: m.ForwardedFrom, ReplyToID: m.ReplyToID, ReplyToText: m.ReplyToText,
		ReplyToSender: m.ReplyToSender, Topic: m.Topic, Title: m.Title, RoutedTo: m.RoutedTo, Verb: m.Verb,
		Attachments: m.Attachments, Source: m.Source, TurnID: m.TurnID,
		PlatformID: m.PlatformID, ChatName: m.ChatName, Errored: m.Errored,
		LinkContext: m.LinkContext,
	}
	key, err := messageKeyOf(msg)
	if err != nil {
		return nil, err
	}
	attrs := messageAttrs(msg)
	for _, name := range attachmentAttrs {
		if value, ok := m.AttachmentRetry[name]; ok {
			attrs[name] = value
		}
	}
	if _, err := attachmentRetryFrom(objstore.Item{Key: key, Attrs: attrs}); err != nil {
		return nil, err
	}
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

		writes = append(writes, feed)
	}
	if w, owes := deliveryWrite(key, msg, m.Status, m.DeliveryAttempts); owes {
		for name, value := range m.Delivery {
			if !slices.Contains(deliveryWorkAttrs, name) {
				return nil, fmt.Errorf("unknown delivery work attribute %q", name)
			}
			setAttr(w.Attrs, name, value)
		}
		writes = append(writes, w)
	}
	return writes, nil
}
