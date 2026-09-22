package store

// messages_index.go is the projection of `messages` that ranked search is built
// from. THE INDEX ITSELF IS NOT HERE: it is derived, it is memory, and it
// belongs to routd/msgsearch (spec 6/42-dynamo-over-postgres §8); the read is here because the
// coordinate and the attribute names are this package's declaration. It is a
// PROJECTION: `reply_to_text`, `attachments` and `link_context` run to
// kilobytes, and a boot walk over 24,296 messages must not pull them.

import (
	"context"
	"fmt"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// IndexedMessage is one message as a search index holds it. Sort is the
// message's own position, so a document is addressed by the coordinate the
// store keeps it at. Timestamp is that position's instant, READ HERE, so an
// index takes a row with nothing left to refuse: a parse that fails after the
// write has committed reports a stored message as a failure.
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

// indexProjection is what the walk asks each item for; the key comes back
// whatever a query projects.
var indexProjection = []string{
	attrMsgContent, attrMsgSender, attrMsgRoutedTo, attrMsgFromMe, attrMsgBot,
}

// IndexedFrom renders the message a writer is holding as the row an index
// takes, and refuses one whose instant is not a position: the same refusal the
// write itself makes.
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
// holds: the whole partition. A POSITION THAT IS NOT AN INSTANT STOPS THE WALK:
// skipping it would leave an index that looks complete and answers "no such
// message" about whatever it dropped.
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
