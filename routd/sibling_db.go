package routd

import (
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

// sibling_db.go is the federation surface: every table routd needs is now read
// from routd's OWN routd.db (acl/secrets/tasks/pane) or federated over HTTP
// (identity → authd, session_log → runed). routd opens NO sibling messages.db
// — the last sibling-read (pane_sessions) moved here.
//
// ACL (acl/acl_membership): routd OWNS those tables in routd.db (spec 5/5 §
// Daemon ownership; migration 0007). See aclEval. Secrets (secrets/
// secret_use_log): routd OWNS them in routd.db (migration 0008; it holds
// SECRETS_KEY for connector injection). See secretStore. Tasks (scheduled_tasks/
// task_run_logs): routd OWNS them in routd.db (migration 0009) and serves timed's
// read+write via GET /v1/tasks/due + POST /v1/tasks/runlog. See taskStore.
// Pane (pane_sessions): routd OWNS it in routd.db (migration 0010) and serves
// slakd's pane-write via POST /v1/pane. See paneStore. Identity (identities/
// identity_claims): authd OWNS it and serves GET /v1/identities/{sub}; routd
// snapshots it over HTTP (identity.go). session_log: runed OWNS it and serves
// GET /v1/sessions/recent; routd federates it over HTTP (session.go).

// SiblingTasks reads scheduled_tasks from routd's OWN routd.db (routd owns the
// table — migration 0009) for the tasks.json spawn snapshot. Reuses
// store.ListTasks: a root group sees every task (owner filter empty); a child
// sees only its own.
func (d *DB) SiblingTasks(folder string, isRoot bool) []core.Task {
	return d.taskStore().ListTasks(folder, isRoot)
}

// CountActiveTasks counts active scheduled_tasks in routd's OWN routd.db for
// the /status surface. Reuses store.CountActiveTasks.
func (d *DB) CountActiveTasks() int {
	return d.taskStore().CountActiveTasks()
}

// SiblingPaneContextJID reads pane_sessions from routd's OWN routd.db (routd
// owns the table — migration 0010) and returns (contextJID, true) when a Slack
// assistant pane exists for the DM channelID. Reuses store.GetPaneByChannel.
// No row → ("", false).
func (d *DB) SiblingPaneContextJID(channelID string) (string, bool) {
	p, ok := d.paneStore().GetPaneByChannel(channelID)
	if !ok {
		return "", false
	}
	return p.ContextJID, true
}

// SetPaneContext upserts the workspace-channel context for the pane keyed by
// channelID into routd's OWN routd.db (behind POST /v1/pane). Audit-free
// (pane writes never touched audit_log); reuses store.SetPaneContextByChannel.
func (d *DB) SetPaneContext(channelID, contextJID string) error {
	return d.paneStore().SetPaneContextByChannel(channelID, contextJID)
}

// aclEval wraps routd's OWN routd.db handle as a *store.Store so the ACL
// evaluator (auth.AuthorizeWith) + readers (store.ListACL, store.UserScopes)
// run against routd.db's acl/acl_membership tables — routd owns them (spec 5/5
// § Daemon ownership). Same overlay precedence, deny-wins, membership
// expansion. routd.db always has the acl tables (migration 0007), so this is
// never nil; an empty table yields tier-default behaviour, same as gated
// against an empty store.
func (d *DB) aclEval() *store.Store { return store.New(d.db) }

// secretStore wraps routd's OWN routd.db handle as a *store.Store with the
// SECRETS_KEY keyring set, so secret reads (FolderSecretsResolved) decrypt `v2:`
// values and writes (PutSecretRow/DeleteSecretRow) seal them — routd OWNS the
// secrets table (spec 5/5 § Daemon ownership; routd already holds SECRETS_KEY
// for connector injection). routd.db always has the secrets table (migration
// 0008), so this is never nil; an empty table yields no secrets, same as gated
// against an empty store.
func (d *DB) secretStore() *store.Store {
	s := store.New(d.db)
	if len(d.secretKeyring) > 0 {
		s.SetSecretKeys(d.secretKeyring...)
	}
	return s
}

// taskStore wraps routd's OWN routd.db handle as a *store.Store so routd reuses
// gated's exact task readers/writers verbatim — routd OWNS scheduled_tasks +
// task_run_logs (spec 5/5 § Daemon ownership; migration 0009). routd.db always
// has the task tables, so this is never nil; an empty table yields no tasks,
// same as gated against an empty store. Writes go through the audit-free
// variants (PutTaskRow/SetTaskStatus/RemoveTask/RecordTaskRun) since routd.db
// has no audit_log table.
func (d *DB) taskStore() *store.Store { return store.New(d.db) }

// paneStore wraps routd's OWN routd.db handle as a *store.Store so routd reuses
// gated's exact pane readers/writers verbatim — routd OWNS pane_sessions (spec
// 5/5 § Daemon ownership; migration 0010). routd.db always has the table, so an
// empty table yields no pane, same as gated against an empty store. Pane writes
// are audit-free (they never touched audit_log).
func (d *DB) paneStore() *store.Store { return store.New(d.db) }

// FolderSecrets resolves the folder/user-scoped secret set for `folder` from
// routd's OWN routd.db `secrets` table, DECRYPTING `v2:` values via the
// SECRETS_KEY keyring. Reuses store.FolderSecretsResolved verbatim through
// secretStore() (same deepest-wins folder-ancestry precedence gated uses).
// SECRETS_KEY unset / read error → empty map (no hard fail; the caller treats
// absent secrets as "inject nothing").
func (d *DB) FolderSecrets(folder string) map[string]string {
	out, err := d.secretStore().FolderSecretsResolved(folder)
	if err != nil {
		return map[string]string{}
	}
	return out
}

// SetSecret seals + upserts one folder/user secret into routd's OWN routd.db
// (the operator write path behind POST /v1/secrets). Audit-free (routd.db has
// no audit_log table); reuses store's crypto via PutSecretRow.
func (d *DB) SetSecret(scope store.SecretScope, scopeID, key, value string) error {
	return d.secretStore().PutSecretRow(scope, scopeID, key, value)
}

// DeleteSecret removes one folder/user secret from routd's OWN routd.db (behind
// DELETE /v1/secrets/{key}). ErrSecretNotFound when no row matched.
func (d *DB) DeleteSecret(scope store.SecretScope, scopeID, key string) error {
	return d.secretStore().DeleteSecretRow(scope, scopeID, key)
}

// ConnectorSecrets narrows the folder's resolved secrets to the `required`
// names a connector declares (its [[mcp_connector]] secrets= list), mirroring
// gated's requires=/RequiresSecrets intersection: a connector only ever sees
// the keys it asked for, never the folder's full secret set. Missing keys are
// omitted (renderEnv leaves their placeholder empty; the scrubber skips empty
// values). nil/empty required → empty map.
func (d *DB) ConnectorSecrets(folder string, required []string) map[string]string {
	if len(required) == 0 {
		return map[string]string{}
	}
	all := d.FolderSecrets(folder)
	out := make(map[string]string, len(required))
	for _, k := range required {
		if v, ok := all[k]; ok {
			out[k] = v
		}
	}
	return out
}

// ListACL returns acl rows from routd's OWN routd.db, optionally filtered by
// principal. Port of store.ListACL; used for the mcp:<tool> operator overlay
// (deriveFolderGrants) and the list_acl tool.
func (d *DB) ListACL(principal string) []core.ACLRow {
	return d.aclEval().ListACL(principal)
}

// UserScopes returns the distinct allow-scopes a sub has against routd's OWN
// acl rows, expanding membership transitively. Backs the
// GET /v1/users/{sub}/scopes surface authd snapshots at login. Port of
// store.UserScopes.
func (d *DB) UserScopes(sub string) []string {
	return d.aclEval().UserScopes(sub)
}

// Authorize is the per-call row-ACL check for an in-container agent tool
// call, evaluated against routd's OWN routd.db acl rows. Faithful to gated's
// StoreFns.Authorize (gateway.go): same Caller, same tier-default fallback
// config. sub is the canonical agent principal (folder:<folder>). With no
// operator rows present, auth.AuthorizeWith's step 5 reduces to the
// tier-default fallback for mcp:* actions on the agent's own folder.
func (d *DB) Authorize(sub, folder, action string, params map[string]string) bool {
	id := auth.Resolve(folder)
	caller := auth.Caller{Principal: sub}
	opts := auth.AuthorizeOpts{Folder: folder, WorldFolder: id.World, Tier: id.Tier}
	return auth.AuthorizeWith(d.aclEval(), caller, action, folder, params, opts)
}

// SiblingGetTask reads one scheduled_tasks row from routd's OWN routd.db by id,
// for the inspect_tasks per-task authz check. Reuses store.GetTask via
// taskStore(). no row → (zero, false).
func (d *DB) SiblingGetTask(id string) (core.Task, bool) {
	return d.taskStore().GetTask(id)
}

// CreateTask inserts one scheduled task into routd's OWN routd.db (behind the
// schedule_task agent tool). Audit-free (routd.db has no audit_log table);
// reuses store's insert via PutTaskRow.
func (d *DB) CreateTask(t core.Task) error { return d.taskStore().PutTaskRow(t) }

// SetTaskStatus updates one task's status in routd's OWN routd.db (behind
// pause_task/resume_task). Audit-free.
func (d *DB) SetTaskStatus(id, status string) error { return d.taskStore().SetTaskStatus(id, status) }

// DeleteTask removes one task from routd's OWN routd.db (behind cancel_task).
// Audit-free.
func (d *DB) DeleteTask(id string) error { return d.taskStore().RemoveTask(id) }

// DueTasks atomically claims + returns the tasks ready to fire, backing
// GET /v1/tasks/due (timed's read half).
func (d *DB) DueTasks(now time.Time) ([]core.Task, error) { return d.taskStore().DueTasks(now) }

// RecordTaskRun appends one task_run_logs row, backing POST /v1/tasks/runlog
// (timed's write half).
func (d *DB) RecordTaskRun(l store.TaskRunLog) error { return d.taskStore().RecordTaskRun(l) }

// SiblingTaskRunLogs reads task_run_logs rows from routd's OWN routd.db for the
// inspect_tasks per-task run history. Reuses store.TaskRunLogs via taskStore(),
// translating store.TaskRunLog → ipc.TaskRunLog.
func (d *DB) SiblingTaskRunLogs(taskID string, limit int) []ipc.TaskRunLog {
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
