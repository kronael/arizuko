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
// when it was measured (BUGS L97). Rows before cursors is no longer an order to
// preserve: this file writes the rows first, through the one writer that renders
// every instant at the declared width, and then derives each chat's cursor from
// the keys that landed. A cursor cannot be rendered ahead of its rows because it
// is not carried at all.
//
// ONE READER SETTLES BOTH. A cursor and the rows it is a position among are
// parsed by store.ReadMessageInstant and nothing else, so a value either side
// can name — the hand-written `2026-04-02 12:38:47` two krons rows carry
// included — is read the same way on both. A cursor of a shape no row can be
// built from is refused by name rather than guessed at (normaliseCursor).
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
	"sort"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// HotEntities is the order they are read, reported and written in. `chats`
// leads because an operator reads the report that way; nothing else depends on
// it, because an entity has no foreign key to satisfy.
var HotEntities = []string{
	Chats, Sessions, ChatReplyState, ChatProactive, SystemMessages,
	UserProfiles, TurnResults, GroupWatchers, TaskRunLogs, TurnContexts, Messages,
}

// MessageEntities are the four a message write touches, plus the folder counter.
// A migration names the set it may create, because CreateEntity probes the three
// columns it needs and would otherwise fail on a table a different tool has not
// moved yet.
var MessageEntities = []string{
	Messages, MessageFolders, MessageFolderCounts, MessageActivity, MessageDelivery,
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
	TurnContexts:   {"turn_id"},
	// A message's coordinate is its chat and its position, and the position is
	// two columns: the instant and the id that keeps two messages of one instant
	// apart. 245 pairs share an instant inside one chat on krons.
	Messages: {"chat_jid", "timestamp", "id"},
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
	TurnContexts:   {AttrTurnStartedAt},
}

// hotCursors names, per entity, the attribute that is a cursor into a message's
// position and is carried VERBATIM rather than re-derived.
//
// `sessions.observed_cursor` is the only one left. It stays a bare instant
// because the ambient window compares it `> cursor` and takes the NEWEST rows, so
// a cut can only fall at the old end and the newest rendered message is always
// whole — a bound of "just past this instant" is exactly what the comparison
// meant.
//
// `chats.agent_cursor` is NOT here any more. It holds a whole message key now, so
// it cannot be carried: the key does not exist until the messages have landed.
// DeriveCarriedCursors builds it after they do, as the greatest key at or before
// the instant the chat held — the same position, a new rendering.
var hotCursors = map[string]string{
	Sessions: AttrSessObservedCursor,
}

// hotDerivedCursors names the attributes this move builds rather than carries,
// and the legacy column each was built from.
var hotDerivedCursors = map[string]string{
	Chats: AttrChatAgentCursor,
}

// normaliseCursor re-renders a cursor at core.TS's width, or names it.
//
// It accepts exactly what a message row accepts, because store.ReadMessageInstant
// is the one parser for both: an RFC3339 or RFC3339Nano instant ending in Z, of
// which core.TSLayout is the fixed-width member, or a naive datetime read as UTC.
// A value of any other shape is refused by name — a cursor this move cannot
// render names a position among rows it cannot render either.
func normaliseCursor(entity, attr, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	t, err := ReadMessageInstant(raw)
	if err != nil {
		return "", fmt.Errorf("%s.%s is %q, and a cursor names a position among messages: %w",
			entity, attr, raw, err)
	}
	return core.TS(t), nil
}

// hotFlags names the columns that were an INTEGER 0/1 and are now an attribute
// an item carries when true and does not carry when false. Absent is the only
// null this model has, so a stored "0" would be a false flag every reader finds
// set.
var hotFlags = map[string][]string{
	Chats:        {attrChatErrored, attrChatIsGroup},
	TurnContexts: {attrTurnRunReturned},
}

// hotZeros names the columns whose "0" is the attribute's ABSENCE rather than a
// value, because both are NOT NULL DEFAULT 0 and both have a live writer that
// stores no attribute at all for the zero: a turn that has not been retried
// carries no count, and SetUserCap(0) REMOVES the cap rather than writing one.
// Carrying the zeros would put two shapes on one fact — measured: every one of
// marinade's 2 and sloth's 3 profiles carries `cost_cap_cents_per_day":"0"`, and
// 2,938 of krons' 2,954 turns carry a zero retry count.
var hotZeros = map[string][]string{
	TurnContexts: {attrTurnRetryCount},
	UserProfiles: {attrProfCostCap},
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
	case TurnContexts:
		return turnKey(key[0])
	case Messages:
		t, err := ReadMessageInstant(key[1])
		if err != nil {
			// The abort is the operator's only way in, so it names the row: a
			// value no layout reads is one a person put there by hand, and the
			// id is what finds it again among twenty thousand.
			return objstore.Key{}, fmt.Errorf("message %q in chat %q: %w", key[2], key[0], err)
		}
		return MessageKey(key[0], core.TS(t), key[2])
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
	for _, attr := range hotZeros[entity] {
		if attrs[attr] == "0" {
			delete(attrs, attr)
		}
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
	if attr, has := hotDerivedCursors[entity]; has {
		// A derived cursor is not carried: it names a position that does not
		// exist until the messages have landed, so writing the legacy instant
		// here would leave a cursor of the wrong SHAPE behind if the derivation
		// then failed. ImportHot builds it after.
		delete(attrs, attr)
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
			// The WHOLE coordinate, because a message's partition is its chat:
			// two chats legitimately hold one position, and comparing the sort
			// key alone would call that a collision.
			at := w.Key.Partition + KeySep + w.Key.Sort
			if seen[at] {
				return nil, fmt.Errorf("%s carries two rows at %s=%q, so one would "+
					"overwrite the other", entity,
					strings.Join(hotKeyFields[entity], ","), at)
			}
			seen[at] = true
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

// legacyChatCursors reads each chat's stored cursor out of the snapshot, before
// the write that drops it.
func legacyChatCursors(snap RoutdSnapshot) map[string]string {
	out := map[string]string{}
	for _, r := range snap[Chats] {
		if len(r.Key) == 0 {
			continue
		}
		if cur := r.Attrs[AttrChatAgentCursor]; cur != "" {
			out[r.Key[0]] = cur
		}
	}
	return out
}

// DeriveCarriedCursors turns each chat's stored INSTANT into the message key the
// dispatch cursor holds now: the greatest key at or before that instant.
//
// IT IS THE SAME POSITION, and that is the property the move has to keep. Every
// message at or before the instant sorts at or below the derived key and every
// message after it sorts above, so the set the poll loop will feed is exactly the
// set it would have fed — proven per chat against all three live snapshots
// (messages_migrate_test.go). A chat whose cursor names an instant older than
// every message it holds derives nothing, which reads as "feed from the start"
// and is what the instant already meant.
//
// ONE BAD CHAT DOES NOT STOP THE REST, for DeriveAgentCursors' reason: a move
// that derived only the cursors alphabetically before the first failure leaves
// the poll loop treating the rest of the history as unread.
func (s *Store) DeriveCarriedCursors(ctx context.Context, stored map[string]string) (int, error) {
	jids := make([]string, 0, len(stored))
	for jid := range stored {
		jids = append(jids, jid)
	}
	sort.Strings(jids)
	moved := 0
	var failed []string
	for _, jid := range jids {
		instant, err := normaliseCursor(Chats, AttrChatAgentCursor, stored[jid])
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", jid, err))
			continue
		}
		cursor, err := s.CursorAtOrBefore(ctx, jid, instant)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", jid, err))
			continue
		}
		if cursor == "" {
			continue
		}
		key, err := chatKey(jid)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", jid, err))
			continue
		}
		if err := s.WriteEntities(ctx, nil, objstore.Write{
			Op: objstore.Update, Entity: Chats, Key: key,
			Attrs: map[string]string{AttrChatAgentCursor: cursor},
		}); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", jid, err))
			continue
		}
		moved++
	}
	if len(failed) > 0 {
		return moved, fmt.Errorf("%d of %d chats kept no dispatch cursor, so the poll loop "+
			"reads their whole history as unread: %s",
			len(failed), len(jids), strings.Join(failed, "; "))
	}
	return moved, nil
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
	stored := legacyChatCursors(snap)
	for _, entity := range HotEntities {
		if entity == Messages {
			// A message is four items and a counter, and the archive's writer
			// already renders all of them. Two renderers would be two opinions
			// of what a message's indexes hold.
			if err := s.importMessageRows(ctx, messageRowsFrom(snap[entity])); err != nil {
				return nil, err
			}
			continue
		}
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
	// ROWS BEFORE CURSORS, and here it is not an order somebody preserves: the
	// derived cursor is a message KEY, so it cannot be built until the messages
	// it names are items.
	if _, err := s.DeriveCarriedCursors(ctx, stored); err != nil {
		return nil, err
	}
	return counts, nil
}

// messageRowsFrom renders the legacy message rows as the archive's row shape, so
// the move and the restore reach ONE writer.
//
// The naive datetimes two krons rows carry — `2026-04-02 12:38:47` — are read as
// UTC here. Every item needs a position and a position needs an instant, so the
// choice is to read them or to drop two messages. The instance writes UTC and has
// always written UTC, so the zone is the one the row was recorded in.
func messageRowsFrom(rows []RoutdRow) []ArchiveMessageRow {
	out := make([]ArchiveMessageRow, 0, len(rows))
	for _, r := range rows {
		m := ArchiveMessageRow{
			ChatJID: r.Key[0], Timestamp: r.Key[1], ID: r.Key[2],
			Sender: r.Attrs["sender"], SenderName: r.Attrs["sender_name"],
			Content: r.Attrs["content"], ForwardedFrom: r.Attrs["forwarded_from"],
			ReplyToID: r.Attrs["reply_to_id"], ReplyToText: r.Attrs["reply_to_text"],
			ReplyToSender: r.Attrs["reply_to_sender"], Topic: r.Attrs["topic"],
			RoutedTo: r.Attrs["routed_to"], Verb: r.Attrs["verb"],
			Attachments: r.Attrs["attachments"], Source: r.Attrs["source"],
			TurnID: r.Attrs["turn_id"], Status: r.Attrs["status"],
			PlatformID: r.Attrs["platform_id"], ChatName: r.Attrs["chat_name"],
			LinkContext:  r.Attrs["link_context"],
			IsFromMe:     r.Attrs["is_from_me"] == "1",
			IsBotMessage: r.Attrs["is_bot_message"] == "1",
			IsObserved:   r.Attrs["is_observed"] == "1",
			Errored:      r.Attrs["errored"] == "1",
		}
		// A parse this cannot make is a zero, because an attempt count is what
		// the dead-letter page reports and not what any decision turns on: it
		// must never be the reason a message does not travel.
		m.DeliveryAttempts, _ = strconv.Atoi(r.Attrs["delivery_attempts"])
		out = append(out, m)
	}
	return out
}
