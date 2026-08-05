package routd

import (
	"slices"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

// sibling_db.go is the federation surface: every table routd needs is read from
// routd.db (acl/secrets/tasks/pane) or federated over HTTP (identity → authd,
// session_log → runed). routd opens no sibling messages.db.
//
// routd owns acl/acl_membership (aclEval), secrets/secret_use_log (secretStore;
// it holds SECRETS_KEY), scheduled_tasks/task_run_logs (taskStore; serves timed
// via /v1/tasks/*), and pane_sessions (paneStore; serves slakd via /v1/pane).
// authd owns identity (snapshotted over HTTP in identity.go); runed owns
// session_log (federated in session.go).

// Tasks reads scheduled_tasks for the tasks.json spawn snapshot: a root group
// sees every task (owner filter empty); a child sees only its own.
func (d *DB) Tasks(folder string, isRoot bool) []core.Task {
	return d.taskStore().ListTasks(folder, isRoot)
}

// CountActiveTasks counts active scheduled_tasks for the /status surface.
func (d *DB) CountActiveTasks() int {
	return d.taskStore().CountActiveTasks()
}

// PaneContextJID returns (contextJID, true) when a Slack assistant pane exists
// for the DM channelID. No row → ("", false).
func (d *DB) PaneContextJID(channelID string) (string, bool) {
	p, ok := d.paneStore().GetPaneByChannel(channelID)
	if !ok {
		return "", false
	}
	return p.ContextJID, true
}

// SetPaneContext upserts the workspace-channel context for the pane keyed by
// channelID (behind POST /v1/pane).
func (d *DB) SetPaneContext(channelID, contextJID string) error {
	return d.paneStore().SetPaneContextByChannel(channelID, contextJID)
}

// UpsertPane creates/refreshes the pane row keyed by (team, user, thread) (behind
// POST /v1/pane upsert=open). slakd calls this over HTTP.
func (d *DB) UpsertPane(teamID, userID, threadTS, channelID string) error {
	return d.paneStore().UpsertPane(teamID, userID, threadTS, channelID)
}

// SetPaneContextByTriple updates the pane's workspace-channel context keyed by
// (team, user, thread) — slakd's context-change path, served by POST /v1/pane.
// Empty contextJID clears it.
func (d *DB) SetPaneContextByTriple(teamID, userID, threadTS, contextJID string) error {
	return d.paneStore().SetPaneContext(teamID, userID, threadTS, contextJID)
}

// aclEval wraps routd.db as a *store.Store for the ACL evaluator (auth.Authorize)
// + readers (ListACL, UserScopes). An empty acl table denies everything — there is
// no fallback grant set.
func (d *DB) aclEval() *store.Store { return store.New(d.db) }

// secretStore wraps routd.db as a *store.Store with the SECRETS_KEY keyring set,
// so secret reads decrypt `v2:` values and writes seal them.
func (d *DB) secretStore() *store.Store {
	s := store.New(d.db)
	if len(d.secretKeyring) > 0 {
		s.SetSecretKeys(d.secretKeyring...)
	}
	return s
}

// taskStore wraps routd.db as a *store.Store for the task readers/writers.
func (d *DB) taskStore() *store.Store { return store.New(d.db) }

// paneStore wraps routd.db as a *store.Store for the pane readers/writers.
func (d *DB) paneStore() *store.Store { return store.New(d.db) }

// userStore wraps routd.db as a *store.Store for the per-user cap reader/writer
// against user_profiles. Reads only the cap column; authd owns the full identity
// record.
func (d *DB) userStore() *store.Store { return store.New(d.db) }

// UserCap returns the per-day cap for a user_sub in cents. Zero means uncapped
// (the default).
func (d *DB) UserCap(userSub string) (int, error) {
	return d.userStore().UserCap(userSub)
}

// SetUserCap writes the per-day cap for one user_sub. 0 = uncapped. The
// operator write half of UserCap, so `arizuko budget set user` lands on the
// row budgetGate reads.
func (d *DB) SetUserCap(userSub string, cents int) error {
	return d.userStore().SetUserCap(userSub, cents)
}

// FolderSecrets resolves the folder-scoped secret set for `folder`,
// DECRYPTING `v2:` values via the SECRETS_KEY keyring (deepest-wins
// folder-ancestry precedence). SECRETS_KEY unset / read error → empty map (the
// caller treats absent secrets as "inject nothing").
func (d *DB) FolderSecrets(folder string) map[string]string {
	out, err := d.secretStore().FolderSecretsResolved(folder)
	if err != nil {
		return map[string]string{}
	}
	return out
}

// FolderSecretsForUser is FolderSecrets with the trigger user's user-scoped
// secrets overlaid (a user's own key shadows the folder default) — the BYOA
// spawn-time resolution. Empty userSub or read error → the plain folder set.
func (d *DB) FolderSecretsForUser(folder, userSub string) map[string]string {
	out, err := d.secretStore().FolderSecretsResolvedForUser(folder, userSub)
	if err != nil {
		return d.FolderSecrets(folder)
	}
	return out
}

// SetSecret seals + upserts one folder/user secret (the operator write path
// behind POST /v1/secrets).
func (d *DB) SetSecret(scope store.SecretScope, scopeID, key, value string) error {
	return d.secretStore().SetSecret(scope, scopeID, key, value)
}

// DeleteSecret removes one folder/user secret (behind DELETE /v1/secrets/{key}).
// ErrSecretNotFound when no row matched.
func (d *DB) DeleteSecret(scope store.SecretScope, scopeID, key string) error {
	return d.secretStore().DeleteSecret(scope, scopeID, key)
}

// ConnectorSecrets narrows the resolved secrets to the `required` names a
// connector declares. callerSub overlays the triggering user's user-scoped
// secrets on top of the folder defaults (BYOA: user's own GITHUB_TOKEN wins
// over a shared folder token). Empty callerSub → folder scope only.
// Missing keys are omitted. nil/empty required → empty map.
//
// A required key backed by a user-scoped surrogate-OAuth row (spec 5/15) is
// refreshed in place when within 60s of expiry before it is returned, so the
// connector always gets a live token. A DEFINITIVE refresh rejection (revoked
// refresh_token) nulls the row's oauth columns, drops the key, and returns a
// non-nil "reconnect" error the caller surfaces to the agent — the other keys
// still resolve. A transient refresh failure keeps the stale token and no error.
func (d *DB) ConnectorSecrets(folder, callerSub string, required []string) (map[string]string, error) {
	if len(required) == 0 {
		return map[string]string{}, nil
	}
	all := d.FolderSecretsForUser(folder, callerSub)
	out := make(map[string]string, len(required))
	for _, k := range required {
		if v, ok := all[k]; ok {
			out[k] = v
		}
	}
	if d.surrogate == nil || callerSub == "" {
		return out, nil
	}
	return out, d.refreshNearExpiry(callerSub, required, out)
}

// ListACL returns acl rows, optionally filtered by principal. Used for the
// mcp:<tool> operator overlay (deriveFolderGrants) and the list_acl tool.
func (d *DB) ListACL(principal string) []core.ACLRow {
	return d.aclEval().ListACL(principal)
}

// UserScopes returns the distinct allow-scopes a sub has, expanding membership
// transitively. Backs GET /v1/users/{sub}/scopes (authd snapshots it at login).
func (d *DB) UserScopes(sub string) []string {
	return d.aclEval().UserScopes(sub)
}

// AddACLRow inserts one acl row.
func (d *DB) AddACLRow(row core.ACLRow) error { return d.aclEval().AddACLRow(row) }

// AddMembership inserts one (child→parent) acl_membership edge (the operator `**`
// grant maps to role:operator membership; same self/cycle rejection).
func (d *DB) AddMembership(child, parent, addedBy string) error {
	return d.aclEval().PutMembership(child, parent, addedBy)
}

// Authorize is the per-call row-ACL check for an in-container agent tool call.
// sub is the canonical agent principal (folder:<folder>). Magnitude comes from the
// folder's role membership + operator grants; a no-match denies.
func (d *DB) Authorize(sub, folder, action string, params map[string]string) bool {
	caller := auth.Caller{Principal: sub}
	return auth.Authorize(d.aclEval(), caller, action, folder, params)
}

// IsOperator reports whether sub carries the operator `**` grant. Operators are
// emergent from grants, never a role flag: the `**` grant is stored as a
// role:operator acl_membership edge (grantACLTx), so operator status IS
// transitive membership in role:operator. A direct `admin`/`*` grant on the whole
// tree (`**`) also counts. This is the gate for /root elevation (steer.go
// cmdRoot): only a `**` holder may raise a turn to root. Empty sub → false.
func (d *DB) IsOperator(sub string) bool {
	if sub == "" {
		return false
	}
	if slices.Contains(d.aclEval().Ancestors(sub), "role:operator") {
		return true
	}
	return auth.Authorize(d.aclEval(), auth.Caller{Principal: sub}, "admin", "**", nil)
}

// GetTask reads one scheduled_tasks row by id, for the inspect_tasks per-task
// authz check. No row → (zero, false).
func (d *DB) GetTask(id string) (core.Task, bool) {
	return d.taskStore().GetTask(id)
}

// DueTasks atomically claims + returns the tasks ready to fire, backing
// GET /v1/tasks/due (timed's read half).
func (d *DB) DueTasks(now time.Time) ([]core.Task, error) { return d.taskStore().DueTasks(now) }

// RecoverFiringTasks re-arms tasks stranded in 'firing' by a crash mid-fire.
// Called once at routd startup (crash recovery).
func (d *DB) RecoverFiringTasks() (int64, error) { return d.taskStore().RecoverFiringTasks() }

// RecordTaskRun appends one task_run_logs row, backing POST /v1/tasks/runlog
// (timed's write half).
func (d *DB) RecordTaskRun(l store.TaskRunLog) error { return d.taskStore().RecordTaskRun(l) }

// RescheduleTask sets a fired task's next_run + status, backing POST
// /v1/tasks/{id}/reschedule (timed's reschedule half).
func (d *DB) RescheduleTask(id, nextRun, status string) error {
	return d.taskStore().RescheduleTask(id, nextRun, status)
}

// TaskRunLogs reads task_run_logs rows for the inspect_tasks per-task run history,
// translating store.TaskRunLog → ipc.TaskRunLog.
func (d *DB) TaskRunLogs(taskID string, limit int) []ipc.TaskRunLog {
	rows := d.taskStore().TaskRunLogs(taskID, limit)
	out := make([]ipc.TaskRunLog, len(rows))
	for i, r := range rows {
		out[i] = ipc.TaskRunLog{
			ID: r.ID, TaskID: r.TaskID, RunAt: r.RunAt,
			DurationMS: r.DurationMS, Status: r.Status,
			Result: r.Result, Error: r.Error,
		}
	}
	return out
}

// AllRunLogs returns recent task_run_logs rows across all tasks, for
// GET /v1/tasks/runs (timed's dashboard cross-task run feed).
func (d *DB) AllRunLogs(limit int) []ipc.TaskRunLog {
	rows := d.taskStore().AllRunLogs(limit)
	out := make([]ipc.TaskRunLog, len(rows))
	for i, r := range rows {
		out[i] = ipc.TaskRunLog{
			ID: r.ID, TaskID: r.TaskID, RunAt: r.RunAt,
			DurationMS: r.DurationMS, Status: r.Status,
			Result: r.Result, Error: r.Error,
		}
	}
	return out
}
