package store

// pane_sessions.go holds every read and write of `pane_sessions`: one item per
// Slack assistant pane, opened when a user starts one and refreshed while it
// stays open.
//
// The partition is a constant and the sort key the (team, user, thread)
// triple, the only coordinate every write can name: routd's POST /v1/pane
// hands over exactly those three fields. The two channel-addressed reads scan
// the partition (81 items fleet-wide, all on marinade, 2026-09-07; no
// drainage). A channel names MORE THAN ONE pane (one marinade channel covers
// 31), so the channel read is a MAX and opened_at IS ORDERED: 8 of the 81 rows
// carried a 29-byte instant against 30 (BUGS L97), and the import re-renders
// them.

import (
	"context"
	"fmt"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// panePartition is the one partition every pane lives in.
const panePartition = "pane"

// paneKeySep separates the three fields of the sort key. Slack ids and a
// thread_ts never carry it; paneKey refuses one that does.
const paneKeySep = "/"

// The attributes an item carries. team_id, user_id and thread_ts ARE the sort
// key.
const (
	attrPaneChannel  = "channel_id"
	attrPaneContext  = "context_jid"
	attrPaneOpenedAt = "opened_at"
)

// The ordered declaration is what makes "the newest pane in this channel" mean
// the newest one (BUGS L97).
func init() { objstore.OrderedAttr(PaneSessions, attrPaneOpenedAt, tsWidth) }

// PaneSession is a Slack assistant pane session, one per (team, user,
// thread_ts). ChannelID is the channel where the pane lives; ContextJID is the
// workspace channel the user is viewing while the pane is open (may be empty).
type PaneSession struct {
	TeamID     string
	UserID     string
	ThreadTS   string
	ChannelID  string
	ContextJID string
	OpenedAt   string
}

// paneKey is the coordinate of one pane. A field carrying the separator is
// refused: two panes would address one item.
func paneKey(teamID, userID, threadTS string) (objstore.Key, error) {
	for name, v := range map[string]string{
		"team_id": teamID, "user_id": userID, "thread_ts": threadTS,
	} {
		if v == "" {
			return objstore.Key{}, fmt.Errorf("pane %s is empty", name)
		}
		if strings.Contains(v, paneKeySep) {
			return objstore.Key{}, fmt.Errorf("pane %s %q carries %q, which separates the "+
				"three fields of the key", name, v, paneKeySep)
		}
	}
	return objstore.Key{
		Partition: panePartition,
		Sort:      teamID + paneKeySep + userID + paneKeySep + threadTS,
	}, nil
}

// paneFrom renders one stored item as the row a caller reads.
func paneFrom(it objstore.Item) PaneSession {
	p := PaneSession{
		ChannelID:  it.Attrs[attrPaneChannel],
		ContextJID: it.Attrs[attrPaneContext],
		OpenedAt:   it.Attrs[attrPaneOpenedAt],
	}
	parts := strings.SplitN(it.Key.Sort, paneKeySep, 3)
	if len(parts) == 3 {
		p.TeamID, p.UserID, p.ThreadTS = parts[0], parts[1], parts[2]
	}
	return p
}

// OpenPane records that a pane is open, and in which channel. opened_at is set
// ON CREATION ONLY and the channel is refreshed on every call. Two writes: a
// Put would erase the instant the pane opened and the context the user set, so
// the create-if-absent goes first and its refusal selects the refresh.
func (s *Store) OpenPane(teamID, userID, threadTS, channelID string) error {
	key, err := paneKey(teamID, userID, threadTS)
	if err != nil {
		return err
	}
	ctx := context.Background()
	opened, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Put, Entity: PaneSessions, Key: key,
		Attrs: map[string]string{
			attrPaneChannel:  channelID,
			attrPaneOpenedAt: core.NowTS(),
		},
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil {
		return fmt.Errorf("open the pane in %s: %w", channelID, err)
	}
	if opened {
		return nil
	}
	if err := s.WriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: PaneSessions, Key: key,
		Attrs: map[string]string{attrPaneChannel: channelID},
	}); err != nil {
		return fmt.Errorf("move the pane to %s: %w", channelID, err)
	}
	return nil
}

// PaneInChannel answers which pane a channel is showing: the newest one, when
// the channel names more than one. (zero, false) when it names none. The
// winner is picked here because the sort key is the triple and the order
// wanted is the instant.
func (s *Store) PaneInChannel(channelID string) (PaneSession, bool) {
	items, err := s.panesInChannel(context.Background(), channelID)
	if err != nil || len(items) == 0 {
		return PaneSession{}, false
	}
	newest := items[0]
	for _, it := range items[1:] {
		if it.Attrs[attrPaneOpenedAt] > newest.Attrs[attrPaneOpenedAt] {
			newest = it
		}
	}
	return paneFrom(newest), true
}

// panesInChannel is the one read behind both channel-addressed operations.
func (s *Store) panesInChannel(ctx context.Context, channelID string) ([]objstore.Item, error) {
	return s.Obj().Query(ctx, objstore.Query{
		Entity: PaneSessions, Partition: panePartition,
		Where: []objstore.Cmp{{Attr: attrPaneChannel, Op: objstore.Eq, Value: channelID}},
	})
}

// SetPaneContext records the workspace channel the user is viewing while the
// pane is open, addressed by the triple. An empty contextJID clears it. A pane
// that is not there is NOT an error: slakd opens the pane and routd sets the
// context, and the two arrive over different transports.
func (s *Store) SetPaneContext(teamID, userID, threadTS, contextJID string) error {
	key, err := paneKey(teamID, userID, threadTS)
	if err != nil {
		return err
	}
	// A refusal is a pane slakd has not opened yet.
	if _, err := s.TryWriteEntities(context.Background(), nil,
		paneContextWrite(key, contextJID)); err != nil {
		return fmt.Errorf("set the pane's context: %w", err)
	}
	return nil
}

// SetPaneContextByChannel is SetPaneContext reached by channel id, the shape
// routd's POST /v1/pane takes when slakd hands it a {channel_id, jid} and holds
// no triple. It writes EVERY pane the channel names (a channel covering 31
// panes is the live case); a channel that names none is a no-op.
func (s *Store) SetPaneContextByChannel(channelID, contextJID string) error {
	ctx := context.Background()
	items, err := s.panesInChannel(ctx, channelID)
	if err != nil {
		return fmt.Errorf("read the panes in %s: %w", channelID, err)
	}
	writes := make([]objstore.Write, 0, len(items))
	for _, it := range items {
		writes = append(writes, paneContextWrite(it.Key, contextJID))
	}
	if _, err := s.applyInChunks(ctx, "set the context of the panes in "+channelID, writes); err != nil {
		return err
	}
	return nil
}

// paneContextWrite is the single renderer of the context change. An empty
// context REMOVES the attribute rather than writing "": absent is the only
// null this model has.
func paneContextWrite(key objstore.Key, contextJID string) objstore.Write {
	w := objstore.Write{Op: objstore.Update, Entity: PaneSessions, Key: key}
	if contextJID == "" {
		w.Remove = []string{attrPaneContext}
		return w
	}
	w.Attrs = map[string]string{attrPaneContext: contextJID}
	return w
}
