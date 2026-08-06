package audit

// The READ half of audit_log. Emit/EmitInTx/EmitDB write the table; Query
// reads it back. Both live here because four daemons own an audit_log
// (routd.db, runed.db, auth.db, onbod.db) and every one of them serves it at
// GET /v1/audit through resreg — a per-daemon SELECT would be four
// projections of one table drifting apart, and the column list is precisely
// what must not drift (a forgotten COALESCE renders NULL as "<nil>" on one
// daemon and "" on another). Spec: specs/5/I-tool-call-logging.md.
//
// Row is also the resreg RowType (resreg/resources/audit.go), so the JSON an
// operator receives IS the struct /openapi.json emits a schema from IS the
// scan target here. resreg imports audit, so the struct has to live on this
// side of that edge; that constraint happens to give the single owner the
// "one renderer, many sinks" rule wants anyway.

import (
	"context"
	"database/sql"
	"strings"
)

// Row is one audit_log record on the wire. Every nullable column is scanned
// through COALESCE into a plain string: the reader's job is to render, and a
// three-valued column crossing a JSON boundary buys nothing but null-checks
// in every consumer.
type Row struct {
	ID            int64  `db:"id"             json:"id"`
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
	BeforeID int64  // cursor: only rows with a lower id (0 = newest page)
	Limit    int    // clamped to [1, MaxLimit]; 0 → DefaultLimit
}

const (
	// DefaultLimit is one dashboard page.
	DefaultLimit = 50
	// MaxLimit caps what one call can drain. The table is append-only and
	// unbounded (retention is specs/5/I open question 3), so an unclamped
	// limit is a whole-history download per request.
	MaxLimit = 200
)

// selectCols is the single column list. Order matches Row's fields and the
// Scan below; adding a column means editing all three together, which is why
// they sit adjacent.
const selectCols = `id, created_at, category, action, actor,
	COALESCE(actor_sub,''), COALESCE(resource,''), COALESCE(scope,''),
	COALESCE(surface,''), COALESCE(params_summary,''), outcome,
	COALESCE(error_msg,''), COALESCE(duration_ms,0), COALESCE(turn_id,''),
	COALESCE(folder,''), COALESCE(instance,''), COALESCE(request_id,''),
	COALESCE(source_ip,'')`

// Query returns the newest rows matching f, newest first.
//
// The folder predicate is a SUBTREE match (`acme` reaches `acme/support`),
// matching how a grant is written — `acme/**` — and how runed's ownsFolder
// already contains a run. An exact match would tell a caller authorized over
// a subtree that nothing happened in it.
//
// A folder-bounded read deliberately EXCLUDES the folder-less rows
// (daemon.start, and every authn event that predates a folder). Those are
// instance-scope facts; surfacing them to a tenant because their folder
// column is NULL is the same leak in a different direction.
//
// Ordering and paging are both on `id`, never on created_at. Within one table
// the two are co-monotonic — the AUTOINCREMENT id and the created_at default
// are assigned at the same insert, and no writer supplies created_at — so id
// order IS time order, and it is the one that admits an exact cursor. dashd's
// federated page relies on exactly that: it merges the three sources on
// created_at (comparable across DBs) while each source pages on its own id
// (unique within a DB, which created_at is not).
func Query(ctx context.Context, db *sql.DB, f Filter) ([]Row, error) {
	where := []string{"1=1"}
	args := []any{}

	if f.Folder != "" {
		where = append(where, "(folder = ? OR folder LIKE ? || '/%')")
		args = append(args, f.Folder, f.Folder)
	}
	if f.Category != "" {
		where = append(where, "category = ?")
		args = append(args, f.Category)
	}
	if f.Actor != "" {
		where = append(where, "actor LIKE '%' || ? || '%'")
		args = append(args, f.Actor)
	}
	if f.BeforeID > 0 {
		where = append(where, "id < ?")
		args = append(args, f.BeforeID)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	args = append(args, limit)

	rows, err := db.QueryContext(ctx,
		`SELECT `+selectCols+`
		 FROM audit_log
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Row{}
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.ID, &r.CreatedAt, &r.Category, &r.Action, &r.Actor,
			&r.ActorSub, &r.Resource, &r.Scope, &r.Surface, &r.ParamsSummary,
			&r.Outcome, &r.ErrorMsg, &r.DurationMS, &r.TurnID, &r.Folder,
			&r.Instance, &r.RequestID, &r.SourceIP); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
