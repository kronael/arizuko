package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

const aclWildcardMigration = "acl-wildcard-v1"

type stateIndexes struct{}

func (stateIndexes) Indexed(entity string) bool {
	switch entity {
	case ScheduledTasks, TurnContexts, TurnArchive, MessageDelivery, Messages, Onboarding, OnboardingGates, OnboardingWork, ACLMembership, RouteTokens, PendingActions, RoutdWork, Chats, TurnControl, Routes, Groups, ACL:
		return true
	}
	return false
}

func (stateIndexes) Keys(entity string, it objstore.Item) []objstore.Key {
	switch entity {
	case ACL:
		principal, _, _ := strings.Cut(it.Key.Sort, KeySep)
		if strings.Contains(principal, "*") {
			return []objstore.Key{{Partition: "acl/wildcard", Sort: it.Key.Sort}}
		}
		return nil
	case Chats:
		cursor := it.Attrs[AttrChatAgentCursor]
		if it.Attrs[attrChatInputLast] <= cursor {
			return nil
		}
		at, _, ok := MsgCoord(max(it.Attrs[attrChatInputFirst], cursor))
		if !ok {
			return nil
		}
		return []objstore.Key{objstore.DueIndexKey("chats/input", at, entity, it.Key)}
	case PendingActions, TurnControl, Routes, Groups:
		return nil
	case RoutdWork:
		return []objstore.Key{objstore.DueIndexKey("routd_work/"+it.Key.Partition, it.Attrs["due"], entity, it.Key)}
	case RouteTokens:
		if it.Attrs[attrTokKind] == RouteTokenKindRoute && it.Attrs[attrTokTargetFolder] != "" {
			return []objstore.Key{{Partition: indexPartition("route_tokens/jid", it.Attrs[attrTokJID]), Sort: it.Key.Sort}}
		}
		return nil
	case Onboarding:
		return onboardingIndexes(it)
	case OnboardingGates:
		return nil
	case OnboardingWork:
		return []objstore.Key{objstore.DueIndexKey("onboarding/gates/due", it.Attrs["due"], entity, it.Key)}
	case ACLMembership:
		if it.Attrs[attrMemAddedBy] == PairingAddedBy && it.Attrs["_onboarding_ack"] == "" {
			return []objstore.Key{{Partition: "onboarding/pairings", Sort: it.Key.Sort}}
		}
		return nil
	}
	if entity == Messages {
		return messageIndexes(it)
	}
	if entity == TurnContexts || entity == TurnArchive {
		return turnIndexes(entity, it)
	}
	if entity == MessageDelivery {
		if it.Key.Partition != core.MessageStatusPending {
			return nil
		}
		due, _ := deliveryDueAt(it)
		if expires, err := time.Parse(time.RFC3339Nano, it.Attrs["expires_at"]); err == nil && expires.Before(due) {
			due = expires
		}
		return []objstore.Key{objstore.DueIndexKey("outbound/due", core.TS(due), entity, it.Key)}
	}
	switch it.Attrs[attrTaskStatus] {
	case core.TaskActive:
		due := time.Time{}
		if at, ok := taskNextRun(it); ok {
			due = at
		}
		return []objstore.Key{objstore.DueIndexKey("tasks/due", core.TS(due), entity, it.Key)}
	case TaskStatusFiring:
		return []objstore.Key{{Partition: "tasks/firing", Sort: it.Key.Sort}}
	}
	return nil
}

func (s *Store) MigrateStateIndexes(ctx context.Context) error {
	if err := objstore.Reindex(ctx, s.Obj(), "tasks-v1", []objstore.Query{
		{Entity: ScheduledTasks, Partition: OwnerRoutd},
	}); err != nil {
		return err
	}
	if err := objstore.Reindex(ctx, s.Obj(), "turns-v1", []objstore.Query{
		{Entity: TurnContexts, Partition: OwnerRoutd}, {Entity: TurnArchive, Partition: OwnerRoutd},
	}); err != nil {
		return err
	}
	if err := objstore.Reindex(ctx, s.Obj(), "outbound-v1", []objstore.Query{{Entity: MessageDelivery, Partition: core.MessageStatusPending}}); err != nil {
		return err
	}
	if err := objstore.Reindex(ctx, s.Obj(), "pairings-v1", []objstore.Query{{Entity: ACLMembership, Partition: OwnerRoutd}}); err != nil {
		return err
	}
	if err := objstore.Reindex(ctx, s.Obj(), "route-tokens-v1", []objstore.Query{{Entity: RouteTokens, Partition: OwnerRoutd}}); err != nil {
		return err
	}
	if err := objstore.Reindex(ctx, s.Obj(), "approvals-v1", []objstore.Query{{Entity: PendingActions, Partition: OwnerRoutd}}); err != nil {
		return err
	}
	if err := objstore.Reindex(ctx, s.Obj(), "chat-input-v1", []objstore.Query{{Entity: Chats, Partition: OwnerRoutd}}); err != nil {
		return err
	}
	if err := objstore.Reindex(ctx, s.Obj(), "turn-access-v2", []objstore.Query{{Entity: TurnContexts, Partition: OwnerRoutd}}); err != nil {
		return err
	}
	if err := objstore.Reindex(ctx, s.Obj(), "control-work-v1", []objstore.Query{{Entity: TurnControl, Partition: OwnerRoutd}}); err != nil {
		return err
	}
	if err := s.migrateACLWildcard(ctx); err != nil {
		return err
	}
	marker := objstore.Key{Partition: "migrations", Sort: "messages-v1"}
	if _, done, err := s.itemAt(ctx, objstore.IndexEntity, marker); err != nil || done {
		return err
	}
	jids, err := s.ChatJIDs(ctx)
	if err != nil {
		return err
	}
	partitions := make([]objstore.Query, 0, len(jids))
	for _, jid := range jids {
		partitions = append(partitions, objstore.Query{Entity: Messages, Partition: jid})
	}
	return objstore.Reindex(ctx, s.Obj(), marker.Sort, partitions)
}

func turnIndexes(entity string, it objstore.Item) []objstore.Key {
	a := it.Attrs
	var keys []objstore.Key
	add := func(partition, sort string) { keys = append(keys, objstore.Key{Partition: partition, Sort: sort}) }
	if at, err := time.Parse(time.RFC3339Nano, a[AttrTurnStartedAt]); err == nil {
		keys = append(keys, objstore.DueIndexKey(entity+"/started", core.TS(at), entity, it.Key))
		if entity == TurnContexts && a[attrTurnState] == TurnRunning &&
			(a["provider_failure_attempt"] == "" || a["provider_failure_attempt"] != a["attempt_id"]) {
			keys = append(keys, objstore.DueIndexKey("turns/expiry", core.TS(at), entity, it.Key))
		}
	}
	if entity == TurnArchive {
		return keys
	}
	switch a[attrTurnState] {
	case TurnRunning, TurnPendingRetry:
		add("turns/live", it.Key.Sort)
		add(indexPartition("turns/folder", a[attrTurnFolder]), it.Key.Sort)
		add(indexPartition("turns/chat", a[attrTurnChatJID]), it.Key.Sort)
	}
	if a[attrTurnState] == TurnRunning && a[attrTurnRunReturned] == flagSet {
		add("turns/recovery", it.Key.Sort)
	}
	if a[attrTurnState] == TurnPendingRetry && a["next_attempt_at"] != "" {
		keys = append(keys, objstore.DueIndexKey("turns/retry", a["next_attempt_at"], entity, it.Key))
	}
	if _, owed := a["status_owed"]; owed {
		add("turns/status", it.Key.Sort)
	}
	return keys
}

func (s *Store) indexedTurns(ctx context.Context, partition string, key objstore.KeyPredicate) ([]TurnContext, error) {
	items, err := objstore.IndexItems(ctx, s.Obj(), partition, key, 0)
	if err != nil {
		return nil, err
	}
	turns := make([]TurnContext, 0, len(items))
	for _, it := range items {
		turns = append(turns, turnFrom(it))
	}
	return turns, nil
}

func (s *Store) RecoverableTurns(ctx context.Context) ([]TurnContext, error) {
	return s.indexedTurns(ctx, "turns/recovery", objstore.KeyPredicate{})
}

func indexPartition(kind string, values ...string) string {
	data, _ := json.Marshal(values)
	return fmt.Sprintf("%s/%x", kind, sha256.Sum256(data))
}

func messageIndexes(it objstore.Item) []objstore.Key {
	_, id, ok := MsgCoord(it.Key.Sort)
	if !ok {
		return nil
	}
	jid, a := it.Key.Partition, it.Attrs
	keys := []objstore.Key{{Partition: indexPartition("message/id", jid, id), Sort: it.Key.Sort}}
	add := func(kind string, values ...string) {
		keys = append(keys, objstore.Key{Partition: indexPartition("message/"+kind, append([]string{jid}, values...)...), Sort: it.Key.Sort})
	}
	if v := a[AttrMsgPlatformID]; v != "" {
		add("platform", v)
	}
	if v := a["status_send_key"]; v != "" {
		add("send", v)
	}
	if a[attrMsgVerb] == verbStatus {
		add("status", a[attrMsgTurnID])
	}
	if _, bot := a[attrMsgBot]; bot {
		add("bot")
		if a[attrMsgVerb] != verbStatus {
			add("reply", a[attrMsgTurnID])
		}
	} else if _, source := a[attrMsgSource]; source {
		add("source")
	}
	return keys
}

func (s *Store) LiveTurnsInFolder(ctx context.Context, folder string) ([]TurnContext, error) {
	return s.indexedTurns(ctx, indexPartition("turns/folder", folder), objstore.KeyPredicate{})
}

func (s *Store) LiveTurnsInChat(ctx context.Context, jid string) ([]TurnContext, error) {
	return s.indexedTurns(ctx, indexPartition("turns/chat", jid), objstore.KeyPredicate{})
}

func (s *Store) migrateACLWildcard(ctx context.Context) error {
	marker := objstore.Key{Partition: "migrations", Sort: aclWildcardMigration}
	if _, done, err := s.itemAt(ctx, objstore.IndexEntity, marker); err != nil || done {
		return err
	}
	q := objstore.Query{Entity: ACL, Partition: OwnerRoutd}
	items, err := s.Obj().Query(ctx, q)
	if err != nil {
		return err
	}
	if _, err := aclRows("validate wildcard migration", items); err != nil {
		return err
	}
	return objstore.Reindex(ctx, s.Obj(), marker.Sort, []objstore.Query{q})
}
