package routd

import (
	"context"
	"slices"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

// sibling_db.go is the federation surface: routd owns acl/acl_membership,
// secrets/secret_use_log (it holds SECRETS_KEY), scheduled_tasks/task_run_logs
// (serving timed) and pane_sessions (serving slakd); authd owns identity and
// runed owns session_log, both federated over HTTP.

// Tasks reads scheduled_tasks for the tasks.json spawn snapshot: a root group
// sees every task; a child sees only its own.
func (d *DB) Tasks(folder string, isRoot bool) []core.Task {
	return d.store().ListTasks(folder, isRoot)
}

// CountActiveTasks counts active scheduled_tasks for the /status surface.
func (d *DB) CountActiveTasks() int {
	return d.store().CountActiveTasks()
}

// PaneContextJID returns (contextJID, true) when a Slack assistant pane exists
// for the DM channelID.
func (d *DB) PaneContextJID(channelID string) (string, bool) {
	p, ok := d.store().PaneInChannel(channelID)
	if !ok {
		return "", false
	}
	return p.ContextJID, true
}

// SetPaneContext upserts the workspace-channel context for the pane keyed by
// channelID (behind POST /v1/pane).
func (d *DB) SetPaneContext(channelID, contextJID string) error {
	return d.store().SetPaneContextByChannel(channelID, contextJID)
}

// UpsertPane creates/refreshes the pane row keyed by (team, user, thread)
// (behind POST /v1/pane op=open).
func (d *DB) UpsertPane(teamID, userID, threadTS, channelID string) error {
	return d.store().OpenPane(teamID, userID, threadTS, channelID)
}

// SetPaneContextByTriple updates the pane's workspace-channel context keyed by
// (team, user, thread); empty contextJID clears it.
func (d *DB) SetPaneContextByTriple(teamID, userID, threadTS, contextJID string) error {
	return d.store().SetPaneContext(teamID, userID, threadTS, contextJID)
}

// store wraps routd's handle as a *store.Store — the ONE seam routd reaches its
// own tables through. Every one is an objstore entity whose coordinate and
// attribute names store declares, so a reader that went to the driver would be
// a second spelling of a format store owns. An empty acl table denies
// everything — there is no fallback grant set.
func (d *DB) store() *store.Store { return store.NewOver(d.obj) }

// secretStore is the one variant: the same handle with the SECRETS_KEY keyring
// set, so secret reads decrypt `v2:` values and writes seal them.
func (d *DB) secretStore() *store.Store {
	s := d.store()
	if len(d.secretKeyring) > 0 {
		s.SetSecretKeys(d.secretKeyring...)
	}
	return s
}

// UserCap returns the per-day cap for a user_sub in cents; zero is uncapped.
func (d *DB) UserCap(userSub string) (int, error) {
	return d.store().UserCap(userSub)
}

// SetUserCap writes the per-day cap for one user_sub; 0 = uncapped. The
// operator write half of UserCap.
func (d *DB) SetUserCap(userSub string, cents int) error {
	return d.store().SetUserCap(userSub, cents)
}

// ProfileNames answers every sub the instance knows a profile for and its
// name — GET /v1/principals, the datalist an operator's grant form offers.
func (d *DB) ProfileNames(ctx context.Context) (map[string]string, error) {
	return d.store().ProfileNames(ctx)
}

// EnvProfileSecrets resolves the caller's OWN user-scoped model credentials
// (BYOA) for the container env: those keys are spent by the SDK subprocess, not
// a tool call, so they have no broker path (spec 5/13 §Trust model). The
// narrowing lives HERE, not at the dispatch call site: an exported bulk reader
// is what a later one-liner re-opens the hole with, and this reader never asks
// for a folder at all.
func (d *DB) EnvProfileSecrets(userSub string) map[string]string {
	out := map[string]string{}
	if userSub == "" {
		return out
	}
	rows, err := d.secretStore().ListSecrets(store.ScopeUser, userSub)
	if err != nil {
		return out
	}
	for _, r := range rows {
		if _, ok := store.EnvProfileKeys[r.Key]; ok {
			out[r.Key] = r.Value
		}
	}
	return out
}

// ShellEnvSecrets resolves the folder credentials an explicit `shell:env:<KEY>`
// grant marks readable from the agent's own shell — ONLY granted keys, never
// the whole set, the EnvProfileSecrets discipline. Env-profile keys are dropped
// even when granted: they already have a path, and the store rejects folder
// scope for them.
func (d *DB) ShellEnvSecrets(folder, userSub string) map[string]string {
	out := map[string]string{}
	if folder == "" {
		return out
	}
	vals, _ := d.folderSecretsForUser(folder, userSub)
	keys := make([]string, 0, len(vals))
	for k := range vals {
		if _, ok := store.EnvProfileKeys[k]; ok {
			continue
		}
		keys = append(keys, k)
	}
	caller := auth.Caller{Principal: "folder:" + folder}
	for _, k := range auth.ShellEnvKeys(d.store(), caller, folder, keys) {
		out[k] = vals[k]
	}
	return out
}

// folderSecrets resolves the folder-scoped set, DECRYPTED via the keyring
// (deepest-wins ancestry); unset key / read error → empty. Unexported: an
// exported bulk reader of folder capability secrets is what EnvProfileSecrets
// exists to deny.
func (d *DB) folderSecrets(folder string) map[string]string {
	out, err := d.secretStore().FolderSecretsResolved(folder)
	if err != nil {
		return map[string]string{}
	}
	return out
}

// folderSecretsForUser is the one resolution body behind the broker: values
// plus the scope each key resolved from ("user" | "folder"), which
// secret_use_log records (spec 5/13 § Audit).
func (d *DB) folderSecretsForUser(folder, userSub string) (map[string]string, map[string]store.SecretScope) {
	out, scopes, err := d.secretStore().FolderSecretsResolvedForUser(folder, userSub)
	if err == nil {
		return out, scopes
	}
	// Fallback path: folder scope only, so the provenance is uniform.
	out = d.folderSecrets(folder)
	scopes = make(map[string]store.SecretScope, len(out))
	for k := range out {
		scopes[k] = store.ScopeFolder
	}
	return out, scopes
}

// SetSecret seals + upserts one folder/user secret (behind POST /v1/secrets).
func (d *DB) SetSecret(scope store.SecretScope, scopeID, key, value string) error {
	return d.secretStore().SetSecret(scope, scopeID, key, value)
}

// DeleteSecret removes one folder/user secret (behind DELETE /v1/secrets/{key});
// ErrSecretNotFound when no row matched.
func (d *DB) DeleteSecret(scope store.SecretScope, scopeID, key string) error {
	return d.secretStore().DeleteSecret(scope, scopeID, key)
}

// ConnectorSecrets narrows the resolved secrets to the `required` names a
// connector declares; callerSub overlays the user's own secrets on the folder
// defaults (BYOA). A user-scoped surrogate-OAuth row (spec 5/15) is refreshed
// in place near expiry: a DEFINITIVE rejection nulls the row, drops the key and
// returns a "reconnect" error; a transient failure keeps the stale token. The
// scopes are taken AFTER the refresh, so a dropped key reads as missing, not
// as a credential that was spent.
func (d *DB) ConnectorSecrets(folder, callerSub string, required []string) (map[string]string, map[string]store.SecretScope, error) {
	return d.connectorSecrets(folder, callerSub, required, false)
}

// RefreshConnectorSecrets FORCES the surrogate refresh: the reactive half of
// spec 5/15, called once behind a 401, because an optimistic `expires_in`
// leaves expires_at looking healthy. No secret_use_log row: a retry of the
// SAME call, already audited by the ConnectorSecrets that opened it.
func (d *DB) RefreshConnectorSecrets(folder, callerSub string, required []string) (map[string]string, error) {
	out, _, err := d.connectorSecrets(folder, callerSub, required, true)
	return out, err
}

func (d *DB) connectorSecrets(folder, callerSub string, required []string, force bool) (map[string]string, map[string]store.SecretScope, error) {
	if len(required) == 0 {
		return map[string]string{}, map[string]store.SecretScope{}, nil
	}
	all, allScopes := d.folderSecretsForUser(folder, callerSub)
	out := make(map[string]string, len(required))
	for _, k := range required {
		if v, ok := all[k]; ok {
			out[k] = v
		}
	}
	var err error
	if d.surrogate != nil && callerSub != "" {
		err = d.refreshOAuth(callerSub, required, out, force)
	}
	scopes := make(map[string]store.SecretScope, len(required))
	for _, k := range required {
		if _, ok := out[k]; !ok {
			scopes[k] = store.ScopeMissing
			continue
		}
		scopes[k] = allScopes[k]
	}
	return out, scopes, err
}

// ListACL returns acl rows, optionally filtered by principal. A read failure is
// an error, never an empty list (BUGS A4).
func (d *DB) ListACL(principal string) ([]core.ACLRow, error) {
	return d.store().ListACL(principal)
}

// UserScopes returns the distinct allow-scopes a sub has, expanding membership
// transitively — the snapshot authd takes at login. A listing, never a verdict.
func (d *DB) UserScopes(sub string) ([]string, error) {
	return auth.UserScopes(d.store(), sub)
}

// Authorize is the per-call row-ACL check for a caller that IS its own
// principal — the operator REST/resreg face. A no-match denies. The agent turn
// uses AuthorizeTurn.
func (d *DB) Authorize(sub, folder, action string, params map[string]string) bool {
	caller := auth.Caller{Principal: sub}
	return auth.Authorize(d.store(), caller, action, folder, params)
}

// AuthorizeTurn is the agent tool-call check on the two axes of 5/33:
// turnFolder is the ROUTE and confers in full; senderSub is the attested sender
// and adds only inside that folder's subtree; scope is the ACTUAL target. Two
// fields, never one stacked string — that is how a confused deputy gets built.
func (d *DB) AuthorizeTurn(senderSub, turnFolder, scope, action string, params map[string]string) bool {
	caller := auth.Caller{Principal: senderSub, Route: turnFolder}
	return auth.Authorize(d.store(), caller, action, scope, params)
}

// CheckHold asks whether this folder's call must wait for a human (spec 5/19).
// Deliberately NOT routed through Authorize — see auth.CheckHold for why the
// operator's (*, **) row would otherwise hold every tool.
func (d *DB) CheckHold(sub, folder, tool string, params map[string]string) bool {
	return auth.CheckHold(d.store(), auth.Caller{Principal: sub}, tool, folder, params)
}

// IsOperator reports whether sub carries the operator `**` grant: transitive
// membership in role:operator, or a direct `admin` on `**`. Operators are
// emergent from grants, never a role flag. The gate for /root elevation.
func (d *DB) IsOperator(sub string) bool {
	if sub == "" {
		return false
	}
	if slices.Contains(d.store().Ancestors(sub), "role:operator") {
		return true
	}
	return auth.Authorize(d.store(), auth.Caller{Principal: sub}, "admin", "**", nil)
}

// GetTask reads one scheduled_tasks row by id; no row → (zero, false).
func (d *DB) GetTask(id string) (core.Task, bool) {
	return d.store().GetTask(id)
}

// DueTasks atomically claims + returns the tasks ready to fire (timed's read half).
func (d *DB) DueTasks(now time.Time) ([]core.Task, error) { return d.store().DueTasks(now) }

// RecoverFiringTasks re-arms tasks stranded in 'firing' by a crash mid-fire.
func (d *DB) RecoverFiringTasks() (int64, error) { return d.store().RecoverFiringTasks() }

// RecordTaskRun appends one task_run_logs row (timed's write half).
func (d *DB) RecordTaskRun(l store.TaskRunLog) error { return d.store().RecordTaskRun(l) }

// RescheduleTask sets a fired task's next_run + status (timed's reschedule half).
func (d *DB) RescheduleTask(id, nextRun, status string) error {
	return d.store().RescheduleTask(id, nextRun, status)
}

// TaskRunLogs reads a task's run history, translated to ipc.TaskRunLog.
func (d *DB) TaskRunLogs(taskID string, limit int) []ipc.TaskRunLog {
	rows := d.store().TaskRunLogs(taskID, limit)
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

// AllRunLogs returns recent task_run_logs rows across all tasks (GET /v1/tasks/runs).
func (d *DB) AllRunLogs(limit int) []ipc.TaskRunLog {
	rows := d.store().AllRunLogs(limit)
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
