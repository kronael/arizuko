package store

import (
	"context"
	"encoding/json"

	"github.com/kronael/arizuko/audit"
)

// LogIPCAudit records a mutating MCP tool call as one audit_log row.
// params is JSON-encoded; secret values must be redacted by the
// caller before encoding. outcome maps onto the audit_log outcome
// enum: ok | error: <msg> | authz_denied. Spec 5/I + audit/PLAN.md.
//
// The legacy ipc_audit table is not written to; existing rows remain
// for backfill. New callers should prefer audit.EmitInTx so the audit
// row commits with the underlying mutation.
func (s *Store) LogIPCAudit(folder, sub, tool, params, outcome string) error {
	out, errMsg := mapIPCAuditOutcome(outcome)
	var paramsMap map[string]any
	if params != "" && params != "{}" {
		_ = json.Unmarshal([]byte(params), &paramsMap)
	}
	_, err := audit.EmitDB(context.Background(), s.db, audit.Event{
		Category:      audit.CategoryAgent,
		Action:        "mcp.tool.invoke",
		Actor:         sub,
		ActorSub:      sub,
		Surface:       audit.SurfaceMCP,
		Resource:      "mcp/" + tool,
		Folder:        folder,
		Outcome:       out,
		ErrorMsg:      errMsg,
		ParamsSummary: paramsMap,
	})
	return err
}

// mapIPCAuditOutcome translates the legacy ipc_audit outcome string
// into the audit_log outcome enum + optional error message.
func mapIPCAuditOutcome(in string) (string, string) {
	switch in {
	case "ok":
		return audit.OutcomeOK, ""
	case "authz_denied":
		return audit.OutcomeDenied, ""
	}
	if len(in) > 7 && in[:7] == "error: " {
		return audit.OutcomeError, in[7:]
	}
	return audit.OutcomeError, in
}
