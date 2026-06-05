package routd

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

// sibling_db.go gives routd READ-ONLY access to the sibling DBs that share
// store/ but are owned (written) by other daemons in the split topology:
//
//   - messages.db — timed writes scheduled_tasks; slakd writes pane_sessions.
//   - runed.db    — runed writes session_log (per-spawn history).
//
// routd reads them to reach gated's full prompt/spawn context (tasks.json
// snapshot, Slack-pane hints, previous-session continuity). Ownership stays
// with the writers; routd never mutates these tables. A missing file leaves
// the handle nil and every accessor returns the empty result — same shape as
// gated against an empty store, no hard dependency on the sibling daemon.

// openSiblings opens read-only handles to the sibling DBs in dir, if present.
// Absent file → nil handle (the owning daemon may not run in this deployment).
func openSiblings(dir string) (msgs, runed *sql.DB) {
	return openRO(filepath.Join(dir, "messages.db")), openRO(filepath.Join(dir, "runed.db"))
}

// openRO opens path read-only. Returns nil when the file is absent or the open
// fails — callers treat nil as "no data", never an error.
func openRO(path string) *sql.DB {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil
	}
	return db
}

// SiblingTasks reads scheduled_tasks from messages.db (timed's table) for the
// tasks.json spawn snapshot. Port of store.ListTasks: a root group sees every
// task (owner filter empty); a child sees only its own. nil handle → nil.
func (d *DB) SiblingTasks(folder string, isRoot bool) []core.Task {
	if d.msgs == nil {
		return nil
	}
	owner := folder
	if isRoot {
		owner = ""
	}
	rows, err := d.msgs.Query(
		`SELECT id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode
		 FROM scheduled_tasks WHERE (? = '' OR owner = ?) ORDER BY created_at DESC`, owner, owner)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []core.Task
	for rows.Next() {
		var t core.Task
		var cron, nextRun *string
		var created string
		if err := rows.Scan(&t.ID, &t.Owner, &t.ChatJID, &t.Prompt,
			&cron, &nextRun, &t.Status, &created, &t.ContextMode); err != nil {
			return out
		}
		t.Created, _ = time.Parse(time.RFC3339, created)
		if cron != nil {
			t.Cron = *cron
		}
		if nextRun != nil {
			v, _ := time.Parse(time.RFC3339, *nextRun)
			t.NextRun = &v
		}
		if t.ContextMode == "" {
			t.ContextMode = "group"
		}
		out = append(out, t)
	}
	return out
}

// CountActiveTasks counts active scheduled_tasks in messages.db (timed's
// table) for the /status surface. Port of store.CountActiveTasks. nil handle
// → 0 (timed may not run in this deployment).
func (d *DB) CountActiveTasks() int {
	if d.msgs == nil {
		return 0
	}
	var n int
	d.msgs.QueryRow("SELECT COUNT(*) FROM scheduled_tasks WHERE status=?", core.TaskActive).Scan(&n)
	return n
}

// SiblingPaneContextJID reads pane_sessions from messages.db (slakd's table)
// and returns (contextJID, true) when a Slack assistant pane exists for the DM
// channelID. Port of store.GetPaneByChannel, narrowed to the field paneHints
// needs. nil handle / no row → ("", false).
func (d *DB) SiblingPaneContextJID(channelID string) (string, bool) {
	if d.msgs == nil {
		return "", false
	}
	var ctx sql.NullString
	err := d.msgs.QueryRow(
		`SELECT context_jid FROM pane_sessions WHERE channel_id = ?
		 ORDER BY opened_at DESC LIMIT 1`, channelID).Scan(&ctx)
	if err != nil {
		return "", false
	}
	return ctx.String, true
}

// aclStore wraps the sibling messages.db handle as a *store.Store so routd
// reuses gated's exact ACL evaluator (auth.AuthorizeWith) + readers
// (store.ListACL) verbatim — same overlay precedence, deny-wins, membership
// expansion. The acl/acl_membership rows are owned (written) by the operator
// via `arizuko grant` / authd; routd only reads. nil handle → nil store and
// every accessor returns the empty result (no acl table in this deployment →
// tier-default behaviour, same as gated against an empty store).
func (d *DB) aclStore() *store.Store {
	if d.msgs == nil {
		return nil
	}
	s := store.New(d.msgs)
	if len(d.secretKeyring) > 0 {
		s.SetSecretKeys(d.secretKeyring...)
	}
	return s
}

// FolderSecrets resolves the folder/user-scoped secret set for `folder`,
// reading the sibling messages.db `secrets` table and DECRYPTING `v2:` values
// via the SECRETS_KEY keyring. Reuses store.FolderSecretsResolved verbatim
// through aclStore() (same deepest-wins folder-ancestry precedence gated uses).
// nil sibling handle / SECRETS_KEY unset / read error → empty map (no hard
// fail; the caller treats absent secrets as "inject nothing"). routd is
// read-only here — the encrypt-at-rest WRITE path (store.EncryptPlaintextSecrets)
// stays with a secrets-owning daemon (gated today; unresolved in the pure split).
func (d *DB) FolderSecrets(folder string) map[string]string {
	s := d.aclStore()
	if s == nil {
		return map[string]string{}
	}
	out, err := s.FolderSecretsResolved(folder)
	if err != nil {
		return map[string]string{}
	}
	return out
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

// ListACL returns acl rows from the sibling messages.db, optionally filtered
// by principal. Read-only port of store.ListACL; used for the mcp:<tool>
// operator overlay (deriveFolderGrants) and the list_acl tool. nil handle →
// nil.
func (d *DB) ListACL(principal string) []core.ACLRow {
	s := d.aclStore()
	if s == nil {
		return nil
	}
	return s.ListACL(principal)
}

// Authorize is the per-call row-ACL check for an in-container agent tool
// call, evaluated against the sibling messages.db acl rows. Faithful to
// gated's StoreFns.Authorize (gateway.go): same Caller, same tier-default
// fallback config. sub is the canonical agent principal (folder:<folder>).
//
// When the sibling messages.db is absent (no acl table in this deployment),
// there are no operator rows, so authorization reduces to the tier-default
// fallback — exactly auth.AuthorizeWith's step 5 for mcp:* actions on the
// agent's own folder. We run that branch directly because AuthorizeWith
// returns false on a nil store. Non-mcp actions deny (no structural surface
// reaches routd's MCP firewall — those tools resolve via authorizeJID/
// AuthorizeStructural, not this row check).
func (d *DB) Authorize(sub, folder, action string, params map[string]string) bool {
	id := auth.Resolve(folder)
	if s := d.aclStore(); s != nil {
		caller := auth.Caller{Principal: sub}
		opts := auth.AuthorizeOpts{Folder: folder, WorldFolder: id.World, Tier: id.Tier}
		return auth.AuthorizeWith(s, caller, action, folder, params, opts)
	}
	tool, ok := strings.CutPrefix(action, "mcp:")
	if !ok {
		return false
	}
	rules := grants.DeriveRules(d, folder, id.Tier, id.World)
	return grants.CheckAction(rules, tool, params)
}

// SiblingIdentityForSub resolves a platform sub to its canonical identity and
// the full set of subs that identity claims, reading the identities/
// identity_claims tables in the sibling messages.db (gated's store). Reuses
// store.GetIdentityForSub verbatim via aclStore() so the lookup matches gated's
// exactly. Returns the ipc shape directly (routd imports ipc anyway). nil
// handle / unclaimed sub → (zero, nil, false) — the inspect_identity tool then
// renders the {identity:null, subs:[]} unclaimed shape.
func (d *DB) SiblingIdentityForSub(sub string) (ipc.Identity, []string, bool) {
	s := d.aclStore()
	if s == nil {
		return ipc.Identity{}, nil, false
	}
	idn, subs, ok := s.GetIdentityForSub(sub)
	if !ok {
		return ipc.Identity{}, nil, false
	}
	return ipc.Identity{ID: idn.ID, Name: idn.Name, CreatedAt: idn.CreatedAt}, subs, true
}

// SiblingGetTask reads one scheduled_tasks row from messages.db (timed's
// table) by id, for the inspect_tasks per-task authz check. Reuses
// store.GetTask via aclStore(). nil handle / no row → (zero, false).
func (d *DB) SiblingGetTask(id string) (core.Task, bool) {
	s := d.aclStore()
	if s == nil {
		return core.Task{}, false
	}
	return s.GetTask(id)
}

// SiblingTaskRunLogs reads task_run_logs rows from messages.db (timed's table)
// for the inspect_tasks per-task run history. Reuses store.TaskRunLogs via
// aclStore(), translating store.TaskRunLog → ipc.TaskRunLog. nil handle → nil.
func (d *DB) SiblingTaskRunLogs(taskID string, limit int) []ipc.TaskRunLog {
	s := d.aclStore()
	if s == nil {
		return nil
	}
	rows := s.TaskRunLogs(taskID, limit)
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

// SiblingRecentSessions reads the n most recent session_log rows from runed.db
// (runed's table) for the new_session continuity hint. Port of
// store.RecentSessions. nil handle → nil.
func (d *DB) SiblingRecentSessions(folder string, n int) []core.SessionRecord {
	if d.runedDB == nil {
		return nil
	}
	rows, err := d.runedDB.Query(
		`SELECT id, group_folder, session_id, started_at, ended_at,
		        result, error, message_count
		 FROM session_log WHERE group_folder = ? ORDER BY id DESC LIMIT ?`, folder, n)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []core.SessionRecord
	for rows.Next() {
		var sr core.SessionRecord
		var startedAt string
		var endedAt, result, errStr *string
		var msgCount *int
		if err := rows.Scan(&sr.ID, &sr.Folder, &sr.SessionID, &startedAt,
			&endedAt, &result, &errStr, &msgCount); err != nil {
			return out
		}
		sr.StartedAt, _ = time.Parse(time.RFC3339, startedAt)
		if endedAt != nil {
			t, _ := time.Parse(time.RFC3339, *endedAt)
			sr.EndedAt = &t
		}
		if result != nil {
			sr.Result = *result
		}
		if errStr != nil {
			sr.Error = *errStr
		}
		if msgCount != nil {
			sr.MsgCount = *msgCount
		}
		out = append(out, sr)
	}
	return out
}
