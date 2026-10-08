package audit

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// Outcome enum strings. Closed set per audit/PLAN.md.
const (
	OutcomeOK     = "ok"
	OutcomeError  = "error"
	OutcomeDenied = "denied"
)

// Surface enum strings. Closed set; see specs/5/I-tool-call-logging and PLAN.md.
const (
	SurfaceMCP           = "mcp"
	SurfaceREST          = "rest"
	SurfaceCLI           = "cli"
	SurfaceGateway       = "gateway"
	SurfaceCron          = "cron"
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
// Exported so a UI renders the whole vocabulary: a category with no rows yet
// is still a category.
var AllCategories = []string{
	CategoryAuthN, CategoryAuthZ, CategoryAccess, CategoryMutation,
	CategorySystem, CategoryNetwork, CategoryChannel, CategoryAgent,
	CategorySecret, CategoryScheduler,
}

// Event is the homogeneous audit_log row shape. Zero-value safe; the emitters
// fill in instance from package state. Outcome must be set (defaults to "ok" on
// empty); Category + Action + Actor are required.
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

// redactRE matches keys that hold sensitive values (specs/5/I-tool-call-logging Open question 1,
// OWASP ASVS V8.3). `key` is SINGULAR and end-anchored so `serving_keys` (a
// count in authd's daemon.start row) stays readable; `dsn` because that row
// put its db path there in the clear; `api_?key` covers the separator-less
// `apikey`. `session` is ABSENT: session_id is a turn identifier, not a
// credential, and redacting it would blind the forensic join.
var redactRE = regexp.MustCompile(`(?i)pass(word|phrase)?|token|secret|credential|authorization|cookie|dsn|api_?key|^key$|[_-]key$`)

// maxParamsBytes caps the encoded params_summary.
const maxParamsBytes = 512

// maxValueChars is the per-VALUE budget applied before the whole-map cap;
// without it one fat argument collapsed the entire row to `{"_truncated":true}`.
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

// truncateValue clips one over-long string value to maxValueChars runes and
// states what it dropped. Rune-wise: cutting mid-codepoint yields bytes that
// json.Marshal silently replaces with U+FFFD.
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

// marshalParams JSON-encodes the params after redaction and per-value
// truncation; empty input encodes as "". Reaching the `{"_truncated":true}`
// floor means the row had hundreds of keys.
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
