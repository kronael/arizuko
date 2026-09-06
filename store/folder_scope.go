package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/kronael/arizuko/groupfolder"
)

// folderScope names ONE column that carries a folder coordinate, and how the
// folder is spelled inside it. It is the argument of the versioned replace
// (spec 6/40 §3): a group delete is `replace(subtree(folder), ∅)`, so the scope
// predicate is all the operation needs to name.
type folderScope struct {
	table string
	col   string
	// prefix precedes the folder inside col. "folder:" for the acl principal a
	// folder holds its grants as — the row SeedMemberRoleTx writes.
	prefix string
	// seps are the characters that may follow the folder inside col. "/" opens a
	// child folder; "#" opens a topic, which only routes.target carries.
	seps []string
	// and restricts the rows this scope owns, for a table whose column holds
	// more than folders.
	and string
	// set, when non-empty, is an UPDATE assignment applied instead of a DELETE:
	// the row outlives the folder and only its binding to the folder goes.
	set string
}

// stmt renders the statement that removes folder's rows from one scope.
func (f folderScope) stmt(folder string) (string, []any) {
	val := f.prefix + folder
	preds := make([]string, 0, len(f.seps))
	args := make([]any, 0, 3*len(f.seps))
	for _, sep := range f.seps {
		preds = append(preds, groupfolder.SQLSubtree(f.col, sep))
		args = append(args, groupfolder.SubtreeArgs(val, sep)...)
	}
	where := strings.Join(preds, " OR ")
	if f.and != "" {
		where = f.and + " AND (" + where + ")"
	}
	if f.set != "" {
		return "UPDATE " + f.table + " SET " + f.set + " WHERE " + where, args
	}
	return "DELETE FROM " + f.table + " WHERE " + where, args
}

// folderScopePurge is the scope predicate of the group delete: every column
// holding a row that CONFERS on the folder or ROUTES into it. A row left here
// is authority the next tenant of the same path inherits, which is why the list
// is one declaration and not one per caller.
//
// groups leads so the ON DELETE CASCADE hanging off it (web_routes,
// route_tokens.owner_folder, github_sources — foreign_keys rides every DSN,
// db_utils.FileDSN) fires inside the same transaction.
var folderScopePurge = []folderScope{
	// The folder itself and its children. Deleting a parent and leaving the
	// children is an orphan with grants of its own.
	{table: "groups", col: "folder", seps: []string{"/"}},

	// Confers. An acl row scoped at the subtree names who may act on it; a
	// membership edge gives the folder principal a parent's grants; a
	// folder-scoped secret is a live credential; a network rule is egress.
	{table: "acl", col: "scope", seps: []string{"/"}},
	{table: "acl_membership", col: "child", prefix: "folder:", seps: []string{"/"}},
	{table: "secrets", col: "scope_id", seps: []string{"/"}, and: "scope_kind = 'folder'"},
	{table: "network_rules", col: "folder", seps: []string{"/"}},

	// Routes in. A route target, an engagement, a sticky binding and a
	// scheduled task each deliver a message the next tenant answers.
	{table: "routes", col: "target", seps: []string{"/", "#"}},
	{table: "chat_reply_state", col: "engaged_folder", seps: []string{"/"}},
	{table: "scheduled_tasks", col: "owner", seps: []string{"/"}},
	// The chat outlives the folder — it carries its own cursor — so the
	// binding is cleared and the row stays.
	{table: "chats", col: "sticky_group", seps: []string{"/"},
		set: "sticky_group = NULL, sticky_topic = NULL"},

	// Reaches the next tenant's agent. A session id resumes the previous
	// tenant's transcript; a watched source is read straight into the prompt; a
	// held action is released into the folder.
	{table: "sessions", col: "group_folder", seps: []string{"/"}},
	{table: "group_watchers", col: "observer", seps: []string{"/"}},
	{table: "group_watchers", col: "source", seps: []string{"/"}},
	{table: "pending_actions", col: "group_folder", seps: []string{"/"}},

	// Owned assets, which name identities the folder installed.
	{table: "installed_packages", col: "folder", seps: []string{"/"}},
}

// folderScopeKeep names every other table in the schema and why a group delete
// leaves it alone. With folderScopePurge it is a TOTAL classification: the
// completeness test fails on a table in neither list, so a table joining the
// schema cannot skip the question of whether it confers on a folder.
var folderScopeKeep = map[string]string{
	// History. What happened is not the folder's to take away, and this
	// transaction writes the delete's own row into audit_log.
	"audit_log":      "the record of the delete itself commits with it",
	"cli_audit":      "history",
	"ipc_audit":      "history",
	"secret_use_log": "history",
	"session_log":    "history",
	"task_run_logs":  "history; cascaded by scheduled_tasks.id where a run is still linked",
	"cost_log":       "the spend a deleted folder booked is still owed",
	// The gateway meters spend now (spec 6/39); this holds what arizuko
	// booked before it, frozen, with no writer. A delete must not erase a
	// folder's billing record any more than it erases its audit trail.
	"cost_log_archive": "history; the ledger arizuko kept before the gateway",
	"messages":         "the transcript of a chat that outlives the folder",

	// The FTS index over messages and the four shadow tables sqlite hangs off
	// it. Its triggers follow messages, which this delete does not touch.
	"messages_fts":         "index over messages",
	"messages_fts_data":    "fts5 shadow table",
	"messages_fts_idx":     "fts5 shadow table",
	"messages_fts_docsize": "fts5 shadow table",
	"messages_fts_config":  "fts5 shadow table",

	// Cascaded by the groups row this delete removes.
	"web_routes":     "FK groups(folder) ON DELETE CASCADE",
	"route_tokens":   "FK groups(folder) ON DELETE CASCADE; a token naming a gone target is refused at webd ingress (BUGS L5)",
	"github_sources": "FK groups(folder) ON DELETE CASCADE",

	// Turn state, keyed by a turn id the next tenant never mints.
	"turn_context": "keyed by turn_id; a re-created folder mints new ones",
	"turn_results": "keyed by turn_id; a re-created folder mints new ones",

	// No folder coordinate at all.
	"chat_proactive":   "keyed by chat jid",
	"chat_sessions":    "keyed by chat jid",
	"pane_sessions":    "keyed by slack thread",
	"user_profiles":    "a person, not a folder",
	"auth_users":       "a person, not a folder",
	"auth_sessions":    "a person, not a folder",
	"identities":       "a person, not a folder",
	"identity_claims":  "a person, not a folder",
	"identity_codes":   "a person, not a folder",
	"onboarding":       "a person, not a folder",
	"onboarding_gates": "instance-wide",
	"proxyd_routes":    "owner is a package or the instance, never a folder",
	"idempotency_keys": "keyed by request",
	"router_state":     "instance-wide",
	"config_meta":      "instance-wide",
	"migrations":       "schema bookkeeping",
	"sqlite_sequence":  "schema bookkeeping",
}

// folderScopeUnpurged names a table that carries a folder coordinate the delete
// does NOT remove, and what blocks it. A row here outlives its folder and the
// next tenant of the path sees it.
var folderScopeUnpurged = map[string]string{
	// routd.db keys the queue on `folder`, messages.db on the pre-split
	// `group_id`, and one statement cannot name both. Renaming messages.db's
	// column moves it out from under cmd/arizuko/migrate_split.go, which reads
	// `group_id, origin, event, created_at` by name. FlushSysMsgs drains the
	// queue into the agent's prompt, so what survives is the previous tenant's
	// queued events.
	"system_messages": "the column is `folder` in routd.db and `group_id` in messages.db",

	// onbod owns both, in onbod.db. `target_glob` names a folder subtree — an
	// unredeemed invite admits someone to the path, and a redemption record
	// authorizes handleCreateWorld under it — but the purge is one transaction
	// on the owner's database and cannot reach a second one. Closing this needs
	// the delete to reach onbod's /v1, or the generation (specs/6/37 redesign 1),
	// which makes reach unnecessary.
	"invites":            "onbod owns it, in onbod.db",
	"invite_redemptions": "onbod owns it, in onbod.db",
}

// FolderScopeClassified reports whether table carries a verdict in the
// folder-scope declaration. The completeness tests — one per schema, because
// routd.db and messages.db do not hold the same tables — walk sqlite_master
// through it.
func FolderScopeClassified(table string) bool {
	if _, ok := folderScopeKeep[table]; ok {
		return true
	}
	if _, ok := folderScopeUnpurged[table]; ok {
		return true
	}
	for _, f := range folderScopePurge {
		if f.table == table {
			return true
		}
	}
	return false
}

// errNoSuchGroup rolls the purge back when the subtree holds no groups row.
// DeleteGroup swallows it: nothing was there, so nothing happened and nothing is
// recorded.
var errNoSuchGroup = errors.New("store: no group row in subtree")

// purgeFolder removes folder's subtree from every scope, and returns how many
// groups rows went with it. One transaction: a partial purge IS the state this
// exists to prevent.
func purgeFolder(tx *sql.Tx, folder string) (int, error) {
	removed := 0
	for _, f := range folderScopePurge {
		q, args := f.stmt(folder)
		res, err := tx.Exec(q, args...)
		if err != nil {
			return 0, err
		}
		if f.table == "groups" {
			n, err := res.RowsAffected()
			if err != nil {
				return 0, err
			}
			removed = int(n)
		}
	}
	return removed, nil
}
