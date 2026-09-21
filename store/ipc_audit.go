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
// for backfill. A new caller carries the record in the mutation's own
// GuardedBatch — audit.WriteFor renders the write — so the row commits
// with the mutation or not at all (spec 6/40). This function cannot: an
// MCP tool's mutation is whatever the tool did, so the row is a separate
// write and a lost one is a silent gap.
func (s *Store) LogIPCAudit(folder, sub, tool, params, outcome string) error {
	out, errMsg := mapIPCAuditOutcome(outcome)
	var paramsMap map[string]any
	if params != "" && params != "{}" {
		_ = json.Unmarshal([]byte(params), &paramsMap)
	}
	_, err := audit.EmitTo(context.Background(), s.Obj(), audit.Event{
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
