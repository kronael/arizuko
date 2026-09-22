package runed

import (
	"context"
	"log/slog"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
)

// The audit trail for runed's run-slot calls (spec 5/I): WHO claimed or freed
// a folder's run slot, not what ran in it. The spawns row already carries the
// turn; what it cannot answer is who asked, and a kill leaves a spawns row
// indistinguishable from a clean stop while a busy hold leaves none at all.
// Denied calls are NOT recorded here: they are category `authz`, an open gap
// in specs/5/I.

// Audit actions runed emits (category `agent`). Named for the run slot, not
// the container: a hold spawns nothing.
const (
	actionRunHold = "run.hold"
	actionRunKill = "run.kill"
)

// auditRunSlot records one run-slot call: the single renderer for both kill
// routes and the hold. EmitTo, so the write goes to runed's own store. A
// failed write is logged, not returned: answering 500 would make the caller
// retry a non-idempotent act.
func (s *Server) auditRunSlot(ctx context.Context, sub, action, folder, runID string, params map[string]any, err error) {
	actor, actorSub := auditActor(sub)
	resource := ""
	if runID != "" {
		resource = "runs/" + runID
	}
	ev := audit.Event{
		Category:      audit.CategoryAgent,
		Action:        action,
		Actor:         actor,
		ActorSub:      actorSub,
		Surface:       audit.SurfaceREST,
		Resource:      resource,
		Folder:        folder,
		ParamsSummary: params,
		Outcome:       audit.OutcomeOK,
	}
	if err != nil {
		ev.Outcome = audit.OutcomeError
		ev.ErrorMsg = err.Error()
	}
	if _, dbErr := audit.EmitTo(ctx, s.db.Obj(), ev); dbErr != nil {
		slog.Error("runed audit emit", "action", action, "folder", folder, "run_id", runID, "err", dbErr)
	}
}

// auditActor renders the (Actor, ActorSub) pair from a verified bearer's JWT
// subject: actor keeps the prefix, actor_sub is the bare principal that joins
// to acl rows. An empty sub is open mode: "system".
func auditActor(sub string) (actor, actorSub string) {
	if sub == "" {
		return "system", ""
	}
	return sub, auth.BareSub(sub)
}

// holdParams renders the facts a hold row carries; nil when absent, so a
// claimed hold's row has no `"busy":false` field.
func holdParams(reason string, busy bool) map[string]any {
	p := map[string]any{}
	if reason != "" {
		p["reason"] = reason
	}
	if busy {
		p["busy"] = true
	}
	if len(p) == 0 {
		return nil
	}
	return p
}

// killParams records whether anything was actually stopped. Always present: a
// kill that found nothing live is the case with no spawns row.
func killParams(killed bool) map[string]any {
	return map[string]any{"killed": killed}
}
