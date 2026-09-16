package store

// message_folders.go holds the folder feed: one item per message that carries a
// `routed_to`, addressed by the folder it was routed to.
//
// IT IS AN INDEX, NOT A COPY. `ObservedSince` reads by folder across siblings
// and watched sources, and routing is dynamic — a route table rewritten today
// cannot recover which folder a message reached last month — so the fact has to
// be written when `routed_to` is stamped. But the message itself already lives
// in `messages`, so this item carries a POINTER: the chat the message is in,
// plus the three attributes the feed FILTERS on. A reader narrows on those and
// then fetches only the messages it will render. Two copies of a body would be
// two bodies that can disagree, and `EnrichMessage` rewrites one of them.
//
// THE PARTITION IS THE FOLDER AND THE SORT KEY IS THE MESSAGE'S OWN. A shared
// key is what lets a folder read and a chat read name the same row, and what
// makes the observed cursor — a bare instant compared `> cursor` — mean the same
// thing on both. 5,531 of krons' 24,013 messages carry a routed_to, over 95
// folders.
//
// BOTH WRITERS OF routed_to WRITE HERE. `PutMessage` stamps it on an outbound
// row and on an inbound one an adapter already resolved; `MarkMessagesObserved`
// stamps it on a route miss the route table sent to `folder#observe`. A feed fed
// by only one of them would miss whichever half the reader cared about, and the
// second half is the one `ObservedSince`'s watched-source branch reads, because
// that branch asks for `is_observed=0` rows the observe path never writes.
//
// THE COUNT IS AN ATTRIBUTE OF THE FOLDER, NOT A SCAN. `MessageActivityByFolder`
// asks for a count over 95 folders at once, and reading 95 partitions to answer
// it is the whole-table read the move exists to remove. objstore.Increment moves
// the counter inside the same batch as the message, so the number cannot drift
// from the rows unless a batch commits, which is the one case neither can.

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

// counterAttrN is the attribute a folder's message count lives in. It is the
// same name Counters uses, because it counts the same way.
const counterAttrN = counterAttr

// The feed is read in message order, so its key carries the same ordered lead.
func init() { objstore.OrderedLead(MessageFolders, "", tsWidth) }

// folderFeedKey is the coordinate of one message inside one folder's feed.
func folderFeedKey(folder string, msgKey objstore.Key) (objstore.Key, error) {
	if folder == "" {
		return objstore.Key{}, errors.New("folder feed entry carries no folder, which is " +
			"its partition")
	}
	return objstore.Key{Partition: folder, Sort: msgKey.Sort}, nil
}

// folderCountKey is the coordinate of one folder's message count.
func folderCountKey(folder string) objstore.Key {
	return objstore.Key{Partition: OwnerRoutd, Sort: folder}
}

// folderFeedWrite renders the feed entry a message belongs in, and reports
// whether it belongs in one at all. A message with no routed_to is in no
// folder's feed and moves no counter.
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

// countFolderMessage moves one folder's count and commits the writes that
// EARNED the move in the same batch, so a counter cannot run ahead of its rows.
func (s *Store) countFolderMessage(ctx context.Context, folder string,
	writes []objstore.Write) error {
	// A refusal here is the message's own create-if-absent: the id is already
	// stored, so the row is not appended and the folder must not count it twice.
	_, err := s.tryCountFolderMessageBy(ctx, folder, 1, writes)
	return err
}

// errNoCounter is what a folder with no counter yet answers, so the seed runs
// once rather than on every message.
var errNoCounter = errors.New("the folder has no message counter")

// addFolderCount moves one folder's count by delta with `also` riding the same
// batch, and ANSWERS whether it committed.
//
// The verdict is returned rather than swallowed because the two callers mean
// different things by a refusal. The counter's own guard is the value this call
// read, and a companion write's condition is the only other thing that can
// refuse — so on the append path a false is the message's own create-if-absent
// saying the row is already there, which is exactly when the count must NOT
// move. Every other caller writes unconditional Puts, where a false is a fault.
// Dropping the bit made both read as success, which is the shape of BUGS L268.
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

// FolderMessageCounts answers how many messages each named folder has taken, for
// the folders that have taken any.
//
// ONE READ ANSWERS EVERY FOLDER, because the counts share a partition: the
// statement it replaces grouped over the whole table and this reads 95 items. A
// folder absent from the answer has taken none, which is what lets the caller
// tell "never used" from "used and counted zero".
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

// FolderLastMessageAt answers when one folder last took a message, or empty when
// it has taken none. It is the newest key of the folder's feed, which is one
// item, not a scan.
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

// MarkMessagesObserved records messages as ambient context for a folder — the
// poll loop calls it when the resolved route target is `folder#observe`.
//
// It stamps the message AND the feed, in one batch per message, because the feed
// is what the ambient read asks and the message is what it renders. A message
// already routed somewhere else moves: its old feed entry goes and the old
// folder's count comes down, so a folder never counts a message it no longer
// holds.
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
			// Already counted here, so only the flag moves.
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
	// Two writes per message, and the counter rides the same batch, so the chunk
	// is sized in MESSAGES rather than in writes.
	for len(fresh) > 0 {
		n := min(len(fresh), objstore.MaxBatchActions-2)
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

// countFolderMessageBy is countFolderMessage for a batch that earns more than
// one.
// countFolderMessageBy is the door for a caller whose writes carry no condition,
// so a refusal is a fault and is raised.
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

// tryCountFolderMessageBy is countFolderMessageBy for the caller that ASKED a
// question, and it seeds the counter the first time a folder takes a message.
//
// The counter has to exist before the first increment — objstore.Increment
// refuses an item that is not there, because "a counter with no zero is a
// modelling gap rather than a value to invent" — so a refusal that names a
// missing item seeds it and the call retries once. The seed is a
// create-if-absent, so two chats writing into one new folder at the same moment
// both end up counted.
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
// window will hold.
//
// The feed's own filters — observed, bot, sender — narrow to the rows that
// qualify, so the only drop left in the reader is an empty body, which is an
// attachment-only row. Three for one covers that with room and still bounds the
// read at a few hundred items on the busiest folder.
const observedFetchFactor = 3

// ObservedSince answers the trailing window of observed messages for a folder
// whose position is strictly after cursor, oldest first.
//
// THE CURSOR IS A BARE INSTANT and stays one. `sessions.observed_cursor` is
// compared `> cursor` and the read takes the NEWEST window, so a cut can only
// fall at the old end — the newest rendered message is always whole. A bound of
// "just past the instant" is exactly what `timestamp > cursor` meant, so the
// cursor keeps its width and the move does not reach `sessions`.
//
// Isolated scheduled runs are excluded. They get their own container and their
// own session precisely so a folder's crons never enter the conversation the
// people in that chat are having (BUGS L54), but their rows land routed_to the
// folder with is_observed=1 — so the ambient window served every cron prompt
// back to every topic in the folder on its next turn, which re-opened that hole
// through a second door.
func (s *Store) ObservedSince(ctx context.Context, folder, cursor string, maxMsgs,
	maxChars int, watched []string) ([]core.Message, error) {
	if maxMsgs <= 0 || maxChars <= 0 {
		return nil, nil
	}
	key := objstore.KeyPredicate{}
	if cursor != "" {
		key = objstore.KeyPredicate{Op: objstore.GeKey, Value: cursor + string(KeySep[0]+1)}
	}
	// The observer's own folder contributes the rows the route table sent to
	// `folder#observe`; a watched source contributes its PRIMARY deliveries,
	// which is the observe_group ambient join and is why the two halves ask for
	// opposite values of the same flag.
	reads := []struct {
		partition string
		observed  objstore.Op
	}{{partition: folder, observed: objstore.Present}}
	for _, w := range watched {
		reads = append(reads, struct {
			partition string
			observed  objstore.Op
		}{partition: w, observed: objstore.Absent})
	}
	var found []objstore.Item
	for _, r := range reads {
		items, err := s.Obj().Query(ctx, objstore.Query{
			Entity: MessageFolders, Partition: r.partition,
			Key: key,
			Where: []objstore.Cmp{
				{Attr: attrFeedObserved, Op: r.observed},
				{Attr: attrFeedBot, Op: objstore.Absent},
			},
			Descending: true, Limit: maxMsgs * observedFetchFactor,
		})
		if err != nil {
			return nil, fmt.Errorf("read %s's ambient window: %w", r.partition, err)
		}
		found = append(found, items...)
	}
	// The halves arrive newest-first each; one merge puts the whole window in
	// that order, which is the order the caps are applied in.
	sortItemsDescending(found)
	out := make([]core.Message, 0, maxMsgs)
	chars := 0
	for _, it := range found {
		if len(out) >= maxMsgs {
			break
		}
		if core.IsIsolatedSender(it.Attrs[attrFeedSender]) {
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

// messageBehind fetches the message one feed entry points at.
//
// A pointer with no message is NOT an error: a chat's errored rows are deleted
// with their index entries, and a feed read that raced that delete sees the
// entry a moment longer. It drops out of the window, which is what a deleted
// message means.
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
