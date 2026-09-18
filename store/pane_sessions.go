package store

// pane_sessions.go holds every read and write of `pane_sessions`: one item per
// Slack assistant pane, opened when a user starts one and refreshed while it
// stays open.
//
// THE PARTITION IS A CONSTANT AND THE SORT KEY IS THE (team, user, thread)
// TRIPLE, which is the primary key the table already had. Three of the five
// operations address that triple and hold nothing else — routd's POST /v1/pane
// hands over exactly those three fields (routd/server.go:723) — so it is the
// only coordinate every write can name.
//
// THE OTHER TWO ADDRESS A CHANNEL, and they become filtered whole-partition
// reads: PaneInChannel and SetPaneContextByChannel receive a Slack channel id
// and no team. A channel is not the partition for that reason, and it could not
// be one anyway — the two triple-keyed writes arrive with no channel at all.
//
// WHAT THE CONSTANT COSTS is that both channel reads examine every pane in the
// database. Measured 2026-09-07 across all three live snapshots: 81 items, all
// of them on marinade, over 7 distinct channels; krons and sloth hold none.
// There is no drainage, so the bound is the number of distinct panes ever
// opened, and that is the honest cost — this is not `session_log`, which grows
// one item per turn, but it does not shrink either.
//
// A CHANNEL NAMES MORE THAN ONE PANE, so the channel read is a MAX and not a
// point lookup. The table's own comment says a DM channel is 1:1 with a pane;
// the live rows disagree — 79 of marinade's 81 carry a workspace channel id and
// five of those ids cover 79 rows, one of them 31 — so `ORDER BY opened_at DESC
// LIMIT 1` was doing real work and the reader keeps doing it.
//
// WHICH MAKES opened_at AN ORDERED ATTRIBUTE, declared, and that is a bug fix
// the move forces rather than chooses. 8 of marinade's 81 rows store 29 bytes
// against the other 73's 30 — a nanosecond fraction that lost its trailing zero
// — so byte order already disagrees with time order inside those seconds, which
// is BUGS L97 on the column the channel read picks its winner by. The
// declaration refuses a short value on every driver, and the import re-renders
// the eight through core.TS (import.go).

import (
	"context"
	"fmt"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// panePartition is the one partition every pane lives in. The file header says
// why it is a constant and what that costs.
const panePartition = "pane"

// paneKeySep separates the three fields of the sort key. Slack team and user
// ids are upper-case alphanumerics and a thread_ts is `<seconds>.<micros>`, so
// none of the three carries it — measured on all 81 live rows, and keyed
// deliberately rather than trusted (paneKey refuses one that does).
const paneKeySep = "/"

// The attributes an item carries. team_id, user_id and thread_ts are NOT among
// them: they ARE the sort key, and storing the coordinate twice is two facts
// that can disagree.
const (
	attrPaneChannel  = "channel_id"
	attrPaneContext  = "context_jid"
	attrPaneOpenedAt = "opened_at"
)

// The ordered declaration is what makes "the newest pane in this channel" mean
// the newest one: every driver refuses a value of another width, so a
// short timestamp cannot sort into the wrong place (BUGS L97).
func init() { objstore.OrderedAttr(PaneSessions, attrPaneOpenedAt, tsWidth) }

// PaneSession is a Slack assistant pane session — one per (team, user,
// thread_ts) tuple. ChannelID is the channel where the pane lives; ContextJID
// is the workspace channel the user is viewing while the pane is open (may be
// empty).
type PaneSession struct {
	TeamID     string
	UserID     string
	ThreadTS   string
	ChannelID  string
	ContextJID string
	OpenedAt   string
}

// paneKey is the coordinate of one pane. A field carrying the separator is
// refused rather than joined: two panes would address one item, and the read
// that splits the key back apart would answer a triple neither of them has.
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

// paneFrom renders one stored item as the row a caller reads. The triple comes
// back out of the sort key, which is where it was written.
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

// OpenPane records that a pane is open, and in which channel.
//
// opened_at is set ON CREATION ONLY and the channel is refreshed on every call,
// which is what the upsert this replaces did. That is two shapes because the
// interface has two: a Put carries the whole attribute set, so writing one
// unconditionally would erase the instant the pane opened and the context the
// user set. The create-if-absent goes first and its refusal — the pane is
// already open — is what selects the refresh.
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

// PaneInChannel answers which pane a channel is showing — the newest one, when
// the channel names more than one. (zero, false) when it names none.
//
// The winner is picked here rather than by the driver because the sort key is
// the triple and the order wanted is the instant. The file header says what
// bounds the read that costs, and why more than one pane per channel is the
// case the live rows actually hold.
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

// panesInChannel is the one read behind both channel-addressed operations, so
// "which panes does this channel name" cannot mean one thing to the reader and
// another to the writer.
func (s *Store) panesInChannel(ctx context.Context, channelID string) ([]objstore.Item, error) {
	return s.Obj().Query(ctx, objstore.Query{
		Entity: PaneSessions, Partition: panePartition,
		Where: []objstore.Cmp{{Attr: attrPaneChannel, Op: objstore.Eq, Value: channelID}},
	})
}

// SetPaneContext records the workspace channel the user is viewing while the
// pane is open, addressed by the triple. An empty contextJID clears it.
//
// A pane that is not there is NOT an error, which is the answer the UPDATE it
// replaces gave by affecting no rows: slakd opens the pane and routd sets the
// context, and the two arrive over different transports.
func (s *Store) SetPaneContext(teamID, userID, threadTS, contextJID string) error {
	key, err := paneKey(teamID, userID, threadTS)
	if err != nil {
		return err
	}
	// A refusal is a pane slakd has not opened yet, which the doc above allows.
	if _, err := s.TryWriteEntities(context.Background(), nil,
		paneContextWrite(key, contextJID)); err != nil {
		return fmt.Errorf("set the pane's context: %w", err)
	}
	return nil
}

// SetPaneContextByChannel is SetPaneContext reached by channel id, which is the
// shape routd's POST /v1/pane takes when slakd hands it a {channel_id, jid} and
// holds no triple.
//
// It writes EVERY pane the channel names, which is what the UPDATE it replaces
// did — the statement carried no LIMIT, and a channel covering 31 panes is the
// live case, not a hypothetical one. A channel that names none is a no-op.
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

// paneContextWrite is the single renderer of the context change, so the two
// callers cannot disagree about what clearing it means.
//
// An empty context REMOVES the attribute rather than writing "": absent is the
// only null this model has, so a reader testing for the field's presence must
// not find an empty value sitting where nothing should be.
func paneContextWrite(key objstore.Key, contextJID string) objstore.Write {
	w := objstore.Write{Op: objstore.Update, Entity: PaneSessions, Key: key}
	if contextJID == "" {
		w.Remove = []string{attrPaneContext}
		return w
	}
	w.Attrs = map[string]string{attrPaneContext: contextJID}
	return w
}
