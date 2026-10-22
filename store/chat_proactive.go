package store

// chat_proactive.go holds every read and write of `chat_proactive`: one item per
// chat that has ever spoken unprompted, carrying the instant it last did.
//
// The partition is OwnerRoutd and the sort key the jid (chats.go's reason). It
// is a SEPARATE entity from `chats` and stays one: `chats` is written on the
// hot path and this once a day at most, so folding them would put the
// cooldown's write in contention with the cursor's. proactive_last_fired_at IS
// ORDERED: dashd's panel reads the most recently fired first (BUGS L97).

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// AttrProactiveFiredAt is when a chat last spoke unprompted. Exported because
// the move that carries these rows names the instant it re-renders.
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

// proactiveFireWrite renders the ONE write a cooldown stamp is. A Merge, not a
// Put: a Put drops the second attribute the day this entity grows one, and one
// action on one item can ride the batch that appends the synthetic row.
func proactiveFireWrite(jid string, at time.Time) (objstore.Write, error) {
	key, err := proactiveKey(jid)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{
		Op: objstore.Merge, Entity: ChatProactive, Key: key,
		Attrs: map[string]string{AttrProactiveFiredAt: core.TS(at)},
	}, nil
}

// RecordProactiveFire stamps a chat's cooldown alone. FireProactive does NOT
// call it: the stamp and the synthetic inbound row go in one batch there, so a
// crash before dispatch leaves the cooldown SET; at worst one missed turn,
// never a double fire.
func (s *Store) RecordProactiveFire(ctx context.Context, jid string, at time.Time) error {
	w, err := proactiveFireWrite(jid, at)
	if err != nil {
		return err
	}
	if err := s.WriteEntities(ctx, nil, w); err != nil {
		return fmt.Errorf("stamp the proactive cooldown of %s: %w", jid, err)
	}
	return nil
}

// ProactiveCooldowns answers the chats that have spoken unprompted, most recent
// first, up to limit. The order runs here because the sort key is the jid.
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
