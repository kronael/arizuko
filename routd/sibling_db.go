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

// sibling_db.go is the federation surface. routd owns acl/acl_membership,
// secrets/secret_use_log (it holds SECRETS_KEY), scheduled_tasks/task_run_logs
// (serving timed) and pane_sessions (serving slakd); authd owns identity and runed
// owns session_log, both federated over HTTP. routd opens no sibling messages.db.

// Tasks reads scheduled_tasks for the tasks.json spawn snapshot: a root group
// sees every task (owner filter empty); a child sees only its own.
func (d *DB) Tasks(folder string, isRoot bool) []core.Task {
	return d.store().ListTasks(folder, isRoot)
}

// CountActiveTasks counts active scheduled_tasks for the /status surface.
func (d *DB) CountActiveTasks() int {
	return d.store().CountActiveTasks()
}

// PaneContextJID returns (contextJID, true) when a Slack assistant pane exists
// for the DM channelID. No row → ("", false).
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

// UpsertPane creates/refreshes the pane row keyed by (team, user, thread) (behind
// POST /v1/pane upsert=open). slakd calls this over HTTP.
func (d *DB) UpsertPane(teamID, userID, threadTS, channelID string) error {
	return d.store().OpenPane(teamID, userID, threadTS, channelID)
}

// SetPaneContextByTriple updates the pane's workspace-channel context keyed by
// (team, user, thread) — slakd's context-change path, served by POST /v1/pane.
// Empty contextJID clears it.
func (d *DB) SetPaneContextByTriple(teamID, userID, threadTS, contextJID string) error {
	return d.store().SetPaneContext(teamID, userID, threadTS, contextJID)
}

// store wraps routd.db as a *store.Store. It is the ONE seam routd reaches its
// own tables through: acl and acl_membership (auth.Authorize and its readers),
// groups, routes, route_tokens, scheduled_tasks, network_rules, pane_sessions
// and the per-user cap. Every one of those is an objstore entity whose
// coordinate and attribute names are declared in store, so a reader here that
// went to the driver itself would be a second spelling of a format store owns.
//
// An empty acl table denies everything — there is no fallback grant set.
func (d *DB) store() *store.Store { return store.NewOver(d.obj) }

// secretStore is the one variant: the same handle with the SECRETS_KEY keyring
// set, so secret reads decrypt `v2:` values and writes seal them. Every other
// caller wants the plain handle.
func (d *DB) secretStore() *store.Store {
	s := d.store()
	if len(d.secretKeyring) > 0 {
		s.SetSecretKeys(d.secretKeyring...)
	}
	return s
}

// UserCap returns the per-day cap for a user_sub in cents. Zero means uncapped
// (the default).
func (d *DB) UserCap(userSub string) (int, error) {
	return d.store().UserCap(userSub)
}

// SetUserCap writes the per-day cap for one user_sub. 0 = uncapped. The
// operator write half of UserCap, so `arizuko budget set user` lands on the
// row budgetGate reads.
func (d *DB) SetUserCap(userSub string, cents int) error {
	return d.store().SetUserCap(userSub, cents)
}

// ProfileNames answers every sub the instance knows a profile for and the name
// it holds, backing GET /v1/principals — the datalist an operator's grant form
// offers, which dashd reads over HTTP rather than opening routd.db for it.
func (d *DB) ProfileNames(ctx context.Context) (map[string]string, error) {
	return d.store().ProfileNames(ctx)
}

// EnvProfileSecrets resolves the caller's OWN user-scoped model credentials (BYOA)
// for the container env. Those keys are spent by the Claude Code SDK subprocess,
// not by a tool call, so they have no broker path. Every capability credential
// reaches a TOOL solely through ResolveConnectorSecrets (spec 5/13 §Trust model);
// the one exception is ShellEnvSecrets below, which needs its own grant per key.
//
// The narrowing lives HERE, not at the dispatch call site, because the call site
// is bypassable: an exported reader handing back the whole decrypted folder set
// would let the next writer of RunRequest.Secrets re-open the hole with a one-liner
// that reads correct. This reader never asks for a folder at all, and the query is
// SQL-scoped to scope_kind='user'.
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

// ShellEnvSecrets resolves the folder's capability credentials that an explicit
// `shell:env:<KEY>` grant marks readable from the agent's own shell, so a CLI the
// agent drives itself (`gh`) can authenticate. It returns ONLY granted keys and
// never the whole folder set — the same narrowing discipline as EnvProfileSecrets,
// and for the same reason: an exported bulk reader is what a later one-liner
// re-opens the hole with.
//
// Env-profile keys are dropped even when granted. They already have a path
// (EnvProfileSecrets), and the store rejects folder scope for them, so a
// `shell:env:ANTHROPIC_API_KEY` row can only ever name a user-scoped model
// credential the container already holds.
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

// folderSecrets resolves the folder-scoped secret set for `folder`, DECRYPTING
// `v2:` values via the SECRETS_KEY keyring (deepest-wins folder-ancestry
// precedence). SECRETS_KEY unset / read error → empty map. Unexported: the only
// consumer is the broker resolution below, and an exported bulk reader of folder
// capability secrets is exactly what EnvProfileSecrets exists to deny.
func (d *DB) folderSecrets(folder string) map[string]string {
	out, err := d.secretStore().FolderSecretsResolved(folder)
	if err != nil {
		return map[string]string{}
	}
	return out
}

// folderSecretsForUser is the one resolution body behind the broker: values plus
// the scope each key resolved from ("user" | "folder"). secret_use_log must
// record whether a call spent the caller's OWN credential or the folder default,
// and that distinction exists nowhere downstream of the overlay (spec 5/13
// § Audit).
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

// ConnectorSecrets narrows the resolved secrets to the `required` names a connector
// declares. callerSub overlays the triggering user's user-scoped secrets on top of
// the folder defaults (BYOA). Missing keys are omitted.
//
// A required key backed by a user-scoped surrogate-OAuth row (spec 5/15) is
// refreshed in place when within 60s of expiry. A DEFINITIVE rejection (revoked
// refresh_token) nulls the row's oauth columns, drops the key, and returns a
// "reconnect" error the caller surfaces to the agent; a transient failure keeps the
// stale token and no error.
//
// The second return names the scope each REQUIRED key resolved from — the audit
// provenance secret_use_log records. It is taken AFTER any surrogate refresh, so a
// key the refresh dropped reads as missing rather than as a credential that was
// spent.
func (d *DB) ConnectorSecrets(folder, callerSub string, required []string) (map[string]string, map[string]store.SecretScope, error) {
	return d.connectorSecrets(folder, callerSub, required, false)
}

// RefreshConnectorSecrets is ConnectorSecrets with the surrogate-OAuth refresh
// FORCED rather than gated on near-expiry: the reactive half of spec 5/15, called
// once behind a 401. A provider with an optimistic `expires_in` leaves expires_at
// looking healthy, so the proactive check would refresh nothing.
//
// It writes no secret_use_log row: this is a retry of the SAME call, already
// audited by the ConnectorSecrets that opened it.
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

// ListACL returns acl rows, optionally filtered by principal. Backs the list_acl
// tool (acl_resource.go). A read failure is an error, never an empty list
// (BUGS A4).
func (d *DB) ListACL(principal string) ([]core.ACLRow, error) {
	return d.store().ListACL(principal)
}

// UserScopes returns the distinct allow-scopes a sub has, expanding membership
// transitively. Backs GET /v1/users/{sub}/scopes (authd snapshots it at login).
// A listing, never a verdict — see auth.UserScopes.
func (d *DB) UserScopes(sub string) ([]string, error) {
	return auth.UserScopes(d.store(), sub)
}

// Authorize is the per-call row-ACL check for a caller that IS its own principal —
// the operator REST/resreg face, where the JWT sub is the whole answer to "who".
// Magnitude comes from the principal's role membership + operator grants; a no-match
// denies. The agent turn does not use this: see AuthorizeTurn.
func (d *DB) Authorize(sub, folder, action string, params map[string]string) bool {
	caller := auth.Caller{Principal: sub}
	return auth.Authorize(d.store(), caller, action, folder, params)
}

// AuthorizeTurn is the per-call row-ACL check for an in-container agent tool call,
// on the two axes of 5/33: turnFolder is the ROUTE the router resolved this turn to
// and confers in full; senderSub is the attested sender and adds only inside that
// folder's subtree. scope is the ACTUAL target — the caller's own folder for
// magnitude, a descendant for a management tool's containment.
//
// It takes the sender rather than a principal because a turn HAS no single "who" to
// pass: the folder answers "what may be reached through here" and the sender answers
// "what does this person hold". Stacking them into one string is how a confused
// deputy gets built, so the two arrive as two fields and auth.Authorize keeps them
// apart.
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

// IsOperator reports whether sub carries the operator `**` grant. Operators are
// emergent from grants, never a role flag: the `**` grant is stored as a
// role:operator acl_membership edge (grantACLInto), so operator status IS
// transitive membership in role:operator. A direct `admin`/`*` grant on the whole
// tree (`**`) also counts. This is the gate for /root elevation (steer.go
// cmdRoot): only a `**` holder may raise a turn to root. Empty sub → false.
func (d *DB) IsOperator(sub string) bool {
	if sub == "" {
		return false
	}
	if slices.Contains(d.store().Ancestors(sub), "role:operator") {
		return true
	}
	return auth.Authorize(d.store(), auth.Caller{Principal: sub}, "admin", "**", nil)
}

// GetTask reads one scheduled_tasks row by id, for the inspect_tasks per-task
// authz check. No row → (zero, false).
func (d *DB) GetTask(id string) (core.Task, bool) {
	return d.store().GetTask(id)
}

// DueTasks atomically claims + returns the tasks ready to fire, backing
// GET /v1/tasks/due (timed's read half).
func (d *DB) DueTasks(now time.Time) ([]core.Task, error) { return d.store().DueTasks(now) }

// RecoverFiringTasks re-arms tasks stranded in 'firing' by a crash mid-fire.
// Called once at routd startup (crash recovery).
func (d *DB) RecoverFiringTasks() (int64, error) { return d.store().RecoverFiringTasks() }

// RecordTaskRun appends one task_run_logs row, backing POST /v1/tasks/runlog
// (timed's write half).
func (d *DB) RecordTaskRun(l store.TaskRunLog) error { return d.store().RecordTaskRun(l) }

// RescheduleTask sets a fired task's next_run + status, backing POST
// /v1/tasks/{id}/reschedule (timed's reschedule half).
func (d *DB) RescheduleTask(id, nextRun, status string) error {
	return d.store().RescheduleTask(id, nextRun, status)
}

// TaskRunLogs reads task_run_logs rows for the inspect_tasks per-task run history,
// translating store.TaskRunLog → ipc.TaskRunLog.
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

// AllRunLogs returns recent task_run_logs rows across all tasks, for
// GET /v1/tasks/runs (timed's dashboard cross-task run feed).
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
