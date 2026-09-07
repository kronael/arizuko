package runed

import (
	"context"
	"log/slog"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
)

// The audit trail for runed's run-slot calls (spec 5/I). runed audits WHO claimed or
// freed a folder's run slot — not what ran in it.
//
// Nothing here records a turn: POST /v1/runs already writes a spawns row carrying
// kind, state, outcome, exit_code, steered and all three timestamps, so a per-turn
// pair would be the same facts at turn volume with less detail. What spawns cannot
// answer is WHO asked, and the two calls where that is the whole content are:
//
//   - run.hold — an external process claims a folder, stopping every agent turn in it.
//   - run.kill — operator intent, deliberately NOT counted as a run failure, so the
//     spawns row it leaves is indistinguishable from a clean stop.
//
// Both also have outcomes that leave NO spawns row at all: a busy hold claims nothing,
// and a kill of an already-terminal run writes nothing.
//
// Denied calls are NOT recorded here. They are category `authz`, they apply to every
// endpoint including the reads, and one uniform gate is the fix — named as an open gap
// in specs/5/I rather than half-built.

// Audit actions runed emits (audit/PLAN.md § Action vocabulary, category
// `agent`). Named for the run slot, not the container: a hold claims the slot
// and spawns nothing, so `container.kill` would be a lie on its release.
const (
	actionRunHold = "run.hold"
	actionRunKill = "run.kill"
)

// auditRunSlot records one run-slot call. The single renderer for both kill routes and
// the hold, so they cannot drift into recording different things about the same act.
//
// Category `agent`, not `mutation`: PLAN.md scopes `mutation` to CRUD on a registry
// resource, and a run slot is the per-turn execution band.
//
// EmitDB, not EmitInTx: Manager.Kill's mutation lands on the docker daemon and
// Manager.Hold's detaches into a goroutine, so neither opens a *sql.Tx for the row to
// ride. EmitDB rather than Emit so the insert goes to runed's own handle. A failed
// insert is logged, not returned — answering 500 would make the caller retry a
// non-idempotent act.
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

// auditActor renders the (Actor, ActorSub) pair from a verified bearer's JWT subject.
// The prefix lives only in the sub claim, so actor keeps it verbatim and actor_sub is
// the bare principal that joins to acl rows. An empty sub is open mode: "system", the
// same word store's auditIdentity writes for an unattributed caller.
func auditActor(sub string) (actor, actorSub string) {
	if sub == "" {
		return "system", ""
	}
	return sub, auth.BareSub(sub)
}

// holdParams renders the facts a hold row carries beyond action + resource.
// nil each when absent, so a claimed hold's row has no `"busy":false` field
// the endpoint has no concept of.
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

// killParams records whether anything was actually stopped. Always present:
// a kill that found nothing live is the case with no spawns row, and it is
// the reason this row exists.
func killParams(killed bool) map[string]any {
	return map[string]any{"killed": killed}
}
