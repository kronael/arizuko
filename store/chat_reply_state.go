package store

// chat_reply_state.go holds every read and write of `chat_reply_state`: one item
// per (jid, topic), saying which message a reply threads onto and which folder
// currently holds the conversation.
//
// The partition is OwnerRoutd and the jid leads the sort key. The engaged
// folder is an ATTRIBUTE and not part of the coordinate, even though the group
// delete purges by it (folder_scope.go): a row exists before any folder claims
// it. The engagement list reads the whole partition and applies the live-window
// and subtree tests here (310 items on krons; it drains with the chat).
//
// engaged_until IS ORDERED: the list reads newest deadline first, and the live
// rows carried three widths (BUGS L97). The declaration keeps every value one
// width, so the order is the order of the deadlines.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a reply-state item carries. `jid` and `topic` are the
// coordinate and are not among them.
const (
	attrReplyLastID = "last_reply_id"
	// Exported because folder_scope.go reads the folder to decide a purge and
	// the move that carries these rows names the instant it re-renders.
	AttrReplyEngagedUntil  = "engaged_until"
	AttrReplyEngagedFolder = "engaged_folder"
)

// The ordered declaration is what makes the engagement list an ordered read.
func init() { objstore.OrderedAttr(ChatReplyState, AttrReplyEngagedUntil, tsWidth) }

// Engagement is one live conversation window: which folder holds (jid, topic),
// and until when. Spec 5/G.
type Engagement struct {
	JID    string
	Topic  string
	Folder string
	Until  time.Time
}

// replyStateKey is the coordinate of one (jid, topic) pair. A topic may be
// empty: that is the chat's main timeline.
func replyStateKey(jid, topic string) (objstore.Key, error) {
	if jid == "" {
		return objstore.Key{}, fmt.Errorf("reply state carries no jid, which is half its "+
			"coordinate (topic %q)", topic)
	}
	for name, v := range map[string]string{"jid": jid, "topic": topic} {
		if strings.Contains(v, KeySep) {
			return objstore.Key{}, fmt.Errorf("reply state %s=%q carries the separator that "+
				"divides the two fields of its coordinate", name, v)
		}
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: jid + KeySep + topic}, nil
}

// replyStateCoord splits a sort key back into (jid, topic).
func replyStateCoord(sortKey string) (jid, topic string, err error) {
	jid, topic, ok := strings.Cut(sortKey, KeySep)
	if !ok {
		return "", "", fmt.Errorf("reply state %q does not split into (jid, topic)", sortKey)
	}
	return jid, topic, nil
}

// replyStateItem reads one pair's row.
func (s *Store) replyStateItem(ctx context.Context, jid, topic string) (objstore.Item, bool, error) {
	key, err := replyStateKey(jid, topic)
	if err != nil {
		return objstore.Item{}, false, err
	}
	return s.itemAt(ctx, ChatReplyState, key)
}

// LastReplyID answers the message a reply into (jid, topic) threads onto.
func (s *Store) LastReplyID(jid, topic string) string {
	it, ok, err := s.replyStateItem(context.Background(), jid, topic)
	if err != nil || !ok {
		return ""
	}
	return it.Attrs[attrReplyLastID]
}

// SetLastReply records the thread anchor for (jid, topic), and on the row it
// CREATES the folder that wrote it. THE FOLDER IS WRITTEN ONCE AND NEVER
// OVERWRITTEN: a later reply from a different group would otherwise silently
// take the window over. Only BumpEngagement moves it. An empty anchor leaves
// the stored one alone.
func (s *Store) SetLastReply(jid, topic, replyID, folder string) error {
	key, err := replyStateKey(jid, topic)
	if err != nil {
		return err
	}
	anchor := map[string]string{}
	setAttr(anchor, attrReplyLastID, replyID)
	born := map[string]string{}
	setAttr(born, AttrReplyEngagedFolder, folder)
	if len(anchor) == 0 && len(born) == 0 {
		return nil
	}
	if err := s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Merge, Entity: ChatReplyState, Key: key, Attrs: anchor, Init: born,
	}); err != nil {
		return fmt.Errorf("record the reply anchor of %s#%s: %w", jid, topic, err)
	}
	return nil
}

// CountReplyStates answers how many (jid, topic) pairs hold reply state.
func (s *Store) CountReplyStates(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: ChatReplyState, Partition: OwnerRoutd,
	})
	if err != nil {
		return 0, fmt.Errorf("read the reply states: %w", err)
	}
	return len(items), nil
}

// BumpEngagement moves the window on (jid, topic) to `until`, claimed by folder.
// A zero `until` ENDS the window, and ending takes the attribute AWAY: absent
// is the only null this model has, and a present cleared deadline would read
// as a window with an unparseable end.
func (s *Store) BumpEngagement(jid, topic, folder string, until time.Time) error {
	return s.bumpEngagement(context.Background(), jid, topic, folder, until)
}

// BumpEngagementWith is BumpEngagement carrying the caller's OWN writes in the
// same batch, for the operator's /v1/engagement, whose audit row has to commit
// with the window it records (spec 6/40).
func (s *Store) BumpEngagementWith(ctx context.Context, jid, topic, folder string,
	until time.Time, also ...objstore.Write) error {
	return s.bumpEngagement(ctx, jid, topic, folder, until, also...)
}

func (s *Store) bumpEngagement(ctx context.Context,
	jid, topic, folder string, until time.Time, also ...objstore.Write) error {
	key, err := replyStateKey(jid, topic)
	if err != nil {
		return err
	}
	attrs := map[string]string{}
	var remove []string
	if until.IsZero() {
		remove = append(remove, AttrReplyEngagedUntil)
	} else {
		attrs[AttrReplyEngagedUntil] = core.TS(until)
	}
	// The merge and the removal are ONE write, so an ended window and its
	// folder land together (five separate writes cost BUGS O1). An EMPTY
	// folder un-claims the window: leaving the attribute alone would let the
	// previous folder keep a window an operator just released.
	if folder == "" {
		remove = append(remove, AttrReplyEngagedFolder)
	} else {
		attrs[AttrReplyEngagedFolder] = folder
	}
	writes := append([]objstore.Write{{
		Op: objstore.Merge, Entity: ChatReplyState, Key: key, Attrs: attrs, Remove: remove,
	}}, also...)
	if err := s.WriteEntities(ctx, nil, writes...); err != nil {
		return fmt.Errorf("move the engagement window on %s#%s: %w", jid, topic, err)
	}
	return nil
}

// Engagement answers the LIVE window on (jid, topic). Not live is: no row, no
// deadline, a deadline this cannot read, or one already past.
func (s *Store) Engagement(jid, topic string) (Engagement, bool) {
	it, ok, err := s.replyStateItem(context.Background(), jid, topic)
	if err != nil || !ok {
		return Engagement{}, false
	}
	e, live := engagementFrom(it.Key.Sort, it.Attrs, time.Now())
	if !live {
		return Engagement{}, false
	}
	return e, true
}

// LiveEngagements answers every window still open at `now`, newest deadline
// first, INCLUDING a window no folder has claimed: the caller decides who may
// see it, and routd's containment test is descendant(row.folder, caller) rather
// than ownsFolder, which counts an empty target as everyone's.
func (s *Store) LiveEngagements(now time.Time) ([]Engagement, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: ChatReplyState, Partition: OwnerRoutd,
		Where: []objstore.Cmp{{Attr: AttrReplyEngagedUntil, Op: objstore.Present}},
	})
	if err != nil {
		return nil, fmt.Errorf("read the engagement windows: %w", err)
	}
	out := make([]Engagement, 0, len(items))
	for _, it := range items {
		e, live := engagementFrom(it.Key.Sort, it.Attrs, now)
		if !live {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Until.After(out[j].Until) })
	return out, nil
}

// engagementFrom reads one item back and reports whether its window is still
// open at `now`. ONE renderer for the point read and the list.
func engagementFrom(sortKey string, attrs map[string]string, now time.Time) (Engagement, bool) {
	jid, topic, err := replyStateCoord(sortKey)
	if err != nil {
		return Engagement{}, false
	}
	e := Engagement{JID: jid, Topic: topic, Folder: attrs[AttrReplyEngagedFolder]}
	until, has := attrs[AttrReplyEngagedUntil]
	if !has {
		return e, false
	}
	t, perr := time.Parse(time.RFC3339Nano, until)
	if perr != nil || !t.After(now) {
		return e, false
	}
	e.Until = t
	return e, true
}
