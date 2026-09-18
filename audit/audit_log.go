package audit

// audit_log.go holds every read and write of `audit_log`: one item per event a
// daemon records, appended and never touched again. It is the whole storage
// half of this package, so a second driver reimplements nothing here — the
// entity is a name a driver resolves, and the daemon that opens the database
// picks which one (spec 6/37 step 5). There is no SQL driver for a statement
// here to run against.
//
// THE PARTITION IS A CONSTANT, and these are the reads that decide it
// (measured on krons' routd.db, 163,750 rows, 2026-09-07):
//
//   - Every read is the same shape — newest first, a cursor, a limit — because
//     every caller is a page of history. The SORT KEY answers all three: it is
//     the instant the event happened, so ordering, paging and the limit are one
//     indexed range and the read is ONE query.
//   - `category` narrows inside that range and the engine applies it, so it
//     costs nothing.
//   - `folder` containment and the `actor` substring have no operator on the
//     interface — a prefix predicate runs on the sort key, not on an attribute,
//     and there is no substring comparison at all — so both are applied by the
//     reader, over items the driver hands back in chunks. THEY ARE ALREADY A
//     SCAN: `EXPLAIN QUERY PLAN` on the folder read and on the actor read both
//     answer `SCAN audit_log`, because `ORDER BY id DESC` takes the rowid index
//     and the partial `audit_log(folder)` index is never used. The move changes
//     where the rows are dropped, not how many are examined.
//
// A folder is not the partition because the read that has no folder — the
// operator's page, and 163,030 of those 163,750 rows carry no folder at all —
// would then have no partition to name, and a query without one is refused by
// every driver. Time is not the partition either: an audit log has no retention
// and no drainage, so a backward walk over time buckets has nothing to stop it,
// and a walk that stops on its own bound reports "no older rows" when it merely
// gave up — on this table that is the worst available lie.
//
// WHAT THE CONSTANT COSTS is one item collection per database that grows
// without bound. Four databases hold one each today — routd's, authd's,
// onbod's, runed's — so the OWNER axis is the file, which is why it is not in
// the key. When those four become one store the owner becomes the partition
// value with no change to any read here (6/40 §"Central and per-owner become
// the same design"), and the move that merges them is where the owner is known.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
)

// Entity is the one entity this package reads and writes.
const Entity = "audit_log"

// EntityMaker is a driver that needs its entities declared before they answer.
// An engine with no DDL implements it as a no-op, which is why it is not on
// objstore.Store: schema is driver-local (spec 6/40).
type EntityMaker interface {
	CreateEntity(ctx context.Context, entity string) error
}

// EnsureEntities prepares a database to record events, and it is the ONLY
// applier — five openers reach it (routd's, runed's, onbod's, authd's, store's)
// plus the import tool, and a loop copied into each is five places for the set
// to drift. Asking twice is a no-op.
//
// A daemon that skipped it would warn and drop every row while reporting
// healthy, which is what BUGS F29 cost.
func EnsureEntities(ctx context.Context, s EntityMaker) error {
	return s.CreateEntity(ctx, Entity)
}

// logPartition is the one partition every event lives in. The file header says
// why it is a constant and what that costs.
const logPartition = "audit"

// IDWidth is the width of a row id, which is the sort key: core.TS's thirty
// bytes, a separator, and eight hex digits. It is exported because a cursor
// arrives from the wire, and a caller that hands one on has to be able to tell
// a cursor from a hand-edited URL before the driver refuses it.
const IDWidth = tsWidth + 1 + suffixHex

// tsWidth is core.TS's width. A test checks it against core.TS itself, because
// a constant that drifted from the layout would refuse every write instead of
// guarding one.
const tsWidth = 30

// suffixHex is how many hex digits separate two events that share an instant.
// Four bytes of randomness: the events that could collide are the ones written
// in the same nanosecond by two processes on one database — dashd, webd and
// proxyd all record into routd's — and a Put that refuses rather than
// overwrites is what makes a collision loud instead of a lost row.
const suffixHex = 8

// The ordered declaration is the whole reason a string id can be a time cursor:
// every driver refuses a key of another width, so a short one cannot sort into
// the wrong place (BUGS L97).
func init() { objstore.OrderedSort(Entity, IDWidth) }

// The attributes an item carries. Each is one column of the row an operator
// reads, and an attribute the event does not carry is ABSENT rather than empty
// — a forensic read asks `folder IS NULL`, an empty string is not that, and
// absent is how the interface spells the missing one.
const (
	attrCategory  = "category"
	attrAction    = "action"
	attrActor     = "actor"
	attrActorSub  = "actor_sub"
	attrResource  = "resource"
	attrScope     = "scope"
	attrSurface   = "surface"
	attrParams    = "params_summary"
	attrOutcome   = "outcome"
	attrErrorMsg  = "error_msg"
	attrDuration  = "duration_ms"
	attrTurnID    = "turn_id"
	attrFolder    = "folder"
	attrInstance  = "instance"
	attrRequestID = "request_id"
	attrSourceIP  = "source_ip"
)

// scanChunk is how many items a read takes at a time while it applies a filter
// of its own. It bounds the memory one page costs — the alternative is holding
// the whole partition, which grows without bound — and the loop terminates
// exactly: a chunk shorter than the ask is the end of the partition, so a short
// page always means there is nothing older and never that the read gave up.
//
// It is a var so a test can shrink it: the loop only runs twice past five
// thousand rows, and a guarantee that needs five thousand rows to observe is one
// nobody checks.
var scanChunk = 5000

// NewID is one event's identity and its position in time: the instant it
// happened, written at core.TS's fixed width so byte order is time order, plus
// the suffix that makes two events in one nanosecond two items.
func NewID(at time.Time) string { return idAt(core.TS(at), randomSuffix()) }

// idAt assembles an id from a rendered instant and a suffix, so the live writer
// and the import that carries an existing row build the same shape.
func idAt(ts, suffix string) string { return ts + "-" + suffix }

// randomSuffix answers eight hex digits. rand.Read on crypto/rand cannot fail.
func randomSuffix() string {
	var b [suffixHex / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("audit: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// createdAt reads the instant back out of an id. The id IS the timestamp plus a
// suffix, so the row's time is not stored a second time: two declarations of
// one fact are two things that can disagree, and the ordering guarantee rests
// on the key rather than on an attribute beside it.
func createdAt(id string) string {
	if len(id) < tsWidth {
		return ""
	}
	return id[:tsWidth]
}

// logKey is the coordinate of one event.
func logKey(id string) objstore.Key {
	return objstore.Key{Partition: logPartition, Sort: id}
}

// writeFor renders one event as the write that records it. It is the single
// renderer: whether the row rides in a batch of its own, in a caller's
// transaction, or in the import, the item is built here and nowhere else.
//
// The Put carries ItemAbsent, so an id that already exists REFUSES the write
// rather than replacing the row under it. An audit log is evidence and an
// overwrite is the one edit it must not accept; the same condition is what
// makes a duplicate id in an import loud.
//
// params is the ENCODED params_summary and it is a parameter rather than a
// field read off e, because its two producers differ: a live emit hands over
// what marshalParams redacted, and the import hands over the document the old
// writer already stored. Re-encoding the second would turn a JSON object into a
// JSON string on every migrated row.
func writeFor(id string, e Event, params string) objstore.Write {
	attrs := map[string]string{
		attrCategory: e.Category,
		attrAction:   e.Action,
		attrActor:    e.Actor,
		attrOutcome:  outcomeOf(e),
	}
	set(attrs, attrActorSub, e.ActorSub)
	set(attrs, attrResource, e.Resource)
	set(attrs, attrScope, e.Scope)
	set(attrs, attrSurface, e.Surface)
	set(attrs, attrParams, params)
	set(attrs, attrErrorMsg, e.ErrorMsg)
	set(attrs, attrTurnID, e.TurnID)
	set(attrs, attrFolder, e.Folder)
	set(attrs, attrInstance, e.Instance)
	set(attrs, attrRequestID, e.RequestID)
	set(attrs, attrSourceIP, e.SourceIP)
	if e.DurationMS != 0 {
		attrs[attrDuration] = strconv.FormatInt(e.DurationMS, 10)
	}
	return objstore.Write{
		Op: objstore.Put, Entity: Entity, Key: logKey(id),
		Attrs: attrs,
		Cond:  objstore.Cond{Item: objstore.ItemAbsent},
	}
}

// set records one optional attribute, leaving it off the item when the event
// does not carry it.
func set(attrs map[string]string, name, value string) {
	if value != "" {
		attrs[name] = value
	}
}

// outcomeOf answers the event's outcome, defaulting to ok. The column has
// always been NOT NULL and an unset outcome has always meant the call worked.
func outcomeOf(e Event) string {
	if e.Outcome == "" {
		return OutcomeOK
	}
	return e.Outcome
}

// rowFrom renders one stored item as the row an operator reads. Every absent
// attribute becomes the empty string: the reader's job is to render, and a
// three-valued field crossing a JSON boundary buys nothing but null-checks in
// every consumer.
func rowFrom(it objstore.Item) Row {
	a := it.Attrs
	return Row{
		ID:            it.Key.Sort,
		CreatedAt:     createdAt(it.Key.Sort),
		Category:      a[attrCategory],
		Action:        a[attrAction],
		Actor:         a[attrActor],
		ActorSub:      a[attrActorSub],
		Resource:      a[attrResource],
		Scope:         a[attrScope],
		Surface:       a[attrSurface],
		ParamsSummary: a[attrParams],
		Outcome:       a[attrOutcome],
		ErrorMsg:      a[attrErrorMsg],
		DurationMS:    durationOf(a[attrDuration]),
		TurnID:        a[attrTurnID],
		Folder:        a[attrFolder],
		Instance:      a[attrInstance],
		RequestID:     a[attrRequestID],
		SourceIP:      a[attrSourceIP],
	}
}

// durationOf reads the stored duration. An item that carries no duration, and
// one whose duration is not a number, both read as zero — the field is evidence
// an operator eyeballs, and refusing a whole page over one malformed value
// would hide the rows around it.
func durationOf(text string) int64 {
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

var (
	logMu       sync.RWMutex
	logStore    objstore.Store
	logInstance string
)

// Init wires the store and the instance name. Daemons call once at startup.
// Passing nil disables Emit (calls become no-ops; this is the path tests take
// when they do not care about audit rows).
func Init(s objstore.Store, instance string) {
	logMu.Lock()
	logStore = s
	logInstance = instance
	logMu.Unlock()
}

// Emit records one event through the store Init named and answers its id.
// Non-fatal: any error is logged as slog warn and dropped, but Init having not
// been called is silent (answers "").
func Emit(ctx context.Context, e Event) string {
	logMu.RLock()
	s := logStore
	logMu.RUnlock()
	if s == nil {
		return ""
	}
	id, err := EmitTo(ctx, s, e)
	if err != nil {
		slog.Warn("audit emit", "err", err, "category", e.Category, "action", e.Action)
	}
	return id
}

// WriteFor renders one event as the write that records it, and answers the id
// that write takes. It is what a caller uses when its OWN mutation is already a
// GuardedBatch: the row rides in that batch, so the mutation and its record
// commit together without a transaction carrying them (spec 6/40 §"The audited
// write commits in a GuardedBatch"). That is how a writer stops needing a
// store.LogSecretUse is the first to leave.
//
// It exists because the alternative is a second renderer. This package owns the
// attribute names and the redaction, and a caller assembling the item itself
// would be a second declaration of both — so the write crosses the boundary,
// never the shape.
func WriteFor(e Event) (string, objstore.Write) {
	id := NewID(time.Now())
	return id, writeFor(id, stamped(e), marshalParams(e.ParamsSummary))
}

// EmitTo records one event in the store the CALLER names, for a daemon that
// holds its own and wants no dependency on Init. Errors are returned, not
// warned.
func EmitTo(ctx context.Context, s objstore.Store, e Event) (string, error) {
	if s == nil {
		return "", fmt.Errorf("audit: store is nil")
	}
	id, w := WriteFor(e)
	committed, err := s.GuardedBatch(ctx, nil, []objstore.Write{w})
	if err != nil {
		return "", err
	}
	if !committed {
		return "", fmt.Errorf("audit: %s is already recorded", id)
	}
	return id, nil
}

// stamped fills in the instance from package state when the event does not
// carry one, which is the one field every emitter shares.
func stamped(e Event) Event {
	if e.Instance == "" {
		logMu.RLock()
		e.Instance = logInstance
		logMu.RUnlock()
	}
	return e
}

// Row is one audit_log record on the wire.
//
// It is also the resreg RowType (resreg/resources/audit.go), so the JSON an
// operator receives IS the struct /openapi.json emits a schema from IS what
// rowFrom builds. resreg imports audit, so the struct has to live on this side
// of that edge; that constraint happens to give the single owner the "one
// renderer, many sinks" rule wants anyway.
//
// ID IS THE SORT KEY, a time-ordered string, and it is not a number. A
// generated id is a sequence no document store has (6/37 rule 4), and this one
// carries the instant it names — so ordering, the page cursor and the row's
// identity are one value, and a cursor stays meaningful across a move between
// engines. What that costs any caller reading it back is that it compares as
// text: dashd's composite cursor, the `before_id` argument and every test that
// asserted on a number all take a string.
type Row struct {
	ID            string `db:"id"             json:"id"`
	CreatedAt     string `db:"created_at"     json:"created_at"`
	Category      string `db:"category"       json:"category"`
	Action        string `db:"action"         json:"action"`
	Actor         string `db:"actor"          json:"actor"`
	ActorSub      string `db:"actor_sub"      json:"actor_sub"`
	Resource      string `db:"resource"       json:"resource"`
	Scope         string `db:"scope"          json:"scope"`
	Surface       string `db:"surface"        json:"surface"`
	ParamsSummary string `db:"params_summary" json:"params_summary"`
	Outcome       string `db:"outcome"        json:"outcome"`
	ErrorMsg      string `db:"error_msg"      json:"error_msg"`
	DurationMS    int64  `db:"duration_ms"    json:"duration_ms"`
	TurnID        string `db:"turn_id"        json:"turn_id"`
	Folder        string `db:"folder"         json:"folder"`
	Instance      string `db:"instance"       json:"instance"`
	RequestID     string `db:"request_id"     json:"request_id"`
	SourceIP      string `db:"source_ip"      json:"source_ip"`
}

// Filter selects a window of rows.
//
// Folder is the CONTAINMENT bound, not a convenience filter, and empty means
// UNBOUNDED — every folder plus the folder-less instance rows. A caller that
// has not proven instance-wide authority must never reach Query with an empty
// Folder. This is the trap the REST list-all leak was filed for: an operator's
// JWT omits the arz/folder claim, but so does a tenant holding two grants
// (routd's handleUserScopes only claims a folder when the sub holds exactly
// one scope), so "empty folder claim" is NOT "is an operator". Every caller
// here derives Folder from the AUTHORIZATION it just passed, never from the
// claim.
type Filter struct {
	Folder   string // subtree bound: the folder itself and everything under it
	Category string // exact category match
	Actor    string // substring match on actor
	Before   string // cursor: only rows older than this id (empty = newest page)
	Limit    int    // clamped to [1, MaxLimit]; 0 → DefaultLimit
}

const (
	// DefaultLimit is one dashboard page.
	DefaultLimit = 50
	// MaxLimit caps what one call can drain. The entity is append-only and
	// unbounded (retention is specs/5/I open question 3), so an unclamped
	// limit is a whole-history download per request.
	MaxLimit = 200
)

// Query returns the newest rows matching f, newest first.
//
// Both halves of this package's storage live in one file because four daemons
// own an audit_log and every one serves it at GET /v1/audit through resreg — a
// per-daemon read would be four projections of one entity drifting apart, and
// the field list is precisely what must not drift.
//
// Ordering and paging are both on the id, which is the instant the event
// happened rendered at a fixed width, so id order IS time order and it admits
// an exact cursor. dashd's federated page relies on exactly that: it merges its
// sources on created_at (comparable across databases) while each source pages on
// its own id (unique within one).
func Query(ctx context.Context, s objstore.Store, f Filter) ([]Row, error) {
	if s == nil {
		return nil, fmt.Errorf("audit: store is nil")
	}
	return page(ctx, s, f)
}

// page reads the newest rows f selects, newest first.
//
// It reads in chunks and applies the two predicates the interface cannot carry
// — folder containment and the actor substring — so the caller gets its whole
// limit rather than the leftovers of one chunk. Without such a filter the first
// read asks for exactly the limit and the loop runs once.
func page(ctx context.Context, s objstore.Store, f Filter) ([]Row, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if f.Before != "" && len(f.Before) != IDWidth {
		return nil, fmt.Errorf("audit cursor %q is %d bytes and a row id is %d",
			f.Before, len(f.Before), IDWidth)
	}
	var where []objstore.Cmp
	if f.Category != "" {
		where = []objstore.Cmp{{Attr: attrCategory, Op: objstore.Eq, Value: f.Category}}
	}
	filtered := f.Folder != "" || f.Actor != ""
	cursor := f.Before
	out := []Row{}
	for {
		ask := limit - len(out)
		if filtered {
			ask = scanChunk
		}
		items, err := s.Query(ctx, objstore.Query{
			Entity: Entity, Partition: logPartition,
			Key:        keyBefore(cursor),
			Where:      where,
			Descending: true,
			Limit:      ask,
		})
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			row := rowFrom(it)
			cursor = row.ID
			if !keeps(f, row) {
				continue
			}
			out = append(out, row)
			if len(out) == limit {
				return out, nil
			}
		}
		// A chunk short of what was asked for is the end of the partition, which
		// is what makes this loop terminate exactly rather than on a bound of its
		// own: a short page means there is nothing older, always.
		if len(items) < ask {
			return out, nil
		}
	}
}

// keyBefore turns a cursor into the sort-key predicate that reads what is older
// than it. An empty cursor is the newest page.
func keyBefore(cursor string) objstore.KeyPredicate {
	if cursor == "" {
		return objstore.KeyPredicate{}
	}
	return objstore.KeyPredicate{Op: objstore.LtKey, Value: cursor}
}

// keeps applies the two predicates the reader owns.
//
// Containment is groupfolder.Contains, the one predicate every scoped read in
// arizuko asks, and it EXCLUDES a row with no folder: those are instance-scope
// facts, and handing them to a folder-bound caller because their folder is
// missing is the same leak in the other direction.
//
// The actor match is a substring and it is case-SENSITIVE on every engine,
// which SQL's LIKE was not — sqlite folds ASCII case and Postgres does not, so
// the same filter answered two different questions depending on the file it ran
// against.
func keeps(f Filter, row Row) bool {
	if f.Folder != "" && !groupfolder.Contains(f.Folder, row.Folder) {
		return false
	}
	if f.Actor != "" && !strings.Contains(row.Actor, f.Actor) {
		return false
	}
	return true
}
