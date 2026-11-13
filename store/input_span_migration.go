package store

import (
	"context"
	"fmt"

	"github.com/kronael/arizuko/objstore"
)

// MigrateChatInputSpan writes the input span (chats.go) once, onto each chat
// holding user rows past its cursor from before appends wrote it.
func (s *Store) MigrateChatInputSpan(ctx context.Context) error {
	marker := objstore.Key{Partition: OwnerRoutd, Sort: "chat-input-span"}
	items, err := s.obj.Query(ctx, objstore.Query{
		Entity: Counters, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: marker.Sort},
	})
	if err != nil || len(items) != 0 {
		return err
	}
	chats, err := s.allChats(ctx)
	if err != nil {
		return err
	}
	for _, chat := range chats {
		first, err := s.inputPast(ctx, chat.Key.Sort, chat.Attrs[AttrChatAgentCursor], false)
		if err != nil {
			return err
		}
		if first == "" {
			continue
		}
		last, err := s.inputPast(ctx, chat.Key.Sort, chat.Attrs[AttrChatAgentCursor], true)
		if err != nil {
			return err
		}
		if err := s.WriteEntities(ctx, nil, objstore.Write{
			Op: objstore.Merge, Entity: Chats, Key: chat.Key,
			Attrs: map[string]string{attrChatInputFirst: first, attrChatInputLast: last},
		}); err != nil {
			return fmt.Errorf("write the input span of %s: %w", chat.Key.Sort, err)
		}
	}
	return s.WriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Put, Entity: Counters, Key: marker,
		Attrs: map[string]string{"done": "1"}, Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
}

// inputPast answers the key of the oldest, or with newest the newest, user row
// of a chat past its cursor; empty when there is none.
func (s *Store) inputPast(ctx context.Context, jid, cursor string, newest bool) (string, error) {
	key := objstore.KeyPredicate{}
	if cursor != "" {
		key = objstore.KeyPredicate{Op: objstore.GtKey, Value: cursor}
	}
	items, err := s.obj.Query(ctx, objstore.Query{
		Entity: Messages, Partition: jid, Key: key, Descending: newest, Limit: 1,
		Where:   []objstore.Cmp{{Attr: attrMsgBot, Op: objstore.Absent}},
		Project: []string{},
	})
	if err != nil {
		return "", fmt.Errorf("read the input of %s: %w", jid, err)
	}
	if len(items) == 0 {
		return "", nil
	}
	return items[0].Key.Sort, nil
}
