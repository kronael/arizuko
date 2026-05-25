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

// IsInitialised returns true after Init has been called with a non-nil db.
func IsInitialised() bool {
	logMu.RLock()
	defer logMu.RUnlock()
	return logDB != nil
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

// redactRE matches keys that hold sensitive values. params_summary
// redaction per audit/PLAN.md "Redaction rules" + OWASP ASVS V8.3
// (no secrets in logs).
var redactRE = regexp.MustCompile(`(?i)pass(word)?|token|secret|api_key|authorization|cookie|^key$`)

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
		out[k] = v
	}
	return out
}

// marshalParams JSON-encodes the params after redaction; truncates to
// 512 bytes (with `_truncated` flag) if over the cap. Empty input →
// empty string so it lands as NULL in the column.
func marshalParams(in map[string]any) string {
	if len(in) == 0 {
		return ""
	}
	red := redactParams(in)
	b, err := json.Marshal(red)
	if err != nil {
		return ""
	}
	const cap = 512
	if len(b) <= cap {
		return string(b)
	}
	red["_truncated"] = true
	b, err = json.Marshal(red)
	if err != nil || len(b) > cap {
		return `{"_truncated":true}`
	}
	return string(b)
}
