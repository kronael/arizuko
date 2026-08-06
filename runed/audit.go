package runed

import (
	"context"
	"log/slog"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
)

// The audit trail for runed's run-slot calls (spec 5/I). runed audits WHO
// claimed or freed a folder's run slot — not what ran in it.
//
// Nothing here records a turn. POST /v1/runs already writes a spawns row
// carrying kind, state, outcome, exit_code, steered and all three timestamps,
// and dashd renders it; a `container.spawn`/`container.exit` pair per turn
// would be the same facts at turn volume with less detail. That is
// audit/PLAN.md § SKIP's own rule for `messages` — the row IS the record —
// applied to the table that is runed's record.
//
// What spawns cannot answer is WHO asked, and the two calls where that is the
// whole content are the ones this file covers:
//
//   - run.hold  (POST /v1/holds)  — an external process claims a folder,
//     stopping every agent turn in it until released.
//   - run.kill  (DELETE /v1/runs/{run_id}, POST /v1/runs/stop) — operator
//     intent, deliberately NOT counted as a run failure (Manager.Kill), so
//     the spawns row it leaves is indistinguishable from a clean stop.
//
// Both also have outcomes that leave NO spawns row at all: a busy hold claims
// nothing, and a kill of an already-terminal run (or of an idle folder, via
// /stop) writes nothing. Those calls happened; without this row nothing
// anywhere says so.
//
// Denied calls — a folder-scoped token reaching for another tenant's run —
// are NOT recorded here. They are category `authz`, they apply to every
// endpoint including the reads, and one uniform gate is the fix; a
// kills-only denial row would be an arbitrary slice of that. Named as an open
// gap in specs/5/I rather than half-built.

// Audit actions runed emits (audit/PLAN.md § Action vocabulary, category
// `agent`). Named for the run slot, not the container: a hold claims the slot
// and spawns nothing, so `container.kill` would be a lie on its release.
const (
	actionRunHold = "run.hold"
	actionRunKill = "run.kill"
)

// auditRunSlot records one run-slot call. The single renderer for both kill
// routes and the hold, so DELETE /v1/runs/{id} and POST /v1/runs/stop cannot
// drift into recording different things about the same act.
//
// Category `agent`, not `mutation`: PLAN.md scopes `mutation` to CRUD on a
// registry resource (routes, tasks, groups), the trail for "what changed in my
// config". A run slot is the per-turn execution band, which is `agent`.
//
// EmitDB, not EmitInTx: Manager.Kill's mutation lands on the docker daemon and
// Manager.Hold's detaches into a goroutine — neither opens a *sql.Tx for the
// row to ride, and runed's writers are all autocommit (runed/db.go). EmitDB
// rather than Emit so the insert goes to runed's own handle instead of
// depending on audit.Init's package state.
//
// A failed insert is logged, not returned: the container is already dead or
// the slot already claimed, and answering 500 would make the caller retry a
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
	if _, dbErr := audit.EmitDB(ctx, s.db.SQL(), ev); dbErr != nil {
		slog.Error("runed audit emit", "action", action, "folder", folder, "run_id", runID, "err", dbErr)
	}
}

// auditActor renders the (Actor, ActorSub) pair from a verified bearer's JWT
// subject. The prefix (`service:routd`, `user:google:114alice`) lives only in
// the sub claim, so actor keeps it verbatim and actor_sub is the bare
// principal that joins to acl rows (auth.BareSub, spec 5/1). An empty sub is
// open mode (verify==nil, local-dev): "system", the same word store's
// auditIdentity writes for an unattributed caller.
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
