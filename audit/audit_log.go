package audit

// audit_log.go holds every read and write of `audit_log`: one item per event a
// daemon records, appended and never touched again.
//
// THE PARTITION IS A CONSTANT. Every read is a page of history — newest first,
// a cursor, a limit — and the SORT KEY is the instant, so all three are one
// indexed range; `category` narrows inside it, and `folder` containment and
// the `actor` substring have no operator on the interface, so the reader
// applies them. A folder is not the partition because the operator's page
// names no folder; time is not either, because a walk over time buckets that
// stops on its own bound reports "no older rows" when it merely gave up. The
// cost is one collection per database that grows without bound (6/40
// §"Central and per-owner become the same design").

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

// EnsureEntities prepares a database to record events and is the ONLY applier.
// Asking twice is a no-op. A daemon that skipped it would drop every row while
// reporting healthy (BUGS F29).
func EnsureEntities(ctx context.Context, s EntityMaker) error {
	return s.CreateEntity(ctx, Entity)
}

// logPartition is the one partition every event lives in.
const logPartition = "audit"

// IDWidth is the width of a row id, the sort key. Exported because a cursor
// arrives from the wire, and a caller must tell a cursor from a hand-edited
// URL before the driver refuses it.
const IDWidth = tsWidth + 1 + suffixHex

// tsWidth is core.TS's width; a test checks it against core.TS itself.
const tsWidth = 30

// suffixHex is how many hex digits separate two events that share an instant.
// dashd, webd and proxyd all record into routd's, and the refusing Put makes a
// collision loud instead of a lost row.
const suffixHex = 8

// The ordered declaration is what lets a string id be a time cursor: every
// driver refuses a key of another width (BUGS L97).
func init() { objstore.OrderedSort(Entity, IDWidth) }

// The attributes an item carries. One the event does not carry is ABSENT
// rather than empty: a forensic read asks `folder IS NULL`, and an empty
// string is not that.
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

// scanChunk is how many items a filtered read takes at a time. It bounds the
// memory one page costs, and a chunk shorter than the ask is the end of the
// partition, so a short page always means nothing older. A var so a test can
// shrink it.
var scanChunk = 5000

// NewID is one event's identity and its position in time: the instant at
// core.TS's fixed width, plus a suffix that makes two events in one nanosecond
// two items.
func NewID(at time.Time) string { return idAt(core.TS(at), randomSuffix()) }

// idAt assembles an id from a rendered instant and a suffix.
func idAt(ts, suffix string) string { return ts + "-" + suffix }

// randomSuffix answers eight hex digits. rand.Read on crypto/rand cannot fail.
func randomSuffix() string {
	var b [suffixHex / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("audit: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// createdAt reads the instant back out of an id; the row's time is not stored
// a second time, so two declarations of one fact cannot disagree.
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

// writeFor renders one event as the write that records it: the single renderer
// for a live emit, a caller's batch and the import. The Put carries
// ItemAbsent: an audit log is evidence, and an overwrite is the one edit it
// must not accept. params is the ENCODED params_summary, a parameter because
// the import hands over the document the old writer stored and re-encoding it
// would turn a JSON object into a JSON string.
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

// set records one optional attribute, leaving it off the item when empty.
func set(attrs map[string]string, name, value string) {
	if value != "" {
		attrs[name] = value
	}
}

// outcomeOf answers the event's outcome, defaulting to ok.
func outcomeOf(e Event) string {
	if e.Outcome == "" {
		return OutcomeOK
	}
	return e.Outcome
}

// rowFrom renders one stored item as the row an operator reads. Every absent
// attribute becomes the empty string.
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

// durationOf reads the stored duration; a missing or malformed value reads as
// zero rather than refusing a whole page over one field.
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

// Init wires the store and the instance name; daemons call once at startup.
// Passing nil disables Emit.
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
// that write takes. A caller whose OWN mutation is a GuardedBatch puts the row
// in that batch, so the mutation and its record commit together (spec 6/40
// §"The audited write commits in a GuardedBatch"). This package owns the
// attribute names and the redaction, so the write crosses the boundary, never
// the shape.
func WriteFor(e Event) (string, objstore.Write) {
	id := NewID(time.Now())
	return id, writeFor(id, stamped(e), marshalParams(e.ParamsSummary))
}

// EmitTo records one event in the store the CALLER names. Errors are returned,
// not warned.
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
// carry one.
func stamped(e Event) Event {
	if e.Instance == "" {
		logMu.RLock()
		e.Instance = logInstance
		logMu.RUnlock()
	}
	return e
}

// Row is one audit_log record on the wire, and the resreg RowType
// (resreg/resources/audit.go): the JSON an operator receives IS the struct
// /openapi.json emits a schema from IS what rowFrom builds. resreg imports
// audit, so the struct lives on this side of that edge.
//
// ID IS THE SORT KEY, a time-ordered string, not a number (6/37 rule 4):
// ordering, the page cursor and the row's identity are one value, so a cursor
// stays meaningful across a move between engines. It compares as text.
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

// Filter selects a window of rows. Folder is the CONTAINMENT bound and empty
// means UNBOUNDED — every folder plus the folder-less instance rows. A caller
// that has not proven instance-wide authority must never reach Query with an
// empty Folder, and "empty folder claim" is NOT "is an operator": a tenant
// holding two grants has no folder claim either. Derive Folder from the
// AUTHORIZATION just passed, never from the claim (the REST list-all leak).
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
	// MaxLimit caps what one call can drain; the entity is append-only and
	// unbounded, so an unclamped limit is a whole-history download.
	MaxLimit = 200
)

// Query returns the newest rows matching f, newest first. Four daemons own an
// audit_log and every one serves it through resreg, so this is the one read
// and the field list is what must not drift. dashd's federated page merges
// sources on created_at while each source pages on its own id.
func Query(ctx context.Context, s objstore.Store, f Filter) ([]Row, error) {
	if s == nil {
		return nil, fmt.Errorf("audit: store is nil")
	}
	return page(ctx, s, f)
}

// page reads the newest rows f selects, newest first, in chunks, applying the
// two predicates the interface cannot carry so the caller gets its whole limit.
// Without such a filter the first read asks for exactly the limit and the loop
// runs once.
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
		// A chunk short of the ask is the end of the partition: a short page
		// means there is nothing older, always.
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

// keeps applies the two predicates the reader owns. Containment EXCLUDES a row
// with no folder: those are instance-scope facts, and handing them to a
// folder-bound caller is the same leak in the other direction. The actor match
// is a substring and case-SENSITIVE on every engine.
func keeps(f Filter, row Row) bool {
	if f.Folder != "" && !groupfolder.Contains(f.Folder, row.Folder) {
		return false
	}
	if f.Actor != "" && !strings.Contains(row.Actor, f.Actor) {
		return false
	}
	return true
}
