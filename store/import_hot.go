package store

// import_hot.go is the target half of the move that carries routd's hot tier
// tables — the two cursors and their neighbours — onto the entities they are
// now (spec 6/37 step 7).
//
// It is the twin of import_routd.go and it reuses that file's row shape, its
// instant reader and its chunked writer, because the only thing that differs is
// WHICH values a coordinate is built from and which instants are re-rendered.
// The SOURCE half is `arizuko migrate-hot`, the only half that knows the old
// columns.
//
// THE TWO CURSORS ARE THE REASON THIS FILE IS SEPARATE FROM ITS TWIN, and the
// reason is an ORDER rather than a shape. `chats.agent_cursor` and
// `sessions.observed_cursor` are copies of some `messages.timestamp` compared
// back against that column with `>`, so re-rendering a cursor while its rows are
// unrendered MOVES it relative to them: a padded cursor sorts before the
// unpadded rows of its own second, which re-fed 28 consumed messages on krons
// when it was measured (routd migration 0044). Rows before cursors is what that
// migration did in one file, and the move splits it across two steps in the same
// order: the chain pads the rows and `arizuko migrate-hot` refuses to start
// until it has, then this file pads the cursors as it carries them.
//
// A CURSOR OF A SHAPE THE ROWS DO NOT SHARE IS REFUSED. Migration 0044 pads only
// the RFC3339-with-Z values and leaves a hand-written `2026-04-02 12:38:47`
// alone, because padding it would invent a zone it never had — so a cursor of
// that shape must be left alone too, and a cursor this move cannot leave alone
// is one it would shift relative to a row it did not touch. normaliseCursor
// names it instead of guessing.
//
// `chat_reply_state.engaged_until`, `chat_proactive.proactive_last_fired_at`,
// `sessions.forked_at` and `turn_results.recorded_at` are re-rendered outright,
// because none is compared against a stored column — the first two against `now`
// and the others against nothing — and the live rows carry three and four widths
// (27 to 30 bytes across all three instances, 2026-09-09), which for the
// engagement list's ordering is BUGS L97 sitting unread on the operator's page.

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// HotEntities is the order they are read, reported and written in. `chats`
// leads because an operator reads the report that way; nothing else depends on
// it, because an entity has no foreign key to satisfy.
var HotEntities = []string{
	Chats, Sessions, ChatReplyState, ChatProactive, SystemMessages,
	UserProfiles, TurnResults, GroupWatchers, TaskRunLogs,
}

// hotKeyFields names each entity's coordinate fields, in order, so the source
// half can build its SELECT and this half can say which column was short.
//
// `system_messages` names three for a two-part coordinate and `task_run_logs`
// names two for a one-part one, and that is rule 4 made concrete: each position
// is an instant plus a suffix, and the suffix carried here is the AUTOINCREMENT
// id the old table ordered by. Two rows written in one nanosecond keep the order
// they had, and no two can collide, because an id is unique.
var hotKeyFields = map[string][]string{
	Chats:          {"jid"},
	Sessions:       {"group_folder", "topic"},
	ChatReplyState: {"jid", "topic"},
	ChatProactive:  {"jid"},
	SystemMessages: {"folder", "created", "id"},
	UserProfiles:   {"sub"},
	TurnResults:    {"folder", "turn_id"},
	GroupWatchers:  {"observer", "source"},
	TaskRunLogs:    {"run_at", "id"},
}

// HotKeyFields answers one entity's coordinate fields, for the reader that has
// to name them in a SELECT.
func HotKeyFields(entity string) ([]string, bool) {
	f, ok := hotKeyFields[entity]
	return f, ok
}

// hotDropped names the legacy columns an entity does NOT keep. A column this
// file does not name still travels, under its own name, so a column that has no
// place in the item has to be named here or it arrives as an attribute nothing
// reads.
//
// `user_profiles.id` is an INTEGER PRIMARY KEY the engine handed out, which no
// statement in the tree ever selected: an item has no server-minted identity,
// and the sub is the coordinate. `hash` and `linked_to_sub` are the two columns
// a PRE-SPLIT database still carries under this table's older name — a password
// digest nothing reads and a link nothing ever wrote — and a secret carried into
// a document by default is a secret nothing scrubs later.
//
// Naming a column no source has is not an error, so one list covers both
// spellings of the source.
var hotDropped = map[string][]string{
	UserProfiles: {"id", "hash", "linked_to_sub"},
}

// hotStamped names, per entity, the attributes this move re-renders at core.TS's
// width. The file header says why the two cursors are NOT here.
var hotStamped = map[string][]string{
	Sessions:       {attrSessForked},
	ChatReplyState: {AttrReplyEngagedUntil},
	ChatProactive:  {AttrProactiveFiredAt},
	TurnResults:    {AttrTurnRecordedAt},
}

// hotCursors names, per entity, the attribute that is a cursor into
// `messages.timestamp`. They are padded like every other instant here and
// UNLIKE them they are refused when their shape is not one the rows share.
var hotCursors = map[string]string{
	Chats:    AttrChatAgentCursor,
	Sessions: AttrSessObservedCursor,
}

// normaliseCursor re-renders a cursor at core.TS's width, or names it.
//
// The layouts it accepts are exactly the ones routd's migration 0044 pads in
// `messages`: an RFC3339 or RFC3339Nano instant ending in Z, which core.TSLayout
// is the fixed-width member of. A value of any other shape — the hand-written
// `2026-04-02 12:38:47` that migration deliberately skips — is refused, because
// its rows were skipped too and moving the cursor alone is the whole failure.
func normaliseCursor(entity, attr, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if strings.HasSuffix(raw, "Z") {
		for _, layout := range []string{core.TSLayout, time.RFC3339Nano, time.RFC3339} {
			if t, err := time.Parse(layout, raw); err == nil {
				return core.TS(t), nil
			}
		}
	}
	return "", fmt.Errorf("%s.%s is %q, which is not a shape routd's migration 0044 pads in "+
		"`messages`. A cursor is compared against that column, so re-rendering one whose rows "+
		"were left alone would move it relative to them and re-feed or skip everything between "+
		"the two values", entity, attr, raw)
}

// hotFlags names the columns that were an INTEGER 0/1 and are now an attribute
// an item carries when true and does not carry when false. Absent is the only
// null this model has, so a stored "0" would be a false flag every reader finds
// set.
var hotFlags = map[string][]string{
	Chats: {attrChatErrored, attrChatIsGroup},
}

// legacyRunLogKey builds a run's position out of the two columns that were its
// order: the instant it ran and the row number that broke a tie. `run_at` is
// second-precision on every live instance, so the id is what keeps two runs of
// one second in the order the operator read them in.
func legacyRunLogKey(runAt, id string) (objstore.Key, error) {
	at, err := normaliseInstant(TaskRunLogs, "run_at", runAt)
	if err != nil {
		return objstore.Key{}, err
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n < 0 {
		return objstore.Key{}, fmt.Errorf("task run log id %q is not a row number, and it is "+
			"what keeps two runs recorded in one second apart", id)
	}
	return runLogKey(RunStamp(at, fmt.Sprintf("%08x", n)))
}

// hotKey renders one row's coordinate through the SAME builder the entity's own
// writer calls, so a migrated item lands where a live write would put it.
func hotKey(entity string, key []string) (objstore.Key, error) {
	want := hotKeyFields[entity]
	if len(want) == 0 {
		return objstore.Key{}, fmt.Errorf("%s is not one of the hot tier's entities", entity)
	}
	if len(key) != len(want) {
		return objstore.Key{}, fmt.Errorf("%s row carries %d coordinate fields and its "+
			"coordinate (%s) has %d", entity, len(key), strings.Join(want, ","), len(want))
	}
	switch entity {
	case Chats:
		return chatKey(key[0])
	case Sessions:
		return sessionKey(key[0], key[1])
	case ChatReplyState:
		return replyStateKey(key[0], key[1])
	case ChatProactive:
		return proactiveKey(key[0])
	case UserProfiles:
		return profileKey(key[0])
	case TurnResults:
		return turnResultKey(key[0], key[1])
	case GroupWatchers:
		return watcherKey(key[0], key[1])
	case TaskRunLogs:
		return legacyRunLogKey(key[0], key[1])
	case SystemMessages:
		at, err := normaliseInstant(entity, "created", key[1])
		if err != nil {
			return objstore.Key{}, err
		}
		id, err := strconv.ParseInt(key[2], 10, 64)
		if err != nil || id < 0 {
			return objstore.Key{}, fmt.Errorf("system message id %q is not a row number, "+
				"and it is what keeps two events queued at one instant apart", key[2])
		}
		return sysMsgKey(key[0], SysStamp(at, fmt.Sprintf("%08x", id)))
	}
	return objstore.Key{}, fmt.Errorf("%s has no coordinate builder", entity)
}

// hotWrite renders one legacy row as the item it becomes.
func hotWrite(entity string, r RoutdRow) (objstore.Write, error) {
	key, err := hotKey(entity, r.Key)
	if err != nil {
		return objstore.Write{}, err
	}
	attrs := make(map[string]string, len(r.Attrs))
	for name, v := range r.Attrs {
		if v == "" {
			continue // absent is the only null this model has
		}
		attrs[name] = v
	}
	for _, col := range hotDropped[entity] {
		delete(attrs, col)
	}
	for _, flag := range hotFlags[entity] {
		if attrs[flag] == "0" {
			delete(attrs, flag)
			continue
		}
		if _, set := attrs[flag]; set {
			attrs[flag] = flagSet
		}
	}
	for _, attr := range hotStamped[entity] {
		text, serr := normaliseInstant(entity, attr, attrs[attr])
		if serr != nil {
			return objstore.Write{}, serr
		}
		if text == "" {
			delete(attrs, attr)
			continue
		}
		attrs[attr] = text
	}
	if attr, has := hotCursors[entity]; has {
		text, cerr := normaliseCursor(entity, attr, attrs[attr])
		if cerr != nil {
			return objstore.Write{}, cerr
		}
		if text == "" {
			delete(attrs, attr)
		} else {
			attrs[attr] = text
		}
	}
	return objstore.Write{Op: objstore.Put, Entity: entity, Key: key, Attrs: attrs}, nil
}

// CheckHotSnapshot renders every row and reports what would be written, WITHOUT
// writing. It runs on a real run too, so a row the coordinate cannot address is
// named before the run that would stop half way through.
//
// A coordinate two rows share is a fault here rather than a silent overwrite.
func CheckHotSnapshot(snap RoutdSnapshot) (RoutdCounts, error) {
	counts := RoutdCounts{}
	for _, entity := range HotEntities {
		rows := snap[entity]
		seen := make(map[string]bool, len(rows))
		for _, r := range rows {
			w, err := hotWrite(entity, r)
			if err != nil {
				return nil, err
			}
			if seen[w.Key.Sort] {
				return nil, fmt.Errorf("%s carries two rows at %s=%q, so one would "+
					"overwrite the other", entity,
					strings.Join(hotKeyFields[entity], ","), w.Key.Sort)
			}
			seen[w.Key.Sort] = true
		}
		counts[entity] = len(rows)
	}
	for entity := range snap {
		if !slices.Contains(HotEntities, entity) {
			return nil, fmt.Errorf("the snapshot carries %q, which is not one of the hot "+
				"tier's entities", entity)
		}
	}
	return counts, nil
}

// ImportHot writes the snapshot as items and answers how many landed per entity.
//
// The write is an unconditional Put: this runs against a database whose old
// tables have just been set aside, so every coordinate is this row's own. It is
// chunked for the reason its twin is, and each chunk commits on its own, so a
// run that dies leaves work a re-run finishes rather than a half-applied
// transaction.
func (s *Store) ImportHot(ctx context.Context, snap RoutdSnapshot) (RoutdCounts, error) {
	counts, err := CheckHotSnapshot(snap)
	if err != nil {
		return nil, err
	}
	for _, entity := range HotEntities {
		writes := make([]objstore.Write, 0, len(snap[entity]))
		for _, r := range snap[entity] {
			w, werr := hotWrite(entity, r)
			if werr != nil {
				return nil, werr
			}
			writes = append(writes, w)
		}
		if _, err := s.applyInChunks(ctx, "import "+entity, writes); err != nil {
			return nil, err
		}
	}
	return counts, nil
}
