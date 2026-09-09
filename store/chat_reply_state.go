package store

// chat_reply_state.go holds every read and write of `chat_reply_state`: one item
// per (jid, topic), saying which message a reply threads onto and which folder
// currently holds the conversation.
//
// THE PARTITION IS OwnerRoutd AND THE JID LEADS THE SORT KEY. Every write names
// one (jid, topic) pair — the reply anchor after each outbound, the engagement
// window on each conversational turn, and the operator's /v1/engagement — and so
// do four of the five reads. The jid leads because a chat's topics are what a
// reader groups by; the folder does not lead, because it is an ATTRIBUTE of the
// row rather than part of its identity: two folders can hold one chat's two
// topics, and the engaged folder moves while the coordinate does not.
//
// THE ENGAGED FOLDER IS NOT THE PARTITION even though the group delete purges by
// it (folder_scope.go), and that is what makes this file's coordinate differ
// from network_rules'. A row exists before any folder claims it — SetLastReply
// writes the anchor with whatever folder sent, and an unclaimed row carries none
// — so the folder cannot be part of the identity of a row that has to be
// addressable without it.
//
// WHAT THE CONSTANT COSTS is the engagement list, which reads the whole
// partition and applies the live-window test and the subtree test here. Both
// were already the reader's: `ORDER BY engaged_until DESC` answered `SCAN
// chat_reply_state` plus a temp b-tree on krons (EXPLAIN QUERY PLAN,
// 2026-09-09), the deadline was compared as parsed time in Go, and containment
// has no operator on the interface because a prefix predicate runs on the sort
// key. The partition is 310 items on krons, 178 on marinade and 103 on sloth,
// and it drains with the chat: a row is deleted only when the folder holding it
// is.
//
// engaged_until IS ORDERED and it is the one attribute here that is. The list
// reads newest deadline first, so a value of another width sorts by width before
// it sorts by time — and the live rows carry three widths today (28, 29 and 30
// bytes on all three instances, 2026-09-09), which is BUGS L97 sitting on the
// operator's engagement page. `arizuko migrate-hot` re-renders them; the
// declaration is what keeps them one width afterwards.

import (
	"context"
	"database/sql"
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
	// AttrReplyEngagedUntil is the deadline the window ends at, and
	// AttrReplyEngagedFolder is the folder holding the conversation. Both are
	// exported because folder_scope.go reads the folder to decide a purge and the
	// move that carries these rows has to name the instant it re-renders.
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

// replyStateKey is the coordinate of one (jid, topic) pair. A topic may be empty
// — that is the chat's main timeline.
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

// SetLastReply records the thread anchor for (jid, topic), and — on the row it
// CREATES — the folder that wrote it.
//
// THE FOLDER IS WRITTEN ONCE AND NEVER OVERWRITTEN. A later reply from a
// different group would otherwise take the conversation over: a broadcast send
// into a chat some folder is engaged with would move the engaged folder to the
// sender, and the window would be silently hijacked. Only BumpEngagement moves
// it, because that is the write an engagement decision made.
//
// An empty anchor leaves the stored one alone, which is what an Update already
// means: a merge carries the attributes it names and leaves the rest. No
// production caller can reach that branch — ipc/ipc.go putOutboundRow calls this
// only when the platform returned an id, and routd's two sites pass a stored
// row's own id — so it is the behaviour of the empty case and not a change to
// any live one.
func (s *Store) SetLastReply(jid, topic, replyID, folder string) error {
	key, err := replyStateKey(jid, topic)
	if err != nil {
		return err
	}
	whole := map[string]string{}
	setAttr(whole, attrReplyLastID, replyID)
	setAttr(whole, AttrReplyEngagedFolder, folder)
	anchor := map[string]string{}
	setAttr(anchor, attrReplyLastID, replyID)
	ctx := context.Background()
	created, err := s.WriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Put, Entity: ChatReplyState, Key: key, Attrs: whole,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil {
		return fmt.Errorf("record the reply anchor of %s#%s: %w", jid, topic, err)
	}
	if created || len(anchor) == 0 {
		return nil
	}
	if _, err := s.WriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: ChatReplyState, Key: key, Attrs: anchor,
	}); err != nil {
		return fmt.Errorf("move the reply anchor of %s#%s: %w", jid, topic, err)
	}
	return nil
}

// CountReplyStates answers how many (jid, topic) pairs hold reply state.
// `arizuko migrate-hot` reads it back to check that every row travelled.
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
// A zero `until` ENDS the window, which is the disengage the operator and the
// agent's own tool both call.
//
// Ending it takes the attribute AWAY rather than writing an empty one: absent is
// the only null this model has, so a cleared deadline that was still present
// would read as a window with an unparseable end.
func (s *Store) BumpEngagement(jid, topic, folder string, until time.Time) error {
	return s.bumpEngagement(context.Background(), nil, jid, topic, folder, until)
}

// BumpEngagementInTx is BumpEngagement on a transaction the CALLER owns, for the
// operator's write, whose audit row commits with it.
func (s *Store) BumpEngagementInTx(ctx context.Context, tx *sql.Tx,
	jid, topic, folder string, until time.Time) error {
	return s.bumpEngagement(ctx, tx, jid, topic, folder, until)
}

func (s *Store) bumpEngagement(ctx context.Context, tx *sql.Tx,
	jid, topic, folder string, until time.Time) error {
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
	// The folder is written whatever it is, and an EMPTY one un-claims the
	// window. The column it replaces was NOT NULL DEFAULT '' and this write
	// overwrote it unconditionally, so an empty folder meant unclaimed — and
	// absent is how this model spells that. Leaving the attribute alone instead
	// would let the previous folder keep a window an operator just released.
	if folder == "" {
		remove = append(remove, AttrReplyEngagedFolder)
	} else {
		attrs[AttrReplyEngagedFolder] = folder
	}
	if _, err := s.upsertReplyState(ctx, tx, key, attrs, remove); err != nil {
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
// first — INCLUDING a window no folder has claimed.
//
// An unclaimed window is a real row and it is listed: the caller decides who may
// see it, and routd's containment test is descendant(row.folder, caller) rather
// than ownsFolder, which counts an empty target as owned by everyone. Dropping
// the unclaimed rows here would make that distinction untestable and hide a row
// an operator can act on.
//
// The window test and the order both run HERE: a query orders on the sort key,
// which is the (jid, topic) pair, and the deadline is an attribute. The file
// header says what that costs and why it was already the reader's work.
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

// engagementFrom reads one item back, and reports whether its window is still
// open at `now`. It is ONE renderer for the point read and the list, because two
// would drift about what "live" means.
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

// upsertReplyState merges attrs into a pair's row and takes `remove` away,
// creating the row when the pair has none.
//
// The two halves are ONE write, which is what makes an ended window and its
// folder land together: five separate writes are what BUGS O1 cost the
// onboarding reset. Which shape lands is decided by the create's REFUSAL rather
// than by a read.
func (s *Store) upsertReplyState(ctx context.Context, tx *sql.Tx, key objstore.Key,
	attrs map[string]string, remove []string) (bool, error) {
	created, err := s.writeVia(ctx, tx, nil, objstore.Write{
		Op: objstore.Put, Entity: ChatReplyState, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil || created {
		return created, err
	}
	if _, err := s.writeVia(ctx, tx, nil, objstore.Write{
		Op: objstore.Update, Entity: ChatReplyState, Key: key,
		Attrs: attrs, Remove: remove,
	}); err != nil {
		return false, err
	}
	return false, nil
}
