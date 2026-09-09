package store

// chat_proactive.go holds every read and write of `chat_proactive`: one item per
// chat that has ever spoken unprompted, carrying the instant it last did.
//
// THE PARTITION IS OwnerRoutd AND THE SORT KEY IS THE JID, for the reason
// chats.go gives and with the same measurements behind it: the cooldown is read
// per candidate chat by jid on the proactive scan, written by jid, and belongs
// to a chat rather than to a folder. There is exactly one read that names no jid
// — dashd's cooldown panel, which asks for the most recent fifty — and it has no
// partition to name under any other choice.
//
// IT IS A SEPARATE ENTITY FROM `chats` and it stays one. The two are keyed
// alike, and folding them would look like a simplification: one item per chat,
// one fewer table. It is not. `chats` is written on the hot path — every inbound
// message advances a cursor — and this is written once a day at most, by the one
// path that fires a turn nobody asked for. Folding them puts the cooldown's
// write in contention with the cursor's, and puts a field the proactive scan
// reads into the item the dispatch loop rewrites. Separate items contend
// nowhere, which is the property a document model buys.
//
// proactive_last_fired_at IS ORDERED. dashd's panel reads the most recently
// fired chats first, so the width rule applies for the same reason it applies to
// the two cursors (BUGS L97). The table holds no rows on any of the three live
// instances (2026-09-09), so the declaration is what keeps it right rather than
// what fixes it.

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// AttrProactiveFiredAt is when a chat last spoke unprompted. It is exported
// because the move that carries these rows names the instant it re-renders.
const AttrProactiveFiredAt = "proactive_last_fired_at"

// The ordered declaration is what makes the cooldown panel an ordered read.
func init() { objstore.OrderedAttr(ChatProactive, AttrProactiveFiredAt, tsWidth) }

// ChatCooldown is one chat's last unprompted message.
type ChatCooldown struct {
	JID     string
	FiredAt string
}

// proactiveKey is the coordinate of one chat's cooldown.
func proactiveKey(jid string) (objstore.Key, error) {
	if jid == "" {
		return objstore.Key{}, fmt.Errorf("proactive cooldown carries no jid, " +
			"which is its coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: jid}, nil
}

// ProactiveLastFired answers when a chat last spoke unprompted. The zero time is
// "never", which is what the mandatory cooldown starts from.
func (s *Store) ProactiveLastFired(jid string) time.Time {
	key, err := proactiveKey(jid)
	if err != nil {
		return time.Time{}
	}
	it, ok, err := s.itemAt(context.Background(), ChatProactive, key)
	if err != nil || !ok {
		return time.Time{}
	}
	t, perr := time.Parse(time.RFC3339Nano, it.Attrs[AttrProactiveFiredAt])
	if perr != nil {
		return time.Time{}
	}
	return t
}

// RecordProactiveFireInTx stamps a chat's cooldown on a transaction the CALLER
// owns.
//
// The transaction is the whole point and it is not this file's: routd appends
// the synthetic inbound row and stamps this in ONE commit, so a crash before the
// run is dispatched leaves the cooldown SET — at worst one missed turn, never a
// double fire. The message is still a row of columns and this is an item, and
// they commit together because the entity write joins the caller's transaction.
func (s *Store) RecordProactiveFireInTx(ctx context.Context, tx *sql.Tx, jid string, at time.Time) error {
	key, err := proactiveKey(jid)
	if err != nil {
		return err
	}
	attrs := map[string]string{AttrProactiveFiredAt: core.TS(at)}
	// Create-if-absent, then merge on the refusal. A bare Put would be a
	// whole-item replace: it costs nothing while the item carries one attribute
	// and drops the second one the day this entity grows a field.
	created, err := s.writeVia(ctx, tx, nil, objstore.Write{
		Op: objstore.Put, Entity: ChatProactive, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil {
		return fmt.Errorf("stamp the proactive cooldown of %s: %w", jid, err)
	}
	if created {
		return nil
	}
	if _, err := s.writeVia(ctx, tx, nil, objstore.Write{
		Op: objstore.Update, Entity: ChatProactive, Key: key, Attrs: attrs,
	}); err != nil {
		return fmt.Errorf("move the proactive cooldown of %s: %w", jid, err)
	}
	return nil
}

// CountProactiveChats answers how many chats carry a cooldown at all, whether or
// not it has ever fired. `arizuko migrate-hot` reads it back to check that every
// row travelled.
func (s *Store) CountProactiveChats(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: ChatProactive, Partition: OwnerRoutd,
	})
	if err != nil {
		return 0, fmt.Errorf("read the proactive cooldowns: %w", err)
	}
	return len(items), nil
}

// ProactiveCooldowns answers the chats that have spoken unprompted, most recent
// first, up to limit.
//
// The order runs HERE because a query orders on the sort key and the sort key is
// the jid. The statement it replaces sorted in the engine over a column with no
// index, so the rows were already all read.
func (s *Store) ProactiveCooldowns(limit int) ([]ChatCooldown, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: ChatProactive, Partition: OwnerRoutd,
		Where: []objstore.Cmp{{Attr: AttrProactiveFiredAt, Op: objstore.Present}},
	})
	if err != nil {
		return nil, fmt.Errorf("read the proactive cooldowns: %w", err)
	}
	out := make([]ChatCooldown, 0, len(items))
	for _, it := range items {
		out = append(out, ChatCooldown{JID: it.Key.Sort, FiredAt: it.Attrs[AttrProactiveFiredAt]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FiredAt > out[j].FiredAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
