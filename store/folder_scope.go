package store

import (
	"context"
	"strings"

	"github.com/kronael/arizuko/core"

	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
)

// entityScope names ONE entity that carries a folder coordinate, and how to
// read the folder out of one of its items. folderOf is a function because the
// folder sits somewhere different in each entity.
type entityScope struct {
	entity   string
	folderOf func(objstore.Item) (string, bool)
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
	{entity: Groups,
		folderOf: func(it objstore.Item) (string, bool) { return it.Key.Sort, true }},
	{entity: ModelPauses,
		folderOf: func(it objstore.Item) (string, bool) { return it.Key.Sort, true }},

	// Confers.
	{entity: ACL, folderOf: func(it objstore.Item) (string, bool) {
		parts := strings.Split(it.Key.Sort, KeySep)
		if len(parts) != 6 {
			return "", false
		}
		return parts[2], true // scope
	}},
	{entity: ACLMembership, folderOf: func(it objstore.Item) (string, bool) {
		child, _, ok := strings.Cut(it.Key.Sort, KeySep)
		if !ok {
			return "", false
		}
		return strings.CutPrefix(child, "folder:")
	}},
	{entity: Secrets, folderOf: func(it objstore.Item) (string, bool) {
		parts := strings.Split(it.Key.Sort, KeySep)
		if len(parts) != 3 || parts[0] != SecretScopeFolder {
			return "", false
		}
		return parts[1], true // scope_id
	}},
	{entity: NetworkRules, folderOf: func(it objstore.Item) (string, bool) {
		folder, _, ok := strings.Cut(it.Key.Sort, KeySep)
		return folder, ok
	}},

	// Routes in: the folder is the target URI's path.
	{entity: Routes, folderOf: func(it objstore.Item) (string, bool) {
		parts := strings.SplitN(it.Key.Sort, KeySep, 3)
		if len(parts) != 3 {
			return "", false
		}
		return core.ParseRouteTarget(parts[1]).Folder, true
	}},
	{entity: ScheduledTasks, folderOf: attrScope(attrTaskOwner)},
	{entity: PendingActions, folderOf: attrScope(AttrPendGroupFolder)},

	// Reaches the next tenant.
	{entity: WebRoutes, folderOf: attrScope(AttrWebFolder)},
	{entity: RouteTokens, folderOf: attrScope(attrTokOwnerFolder)},
	{entity: GitHubSources, folderOf: attrScope(attrGHOwnerFolder)},
	{entity: GDriveFiles, folderOf: attrScope(attrGDOwnerFolder)},
	{entity: GDriveFiles, folderOf: attrScope(attrGDTargetFolder)},

	// Owned assets.
	{entity: InstalledPackages, folderOf: func(it objstore.Item) (string, bool) {
		folder, _, ok := strings.Cut(it.Key.Sort, KeySep)
		return folder, ok
	}},

	// The hot tier: each would be inherited by the next tenant of the path.
	{entity: Sessions, folderOf: func(it objstore.Item) (string, bool) {
		folder, _, err := sessionCoord(it.Key.Sort)
		return folder, err == nil
	}},
	{entity: ChatReplyState, folderOf: attrScope(AttrReplyEngagedFolder)},
	// The folder's message count. The FEED it counts is partitioned by the
	// folder and goes through folderPartitionPurge.
	{entity: MessageFolderCounts,
		folderOf: func(it objstore.Item) (string, bool) { return it.Key.Sort, true }},
	{entity: SystemMessages, folderOf: func(it objstore.Item) (string, bool) {
		return SysMsgFolder(it.Key.Sort)
	}},

	// Watches: whichever end the deleted folder is.
	{entity: GroupWatchers, folderOf: func(it objstore.Item) (string, bool) {
		observer, _, err := watcherCoord(it.Key.Sort)
		return observer, err == nil
	}},
	{entity: GroupWatchers, folderOf: func(it objstore.Item) (string, bool) {
		_, source, err := watcherCoord(it.Key.Sort)
		return source, err == nil
	}},

	// The chat outlives the folder (it carries its own dispatch cursor), so the
	// binding goes and not the item.
	{entity: Chats, folderOf: attrScope(AttrChatStickyGroup),
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
	objstore.IndexEntity: keep("shared derived keys follow primary writes; includes migration markers"),
	RoutdWork:            keep("instance control and approval notifications survive folder retirement"),
	OnboardingWork:       keep("instance admission gates; no folder coordinate"),
	// History.
	"audit_log":      keep("the record of the delete itself commits with it"),
	"secret_use_log": keep("history"),
	"session_log":    keep("history"),
	"task_run_logs":  keep("history; a run names a task id the next tenant never mints"),
	"messages":       keep("the transcript of a chat that outlives the folder"),
	// Projections of `messages` with no folder coordinate.
	"message_activity": keep("one partition over every chat; it follows messages"),
	"message_delivery": keep("keyed by delivery status; it follows messages"),

	// Turn history.
	"turn_context": keep("live turns retire before the subtree is purged"),
	"turn_archive": keep("history, keyed by turn id"),
	"run_turns":    keep("keyed by run id"),
	"turn_results": keep("keyed by turn_id; a re-created folder mints new ones"),
	"turn_control": keep("instance-wide execution state"),

	// No folder coordinate at all.
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
	// Closing this needs the delete to reach onbod's /v1.
	"invites":            "onbod owns it, in onbod.db",
	"invite_redemptions": "onbod owns it, in onbod.db",
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
// because a large subtree can exceed MaxBatchActions. It also returns the
// folders it owned, read before the purge, so a caller can act on each one.
func (s *Store) purgeFolder(ctx context.Context, folder string) ([]string, int, int, error) {
	// An empty folder names the subtree of everything.
	if folder == "" {
		return nil, 0, 0, nil
	}
	// Read FIRST, off `groups`, because the loop below deletes those rows.
	children, err := s.subtreeFolders(ctx, folder)
	if err != nil {
		return nil, 0, 0, err
	}
	owned := append([]string{folder}, children...)
	removed, swept := 0, 0
	for _, e := range entityScopePurge {
		items, err := s.Obj().Query(ctx, objstore.Query{Entity: e.entity, Partition: OwnerRoutd})
		if err != nil {
			return nil, 0, 0, err
		}
		var writes []objstore.Write
		for _, it := range items {
			v, ok := e.folderOf(it)
			if !ok {
				continue
			}
			if !groupfolder.Contains(folder, v) {
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
		if _, err := s.applyInChunks(ctx, "purge "+e.entity+" of "+folder, writes); err != nil {
			return nil, 0, 0, err
		}
		if e.entity == Groups {
			retired, err := s.retireFolderTurns(ctx, folder)
			if err != nil {
				return nil, 0, 0, err
			}
			swept += retired
		}
	}
	if err := s.purgeFolderPartitions(ctx, owned); err != nil {
		return nil, 0, 0, err
	}
	return owned, removed, swept, nil
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
			if _, err := s.applyInChunks(ctx, "purge "+entity+" of "+owned, writes); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) subtreeFolders(ctx context.Context, folder string) ([]string, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Groups, Partition: OwnerRoutd, Project: []string{},
	})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, it := range items {
		if it.Key.Sort != folder && groupfolder.ContainsSep(folder, it.Key.Sort, "/") {
			out = append(out, it.Key.Sort)
		}
	}
	return out, nil
}
