package store

// sessions.go holds every read and write of `sessions`: one item per (folder,
// topic), carrying that topic's agent session id, where it was forked from, and
// `observed_cursor` — the watermark over the ambient window a prompt is built
// from.
//
// THE PARTITION IS OwnerRoutd AND THE FOLDER LEADS THE SORT KEY. Nine of the ten
// operations name one (folder, topic) pair exactly — the session id read and
// write, the lineage read, both cursor moves, the delete, and both forks — and
// each of those is a point read or a single guarded write on one item. The
// folder leads because the two operations that are NOT point reads select a
// folder subtree: the group delete purges (folder_scope.go), and a key predicate
// reaches a LEADING fragment of the key and nothing else. Putting the topic
// first would make both a whole-partition scan with a filter.
//
// THE FOLDER CANNOT BE THE PARTITION, and that is the same answer network_rules
// gives for the same reason: the per-folder tally on the Status page
// (routd/dash_reads_http.go handleSessionCounts) names no folder at all, and a
// query without a partition is refused by every driver. Enumerating partitions
// is not an operation the interface has. What the constant costs is that tally
// and the purge — a whole-partition read with the counting done here — and the
// statement it replaces already answered `SCAN sessions USING COVERING INDEX`
// (EXPLAIN QUERY PLAN, krons, 2026-09-09). The partition is 248 items on krons,
// 150 on marinade and 77 on sloth, and a session is deleted when a topic is
// reset, so it tracks live topics rather than turns.
//
// observed_cursor IS THE ORDERED ATTRIBUTE AND forked_at IS NOT. The cursor is a
// copy of a message timestamp compared back against `messages.timestamp`, so it
// is declared at core.TS's width and a short value is refused at write time
// rather than sorting by width (BUGS L97, migration 0044). `forked_at` is read
// back and rendered and never compared by anything, so it carries no
// declaration — a declaration is a promise about every value in the entity, and
// this one has nothing resting on it. The move normalises it anyway, because
// there is no reason for the one instant it touches to keep the three widths the
// column path left (28, 29 and 30 bytes on all three live instances).

import (
	"context"
	"fmt"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a session item carries. `group_folder` and `topic` are not
// among them: they are the coordinate.
const (
	attrSessID     = "session_id"
	attrSessParent = "parent_topic"
	attrSessForked = "forked_at"
	// AttrSessObservedCursor is the ambient window's watermark. It is exported
	// because the move that carries these rows has to name the attribute it must
	// NOT re-render (import_hot.go).
	AttrSessObservedCursor = "observed_cursor"
)

// The ordered declaration is what makes the observed cursor a cursor.
func init() { objstore.OrderedAttr(Sessions, AttrSessObservedCursor, tsWidth) }

// sessionKey is the coordinate of one topic's session. A topic may be empty —
// that is the folder's main timeline — so the separator still divides the two
// halves and the read splits it back the same way.
func sessionKey(folder, topic string) (objstore.Key, error) {
	if folder == "" {
		return objstore.Key{}, fmt.Errorf("session row carries no folder, which is half "+
			"its coordinate (topic %q)", topic)
	}
	for name, v := range map[string]string{"folder": folder, "topic": topic} {
		if strings.Contains(v, KeySep) {
			return objstore.Key{}, fmt.Errorf("session %s=%q carries the separator that "+
				"divides the two fields of its coordinate", name, v)
		}
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: folder + KeySep + topic}, nil
}

// sessionCoord splits a sort key back into (folder, topic). A key that does not
// split is a LOUD fault: it names an item nobody wrote.
func sessionCoord(sort string) (folder, topic string, err error) {
	folder, topic, ok := strings.Cut(sort, KeySep)
	if !ok {
		return "", "", fmt.Errorf("session %q does not split into (folder, topic)", sort)
	}
	return folder, topic, nil
}

// sessionItem reads one topic's row.
func (s *Store) sessionItem(ctx context.Context, folder, topic string) (objstore.Item, bool, error) {
	key, err := sessionKey(folder, topic)
	if err != nil {
		return objstore.Item{}, false, err
	}
	return s.itemAt(ctx, Sessions, key)
}

// GetSession answers the agent session id persisted for (folder, topic), and
// whether the topic has a row at all.
func (s *Store) GetSession(folder, topic string) (string, bool) {
	it, ok, err := s.sessionItem(context.Background(), folder, topic)
	if err != nil || !ok {
		return "", false
	}
	return it.Attrs[attrSessID], true
}

// SetSession records the session id a run produced for (folder, topic).
//
// It writes THAT FIELD ONLY. Lineage and the observed cursor are owned by
// EnsureTopicLineage, ForkTopic and AdvanceObservedCursor, which always run
// before a topic's first agent turn — so a whole-item Put here would erase a
// cursor the prompt path had already advanced.
func (s *Store) SetSession(folder, topic, id string) error {
	key, err := sessionKey(folder, topic)
	if err != nil {
		return err
	}
	attrs := map[string]string{}
	setAttr(attrs, attrSessID, id)
	if _, err := s.upsertSession(context.Background(), key, attrs); err != nil {
		return fmt.Errorf("record the session of %s#%s: %w", folder, topic, err)
	}
	return nil
}

// EnsureTopicLineage creates the row for (folder, topic) with its lineage when
// the topic has none, and answers whether this call is what created it — the
// caller then copies the parent's transcript. Idempotent; the main topic ("")
// has no parent and is skipped.
//
// THE CHILD INHERITS THE PARENT'S CURSOR, NOT `now`. Stamping now discarded the
// channel's whole ambient backlog at the moment a thread was created: the copied
// transcript carries only the turns that RAN in the parent, and a busy channel's
// chatter is observed rows that never ran one, so a new thread opened blind to
// everything said before it existed (BUGS L87). A cold root has no parent row
// and the child starts with no cursor, which is the normal cold path.
//
// The parent is read and the child is then created under a condition, where the
// statement this replaces read the parent inside its own INSERT. The parent's
// cursor may move between the two, and the child then inherits a slightly older
// one — which re-shows a message the parent already saw, and an observed message
// is context rather than a request. The create itself is still exactly once,
// because it is the condition and not the read that decides it.
func (s *Store) EnsureTopicLineage(folder, topic, parentTopic, newSessionID string) (bool, error) {
	if topic == "" {
		return false, nil
	}
	key, err := sessionKey(folder, topic)
	if err != nil {
		return false, err
	}
	ctx := context.Background()
	attrs := map[string]string{attrSessForked: core.NowTS()}
	setAttr(attrs, attrSessID, newSessionID)
	setAttr(attrs, attrSessParent, parentTopic)
	if parent, ok, perr := s.sessionItem(ctx, folder, parentTopic); perr != nil {
		return false, perr
	} else if ok {
		setAttr(attrs, AttrSessObservedCursor, parent.Attrs[AttrSessObservedCursor])
	}
	created, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Put, Entity: Sessions, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil {
		return false, fmt.Errorf("open the lineage of %s#%s: %w", folder, topic, err)
	}
	return created, nil
}

// DeleteSession drops a topic's continuity — the `/new` reset. Deleting a topic
// that has none is not an error: a reset asks for the topic to have no session,
// and it does not.
func (s *Store) DeleteSession(folder, topic string) error {
	key, err := sessionKey(folder, topic)
	if err != nil {
		return err
	}
	if err := s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Delete, Entity: Sessions, Key: key,
	}); err != nil {
		return fmt.Errorf("reset the session of %s#%s: %w", folder, topic, err)
	}
	return nil
}

// TopicLineage answers where a topic was forked from and how far its ambient
// window has been consumed. The prompt path reads only the cursor; the parent
// and the fork instant are what an operator reads to see where a thread came
// from.
func (s *Store) TopicLineage(folder, topic string) (core.TopicLineage, bool) {
	it, ok, err := s.sessionItem(context.Background(), folder, topic)
	if err != nil || !ok {
		return core.TopicLineage{}, false
	}
	out := core.TopicLineage{
		Folder:         folder,
		Topic:          topic,
		ForkedAt:       it.Attrs[attrSessForked],
		ObservedCursor: it.Attrs[AttrSessObservedCursor],
	}
	// THE FORK INSTANT IS WHAT SAYS A PARENT WAS RECORDED, not the parent
	// attribute. A topic forked from the MAIN timeline has the empty string for
	// its parent, and absent is the only null this model has — so an empty parent
	// carries no attribute and would be indistinguishable from a row that has no
	// lineage at all. The two always travel together, which is a measured fact
	// and not an assumption: across all three live instances zero rows carry one
	// without the other, and 180 of krons' 248 sessions are forked from main.
	if forked, has := it.Attrs[attrSessForked]; has {
		parent := it.Attrs[attrSessParent]
		out.ParentTopic = &parent
		out.ForkedAt = forked
	}
	return out, true
}

// AdvanceObservedCursor moves a topic's ambient watermark to ts, and only
// forward.
//
// IT IS A GUARDED TRANSITION AND NOT AN UPDATE (spec 6/40 §2), which is why it
// is named for the transition: `UpdateObservedCursor` said the SQL verb and hid
// the condition that is the whole operation. It also creates the row when the
// topic has none, because buildAgentPrompt advances the cursor BEFORE SetSession
// creates it — a write that only updated matched nothing on a topic's first turn
// and re-included the whole observed window every time.
func (s *Store) AdvanceObservedCursor(folder, topic, ts string) error {
	key, err := sessionKey(folder, topic)
	if err != nil {
		return err
	}
	_, err = s.advanceCursor(context.Background(), Sessions, key,
		AttrSessObservedCursor, ts, nil)
	return err
}

// ClearObservedCursor takes a topic's watermark away, so the next prompt fills
// its ambient window from the cap instead of from a fork stamp. The fork path
// calls it when the child could not inherit the parent's transcript — without it
// that child starts its first turn with no context at all.
//
// Taking the attribute AWAY is the only way to make it missing: absent is the
// only null this model has, and writing an empty string would leave a value the
// ordered declaration refuses and a reader still finds.
func (s *Store) ClearObservedCursor(folder, topic string) error {
	key, err := sessionKey(folder, topic)
	if err != nil {
		return err
	}
	if err := s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: Sessions, Key: key,
		Remove: []string{AttrSessObservedCursor},
	}); err != nil {
		return fmt.Errorf("clear the observed cursor of %s#%s: %w", folder, topic, err)
	}
	return nil
}

// ForkTopic opens child as a fork of parent: a fresh session id, the parent it
// came from, and a cursor at now so the child reads only post-fork context.
//
// force overwrites an existing child and is the whole-item write that says so;
// without it a collision answers core.ErrTopicExists, decided by the create's
// own condition rather than by a read.
func (s *Store) ForkTopic(folder, parent, child, newSessionID string, force bool) error {
	if child == "" {
		return fmt.Errorf("fork: child topic empty")
	}
	key, err := sessionKey(folder, child)
	if err != nil {
		return err
	}
	now := core.NowTS()
	attrs := map[string]string{attrSessForked: now, AttrSessObservedCursor: now}
	setAttr(attrs, attrSessID, newSessionID)
	setAttr(attrs, attrSessParent, parent)
	w := objstore.Write{Op: objstore.Put, Entity: Sessions, Key: key, Attrs: attrs}
	if !force {
		w.Cond = objstore.Cond{Item: objstore.ItemAbsent}
	}
	forked, err := s.TryWriteEntities(context.Background(), nil, w)
	if err != nil {
		return fmt.Errorf("fork %s#%s from #%s: %w", folder, child, parent, err)
	}
	if !force && !forked {
		return core.ErrTopicExists
	}
	return nil
}

// TopicsPerFolder answers how many topics each folder holds, which is the Status
// page's session tally. The grouping runs HERE because a query orders on the
// sort key and answers no aggregate; the file header says what that costs.
func (s *Store) TopicsPerFolder() (map[string]int, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Sessions, Partition: OwnerRoutd,
	})
	if err != nil {
		return nil, fmt.Errorf("read the sessions: %w", err)
	}
	out := map[string]int{}
	for _, it := range items {
		folder, _, cerr := sessionCoord(it.Key.Sort)
		if cerr != nil {
			return nil, cerr
		}
		out[folder]++
	}
	return out, nil
}

// CountTopics answers how many topics hold a session. `arizuko migrate-hot`
// reads it back to check that every row travelled.
func (s *Store) CountTopics(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: Sessions, Partition: OwnerRoutd})
	if err != nil {
		return 0, fmt.Errorf("read the sessions: %w", err)
	}
	return len(items), nil
}

// upsertSession merges attrs into a topic's row, creating it when the topic has
// none. Which shape lands is decided by the first one's REFUSAL rather than by a
// read, because a read and then a write is two decisions and the item can appear
// between them.
func (s *Store) upsertSession(ctx context.Context, key objstore.Key,
	attrs map[string]string) (bool, error) {
	created, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Put, Entity: Sessions, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil || created {
		return created, err
	}
	if err := s.WriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: Sessions, Key: key, Attrs: attrs,
	}); err != nil {
		return false, err
	}
	return false, nil
}
