package store

// messages_index.go is the projection of `messages` that ranked search is built
// from: a message's coordinate, the content a search matches on, and the three
// attributes it narrows by.
//
// THE INDEX ITSELF IS NOT HERE. It is derived, it is memory, and it belongs to
// the daemon that holds the messages (routd/msgsearch, spec 6/42 §8); what
// belongs here is the READ it derives from, because the coordinate and the
// attribute names are this package's declaration and a second spelling of them
// is a second thing that can be wrong about where a message's content lives.
//
// IT IS A PROJECTION AND NOT A COPY. A message carries eighteen attributes and
// the index reads five; `reply_to_text`, `attachments` and `link_context` are
// the three that run to kilobytes, and a boot walk over 24,296 messages that
// pulled them would move the whole table to fill an index that reads none of
// them.

import (
	"context"
	"fmt"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// IndexedMessage is one message as a search index holds it. Sort is the
// message's own position — the fixed-width instant, the separator and the id —
// so a document is addressed by the coordinate the store keeps the message at
// and by nothing of its own.
//
// Timestamp is that position's instant, READ HERE. Both doors below build it,
// so an index takes a row whose instant is already an instant and has nothing
// left to refuse; the alternative is an index that parses, and a parse that can
// fail after the write has committed reports a stored message as a failure.
type IndexedMessage struct {
	ChatJID   string
	Sort      string
	Timestamp time.Time
	Sender    string
	RoutedTo  string
	Content   string
	FromMe    bool
	BotMsg    bool
}

// indexProjection is what the walk asks each item for. The key comes back
// whatever a query projects, which is where the chat and the position are.
var indexProjection = []string{
	attrMsgContent, attrMsgSender, attrMsgRoutedTo, attrMsgFromMe, attrMsgBot,
}

// IndexedFrom renders the message a writer is holding as the row an index
// takes, and refuses one whose instant is not a position — the same refusal the
// write itself makes, so a caller that indexes what it wrote cannot index it
// under a coordinate the store does not use.
func IndexedFrom(m core.Message) (IndexedMessage, error) {
	key, err := messageKeyOf(m)
	if err != nil {
		return IndexedMessage{}, err
	}
	return IndexedMessage{
		ChatJID:   key.Partition,
		Sort:      key.Sort,
		Timestamp: m.Timestamp.UTC(),
		Sender:    m.Sender,
		RoutedTo:  m.RoutedTo,
		Content:   m.Content,
		FromMe:    m.FromMe,
		BotMsg:    m.BotMsg,
	}, nil
}

// MessagesToIndex answers one chat's messages, projected to what an index
// holds. The whole partition: an index over part of a chat would answer "no
// such message" about the rest.
//
// A POSITION THAT IS NOT AN INSTANT STOPS THE WALK. The row cannot be filtered
// by time or ordered against another, and skipping it would leave an index that
// looks complete and answers "no such message" about whatever it dropped. The
// error names the chat and the position, which is what an operator needs to fix
// the row.
func (s *Store) MessagesToIndex(ctx context.Context, jid string) ([]IndexedMessage, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Messages, Partition: jid, Project: indexProjection,
	})
	if err != nil {
		return nil, fmt.Errorf("read %s's messages for the search index: %w", jid, err)
	}
	out := make([]IndexedMessage, len(items))
	for i, it := range items {
		stamp, _, ok := MsgCoord(it.Key.Sort)
		if !ok {
			return nil, fmt.Errorf("%q in %s is not a message position, so the search index "+
				"has no instant to order or filter it by", it.Key.Sort, jid)
		}
		ts, err := ReadMessageInstant(stamp)
		if err != nil {
			return nil, fmt.Errorf("the position %q in %s: %w", it.Key.Sort, jid, err)
		}
		out[i] = IndexedMessage{
			ChatJID:   it.Key.Partition,
			Sort:      it.Key.Sort,
			Timestamp: ts,
			Sender:    it.Attrs[attrMsgSender],
			RoutedTo:  it.Attrs[attrMsgRoutedTo],
			Content:   it.Attrs[attrMsgContent],
			FromMe:    it.Attrs[attrMsgFromMe] == flagSet,
			BotMsg:    it.Attrs[attrMsgBot] == flagSet,
		}
	}
	return out, nil
}
