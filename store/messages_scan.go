package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/kronael/arizuko/objstore"
)

// scanPage is how many rows one read of a scan asks the store for.
const scanPage = 1000

// ScannedMessage is one message as a search reads it. Sort is the message's
// own position in its chat and Timestamp is that position's instant.
type ScannedMessage struct {
	ChatJID   string
	Sort      string
	Timestamp time.Time
	Sender    string
	RoutedTo  string
	Content   string
	FromMe    bool
	BotMsg    bool
}

var scanProjection = []string{
	attrMsgContent, attrMsgSender, attrMsgRoutedTo, attrMsgFromMe, attrMsgBot,
}

func scannedFrom(it objstore.Item) (ScannedMessage, error) {
	stamp, _, ok := MsgCoord(it.Key.Sort)
	if !ok {
		return ScannedMessage{}, fmt.Errorf("%q in %s is not a message position, so a search "+
			"has no instant to order or filter it by", it.Key.Sort, it.Key.Partition)
	}
	ts, err := ReadMessageInstant(stamp)
	if err != nil {
		return ScannedMessage{}, fmt.Errorf("the position %q in %s: %w",
			it.Key.Sort, it.Key.Partition, err)
	}
	return ScannedMessage{
		ChatJID:   it.Key.Partition,
		Sort:      it.Key.Sort,
		Timestamp: ts,
		Sender:    it.Attrs[attrMsgSender],
		RoutedTo:  it.Attrs[attrMsgRoutedTo],
		Content:   it.Attrs[attrMsgContent],
		FromMe:    it.Attrs[attrMsgFromMe] == flagSet,
		BotMsg:    it.Attrs[attrMsgBot] == flagSet,
	}, nil
}

func positionInstant(sortKey string) string {
	stamp, _, _ := MsgCoord(sortKey)
	return stamp
}

func scanCursor(before time.Time) string {
	if before.IsZero() {
		return ""
	}
	return msgStampAt(before)
}

func (s *Store) pageBefore(ctx context.Context, entity, partition, cursor string,
	limit int, project []string) ([]objstore.Item, error) {
	key := objstore.KeyPredicate{}
	if cursor != "" {
		key = objstore.KeyPredicate{Op: objstore.LtKey, Value: cursor}
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: entity, Partition: partition, Key: key,
		Descending: true, Limit: limit, Project: project,
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s/%s before %q: %w", entity, partition, cursor, err)
	}
	return items, nil
}

// ChatScan reads one chat's messages newest first. It holds a position and
// no message.
type ChatScan struct {
	s      *Store
	jid    string
	cursor string
	page   int
	done   bool
	read   int
}

// ScanChat starts a scan of one chat at the messages strictly older than
// before, or at the newest message when before is zero.
func (s *Store) ScanChat(jid string, before time.Time) *ChatScan {
	return &ChatScan{s: s, jid: jid, cursor: scanCursor(before), page: scanPage}
}

// Read is how many message rows the scan has read from the store.
func (c *ChatScan) Read() int { return c.read }

// Next answers the next messages, newest first, and none when the chat is
// exhausted. A page never ends inside an instant: the rows of the last instant
// of a full read are left for the next one.
func (c *ChatScan) Next(ctx context.Context) ([]ScannedMessage, error) {
	var rows []ScannedMessage
	for !c.done {
		items, err := c.s.pageBefore(ctx, Messages, c.jid, c.cursor, c.page, scanProjection)
		if err != nil {
			return nil, err
		}
		c.read += len(items)
		c.done = len(items) < c.page
		for _, it := range items {
			row, err := scannedFrom(it)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
		if c.done {
			break
		}
		c.cursor = rows[len(rows)-1].Sort
		cut := len(rows)
		for cut > 0 && rows[cut-1].Timestamp.Equal(rows[len(rows)-1].Timestamp) {
			cut--
		}
		if cut > 0 {
			c.cursor = rows[cut-1].Sort
			return rows[:cut], nil
		}
	}
	return rows, nil
}

type folderFeed struct {
	folder string
	cursor string
	buf    []objstore.Item
	done   bool
}

// FolderScan reads the messages routed to a folder subtree newest first. It
// holds one position and at most one page of pointers for each folder.
type FolderScan struct {
	s     *Store
	feeds []*folderFeed
	page  int
	read  int
}

// ScanFolders starts a scan of scope and every folder below it at the messages
// strictly older than before, or at the newest when before is zero. An empty
// scope is every folder. A message with no routed_to is in no folder.
func (s *Store) ScanFolders(ctx context.Context, scope string,
	before time.Time) (*FolderScan, error) {
	key := objstore.KeyPredicate{}
	if scope != "" {
		key = objstore.KeyPredicate{Op: objstore.PrefixKey, Value: scope, Sep: "/"}
	}
	counts, err := s.Obj().Query(ctx, objstore.Query{
		Entity: MessageFolderCounts, Partition: OwnerRoutd, Key: key,
		Project: []string{counterAttrN},
	})
	if err != nil {
		return nil, fmt.Errorf("read the folders under %q that hold messages: %w", scope, err)
	}
	scan := &FolderScan{s: s, page: scanPage}
	for _, it := range counts {
		scan.feeds = append(scan.feeds,
			&folderFeed{folder: it.Key.Sort, cursor: scanCursor(before)})
	}
	return scan, nil
}

// Read is how many message rows the scan has read from the store. A chat
// whose messages go to more than one folder costs rows the scan does not
// return.
func (f *FolderScan) Read() int { return f.read }

// Next answers the next messages, newest first, and none when every folder is
// exhausted. A page never ends inside an instant.
func (f *FolderScan) Next(ctx context.Context) ([]ScannedMessage, error) {
	for {
		pointers, err := f.pointers(ctx)
		if err != nil || len(pointers) == 0 {
			return nil, err
		}
		rows, err := f.messagesBehind(ctx, pointers)
		if err != nil || len(rows) > 0 {
			return rows, err
		}
	}
}

func (f *FolderScan) pointers(ctx context.Context) ([]objstore.Item, error) {
	var out []objstore.Item
	for {
		feed, err := f.newestFeed(ctx)
		if err != nil {
			return nil, err
		}
		if feed == nil {
			return out, nil
		}
		head := feed.buf[0]
		if len(out) >= f.page &&
			positionInstant(head.Key.Sort) != positionInstant(out[len(out)-1].Key.Sort) {
			return out, nil
		}
		out = append(out, head)
		feed.buf = feed.buf[1:]
	}
}

func (f *FolderScan) newestFeed(ctx context.Context) (*folderFeed, error) {
	var newest *folderFeed
	for _, feed := range f.feeds {
		if len(feed.buf) == 0 && !feed.done {
			items, err := f.s.pageBefore(ctx, MessageFolders, feed.folder, feed.cursor,
				f.page, []string{attrFeedChatJID})
			if err != nil {
				return nil, err
			}
			feed.buf = items
			feed.done = len(items) < f.page
			if len(items) > 0 {
				feed.cursor = items[len(items)-1].Key.Sort
			}
		}
		if len(feed.buf) > 0 &&
			(newest == nil || feed.buf[0].Key.Sort > newest.buf[0].Key.Sort) {
			newest = feed
		}
	}
	return newest, nil
}

type chatSpan struct {
	newest string
	oldest string
	want   map[string]bool
}

func (f *FolderScan) messagesBehind(ctx context.Context,
	pointers []objstore.Item) ([]ScannedMessage, error) {
	spans := map[string]*chatSpan{}
	var chats []string
	for _, p := range pointers {
		chat := p.Attrs[attrFeedChatJID]
		if chat == "" {
			return nil, fmt.Errorf("folder feed entry %s/%s names no chat, so the message it "+
				"points at cannot be read", p.Key.Partition, p.Key.Sort)
		}
		span := spans[chat]
		if span == nil {
			span = &chatSpan{newest: p.Key.Sort, want: map[string]bool{}}
			spans[chat] = span
			chats = append(chats, chat)
		}
		span.oldest = p.Key.Sort
		span.want[p.Key.Sort] = true
	}
	var rows []ScannedMessage
	for _, chat := range chats {
		found, err := f.spanRows(ctx, chat, spans[chat])
		if err != nil {
			return nil, err
		}
		rows = append(rows, found...)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Sort > rows[j].Sort })
	return rows, nil
}

func (f *FolderScan) spanRows(ctx context.Context, chat string,
	span *chatSpan) ([]ScannedMessage, error) {
	key := objstore.KeyPredicate{Op: objstore.LeKey, Value: span.newest}
	var rows []ScannedMessage
	for {
		items, err := f.s.Obj().Query(ctx, objstore.Query{
			Entity: Messages, Partition: chat, Key: key,
			Descending: true, Limit: f.page, Project: scanProjection,
		})
		if err != nil {
			return nil, fmt.Errorf("read %s from %q down to %q: %w",
				chat, span.newest, span.oldest, err)
		}
		f.read += len(items)
		for _, it := range items {
			if it.Key.Sort < span.oldest {
				return rows, nil
			}
			if !span.want[it.Key.Sort] {
				continue
			}
			row, err := scannedFrom(it)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
		if len(items) < f.page {
			return rows, nil
		}
		key = objstore.KeyPredicate{Op: objstore.LtKey, Value: items[len(items)-1].Key.Sort}
	}
}
