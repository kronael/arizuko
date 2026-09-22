package store

import (
	"context"
	"encoding/json"

	"github.com/kronael/arizuko/audit"
)

// LogIPCAudit records a mutating MCP tool call as one audit_log row (spec 5/I).
// params is JSON, redacted by the caller; outcome is ok | error: <msg> |
// authz_denied. The row is a separate write, not part of the mutation's batch
// (spec 6/40): an MCP tool's mutation is whatever the tool did, so a lost row
// is a silent gap.
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

// mapIPCAuditOutcome translates the ipc outcome string into the audit_log
// outcome enum plus an optional error message.
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
