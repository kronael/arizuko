package store

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func onboardingIndexes(it objstore.Item) []objstore.Key {
	a := it.Attrs
	var keys []objstore.Key
	add := func(kind string, order string, values ...string) {
		keys = append(keys, objstore.DueIndexKey(indexPartition("onboarding/"+kind, values...), order, Onboarding, it.Key))
	}
	if a["_notice_id"] != "" && a["_notice_queued"] == "" && a["_notice_state"] == a[attrOnbStatus] {
		keys = append(keys, objstore.DueIndexKey("onboarding/notices", a["_notice_retry_at"], Onboarding, it.Key))
	}
	switch a[attrOnbStatus] {
	case statusAwaitingMessage:
		if a[attrOnbUserSub] != "" || a[attrOnbSender] == "" {
			break
		}
		add("sender", "", a[attrOnbSender])
		if a[attrOnbPromptedAt] == "" || a["_prompt_id"] != "" && a["_prompt_queued"] == "" {
			due := a["_prompt_retry_at"]
			if due == "" {
				due = core.TS(time.Time{})
			}
			keys = append(keys, objstore.DueIndexKey("onboarding/prompts", due, Onboarding, it.Key))
		}
	case statusQueued:
		add("queue", a[attrOnbQueuedAt], a[attrOnbGate])
		add("queued_user", "", a[attrOnbUserSub])
	case statusApproved:
		add("approved_user", "", a[attrOnbUserSub])
		if at := a[attrOnbAdmittedAt]; at != "" && a[attrOnbGate] != "" {
			add("admitted", at, a[attrOnbGate], strings.SplitN(at, "T", 2)[0])
		}
	}
	return keys
}

func (stateIndexes) Companions(entity string, before, after objstore.Item) []objstore.Write {
	var gates []string
	switch entity {
	case OnboardingGates:
		if before.Attrs[objstore.IndexRevision] != "" && before.Key == after.Key && before.Attrs[attrGateEnabled] == after.Attrs[attrGateEnabled] && before.Attrs[attrGateLimitPerDay] == after.Attrs[attrGateLimitPerDay] {
			return nil
		}
		gates = []string{before.Key.Sort, after.Key.Sort}
	case Onboarding:
		changed := before.Attrs[objstore.IndexRevision] == ""
		for _, field := range []string{attrOnbStatus, attrOnbGate, attrOnbQueuedAt, attrOnbAdmittedAt} {
			changed = changed || before.Attrs[field] != after.Attrs[field]
		}
		if !changed {
			return nil
		}
		for _, it := range []objstore.Item{before, after} {
			if it.Attrs[attrOnbStatus] == statusQueued || it.Attrs[attrOnbStatus] == statusApproved {
				gates = append(gates, it.Attrs[attrOnbGate])
			}
		}
	}
	slices.Sort(gates)
	var writes []objstore.Write
	for _, gate := range slices.Compact(gates) {
		if gate != "" {
			writes = append(writes, objstore.Write{Op: objstore.Merge, Entity: OnboardingWork,
				Key: objstore.Key{Partition: OwnerOnbod, Sort: gate}, Attrs: map[string]string{"generation": rand.Text(), "due": core.TS(time.Time{})}})
		}
	}
	return writes
}

func (s *Store) MigrateOnboardingIndexes(ctx context.Context) error {
	return objstore.Reindex(ctx, s.Obj(), "onboarding-v1", []objstore.Query{{Entity: Onboarding, Partition: OwnerOnbod}, {Entity: OnboardingGates, Partition: OwnerOnbod}, {Entity: OnboardingWork, Partition: OwnerOnbod}})
}

func (s *Store) indexedOnboarding(ctx context.Context, partition string) ([]OnboardingRow, error) {
	items, err := objstore.IndexItems(ctx, s.Obj(), partition, objstore.KeyPredicate{}, 0)
	if err != nil {
		return nil, err
	}
	out := make([]OnboardingRow, 0, len(items))
	for _, it := range items {
		out = append(out, onboardingFrom(it))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JID < out[j].JID })
	return out, nil
}

func (s *Store) AwaitingSender(ctx context.Context, sender string) ([]OnboardingRow, error) {
	return s.indexedOnboarding(ctx, indexPartition("onboarding/sender", sender))
}

func (s *Store) DueAdmissionGates(ctx context.Context, now time.Time) ([]objstore.Item, error) {
	return objstore.IndexItems(ctx, s.Obj(), "onboarding/gates/due", objstore.KeyPredicate{Op: objstore.LtKey, Value: core.TS(now) + "\x7f"}, 0)
}

// AdmitGate reads one gate's quota and commits against the same owner version.
// A refused batch leaves its durable work pending for the next pass.
func (s *Store) AdmitGate(ctx context.Context, work objstore.Item, now time.Time) ([]string, error) {
	var gate OnboardingGate
	var exists bool
	var ids []string
	next := time.Time{}
	version, err := objstore.ReadConsistent(ctx, s.Obj(), OwnerScope(OwnerOnbod), func(context.Context) error {
		var err error
		ids, next = nil, time.Time{}
		gate, exists, err = s.Gate(work.Key.Sort)
		if err != nil || !exists || !gate.Enabled {
			return err
		}
		queued, err := s.Obj().Query(ctx, objstore.Query{Entity: objstore.IndexEntity, Partition: indexPartition("onboarding/queue", gate.Gate), Limit: 1})
		if err != nil || len(queued) == 0 {
			return err
		}
		day := now.UTC().Truncate(24 * time.Hour)
		admitted, err := s.AdmittedInWindow(gate.Gate, onbodTS(day), onbodTS(day.Add(24*time.Hour)))
		if err != nil {
			return err
		}
		if admitted >= gate.LimitPerDay {
			next = day.Add(24 * time.Hour)
			return nil
		}
		ids, err = s.QueuedJIDs(gate.Gate, min(gate.LimitPerDay-admitted, objstore.IndexBatchSize))
		return err
	})
	if err != nil {
		return nil, err
	}
	var writes []objstore.Write
	for _, jid := range ids {
		key, err := onboardingKey(jid)
		if err != nil {
			return nil, err
		}
		writes = append(writes, objstore.Write{Op: objstore.Update, Entity: Onboarding, Key: key,
			Attrs: map[string]string{attrOnbStatus: statusApproved, attrOnbAdmittedAt: onbodTS(now)},
			Cond:  objstore.Cond{Where: []objstore.Cmp{{Attr: attrOnbStatus, Op: objstore.Eq, Value: statusQueued}, {Attr: attrOnbGate, Op: objstore.Eq, Value: gate.Gate}}}})
	}
	if len(writes) == 0 {
		write := objstore.Write{Op: objstore.Delete, Entity: OnboardingWork, Key: work.Key,
			Cond: objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{{Attr: "generation", Op: objstore.Eq, Value: work.Attrs["generation"]}}}}
		if !next.IsZero() {
			write.Op, write.Attrs = objstore.Update, map[string]string{"due": core.TS(next)}
		}
		writes = append(writes, write)
	}
	var ok bool
	if len(ids) == 0 {
		ok, err = s.TryWriteEntities(ctx, nil, writes...)
	} else {
		ok, err = s.tryWriteOwnerEntitiesAt(ctx, OwnerOnbod, version, writes)
	}
	if err != nil || !ok {
		return nil, err
	}
	return ids, nil
}

func (s *Store) PairingWork(ctx context.Context) ([]objstore.Item, error) {
	return objstore.IndexItems(ctx, s.Obj(), "onboarding/pairings", objstore.KeyPredicate{}, 0)
}

func (s *Store) AcknowledgePairing(ctx context.Context, work objstore.Item) error {
	// A refused acknowledgement leaves a replacement edge pending.
	_, err := s.TryWriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: ACLMembership, Key: work.Key,
		Attrs: map[string]string{"_onboarding_ack": flagSet}, Cond: objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{
			{Attr: objstore.IndexRevision, Op: objstore.Eq, Value: work.Attrs[objstore.IndexRevision]},
			{Attr: attrMemAddedBy, Op: objstore.Eq, Value: PairingAddedBy},
		}}})
	return err
}

func PairingWorkIdentity(work objstore.Item) (string, string, error) {
	child, parent, err := membershipEdgeOf(work)
	if err != nil {
		return "", "", fmt.Errorf("pairing work: %w", err)
	}
	return child, parent, nil
}

func (s *Store) ReadyOnboardingPrompts(ctx context.Context, now time.Time) ([]OnboardingRow, error) {
	items, err := objstore.IndexItems(ctx, s.Obj(), "onboarding/prompts", objstore.KeyPredicate{Op: objstore.LtKey, Value: core.TS(now) + "\x7f"}, 0)
	if err != nil {
		return nil, err
	}
	rows := make([]OnboardingRow, 0, len(items))
	for _, it := range items {
		rows = append(rows, onboardingFrom(it))
	}
	return rows, nil
}

func (s *Store) OnboardingPrompt(ctx context.Context, jid string) (id string, at time.Time, err error) {
	key, err := onboardingKey(jid)
	if err != nil {
		return "", at, err
	}
	it, exists, err := s.itemAt(ctx, Onboarding, key)
	if err != nil || !exists {
		return "", at, err
	}
	id = it.Attrs["_prompt_id"]
	if id == "" || it.Attrs["_prompt_queued"] != "" || it.Attrs[attrOnbStatus] != statusAwaitingMessage || it.Attrs[attrOnbUserSub] != "" {
		return "", at, nil
	}
	at, err = time.Parse(time.RFC3339, it.Attrs[attrOnbPromptedAt])
	return id, at, err
}

func (s *Store) CompleteOnboardingPrompt(ctx context.Context, jid, id string, queued bool) error {
	return s.completeOnboardingOutput(ctx, jid, "prompt", id, statusAwaitingMessage, queued)
}

func (s *Store) ReadyOnboardingNotices(ctx context.Context, now time.Time) ([]objstore.Item, error) {
	items, err := objstore.IndexItems(ctx, s.Obj(), "onboarding/notices", objstore.KeyPredicate{Op: objstore.LtKey, Value: core.TS(now) + "\x7f"}, 0)
	return slices.DeleteFunc(items, func(it objstore.Item) bool {
		return it.Attrs["_notice_queued"] != "" || it.Attrs["_notice_id"] == "" || it.Attrs["_notice_state"] != it.Attrs[attrOnbStatus] || it.Attrs["_notice_retry_at"] > core.TS(now)
	}), err
}

func (s *Store) CompleteOnboardingNotice(ctx context.Context, work objstore.Item, queued bool) error {
	return s.completeOnboardingOutput(ctx, work.Key.Sort, "notice", work.Attrs["_notice_id"], work.Attrs["_notice_state"], queued)
}

func (s *Store) completeOnboardingOutput(ctx context.Context, jid, kind, id, status string, queued bool) error {
	key, err := onboardingKey(jid)
	if err != nil {
		return err
	}
	prefix := "_" + kind
	attrs := map[string]string{prefix + "_retry_at": core.TS(time.Now().Add(30 * time.Second))}
	if queued {
		attrs = map[string]string{prefix + "_queued": flagSet}
	}
	// A refused acknowledgement belongs to a superseded generation.
	_, err = s.TryWriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: Onboarding, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Where: []objstore.Cmp{{Attr: prefix + "_id", Op: objstore.Eq, Value: id}, {Attr: attrOnbStatus, Op: objstore.Eq, Value: status}}}})
	return err
}

// QueuePairingGreeting commits the credential hash and its single outbound
// message together. Repeating a generation does not mint another credential.
func (s *Store) QueuePairingGreeting(ctx context.Context, id, jid, sender string, at time.Time, base, greeting string) error {
	if _, exists, err := s.messageAt(ctx, jid, id); err != nil || exists {
		return err
	}
	token, credential, err := pairingMint(sender, "")
	if err != nil {
		return err
	}
	minted, err := time.Parse(time.RFC3339Nano, credential.Attrs[attrTokCreatedAt])
	if err != nil {
		return err
	}
	content := "Set up your account: " + strings.TrimRight(base, "/") + "/pair/" + token
	if greeting != "" {
		content = greeting + "\n" + content
	}
	m := core.Message{ID: id, ChatJID: jid, Timestamp: at, Sender: SystemSender, BotMsg: true, Content: content}
	_, err = s.putMessage(ctx, m, core.MessageStatusPending, messageOptions{exact: true,
		delivery: map[string]string{"next_attempt_at": core.NowTS(), "expires_at": core.TS(minted.Add(PairingTTL))}, also: []objstore.Write{credential}})
	if err != nil {
		return err
	}
	_, exists, err := s.messageAt(ctx, jid, id)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("pairing greeting %s was not queued", id)
	}
	return nil
}
