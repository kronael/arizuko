package store

// folder_scope.go is the scope predicate of the group delete: every place a row
// CONFERS on a folder or ROUTES into it, declared once so no caller carries a
// list of its own.
//
// It is the argument of the versioned replace (spec 6/40 §3): a group delete is
// `replace(subtree(folder), ∅)`, so the scope predicate is all the operation
// needs to name.
//
// TWO SHAPES, ONE CLASSIFICATION. Ten of the scopes are objstore entities and
// four are SQL tables, and the completeness tests walk both through
// FolderScopeClassified: a table or entity in neither list fails, so one joining
// the schema cannot skip the question of whether it confers on a folder.
//
// AN ENTITY HAS NO FOREIGN KEY, so no verdict here may say "the groups row's
// cascade removes it". Three tables were kept on exactly that claim —
// web_routes, route_tokens.owner_folder and github_sources — and the claim
// stops being true the moment the row stops being a SQL row: the row would then
// be deleted by NOTHING while a completeness test still passed, because the
// table is still classified (BUGS L253). All three are purged here now, by the
// same predicate every other scope uses.
//
// THE ENTITY PURGE READS AND THEN DELETES, because an entity has no predicate
// delete: the scope's items are queried, filtered by the containment predicate,
// and removed one write each. That is the unbounded read-then-delete spec 6/40
// costs, and a group delete is an operator action measured in ones per week.

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
)

// folderScope names ONE SQL column that carries a folder coordinate, and how
// the folder is spelled inside it.
type folderScope struct {
	table string
	col   string
	// seps are the characters that may follow the folder inside col. "/" opens a
	// child folder; "#" opens a topic.
	seps []string
	// set, when non-empty, is an UPDATE assignment applied instead of a DELETE:
	// the row outlives the folder and only its binding to the folder goes.
	set string
}

// stmt renders the statement that removes folder's rows from one scope.
func (f folderScope) stmt(folder string) (string, []any) {
	preds := make([]string, 0, len(f.seps))
	args := make([]any, 0, 3*len(f.seps))
	for _, sep := range f.seps {
		preds = append(preds, groupfolder.SQLSubtree(f.col, sep))
		args = append(args, groupfolder.SubtreeArgs(folder, sep)...)
	}
	where := strings.Join(preds, " OR ")
	if f.set != "" {
		return "UPDATE " + f.table + " SET " + f.set + " WHERE " + where, args
	}
	return "DELETE FROM " + f.table + " WHERE " + where, args
}

// entityScope names ONE entity that carries a folder coordinate, and how to
// read the folder out of one of its items.
//
// folderOf is a function rather than a coordinate convention because the folder
// sits somewhere different in each: it is the whole key of a groups item, the
// first field of network_rules' composite one, an attribute on web_routes, and
// a `folder:`-prefixed principal inside acl_membership's child. The callback IS
// that declaration, and it is checked by the same completeness test the SQL
// half is.
type entityScope struct {
	entity   string
	folderOf func(objstore.Item) (string, bool)
	// seps are the characters that may follow the folder inside that value.
	seps []string
}

// folderScopePurge is the SQL half: the tables that still hold columns.
//
// `chats` is the one UPDATE. The chat outlives the folder — it carries its own
// cursor — so the binding is cleared and the row stays.
var folderScopePurge = []folderScope{
	{table: "chat_reply_state", col: "engaged_folder", seps: []string{"/"}},
	{table: "sessions", col: "group_folder", seps: []string{"/"}},
	{table: "group_watchers", col: "observer", seps: []string{"/"}},
	{table: "group_watchers", col: "source", seps: []string{"/"}},
	{table: "chats", col: "sticky_group", seps: []string{"/"},
		set: "sticky_group = NULL, sticky_topic = NULL"},
}

// entityScopePurge is the object half.
//
// groups leads, so a reader sees the folder's own row go first; nothing depends
// on the order, because an entity has no cascade to fire and every delete below
// is issued explicitly.
var entityScopePurge = []entityScope{
	// The folder itself and its children. Deleting a parent and leaving the
	// children is an orphan with grants of its own.
	{entity: Groups, seps: []string{"/"},
		folderOf: func(it objstore.Item) (string, bool) { return it.Key.Sort, true }},

	// Confers. An acl row scoped at the subtree names who may act on it; a
	// membership edge gives the folder principal a parent's grants; a
	// folder-scoped secret is a live credential; a network rule is egress.
	{entity: ACL, seps: []string{"/"}, folderOf: func(it objstore.Item) (string, bool) {
		parts := strings.Split(it.Key.Sort, KeySep)
		if len(parts) != 6 {
			return "", false
		}
		return parts[2], true // scope
	}},
	{entity: ACLMembership, seps: []string{"/"}, folderOf: func(it objstore.Item) (string, bool) {
		child, _, ok := strings.Cut(it.Key.Sort, KeySep)
		if !ok {
			return "", false
		}
		return strings.CutPrefix(child, "folder:")
	}},
	{entity: Secrets, seps: []string{"/"}, folderOf: func(it objstore.Item) (string, bool) {
		parts := strings.Split(it.Key.Sort, KeySep)
		if len(parts) != 3 || parts[0] != SecretScopeFolder {
			return "", false
		}
		return parts[1], true // scope_id
	}},
	{entity: NetworkRules, seps: []string{"/"}, folderOf: func(it objstore.Item) (string, bool) {
		folder, _, ok := strings.Cut(it.Key.Sort, KeySep)
		return folder, ok
	}},

	// Routes in. A route target, a scheduled task and a held action each deliver
	// a message or a call the next tenant answers. A target carries a topic, so
	// it is the one scope whose folder may be followed by "#" as well as "/".
	{entity: Routes, seps: []string{"/", "#"}, folderOf: func(it objstore.Item) (string, bool) {
		parts := strings.SplitN(it.Key.Sort, KeySep, 3)
		if len(parts) != 3 {
			return "", false
		}
		return parts[1], true // target
	}},
	{entity: ScheduledTasks, seps: []string{"/"}, folderOf: attrScope(attrTaskOwner)},
	{entity: PendingActions, seps: []string{"/"}, folderOf: attrScope(AttrPendGroupFolder)},

	// Reaches the next tenant. A web route publishes the folder's files, a route
	// token delivers into it, and a github source seeds routes that do.
	{entity: WebRoutes, seps: []string{"/"}, folderOf: attrScope(AttrWebFolder)},
	{entity: RouteTokens, seps: []string{"/"}, folderOf: attrScope(attrTokOwnerFolder)},
	{entity: GitHubSources, seps: []string{"/"}, folderOf: attrScope(attrGHOwnerFolder)},

	// Owned assets, which name identities the folder installed.
	{entity: InstalledPackages, seps: []string{"/"}, folderOf: func(it objstore.Item) (string, bool) {
		folder, _, ok := strings.Cut(it.Key.Sort, KeySep)
		return folder, ok
	}},
}

// attrScope reads the folder out of one named attribute. An item that does not
// carry it holds no folder coordinate, and absent is the only null this model
// has — so it is skipped rather than read as the empty folder, which Contains
// answers false for anyway.
func attrScope(name string) func(objstore.Item) (string, bool) {
	return func(it objstore.Item) (string, bool) {
		v, ok := it.Attrs[name]
		return v, ok
	}
}

// keepReason is why a group delete leaves a table alone.
type keepReason struct{ why string }

// keep is the verdict: a judgement about what the rows mean.
func keep(why string) keepReason { return keepReason{why: why} }

// folderScopeKeep names every other table and entity in the schema and why a
// group delete leaves it alone. With the two purge lists it is a TOTAL
// classification.
var folderScopeKeep = map[string]keepReason{
	// History. What happened is not the folder's to take away, and this
	// transaction writes the delete's own row into audit_log.
	"audit_log":      keep("the record of the delete itself commits with it"),
	"cli_audit":      keep("history"),
	"ipc_audit":      keep("history"),
	"secret_use_log": keep("history"),
	"session_log":    keep("history"),
	"task_run_logs":  keep("history; a run names a task id the next tenant never mints"),
	"cost_log":       keep("the spend a deleted folder booked is still owed"),
	// The gateway meters spend now (spec 6/39); this holds what arizuko
	// booked before it, frozen, with no writer. A delete must not erase a
	// folder's billing record any more than it erases its audit trail.
	"cost_log_archive": keep("history; the ledger arizuko kept before the gateway"),
	"messages":         keep("the transcript of a chat that outlives the folder"),

	// The FTS index over messages and the four shadow tables sqlite hangs off
	// it. Its triggers follow messages, which this delete does not touch.
	"messages_fts":         keep("index over messages"),
	"messages_fts_data":    keep("fts5 shadow table"),
	"messages_fts_idx":     keep("fts5 shadow table"),
	"messages_fts_docsize": keep("fts5 shadow table"),
	"messages_fts_config":  keep("fts5 shadow table"),

	// Turn state, keyed by a turn id the next tenant never mints.
	"turn_context": keep("keyed by turn_id; a re-created folder mints new ones"),
	"turn_results": keep("keyed by turn_id; a re-created folder mints new ones"),

	// No folder coordinate at all.
	"chat_proactive":   keep("keyed by chat jid"),
	"chat_sessions":    keep("keyed by chat jid"),
	"pane_sessions":    keep("keyed by slack thread"),
	"user_profiles":    keep("a person, not a folder"),
	"auth_users":       keep("a person, not a folder"),
	"auth_sessions":    keep("a person, not a folder"),
	"identities":       keep("a person, not a folder"),
	"identity_claims":  keep("a person, not a folder"),
	"identity_codes":   keep("a person, not a folder"),
	"onboarding":       keep("a person, not a folder"),
	"onboarding_gates": keep("instance-wide"),
	"proxyd_routes":    keep("owner is a package or the instance, never a folder"),
	"idempotency_keys": keep("keyed by request"),
	"router_state":     keep("instance-wide"),
	"resreg_scopes":    keep("one version marker per subsystem, instance-wide"),
	"counters":         keep("one server-minted number per name, instance-wide"),
	"config_meta":      keep("instance-wide"),
	"migrations":       keep("schema bookkeeping"),
	"sqlite_sequence":  keep("schema bookkeeping"),
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
	for _, e := range entityScopePurge {
		if e.entity == table {
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
// groups items went with it.
//
// ONE transaction: the caller owns it, every entity write rides in it through
// WriteEntitiesInTx, and a partial purge IS the state this exists to prevent.
// The entity deletes are chunked, because a batch carries at most
// MaxBatchActions and a large folder's subtree can exceed one.
func (s *Store) purgeFolder(ctx context.Context, tx *sql.Tx, folder string) (int, error) {
	if folder == "" {
		return 0, errNoSuchGroup
	}
	removed := 0
	for _, e := range entityScopePurge {
		items, err := s.Obj().Query(ctx, objstore.Query{Entity: e.entity, Partition: OwnerRoutd})
		if err != nil {
			return 0, err
		}
		var writes []objstore.Write
		for _, it := range items {
			v, ok := e.folderOf(it)
			if !ok {
				continue
			}
			inside := false
			for _, sep := range e.seps {
				inside = inside || groupfolder.ContainsSep(folder, v, sep)
			}
			if !inside {
				continue
			}
			writes = append(writes, objstore.Write{
				Op: objstore.Delete, Entity: e.entity, Key: it.Key,
			})
		}
		if e.entity == Groups {
			removed = len(writes)
		}
		if _, err := applyChunks(ctx, "purge "+e.entity+" of "+folder, writes,
			func(ctx context.Context, chunk []objstore.Write) (bool, error) {
				return s.WriteEntitiesInTx(ctx, tx, nil, chunk...)
			}); err != nil {
			return 0, err
		}
	}
	for _, f := range folderScopePurge {
		q, args := f.stmt(folder)
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return 0, err
		}
	}
	return removed, nil
}
