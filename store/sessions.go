package store

// sessions.go holds every read and write of `sessions`: one item per (folder,
// topic), carrying that topic's agent session id, where it was forked from, and
// `observed_cursor`, the watermark over the ambient window a prompt is built
// from.
//
// The partition is OwnerRoutd and the folder leads the sort key: nine of the
// ten operations are point operations on one (folder, topic), and the group
// delete purges a folder subtree, which is a begins_with only with the folder
// leading. The folder cannot be the partition, because the per-folder tally on
// the Status page names no folder and enumerating partitions is not an
// operation the interface has (248 items on krons; a session is deleted when a
// topic is reset).
//
// observed_cursor IS ORDERED: it is compared back against message positions,
// so a short value is refused at write time (BUGS L97). forked_at is compared
// by nothing and carries no declaration.

import (
	"context"
	"fmt"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a session item carries. `group_folder` and `topic` are the
// coordinate.
const (
	attrSessID     = "session_id"
	attrSessParent = "parent_topic"
	attrSessForked = "forked_at"
	// AttrSessObservedCursor is the ambient window's watermark. Exported because
	// import_hot.go names the attribute it must NOT re-render.
	AttrSessObservedCursor = "observed_cursor"
)

// The ordered declaration is what makes the observed cursor a cursor.
func init() { objstore.OrderedAttr(Sessions, AttrSessObservedCursor, tsWidth) }

// sessionKey is the coordinate of one topic's session. A topic may be empty:
// the folder's main timeline.
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

// sessionCoord splits a sort key back into (folder, topic). A key that does
// not split is a LOUD fault: it names an item nobody wrote.
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

// SetSession records the session id a run produced for (folder, topic). It
// writes THAT FIELD ONLY: lineage and the observed cursor are owned by
// EnsureTopicLineage, ForkTopic and AdvanceObservedCursor, which run before a
// topic's first agent turn, so a whole-item Put here would erase a cursor the
// prompt path had already advanced.
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
// the topic has none, and answers whether this call is what created it; the
// caller then copies the parent's transcript. Idempotent; the main topic ("")
// has no parent and is skipped.
//
// THE CHILD INHERITS THE PARENT'S CURSOR, NOT `now`: stamping now discarded the
// channel's whole ambient backlog, so a new thread opened blind to everything
// said before it existed (BUGS L87). The parent's cursor may move between the
// read and the create, and the child then re-shows a message the parent saw,
// which is context rather than a request. The create is still exactly once,
// because the condition and not the read decides it.
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

// DeleteSession drops a topic's continuity: the `/new` reset. Deleting a topic
// that has none is not an error.
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
// window has been consumed.
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
	// attribute: a topic forked from the MAIN timeline has the empty string for
	// its parent, which is absent, so it would be indistinguishable from a row
	// with no lineage. The two always travel together (measured: zero rows
	// carry one without the other).
	if forked, has := it.Attrs[attrSessForked]; has {
		parent := it.Attrs[attrSessParent]
		out.ParentTopic = &parent
		out.ForkedAt = forked
	}
	return out, true
}

// AdvanceObservedCursor moves a topic's ambient watermark to ts, and only
// forward: a guarded transition, not an update (spec 6/40 §2). It also creates
// the row when the topic has none, because buildAgentPrompt advances the
// cursor BEFORE SetSession creates it; a write that only updated matched
// nothing on a topic's first turn and re-included the whole observed window.
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
// its ambient window from the cap instead of from a fork stamp; the fork path
// calls it when the child could not inherit the parent's transcript. Taking
// the attribute AWAY is the only way to make it missing: an empty string is a
// value the ordered declaration refuses.
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
// force overwrites an existing child; without it a collision answers
// core.ErrTopicExists, decided by the create's own condition.
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

// TopicsPerFolder answers how many topics each folder holds: the Status page's
// session tally. The grouping runs here.
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

// upsertSession merges attrs into a topic's row, creating it when the topic
// has none. Which shape lands is decided by the create's REFUSAL rather than
// by a read: the item can appear between a read and a write.
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
