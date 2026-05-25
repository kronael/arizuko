package audit

// Stub for the audit_log SQLite source-of-truth event sink. Round-1
// shape only — no schema migration yet, no callers wired. See
// audit/PLAN.md for the master event list and field schema. Spec
// 5/I + 6/F.

import (
	"context"
	"database/sql"
	"encoding/json"
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

// Event is the homogeneous audit_log row shape. Zero-value safe; Emit
// fills in defaults (created_at via DB default, instance from package
// state). Outcome must be set; Category + Action + Actor are required.
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
// startup. Safe to call multiple times; last call wins. Passing nil
// disables Emit (returns ErrNotInitialised).
func Init(db *sql.DB, instance string) {
	logMu.Lock()
	logDB = db
	logInstance = instance
	logMu.Unlock()
}

// ErrNotInitialised is returned by Emit / EmitInTx when Init has not
// been called. Callers should treat it as non-fatal (slog warn + drop)
// per the round-2 design.
type errNotInit struct{}

func (errNotInit) Error() string { return "audit: Init not called" }

var ErrNotInitialised error = errNotInit{}

// Emit inserts one row using the package-level *sql.DB. Returns the
// inserted ID. Round-1 stub: returns 0, ErrNotInitialised until round-2
// wires the schema + implementation.
func Emit(ctx context.Context, e Event) (int64, error) {
	logMu.RLock()
	db := logDB
	logMu.RUnlock()
	if db == nil {
		return 0, ErrNotInitialised
	}
	return insertRow(ctx, db, e)
}

// EmitInTx inserts one row inside an already-open transaction. The
// caller MUST NOT Commit/Rollback tx until this returns. Round-1 stub:
// returns 0, ErrNotInitialised until round-2.
func EmitInTx(ctx context.Context, tx *sql.Tx, e Event) (int64, error) {
	if tx == nil {
		return 0, ErrNotInitialised
	}
	return insertRowTx(ctx, tx, e)
}

// insertRow / insertRowTx are filled in by round-2; here they are
// stubs that return ErrNotInitialised to surface "not yet wired".
func insertRow(ctx context.Context, db *sql.DB, e Event) (int64, error) {
	_ = ctx
	_ = db
	_ = e
	return 0, ErrNotInitialised
}

func insertRowTx(ctx context.Context, tx *sql.Tx, e Event) (int64, error) {
	_ = ctx
	_ = tx
	_ = e
	return 0, ErrNotInitialised
}

// redactRE matches keys that hold sensitive values. params_summary
// redaction follows audit/PLAN.md "Redaction rules".
var redactRE = regexp.MustCompile(`(?i)pass(word)?|token|secret|key$|api_key|authorization|cookie`)

// redactParams returns a shallow copy of in with sensitive values
// replaced by `<redacted:Nchars>`. The original map is unchanged.
// Round-1 helper; round-2 uses this at insert time.
func redactParams(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if redactRE.MatchString(k) {
			if s, ok := v.(string); ok {
				out[k] = redactedStr(len(s))
				continue
			}
			out[k] = "<redacted>"
			continue
		}
		out[k] = v
	}
	return out
}

func redactedStr(n int) string {
	return fmtRedacted(n)
}

// fmtRedacted produces "<redacted:Nchars>" without importing fmt for
// one helper (keeps the stub package small).
func fmtRedacted(n int) string {
	const tpl = "<redacted:"
	// 4 = max digits we ever bother with (>9999 chars stays accurate).
	buf := make([]byte, 0, len(tpl)+8)
	buf = append(buf, tpl...)
	if n == 0 {
		buf = append(buf, '0')
	} else {
		var digits [10]byte
		i := len(digits)
		for n > 0 {
			i--
			digits[i] = '0' + byte(n%10)
			n /= 10
		}
		buf = append(buf, digits[i:]...)
	}
	buf = append(buf, "chars>"...)
	return string(buf)
}

// marshalParams is a placeholder for the JSON serialisation step that
// round-2 will use. Exported so test fixtures can verify shape without
// going through the (not-yet-wired) DB.
func marshalParams(in map[string]any) string {
	if len(in) == 0 {
		return ""
	}
	b, err := json.Marshal(redactParams(in))
	if err != nil {
		return ""
	}
	const cap = 512
	if len(b) > cap {
		// Truncate; round-2 may add a `_truncated: true` field.
		return string(b[:cap])
	}
	return string(b)
}
