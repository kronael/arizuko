package store

// coordinate.go holds every entity's coordinate builder in one place, and the
// one writer that uses them raw.
//
// SeedItem is FOR TESTS. Every production write goes through the entity's own
// method, which stamps its own instant, so a turn already two hours old or a
// task run recorded yesterday is a state no live writer can reach. A test that
// needs one builds it here. The coordinate still travels through the entity's
// own key builder, so a seeded item lands exactly where a live write would put
// it; only the attributes are unchecked.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// SeedItem writes one item: the coordinate rendered from key, the attributes
// verbatim.
func (s *Store) SeedItem(ctx context.Context, entity string, key []string, attrs map[string]string) error {
	k, err := entityKey(entity, key)
	if err != nil {
		return err
	}
	return s.WriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Put, Entity: entity, Key: k, Attrs: attrs,
	})
}

// keyFields names each entity's coordinate, in the order its key builder joins
// the fields. `system_messages` and `task_run_logs` name one field more than
// their coordinate has: the row number that keeps two rows written in one
// nanosecond in the order they had. A test pins every entry to a builder, and
// pins the arity.
var keyFields = map[string][]string{
	Groups:            {"folder"},
	ACL:               {"principal", "action", "scope", "params", "predicate", "effect"},
	ACLMembership:     {"child", "parent"},
	Routes:            {"seq", "target", "match"},
	RouteTokens:       {"token_hash"},
	ScheduledTasks:    {"id"},
	Secrets:           {"scope_kind", "scope_id", "key"},
	PendingActions:    {"id"},
	NetworkRules:      {"folder", "target"},
	ProxydRoutes:      {"path"},
	WebRoutes:         {"path_prefix"},
	InstalledPackages: {"folder", "name"},
	GitHubSources:     {"source"},
	GDriveFiles:       {"file_id"},
	Chats:             {"jid"},
	Sessions:          {"group_folder", "topic"},
	ChatReplyState:    {"jid", "topic"},
	ChatProactive:     {"jid"},
	SystemMessages:    {"folder", "created", "id"},
	UserProfiles:      {"sub"},
	TurnResults:       {"folder", "turn_id"},
	GroupWatchers:     {"observer", "source"},
	TaskRunLogs:       {"run_at", "id"},
	TurnContexts:      {"turn_id"},
	// The position is two columns: the instant and the id that keeps two
	// messages of one instant apart (245 pairs share one on krons).
	Messages: {"chat_jid", "timestamp", "id"},
}

// entityKey renders a coordinate through the SAME builder the entity's own
// writer calls.
func entityKey(entity string, key []string) (objstore.Key, error) {
	want := keyFields[entity]
	if len(want) == 0 {
		return objstore.Key{}, fmt.Errorf("%s declares no coordinate", entity)
	}
	if len(key) != len(want) {
		return objstore.Key{}, fmt.Errorf("%s row carries %d coordinate fields and its "+
			"coordinate (%s) has %d", entity, len(key), strings.Join(want, ","), len(want))
	}
	switch entity {
	case Groups:
		return groupKey(key[0])
	case ACL:
		return aclKey(core.ACLRow{Principal: key[0], Action: key[1], Scope: key[2],
			Params: key[3], Predicate: key[4], Effect: key[5]})
	case ACLMembership:
		return membershipKey(key[0], key[1])
	case Routes:
		seq, err := strconv.Atoi(key[0])
		if err != nil {
			return objstore.Key{}, fmt.Errorf("route seq %q is not a number: %w", key[0], err)
		}
		return routeKey(core.Route{Seq: seq, Target: key[1], Match: key[2]})
	case RouteTokens:
		// The digest arrives already hex-encoded: the column is a BLOB and the
		// coordinate is text.
		if key[0] == "" {
			return objstore.Key{}, fmt.Errorf("route token row carries no token_hash, " +
				"which is its coordinate")
		}
		return objstore.Key{Partition: OwnerRoutd, Sort: key[0]}, nil
	case ScheduledTasks:
		return taskKey(key[0])
	case Secrets:
		return secretKey(SecretScope(key[0]), key[1], key[2])
	case PendingActions:
		return PendingActionKey(key[0])
	case NetworkRules:
		return networkRuleKey(key[0], key[1])
	case ProxydRoutes:
		return proxydRouteKey(key[0])
	case WebRoutes:
		return WebRouteKey(key[0])
	case InstalledPackages:
		return InstalledPackageKey(key[0], key[1])
	case GitHubSources:
		return githubSourceKey(key[0])
	case GDriveFiles:
		return gdriveFileKey(key[0])
	case Chats:
		return chatKey(key[0])
	case Sessions:
		return sessionKey(key[0], key[1])
	case ChatReplyState:
		return replyStateKey(key[0], key[1])
	case ChatProactive:
		return proactiveKey(key[0])
	case UserProfiles:
		return profileKey(key[0])
	case TurnResults:
		return turnResultKey(key[0], key[1])
	case GroupWatchers:
		return watcherKey(key[0], key[1])
	case TaskRunLogs:
		return runLogKeyAt(key[0], key[1])
	case TurnContexts:
		return turnKey(key[0])
	case Messages:
		t, err := ReadMessageInstant(key[1])
		if err != nil {
			// A value no layout reads is one a person put there by hand, and
			// the id is what finds it again among twenty thousand.
			return objstore.Key{}, fmt.Errorf("message %q in chat %q: %w", key[2], key[0], err)
		}
		return MessageKey(key[0], core.TS(t), key[2])
	case SystemMessages:
		at, err := normaliseInstant(entity, "created", key[1])
		if err != nil {
			return objstore.Key{}, err
		}
		id, err := strconv.ParseInt(key[2], 10, 64)
		if err != nil || id < 0 {
			return objstore.Key{}, fmt.Errorf("system message id %q is not a row number, "+
				"and it is what keeps two events queued at one instant apart", key[2])
		}
		return sysMsgKey(key[0], SysStamp(at, fmt.Sprintf("%08x", id)))
	}
	return objstore.Key{}, fmt.Errorf("%s has no coordinate builder", entity)
}

// runLogKeyAt builds a run's position out of the instant it ran and the row
// number that breaks a tie: `run_at` is second-precision on every live
// instance.
func runLogKeyAt(runAt, id string) (objstore.Key, error) {
	at, err := normaliseInstant(TaskRunLogs, "run_at", runAt)
	if err != nil {
		return objstore.Key{}, err
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n < 0 {
		return objstore.Key{}, fmt.Errorf("task run log id %q is not a row number, and it is "+
			"what keeps two runs recorded in one second apart", id)
	}
	return runLogKey(RunStamp(at, fmt.Sprintf("%08x", n)))
}
