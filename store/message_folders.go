package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a folder-feed item carries: the pointer, and what the feed
// filters on before it dereferences one.
const (
	attrFeedChatJID  = "chat_jid"
	attrFeedSender   = "sender"
	attrFeedObserved = "is_observed"
	attrFeedBot      = "is_bot_message"
)

// counterAttrN is the attribute a folder's message count lives in: the same
// name Counters uses.
const counterAttrN = counterAttr

func init() { objstore.OrderedLead(MessageFolders, "", tsWidth) }

func folderFeedKey(folder string, msgKey objstore.Key) (objstore.Key, error) {
	if folder == "" {
		return objstore.Key{}, errors.New("folder feed entry carries no folder, which is " +
			"its partition")
	}
	return objstore.Key{Partition: folder, Sort: msgKey.Sort}, nil
}

func folderCountKey(folder string) objstore.Key {
	return objstore.Key{Partition: OwnerRoutd, Sort: folder}
}

func folderFeedWrite(msgKey objstore.Key, m core.Message) (objstore.Write, bool, error) {
	if m.RoutedTo == "" {
		return objstore.Write{}, false, nil
	}
	key, err := folderFeedKey(m.RoutedTo, msgKey)
	if err != nil {
		return objstore.Write{}, false, err
	}
	attrs := map[string]string{attrFeedChatJID: m.ChatJID}
	setAttr(attrs, attrFeedSender, m.Sender)
	setFlag(attrs, attrFeedBot, m.BotMsg)
	return objstore.Write{
		Op: objstore.Put, Entity: MessageFolders, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}, true, nil
}

func (s *Store) countFolderMessage(ctx context.Context, folder string,
	writes []objstore.Write) error {
	// A refusal is the message's own create-if-absent: the row is not
	// appended and the folder must not count it twice.
	_, err := s.tryCountFolderMessageBy(ctx, folder, 1, writes)
	return err
}

// errNoCounter is what a folder with no counter yet answers, so the seed runs
// once rather than on every message.
var errNoCounter = errors.New("the folder has no message counter")

func (s *Store) addFolderCount(ctx context.Context, folder string, delta int,
	also []objstore.Write) (bool, error) {
	_, committed, err := s.incrementEntityVia(ctx, MessageFolderCounts,
		folderCountKey(folder), counterAttrN, delta, nil, also)
	if err != nil {
		if strings.Contains(err.Error(), "no such item") {
			return false, errNoCounter
		}
		return false, fmt.Errorf("count a message for %s: %w", folder, err)
	}
	return committed, nil
}

// FolderMessageCounts answers how many messages each named folder has taken,
// for the folders that have taken any. ONE READ answers every folder, because
// the counts share a partition. A folder absent from the answer has taken
// none, which lets the caller tell "never used" from "used and counted zero".
func (s *Store) FolderMessageCounts(ctx context.Context, folders []string) (map[string]int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: MessageFolderCounts, Partition: OwnerRoutd, Project: []string{counterAttrN},
	})
	if err != nil {
		return nil, fmt.Errorf("read the folder message counts: %w", err)
	}
	want := make(map[string]bool, len(folders))
	for _, f := range folders {
		want[f] = true
	}
	out := make(map[string]int, len(folders))
	for _, it := range items {
		if !want[it.Key.Sort] {
			continue
		}
		n, err := parseCount(it.Attrs[counterAttrN])
		if err != nil {
			return nil, fmt.Errorf("%s's message count: %w", it.Key.Sort, err)
		}
		out[it.Key.Sort] = n
	}
	return out, nil
}

// FolderLastMessageAt answers when one folder last took a message, or empty
// when it has taken none: the newest key of the folder's feed, one item.
func (s *Store) FolderLastMessageAt(ctx context.Context, folder string) (string, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: MessageFolders, Partition: folder,
		Descending: true, Limit: 1, Project: []string{},
	})
	if err != nil {
		return "", fmt.Errorf("read %s's newest message: %w", folder, err)
	}
	if len(items) == 0 {
		return "", nil
	}
	ts, _, _ := MsgCoord(items[0].Key.Sort)
	return ts, nil
}

// MarkMessagesObserved records messages as ambient context for a folder; the
// poll loop calls it when the resolved route target is `folder?observe`. It
// stamps the message AND the feed, in one batch per message. A message already
// routed somewhere else moves: its old feed entry goes and the old folder's
// count comes down, so a folder never counts a message it no longer holds.
func (s *Store) MarkMessagesObserved(ctx context.Context, folder string,
	msgs []core.Message) error {
	if folder == "" {
		return errors.New("messages cannot be observed into no folder")
	}
	var plain []objstore.Write
	var fresh []objstore.Write
	dropped := map[string]int{}
	for _, m := range msgs {
		msgKey, err := messageKeyOf(m)
		if err != nil {
			return err
		}
		feedKey, err := folderFeedKey(folder, msgKey)
		if err != nil {
			return err
		}
		stamp := objstore.Write{
			Op: objstore.Update, Entity: Messages, Key: msgKey,
			Attrs: map[string]string{attrMsgRoutedTo: folder, attrMsgObserved: flagSet},
		}
		if m.RoutedTo == folder {
			plain = append(plain, stamp, objstore.Write{
				Op: objstore.Update, Entity: MessageFolders, Key: feedKey,
				Attrs: map[string]string{attrFeedObserved: flagSet},
			})
			continue
		}
		if m.RoutedTo != "" {
			oldKey, err := folderFeedKey(m.RoutedTo, msgKey)
			if err != nil {
				return err
			}
			plain = append(plain, objstore.Write{
				Op: objstore.Delete, Entity: MessageFolders, Key: oldKey,
			})
			dropped[m.RoutedTo]++
		}
		attrs := map[string]string{attrFeedChatJID: m.ChatJID, attrFeedObserved: flagSet}
		setAttr(attrs, attrFeedSender, m.Sender)
		setFlag(attrs, attrFeedBot, m.BotMsg)
		fresh = append(fresh, stamp, objstore.Write{
			Op: objstore.Put, Entity: MessageFolders, Key: feedKey, Attrs: attrs,
		})
	}
	if len(plain) > 0 {
		if _, err := s.applyInChunks(ctx, "observe into "+folder, plain); err != nil {
			return err
		}
	}
	for from, n := range dropped {
		if err := s.countFolderMessageBy(ctx, from, -n, nil); err != nil {
			return err
		}
	}
	for len(fresh) > 0 {
		n := min(len(fresh), objstore.IndexBatchSize)
		if n%2 == 1 {
			n--
		}
		if err := s.countFolderMessageBy(ctx, folder, n/2, fresh[:n]); err != nil {
			return err
		}
		fresh = fresh[n:]
	}
	return nil
}

func (s *Store) countFolderMessageBy(ctx context.Context, folder string,
	delta int, writes []objstore.Write) error {
	committed, err := s.tryCountFolderMessageBy(ctx, folder, delta, writes)
	if err != nil {
		return err
	}
	if !committed {
		return fmt.Errorf("%w: counting %d messages for %s", ErrRefused, delta, folder)
	}
	return nil
}

func (s *Store) tryCountFolderMessageBy(ctx context.Context, folder string,
	delta int, writes []objstore.Write) (bool, error) {
	committed, err := s.addFolderCount(ctx, folder, delta, writes)
	if err == nil || !errors.Is(err, errNoCounter) {
		return committed, err
	}
	if seedErr := putIfAbsent(ctx, s.Obj(), objstore.Write{
		Op: objstore.Put, Entity: MessageFolderCounts, Key: folderCountKey(folder),
		Attrs: map[string]string{counterAttrN: "0"},
	}); seedErr != nil {
		return false, fmt.Errorf("open %s's message count: %w", folder, seedErr)
	}
	return s.addFolderCount(ctx, folder, delta, writes)
}

// observedFetchFactor is how many feed entries are read for each message the
// window will hold. The feed's own filters narrow to the rows that qualify, so
// the only drop left in the reader is an empty (attachment-only) body; three
// for one covers that and still bounds the read.
const observedFetchFactor = 3

// ObservedSince answers the trailing window of observed messages for a folder
// whose position is strictly after cursor, oldest first. The folder's own
// replies (every topic's) are part of it, so a turn sees what a sibling topic
// already answered (BUGS L527).
//
// THE CURSOR IS A BARE INSTANT and stays one: the read takes the NEWEST
// window, so a cut can only fall at the old end and the newest rendered
// message is always whole. Scheduled-task prompts are excluded: their rows
// land routed_to the folder with is_observed=1, and serving them back to every
// topic re-opened the hole BUGS L54 closed through a second door.
func (s *Store) ObservedSince(ctx context.Context, folder, cursor string, maxMsgs,
	maxChars int, watched []string) ([]core.Message, error) {
	if maxMsgs <= 0 || maxChars <= 0 {
		return nil, nil
	}
	key := objstore.KeyPredicate{}
	if cursor != "" {
		key = objstore.KeyPredicate{Op: objstore.GeKey, Value: cursor + string(KeySep[0]+1)}
	}
	type read struct {
		partition string
		where     []objstore.Cmp
	}
	reads := []read{
		{folder, []objstore.Cmp{
			{Attr: attrFeedObserved, Op: objstore.Present},
			{Attr: attrFeedBot, Op: objstore.Absent},
		}},
		{folder, []objstore.Cmp{{Attr: attrFeedBot, Op: objstore.Present}}},
	}
	for _, w := range watched {
		reads = append(reads, read{w, []objstore.Cmp{
			{Attr: attrFeedObserved, Op: objstore.Absent},
			{Attr: attrFeedBot, Op: objstore.Absent},
		}})
	}
	var found []objstore.Item
	for _, r := range reads {
		items, err := s.Obj().Query(ctx, objstore.Query{
			Entity: MessageFolders, Partition: r.partition,
			Key:        key,
			Where:      r.where,
			Descending: true, Limit: maxMsgs * observedFetchFactor,
		})
		if err != nil {
			return nil, fmt.Errorf("read %s's ambient window: %w", r.partition, err)
		}
		found = append(found, items...)
	}
	sortItemsDescending(found)
	out := make([]core.Message, 0, maxMsgs)
	chars := 0
	for _, it := range found {
		if len(out) >= maxMsgs {
			break
		}
		if core.IsScheduledSender(it.Attrs[attrFeedSender]) {
			continue
		}
		m, ok, err := s.messageBehind(ctx, it)
		if err != nil {
			return nil, err
		}
		if !ok || m.Content == "" {
			continue
		}
		if chars+len(m.Content) > maxChars {
			break
		}
		chars += len(m.Content)
		out = append(out, m)
	}
	return reversed(out), nil
}

func (s *Store) messageBehind(ctx context.Context, it objstore.Item) (core.Message, bool, error) {
	chatJID := it.Attrs[attrFeedChatJID]
	if chatJID == "" {
		return core.Message{}, false, fmt.Errorf("folder feed entry %s/%s names no chat, so "+
			"the message it points at cannot be read", it.Key.Partition, it.Key.Sort)
	}
	found, ok, err := s.itemAt(ctx, Messages, objstore.Key{Partition: chatJID, Sort: it.Key.Sort})
	if err != nil || !ok {
		return core.Message{}, false, err
	}
	return messageFrom(found), true, nil
}
