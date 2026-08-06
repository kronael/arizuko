package audit

// audit_log SQLite source-of-truth event sink. Spec 5/I + 6/F;
// master event list in audit/PLAN.md. Init wires the *sql.DB once at
// daemon start; Emit/EmitInTx insert rows. State-changing handlers
// call EmitInTx inside their own transaction so the audit row commits
// or rolls back with the mutation. Non-transactional emitters (login,
// container.spawn, daemon.start) call Emit.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
)

// Outcome enum strings. Closed set per audit/PLAN.md.
const (
	OutcomeOK     = "ok"
	OutcomeError  = "error"
	OutcomeDenied = "denied"
)

// Surface enum strings. Closed set; see specs/5/I and PLAN.md.
const (
	SurfaceMCP           = "mcp"
	SurfaceREST          = "rest"
	SurfaceCLI           = "cli"
	SurfaceGateway       = "gateway"
	SurfaceCron          = "cron"
	SurfaceCrackbox      = "crackbox"
	SurfaceAgentPreTool  = "agent_pretool"
	SurfaceAgentPostTool = "agent_posttool"
)

// Category enum strings. Closed set; see audit/PLAN.md "Category taxonomy".
const (
	CategoryAuthN     = "authn"
	CategoryAuthZ     = "authz"
	CategoryAccess    = "access"
	CategoryMutation  = "mutation"
	CategorySystem    = "system"
	CategoryNetwork   = "network"
	CategoryChannel   = "channel"
	CategoryAgent     = "agent"
	CategorySecret    = "secret"
	CategoryScheduler = "scheduler"
)

// AllCategories is the closed set above, in the order a filter offers them.
// Exported so a UI renders the whole vocabulary instead of a SELECT DISTINCT
// over whatever happens to be on screen — a category with no rows yet is still
// a category, and on a FEDERATED page the distinct-values query would have to
// run against three daemons to answer a question the enum already answers.
var AllCategories = []string{
	CategoryAuthN, CategoryAuthZ, CategoryAccess, CategoryMutation,
	CategorySystem, CategoryNetwork, CategoryChannel, CategoryAgent,
	CategorySecret, CategoryScheduler,
}

// Event is the homogeneous audit_log row shape. Zero-value safe;
// Emit fills in instance from package state. Outcome must be set
// (defaults to "ok" on empty); Category + Action + Actor are required.
type Event struct {
	Category      string
	Action        string
	Actor         string
	ActorSub      string
	Resource      string
	Scope         string
	Surface       string
	ParamsSummary map[string]any
	Outcome       string
	ErrorMsg      string
	DurationMS    int64
	TurnID        string
	Folder        string
	Instance      string
	RequestID     string
	SourceIP      string
}

var (
	logMu       sync.RWMutex
	logDB       *sql.DB
	logInstance string
)

// Init wires the *sql.DB and instance name. Daemons call once at
// startup. Passing nil disables Emit (calls become no-ops; this is
// the path tests take when they don't care about audit rows).
func Init(db *sql.DB, instance string) {
	logMu.Lock()
	logDB = db
	logInstance = instance
	logMu.Unlock()
}

// Emit inserts one row using the package-level *sql.DB. Returns the
// inserted ID. Non-fatal: any error is logged as slog warn + dropped,
// but Init having not been called is silent (returns 0).
func Emit(ctx context.Context, e Event) int64 {
	logMu.RLock()
	db := logDB
	inst := logInstance
	logMu.RUnlock()
	if db == nil {
		return 0
	}
	if e.Instance == "" {
		e.Instance = inst
	}
	id, err := insertRow(ctx, db, e)
	if err != nil {
		slog.Warn("audit emit", "err", err, "category", e.Category, "action", e.Action)
	}
	return id
}

// EmitInTx inserts one row inside an already-open transaction. The
// caller MUST NOT Commit/Rollback tx until this returns. Returns an
// error so the caller can Rollback the mutation if the audit insert
// fails — semantically the audit row IS the mutation (per 5/I).
func EmitInTx(ctx context.Context, tx *sql.Tx, e Event) error {
	if tx == nil {
		return fmt.Errorf("audit: tx is nil")
	}
	logMu.RLock()
	inst := logInstance
	logMu.RUnlock()
	if e.Instance == "" {
		e.Instance = inst
	}
	_, err := insertRowTx(ctx, tx, e)
	return err
}

// EmitDB inserts one row using the caller-supplied *sql.DB (no Init
// dependency). Used by daemons that have their own DB handle and want
// to emit without going through the package state. Errors are
// returned, not warned.
func EmitDB(ctx context.Context, db *sql.DB, e Event) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("audit: db is nil")
	}
	logMu.RLock()
	inst := logInstance
	logMu.RUnlock()
	if e.Instance == "" {
		e.Instance = inst
	}
	return insertRow(ctx, db, e)
}

const insertSQL = `INSERT INTO audit_log
  (category, action, actor, actor_sub, resource, scope, surface,
   params_summary, outcome, error_msg, duration_ms, turn_id, folder,
   instance, request_id, source_ip)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func insertArgs(e Event) []any {
	outcome := e.Outcome
	if outcome == "" {
		outcome = OutcomeOK
	}
	params := marshalParams(e.ParamsSummary)
	return []any{
		e.Category, e.Action, e.Actor, nullable(e.ActorSub),
		nullable(e.Resource), nullable(e.Scope), nullable(e.Surface),
		nullable(params), outcome, nullable(e.ErrorMsg),
		nullableInt(e.DurationMS), nullable(e.TurnID), nullable(e.Folder),
		nullable(e.Instance), nullable(e.RequestID), nullable(e.SourceIP),
	}
}

func insertRow(ctx context.Context, db *sql.DB, e Event) (int64, error) {
	res, err := db.ExecContext(ctx, insertSQL, insertArgs(e)...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func insertRowTx(ctx context.Context, tx *sql.Tx, e Event) (int64, error) {
	res, err := tx.ExecContext(ctx, insertSQL, insertArgs(e)...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// nullable returns nil for empty strings so NULL lands in the column.
// Forensic queries `WHERE folder IS NULL` mean different things from
// `WHERE folder = ''`.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

// redactRE matches keys that hold sensitive values — the pinned set of
// specs/5/I Open question 1, per OWASP ASVS V8.3 (no secrets in logs).
// Every alternative is load-bearing; the anchoring is the whole design:
//
//   - `^key$` / `[_-]key$` — the bare `key` param, plus private_key,
//     signing_key, enc_key, service_key. Deliberately SINGULAR and
//     end-anchored so `serving_keys` (a count, authd's daemon.start row)
//     stays readable. PLAN.md's unanchored `key` would have redacted it,
//     and `monkey`/`keyboard` with it.
//   - `dsn` — a DSN is the canonical place a credential hides, and authd's
//     daemon.start row put its auth.db path in `params_summary` in the
//     clear (authd/main.go). Found auditing that column before publishing
//     it over GET /v1/audit; the read surface is why this alternative
//     exists.
//   - `api_?key` covers the separator-less `apikey` that `[_-]key$` cannot.
//
// `session` is deliberately ABSENT: session_id is a Claude Code turn
// identifier, not a credential, and redacting it would blind the forensic
// join this table exists for.
var redactRE = regexp.MustCompile(`(?i)pass(word|phrase)?|token|secret|credential|authorization|cookie|dsn|api_?key|^key$|[_-]key$`)

// maxParamsBytes caps the encoded params_summary. 512, matching
// audit/PLAN.md and the shipped column comment; specs/5/I said "1 KB" and
// nothing ever implemented it, so the spec was corrected to the real
// number rather than the column silently doubling.
const maxParamsBytes = 512

// maxValueChars is the per-VALUE budget applied before the whole-map cap.
// This is the "encoding for fields that hit the cap" specs/5/I left open:
// without it one fat argument collapsed the entire row to
// `{"_truncated":true}` and every sibling field — the caller's folder, the
// resource it touched — was lost with it. Truncating the offending value
// instead keeps the row answerable.
const maxValueChars = 200

// redactParams returns a shallow copy of in with sensitive values
// replaced by `<redacted:Nchars>`. The original map is unchanged.
func redactParams(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if redactRE.MatchString(k) {
			if s, ok := v.(string); ok {
				out[k] = fmt.Sprintf("<redacted:%dchars>", len(s))
				continue
			}
			out[k] = "<redacted>"
			continue
		}
		out[k] = truncateValue(v)
	}
	return out
}

// truncateValue clips one over-long string value to maxValueChars runes
// and states what it dropped. Rune-wise, not byte-wise: cutting a UTF-8
// string mid-codepoint yields invalid JSON input that json.Marshal would
// silently replace with U+FFFD.
func truncateValue(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	r := []rune(s)
	if len(r) <= maxValueChars {
		return v
	}
	return string(r[:maxValueChars]) + fmt.Sprintf("…<truncated:%dchars>", len(r))
}

// marshalParams JSON-encodes the params after redaction + per-value
// truncation. Empty input → empty string so it lands as NULL in the
// column. The whole-map cap is the backstop for a map with many fields;
// per-value truncation already handles the common single-fat-argument
// case, so reaching the `{"_truncated":true}` floor now means the row
// genuinely had hundreds of keys.
func marshalParams(in map[string]any) string {
	if len(in) == 0 {
		return ""
	}
	red := redactParams(in)
	b, err := json.Marshal(red)
	if err != nil {
		return ""
	}
	if len(b) <= maxParamsBytes {
		return string(b)
	}
	red["_truncated"] = true
	b, err = json.Marshal(red)
	if err != nil || len(b) > maxParamsBytes {
		return `{"_truncated":true}`
	}
	return string(b)
}
