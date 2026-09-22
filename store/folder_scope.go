package store

// folder_scope.go is the scope predicate of the group delete: every place a row
// CONFERS on a folder or ROUTES into it, declared once (spec 6/40 §3: a group
// delete is `replace(subtree(folder), ∅)`).
//
// EVERY SCOPE IS AN ENTITY, and the completeness tests walk the whole schema
// through FolderScopeClassified: an entity in neither list fails.
//
// AN ENTITY HAS NO FOREIGN KEY, so no verdict here may say "the groups row's
// cascade removes it": three tables kept on that claim were deleted by NOTHING
// once they stopped being SQL rows, while the completeness test still passed
// (BUGS L253).
//
// The purge reads and then deletes, one write each, because an entity has no
// predicate delete; a group delete is an operator action measured in ones per
// week.

import (
	"context"
	"slices"
	"strings"

	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
)

// entityScope names ONE entity that carries a folder coordinate, and how to
// read the folder out of one of its items. folderOf is a function because the
// folder sits somewhere different in each entity.
type entityScope struct {
	entity   string
	folderOf func(objstore.Item) (string, bool)
	// seps are the characters that may follow the folder inside that value.
	seps []string
	// clear, when non-empty, names the attributes to take away INSTEAD of
	// deleting the item: the item outlives the folder and only its binding
	// goes.
	clear []string
}

// entityScopePurge is every scope. groups leads so the folder's own row goes
// first; nothing depends on the order. An entity may appear twice, which is
// why this is a slice: `group_watchers` carries TWO folder coordinates.
var entityScopePurge = []entityScope{
	// The folder itself and its children.
	{entity: Groups, seps: []string{"/"},
		folderOf: func(it objstore.Item) (string, bool) { return it.Key.Sort, true }},

	// Confers.
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

	// Routes in. A target carries a topic, so it is the one scope whose folder
	// may be followed by "#" as well as "/".
	{entity: Routes, seps: []string{"/", "#"}, folderOf: func(it objstore.Item) (string, bool) {
		parts := strings.SplitN(it.Key.Sort, KeySep, 3)
		if len(parts) != 3 {
			return "", false
		}
		return parts[1], true // target
	}},
	{entity: ScheduledTasks, seps: []string{"/"}, folderOf: attrScope(attrTaskOwner)},
	{entity: PendingActions, seps: []string{"/"}, folderOf: attrScope(AttrPendGroupFolder)},

	// Reaches the next tenant.
	{entity: WebRoutes, seps: []string{"/"}, folderOf: attrScope(AttrWebFolder)},
	{entity: RouteTokens, seps: []string{"/"}, folderOf: attrScope(attrTokOwnerFolder)},
	{entity: GitHubSources, seps: []string{"/"}, folderOf: attrScope(attrGHOwnerFolder)},

	// Owned assets.
	{entity: InstalledPackages, seps: []string{"/"}, folderOf: func(it objstore.Item) (string, bool) {
		folder, _, ok := strings.Cut(it.Key.Sort, KeySep)
		return folder, ok
	}},

	// The hot tier: each would be inherited by the next tenant of the path.
	{entity: Sessions, seps: []string{"/"}, folderOf: func(it objstore.Item) (string, bool) {
		folder, _, err := sessionCoord(it.Key.Sort)
		return folder, err == nil
	}},
	{entity: ChatReplyState, seps: []string{"/"}, folderOf: attrScope(AttrReplyEngagedFolder)},
	// The folder's message count. The FEED it counts is partitioned by the
	// folder and goes through folderPartitionPurge.
	{entity: MessageFolderCounts, seps: []string{"/"},
		folderOf: func(it objstore.Item) (string, bool) { return it.Key.Sort, true }},
	{entity: SystemMessages, seps: []string{"/"}, folderOf: func(it objstore.Item) (string, bool) {
		return SysMsgFolder(it.Key.Sort)
	}},

	// Watches: whichever end the deleted folder is.
	{entity: GroupWatchers, seps: []string{"/"}, folderOf: func(it objstore.Item) (string, bool) {
		observer, _, err := watcherCoord(it.Key.Sort)
		return observer, err == nil
	}},
	{entity: GroupWatchers, seps: []string{"/"}, folderOf: func(it objstore.Item) (string, bool) {
		_, source, err := watcherCoord(it.Key.Sort)
		return source, err == nil
	}},

	// The chat outlives the folder (it carries its own dispatch cursor), so the
	// binding goes and not the item.
	{entity: Chats, seps: []string{"/"}, folderOf: attrScope(AttrChatStickyGroup),
		clear: []string{AttrChatStickyGroup, AttrChatStickyTopic}},
}

// attrScope reads the folder out of one named attribute. An item that does not
// carry it holds no folder coordinate and is skipped, never read as the empty
// folder.
func attrScope(name string) func(objstore.Item) (string, bool) {
	return func(it objstore.Item) (string, bool) {
		v, ok := it.Attrs[name]
		return v, ok
	}
}

// keepReason is why a group delete leaves a table alone.
type keepReason struct{ why string }

// keep is the verdict.
func keep(why string) keepReason { return keepReason{why: why} }

// folderScopeKeep names every other entity and why a group delete leaves it
// alone. With the two purge lists it is a TOTAL classification, and the
// completeness tests keep it total (BUGS L302 is what an untotal list costs a
// reader). `auth_users` and `session_log` belong to other owners: the purge is
// one batch on routd's tables and reaches no other owner.
var folderScopeKeep = map[string]keepReason{
	// History.
	"audit_log":      keep("the record of the delete itself commits with it"),
	"secret_use_log": keep("history"),
	"session_log":    keep("history"),
	"task_run_logs":  keep("history; a run names a task id the next tenant never mints"),
	"messages":       keep("the transcript of a chat that outlives the folder"),
	// Projections of `messages` with no folder coordinate.
	"message_activity": keep("one partition over every chat; it follows messages"),
	"message_delivery": keep("keyed by delivery status; it follows messages"),

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
	// The purge is one batch on routd's tables and cannot reach onbod's.
	// Closing this needs the delete to reach onbod's /v1, or the generation
	// (specs/6/37 redesign 1).
	"invites":            "onbod owns it, in onbod.db",
	"invite_redemptions": "onbod owns it, in onbod.db",
}

// FolderScopeClassified reports whether table carries a verdict in the
// folder-scope declaration; the completeness tests walk the schema through it.
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

// folderPartitionPurge names the entities a folder OWNS A WHOLE PARTITION of.
// `message_folders` is the ambient window `ObservedSince` reads; leaving it
// would hand the next tenant of a re-used path the last one's observed
// messages. The messages themselves stay: the index is what names the folder.
var folderPartitionPurge = []string{MessageFolders}

// purgeFolder removes folder's subtree from every scope. It returns how many
// `groups` items went and, separately, how many rows of every OTHER entity did:
// an orphan sweep counts only in the second, and counting only the first made
// a sweep indistinguishable from a no-op (BUGS L323). The deletes are chunked
// because a large subtree can exceed MaxBatchActions.
func (s *Store) purgeFolder(ctx context.Context, folder string) (int, int, error) {
	// An empty folder names the subtree of everything.
	if folder == "" {
		return 0, 0, nil
	}
	// Read FIRST, off `groups`, because the loop below deletes those rows.
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
// takes the folder list because enumerating partitions is not an operation the
// interface has, and the rows that name them are gone by the time this runs.
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
// that fails answers none rather than an error.
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
