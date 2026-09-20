package store

// folder_scope.go is the scope predicate of the group delete: every place a row
// CONFERS on a folder or ROUTES into it, declared once so no caller carries a
// list of its own.
//
// It is the argument of the versioned replace (spec 6/40 §3): a group delete is
// `replace(subtree(folder), ∅)`, so the scope predicate is all the operation
// needs to name.
//
// EVERY SCOPE IS AN ENTITY, and the completeness tests walk the whole schema
// through FolderScopeClassified: a table or entity in neither list fails, so one
// joining the schema cannot skip the question of whether it confers on a folder.
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
	"errors"
	"slices"
	"strings"

	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
)

// entityScope names ONE entity that carries a folder coordinate, and how to
// read the folder out of one of its items.
//
// folderOf is a function rather than a coordinate convention because the folder
// sits somewhere different in each: it is the whole key of a groups item, the
// first field of network_rules' composite one, an attribute on web_routes, and
// a `folder:`-prefixed principal inside acl_membership's child. The callback IS
// that declaration, and the completeness test is what checks it against the
// schema.
type entityScope struct {
	entity   string
	folderOf func(objstore.Item) (string, bool)
	// seps are the characters that may follow the folder inside that value.
	seps []string
	// clear, when non-empty, names the attributes to take away INSTEAD of
	// deleting the item: the item outlives the folder and only its binding to
	// the folder goes. There is one — `chats`, which carries the chat's own
	// dispatch cursor.
	clear []string
}

// entityScopePurge is every scope.
//
// groups leads, so a reader sees the folder's own row go first; nothing depends
// on the order, because an entity has no cascade to fire and every delete below
// is issued explicitly.
//
// AN ENTITY MAY APPEAR TWICE, and `group_watchers` is why the list is a slice
// rather than a map: a subscription carries TWO folder coordinates and folderOf
// answers one, so the two halves are two entries. The second pass reads through
// the caller's own transaction, so it sees what the first one deleted.
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

	// The hot tier. A topic's continuity, an owed reply and a queued system
	// event each carry a folder coordinate and each would be inherited by the
	// next tenant of the path: the session resumes the last tenant's agent
	// transcript, the reply state hands it a live engagement window, and the
	// queued events arrive in its first prompt.
	{entity: Sessions, seps: []string{"/"}, folderOf: func(it objstore.Item) (string, bool) {
		folder, _, err := sessionCoord(it.Key.Sort)
		return folder, err == nil
	}},
	{entity: ChatReplyState, seps: []string{"/"}, folderOf: attrScope(AttrReplyEngagedFolder)},
	// The folder's message count. The FEED it counts is partitioned by the
	// folder itself and so cannot be reached by a scan of one partition; it goes
	// through folderPartitionPurge below.
	{entity: MessageFolderCounts, seps: []string{"/"},
		folderOf: func(it objstore.Item) (string, bool) { return it.Key.Sort, true }},
	{entity: SystemMessages, seps: []string{"/"}, folderOf: func(it objstore.Item) (string, bool) {
		return SysMsgFolder(it.Key.Sort)
	}},

	// Watches. A subscription hands the next tenant of either path the other
	// one's inbound messages as ambient context, so it goes whichever end the
	// deleted folder is.
	{entity: GroupWatchers, seps: []string{"/"}, folderOf: func(it objstore.Item) (string, bool) {
		observer, _, err := watcherCoord(it.Key.Sort)
		return observer, err == nil
	}},
	{entity: GroupWatchers, seps: []string{"/"}, folderOf: func(it objstore.Item) (string, bool) {
		_, source, err := watcherCoord(it.Key.Sort)
		return source, err == nil
	}},

	// The chat outlives the folder — it carries its own dispatch cursor — so
	// what goes is the binding and not the item. This is the one scope that
	// clears rather than deletes, and it is why entityScope has a `clear` half.
	{entity: Chats, seps: []string{"/"}, folderOf: attrScope(AttrChatStickyGroup),
		clear: []string{AttrChatStickyGroup, AttrChatStickyTopic}},
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

// folderScopeKeep names every other entity and why a group delete leaves it
// alone. With the two purge lists it is a TOTAL classification, and the two
// completeness tests are what keep it total: every entity an owner declares has
// a verdict here or in a purge list, and no verdict names an entity nobody
// declares (BUGS L302 is what an untotal list costs a reader).
//
// `auth_users` is authd's and `session_log` is runed's. They are here rather
// than absent because the question a reader asks is "does a group delete take
// this away", and for another owner's entity the answer is a deliberate no —
// the purge is one batch on routd's tables and reaches no other owner.
var folderScopeKeep = map[string]keepReason{
	// History. What happened is not the folder's to take away, and this
	// transaction writes the delete's own row into audit_log.
	"audit_log":      keep("the record of the delete itself commits with it"),
	"secret_use_log": keep("history"),
	"session_log":    keep("history"),
	"task_run_logs":  keep("history; a run names a task id the next tenant never mints"),
	"messages":       keep("the transcript of a chat that outlives the folder"),
	// The activity feed and the delivery state are projections of `messages`
	// with no folder coordinate at all — one constant partition and the delivery
	// status — so they follow the rows they point at.
	"message_activity": keep("one partition over every chat; it follows messages"),
	"message_delivery": keep("keyed by delivery status; it follows messages"),

	// The folder feed and its counter are PARTITIONED BY THE FOLDER, which is
	// the one thing a re-created folder inherits by name. Keeping them would hand
	// the next tenant the last one's ambient window — `ObservedSince` reads that
	// partition — so they go the way `sessions` goes, and for the same reason.
	// The messages themselves stay: the index is what names the folder, not the
	// transcript.
	// Turn state, keyed by a turn id the next tenant never mints.
	"turn_context": keep("keyed by turn_id; a re-created folder mints new ones"),
	"turn_results": keep("keyed by turn_id; a re-created folder mints new ones"),

	// No folder coordinate at all.
	"chat_proactive":   keep("keyed by chat jid"),
	"pane_sessions":    keep("keyed by slack thread"),
	"user_profiles":    keep("a person, not a folder"),
	"auth_users":       keep("a person, not a folder"),
	"onboarding":       keep("a person, not a folder"),
	"onboarding_gates": keep("instance-wide"),
	"proxyd_routes":    keep("owner is a package or the instance, never a folder"),
	"resreg_scopes":    keep("one version marker per subsystem, instance-wide"),
	"counters":         keep("one server-minted number per name, instance-wide"),
}

// folderScopeUnpurged names a table that carries a folder coordinate the delete
// does NOT remove, and what blocks it. A row here outlives its folder and the
// next tenant of the path sees it.
var folderScopeUnpurged = map[string]string{
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
	for _, e := range entityScopePurge {
		if e.entity == table {
			return true
		}
	}
	return slices.Contains(folderPartitionPurge, table)
}

// folderPartitionPurge names the entities a folder OWNS A WHOLE PARTITION of,
// which is a different shape from every scope above: those read one partition
// and pick the items whose folder is inside the subtree, and these have one
// partition PER folder.
//
// `message_folders` is the ambient window `ObservedSince` reads, keyed by the
// folder it was routed to. Leaving it would hand the next tenant of a re-used
// path the last one's observed messages — the same inheritance `sessions` and
// `chat_reply_state` are purged for, through a coordinate a partition scan
// cannot see. The messages themselves stay: what names the folder is the index.
var folderPartitionPurge = []string{MessageFolders}

// errNoFolder refuses a purge with no folder to purge. An empty folder names the
// whole subtree of everything, so it is the one argument this sweep must not
// take.
//
// IT IS THE ONLY WAY OUT BEFORE THE SWEEP RUNS. There is no "subtree holds no
// groups row" check and no rollback: `Groups` is the first entry in
// `entityScopePurge`, so a present row is simply counted with the rest, and an
// absent one leaves the count to the orphans. `DeleteGroup` then reads
// `removed == 0` as "nothing was there", which it is.
var errNoFolder = errors.New("store: purge needs a folder")

// purgeFolder removes folder's subtree from every scope. It returns how many
// `groups` items went with it, and separately how many rows of every OTHER
// entity did — an orphan sweep, where the groups row is already gone and the
// rows under it are not. Counting only the first made a sweep indistinguishable
// from a no-op, and both callers rendered it as "no group at X" (BUGS L323).
//
// ONE transaction: the caller owns it, every entity write rides in it through
// WriteEntitiesInTx, and a partial purge IS the state this exists to prevent.
// The entity deletes are chunked, because a batch carries at most
// MaxBatchActions and a large folder's subtree can exceed one.
func (s *Store) purgeFolder(ctx context.Context, folder string) (int, int, error) {
	if folder == "" {
		return 0, 0, errNoFolder
	}
	// The folders this purge owns are read FIRST, off `groups`, because the loop
	// below deletes those very rows: a read after it answers nothing and every
	// child folder's partitions survive for the next tenant to inherit.
	owned := append([]string{folder}, s.subtreeFolders(ctx, folder)...)
	removed, swept := 0, 0
	for _, e := range entityScopePurge {
		items, err := s.Obj().Query(ctx, objstore.Query{Entity: e.entity, Partition: OwnerRoutd})
		if err != nil {
			return 0, 0, err
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
			if len(e.clear) > 0 {
				writes = append(writes, objstore.Write{
					Op: objstore.Update, Entity: e.entity, Key: it.Key, Remove: e.clear,
				})
				continue
			}
			writes = append(writes, objstore.Write{
				Op: objstore.Delete, Entity: e.entity, Key: it.Key,
			})
		}
		if e.entity == Groups {
			removed = len(writes)
		} else {
			swept += len(writes)
		}
		if _, err := applyChunks(ctx, "purge "+e.entity+" of "+folder, writes,
			func(ctx context.Context, chunk []objstore.Write) (bool, error) {
				return s.TryWriteEntities(ctx, nil, chunk...)
			}); err != nil {
			return 0, 0, err
		}
	}
	if err := s.purgeFolderPartitions(ctx, owned); err != nil {
		return 0, 0, err
	}
	return removed, swept, nil
}

// purgeFolderPartitions empties the partitions the deleted subtree OWNS. It
// takes the folder list rather than reading it, because enumerating partitions
// is not an operation the interface has and the rows that name them are gone by
// the time this runs.
func (s *Store) purgeFolderPartitions(ctx context.Context, folders []string) error {
	for _, entity := range folderPartitionPurge {
		for _, owned := range folders {
			items, err := s.Obj().Query(ctx, objstore.Query{
				Entity: entity, Partition: owned, Project: []string{},
			})
			if err != nil {
				return err
			}
			writes := make([]objstore.Write, 0, len(items))
			for _, it := range items {
				writes = append(writes, objstore.Write{
					Op: objstore.Delete, Entity: entity, Key: it.Key,
				})
			}
			if _, err := applyChunks(ctx, "purge "+entity+" of "+owned, writes,
				func(ctx context.Context, chunk []objstore.Write) (bool, error) {
					return s.TryWriteEntities(ctx, nil, chunk...)
				}); err != nil {
				return err
			}
		}
	}
	return nil
}

// subtreeFolders answers the folders under one path, read off `groups`. A read
// that fails answers none rather than an error, because the purge above has
// already taken the rows this would have named and a second failure here would
// roll back a delete that succeeded.
func (s *Store) subtreeFolders(ctx context.Context, folder string) []string {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Groups, Partition: OwnerRoutd, Project: []string{},
	})
	if err != nil {
		return nil
	}
	var out []string
	for _, it := range items {
		if it.Key.Sort != folder && groupfolder.ContainsSep(folder, it.Key.Sort, "/") {
			out = append(out, it.Key.Sort)
		}
	}
	return out
}
