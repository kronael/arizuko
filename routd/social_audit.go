package routd

import (
	"context"
	"log/slog"

	"github.com/kronael/arizuko/audit"
)

// The audit trail for message actions (spec 5/Z-message-actions): like, dislike, edit,
// delete, pin, unpin. Every one of them calls auditSocial.

// socialTool runs one Deliverer social verb on the agent socket and records
// it: a new social tool cannot reach the Deliverer without writing the row.
func (s *Server) socialTool(t turnMCP, action, jid, targetID string, params map[string]any, call func() error) error {
	err := call()
	s.auditSocial(action, t.folder, t.turnID, jid, targetID, audit.SurfaceMCP, params, err)
	return err
}

// turnPrincipal renders the agent principal for a turn's folder — the same
// `folder:<path>` subject ServeTurnMCP hands ipc, so an audit row joins to the
// acl rows that authorized it.
func turnPrincipal(folder string) string { return "folder:" + folder }

// auditSocial records one message action in audit_log. These verbs mutate an
// ALREADY-DELIVERED platform message and append no messages row, so this row
// is the only durable trace. Category `agent`, not `mutation`: `mutation` is
// CRUD on an arizuko table, and the enum is closed (audit/event.go). EmitTo,
// not EmitInTx: the mutation lands on a REMOTE platform, so no local
// transaction exists. A failed write is logged, not returned: the platform
// message is already changed, and a 500 would make the agent mutate it twice.
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
	if _, dbErr := audit.EmitTo(context.Background(), s.db.Obj(), ev); dbErr != nil {
		slog.Error("social audit emit", "action", action, "folder", folder, "err", dbErr)
	}
}

// reactionParams and unpinParams render the one distinguishing fact each
// verb's row carries; nil when the fact is absent, so a delete row never
// carries an {"all":false} it has no concept of.
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
