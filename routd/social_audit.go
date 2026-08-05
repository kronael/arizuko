package routd

import (
	"context"
	"log/slog"

	"github.com/kronael/arizuko/audit"
)

// The audit trail for message actions (spec 5/Z): like, dislike, edit, delete,
// pin, unpin. They reach the platform over two faces — the agent's MCP socket
// (routd/mcp.go buildGatedFns) and its REST twin (routd/turns.go) — and both
// call auditSocial, so the faces cannot drift into recording different things.

// socialTool runs one Deliverer social verb on the agent socket and records it.
// The verb and its audit row are one step here for the same reason they are in
// mutate/target: a new social tool cannot reach the Deliverer without going
// through a call that writes the row.
func (s *Server) socialTool(t turnMCP, action, jid, targetID string, params map[string]any, call func() error) error {
	err := call()
	s.auditSocial(action, t.folder, t.turnID, jid, targetID, audit.SurfaceMCP, params, err)
	return err
}

// turnPrincipal renders the agent principal for a turn's folder. Same
// `folder:<path>` subject ServeTurnMCP hands ipc's row-ACL evaluator, so an
// audit row joins to the acl rows that authorized it.
func turnPrincipal(folder string) string { return "folder:" + folder }

// auditSocial records one message action in audit_log.
//
// These verbs mutate an ALREADY-DELIVERED platform message and append no
// messages row, so this row is the only durable trace they leave: the "the
// messages row IS the record" reason audit/PLAN.md § SKIP gives for not
// auditing reply/send does not reach them, and a deleted or edited message
// otherwise vanishes with nothing recording that it ever existed.
//
// Category `agent`, not `mutation`. PLAN.md's taxonomy scopes `mutation` to
// state change on a registry resource — every one of its actions is CRUD on an
// arizuko table (routes, tasks, groups, gates), the trail an operator reads to
// answer "what changed in my config". `agent` is the per-turn band (container
// lifecycle, turn boundaries, state-changing tool calls), which is what these
// are and where per-turn volume already lives. There is no `social` category
// and this does not add one: the enum is closed (audit/log.go).
//
// Emit, not EmitInTx: the mutation lands on a REMOTE platform, so no local
// transaction exists for the row to ride (audit/log.go — "Non-transactional
// emitters ... call Emit"). EmitDB rather than Emit so the insert goes to
// routd's own handle and does not depend on audit.Init's package state.
//
// A failed insert is logged, not returned: the platform message is already
// changed, and answering 500 would make the agent retry and mutate it twice.
func (s *Server) auditSocial(action, folder, turnID, jid, targetID, surface string, params map[string]any, err error) {
	if s.deliver == nil {
		return // no adapter wired: nothing was mutated, so there is nothing to record
	}
	resource := jid
	if targetID != "" {
		resource = jid + "/" + targetID
	}
	ev := audit.Event{
		Category:      audit.CategoryAgent,
		Action:        action,
		Actor:         turnPrincipal(folder),
		Surface:       surface,
		Resource:      resource,
		Folder:        folder,
		TurnID:        turnID,
		ParamsSummary: params,
		Outcome:       audit.OutcomeOK,
	}
	if err != nil {
		ev.Outcome = audit.OutcomeError
		ev.ErrorMsg = err.Error()
	}
	if _, dbErr := audit.EmitDB(context.Background(), s.db.SQL(), ev); dbErr != nil {
		slog.Error("social audit emit", "action", action, "folder", folder, "err", dbErr)
	}
}

// reactionParams and unpinParams render the one distinguishing fact each verb's
// row carries beyond action + resource. nil when the fact is absent: an
// {"all":false} on a delete row would be a field that endpoint has no concept
// of, and an empty reaction says nothing.
func reactionParams(reaction string) map[string]any {
	if reaction == "" {
		return nil
	}
	return map[string]any{"reaction": reaction}
}

func unpinParams(all bool) map[string]any {
	if !all {
		return nil
	}
	return map[string]any{"all": true}
}
