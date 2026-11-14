package store

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

type onboardingNoticeRace struct {
	objstore.Store
	approve func() error
}

func (r *onboardingNoticeRace) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == Onboarding && q.Key.Op == objstore.ExactKey && r.approve != nil {
		approve := r.approve
		r.approve = nil
		if err := approve(); err != nil {
			return nil, err
		}
	}
	return r.Store.Query(ctx, q)
}

func TestOnboardingNoticeSkipsConcurrentApproval(t *testing.T) {
	s := openMem(t)
	queueAdmission(t, s, "chat", "*")
	r := NewOver(&onboardingNoticeRace{Store: s.obj, approve: func() error { return s.ApproveOnboarding("chat") }})
	if work, err := r.ReadyOnboardingNotices(t.Context(), time.Now().Add(time.Second)); err != nil || len(work) != 0 {
		t.Fatalf("stale notice: %v %v", work, err)
	}
}

func TestOnboardingArchiveKeepsWork(t *testing.T) {
	s, dst := openMem(t), openMem(t)
	queueAdmission(t, s, "notice", "*")
	if _, err := s.InsertOnboarding("prompt", "sender"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimOnboardingPrompt("prompt"); err != nil {
		t.Fatal(err)
	}
	id, _, err := s.OnboardingPrompt(t.Context(), "prompt")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ExportOnboarding(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dst.ImportOnboarding(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	if got, _, err := dst.OnboardingPrompt(t.Context(), "prompt"); err != nil || got != id {
		t.Fatalf("greeting lost: %q %v", got, err)
	}
	if notices, err := dst.ReadyOnboardingNotices(t.Context(), time.Now().Add(time.Second)); err != nil || len(notices) != 1 {
		t.Fatalf("notice lost: %v %v", notices, err)
	}
}

func TestOnboardingArchiveKeepsCredentialExpiry(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			s, dst := openMem(t), openMem(t)
			now := time.Now()
			if err := s.QueuePairingGreeting(t.Context(), "pair", "chat", "sender", now, "https://example.com", ""); err != nil {
				t.Fatal(err)
			}
			work, err := s.PendingOutbound(t.Context(), now.Add(time.Second), 1)
			if err != nil || len(work) != 1 {
				t.Fatalf("pending: %v %v", work, err)
			}
			if failed {
				if err := s.DeadLetter(t.Context(), work[0].Message, 3); err != nil {
					t.Fatal(err)
				}
			}
			var archive bytes.Buffer
			if _, err := s.ExportMessagesJSONL(t.Context(), &archive); err != nil {
				t.Fatal(err)
			}
			if _, _, bad, err := dst.ImportMessagesJSONL(t.Context(), &archive, 1); err != nil || len(bad) != 0 {
				t.Fatalf("restore: %v %v", bad, err)
			}
			var restored []PendingMessage
			if failed {
				restored, err = dst.DeadLetters(t.Context(), "", 10)
			} else {
				restored, err = dst.PendingOutbound(t.Context(), now.Add(time.Second), 10)
			}
			if err != nil || len(restored) != 1 || !restored[0].ExpiresAt.Equal(work[0].ExpiresAt) {
				t.Fatalf("expiry lost: %v %v", restored, err)
			}
		})
	}
}

func TestOnboardingDeliveryKeepsConcurrentMetadata(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprint(replay), func(t *testing.T) {
			s := openMem(t)
			if err := s.QueuePairingGreeting(t.Context(), "pair", "chat", "sender", time.Now(), "https://example.com", ""); err != nil {
				t.Fatal(err)
			}
			m, ok := s.MessageByID("chat", "pair")
			if !ok {
				t.Fatal("message missing")
			}
			status := core.MessageStatusPending
			if replay {
				if err := s.DeadLetter(t.Context(), m, 1); err != nil {
					t.Fatal(err)
				}
				status = core.MessageStatusFailed
			}
			key, err := s.deliveryMessageKey(t.Context(), m)
			if err != nil {
				t.Fatal(err)
			}
			source := deliveryKey(status, key)
			expiry := core.TS(time.Now().Add(-time.Second))
			r := NewOver(&onboardingRace{Store: s.obj, fire: func(ctx context.Context) error {
				return s.WriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: MessageDelivery, Key: source, Attrs: map[string]string{"expires_at": expiry}})
			}})
			if replay {
				if ok, err := r.ReplayDeadLetter(t.Context(), m); err != nil || ok {
					t.Fatalf("stale replay: %v %v", ok, err)
				}
			} else if err := r.DeadLetter(t.Context(), m, 1); err != nil {
				t.Fatal(err)
			}
			after, exists, err := s.itemAt(t.Context(), MessageDelivery, source)
			if err != nil || !exists || after.Attrs["expires_at"] != expiry {
				t.Fatalf("new metadata lost: %v %v %v", after, exists, err)
			}
		})
	}
}

func TestOnboardingReadsOnlyDueWork(t *testing.T) {
	s := openMem(t)
	now := time.Now()
	var rows []ArchiveOnboardingRow
	for i := range 40 {
		rows = append(rows, ArchiveOnboardingRow{JID: fmt.Sprint(i), Sender: fmt.Sprint(i), Status: statusAwaitingMessage, Created: onbodTS(now), PromptedAt: onbodTS(now)})
	}
	if _, err := s.ImportOnboarding(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertOnboarding("ready", "sender"); err != nil {
		t.Fatal(err)
	}
	c := &stateReadCounter{Store: s.obj}
	reader := NewOver(c)
	ready, err := reader.ReadyOnboardingPrompts(t.Context(), now.Add(time.Second))
	if err != nil || len(ready) != 1 || ready[0].JID != "ready" || c.rows != 2 {
		t.Fatalf("ready=%v err=%v rows=%d", ready, err, c.rows)
	}
	if _, err := s.ClaimOnboardingPrompt("ready"); err != nil {
		t.Fatal(err)
	}
	id, _, err := s.OnboardingPrompt(t.Context(), "ready")
	if err != nil || id == "" {
		t.Fatalf("generation: %q %v", id, err)
	}
	if err := s.CompleteOnboardingPrompt(t.Context(), "ready", id, true); err != nil {
		t.Fatal(err)
	}
	c.rows = 0
	if ready, err := reader.ReadyOnboardingPrompts(t.Context(), now.Add(time.Hour)); err != nil || len(ready) != 0 || c.rows != 0 {
		t.Fatalf("acked prompts: %v %v rows=%d", ready, err, c.rows)
	}
	c.rows = 0
	awaiting, err := reader.AwaitingSender(t.Context(), "sender")
	if err != nil || len(awaiting) != 1 || c.rows != 2 {
		t.Fatalf("sender: %v %v rows=%d", awaiting, err, c.rows)
	}
}

func TestOnboardingGreetingGenerationAndAtomicQueue(t *testing.T) {
	s := openMem(t)
	if _, err := s.InsertOnboarding("chat", "sender"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimOnboardingPrompt("chat"); err != nil {
		t.Fatal(err)
	}
	id, at, err := s.OnboardingPrompt(t.Context(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.QueuePairingGreeting(t.Context(), id, "chat", "sender", at, "https://example.com", "Hello"); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := s.MessagesSince("chat", time.Time{})
	if err != nil || len(messages) != 1 {
		t.Fatalf("greetings: %v %v", messages, err)
	}
	token := strings.Split(messages[0].Content, "/pair/")[1]
	if sender, err := s.PeekPairing(token); err != nil || sender != "sender" {
		t.Fatalf("credential: %s %v", sender, err)
	}
	tokens, err := s.Obj().Query(t.Context(), objstore.Query{Entity: RouteTokens, Partition: OwnerRoutd})
	if err != nil || len(tokens) != 1 {
		t.Fatalf("credentials: %d %v", len(tokens), err)
	}
	pending, ok, err := s.PendingDelivery(t.Context(), messages[0])
	if err != nil || !ok || pending.ExpiresAt.IsZero() {
		t.Fatalf("pending greeting: %v %v %v", pending, ok, err)
	}
	if err := s.RepromptOnboarding("chat"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimOnboardingPrompt("chat"); err != nil {
		t.Fatal(err)
	}
	next, _, err := s.OnboardingPrompt(t.Context(), "chat")
	if err != nil || next == id {
		t.Fatalf("next generation: %s %v", next, err)
	}
	if err := s.CompleteOnboardingPrompt(t.Context(), "chat", id, true); err != nil {
		t.Fatal(err)
	}
	ready, err := s.ReadyOnboardingPrompts(t.Context(), time.Now().Add(time.Second))
	if err != nil || len(ready) != 1 {
		t.Fatalf("old ack consumed new greeting: %v %v", ready, err)
	}
}

func queueAdmission(t *testing.T, s *Store, jid, gate string) {
	t.Helper()
	if _, err := s.InsertOnboarding(jid, jid); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueOnboarding(jid, "user:"+jid, gate); err != nil {
		t.Fatal(err)
	}
}
func oneGateWork(t *testing.T, s *Store, now time.Time) objstore.Item {
	t.Helper()
	work, err := s.DueAdmissionGates(t.Context(), now)
	if err != nil || len(work) != 1 {
		t.Fatalf("gate work: %v %v", work, err)
	}
	return work[0]
}
func runGate(t *testing.T, s *Store, now time.Time, want string) {
	t.Helper()
	ids, err := s.AdmitGate(t.Context(), oneGateWork(t, s, now), now)
	if err != nil || strings.Join(ids, ",") != want {
		t.Fatalf("admitted %v, want %q: %v", ids, want, err)
	}
}

func TestOnboardingGateTransitionsAndQuotaKeys(t *testing.T) {
	s := openMem(t)
	now := time.Now().UTC()
	if err := s.PutGate("*", 1); err != nil {
		t.Fatal(err)
	}
	var old []ArchiveOnboardingRow
	for i := range 40 {
		old = append(old, ArchiveOnboardingRow{JID: fmt.Sprint(i), Sender: fmt.Sprint(i), Status: statusApproved, Gate: "*", Created: onbodTS(now.Add(-48 * time.Hour)), AdmittedAt: onbodTS(now.Add(-48 * time.Hour))})
	}
	if _, err := s.ImportOnboarding(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	c := &stateReadCounter{Store: s.obj}
	r := NewOver(c)
	day := now.Truncate(24 * time.Hour)
	if n, err := r.AdmittedInWindow("*", onbodTS(day), onbodTS(day.Add(24*time.Hour))); err != nil || n != 0 || c.rows != 0 {
		t.Fatalf("quota read historical rows: %d %v rows=%d", n, err, c.rows)
	}
	queueAdmission(t, s, "first", "*")
	queueAdmission(t, s, "second", "*")
	runGate(t, s, now, "first")
	runGate(t, s, now, "")
	if due, err := s.DueAdmissionGates(t.Context(), now); err != nil || len(due) != 0 {
		t.Fatalf("quota not deferred: %v %v", due, err)
	}
	if err := s.DenyOnboarding("first"); err != nil {
		t.Fatal(err)
	}
	runGate(t, s, now, "second")
	queueAdmission(t, s, "third", "*")
	runGate(t, s, now, "")
	if err := s.PutGate("*", 2); err != nil {
		t.Fatal(err)
	}
	runGate(t, s, now, "third")
	queueAdmission(t, s, "fourth", "*")
	runGate(t, s, now, "")
	runGate(t, s, day.Add(24*time.Hour), "fourth")
}

type onboardingRace struct {
	objstore.Store
	fire func(context.Context) error
}

func (r *onboardingRace) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	if r.fire != nil {
		fire := r.fire
		r.fire = nil
		if err := fire(ctx); err != nil {
			return false, err
		}
	}
	return r.Store.GuardedBatch(ctx, checks, writes)
}

func TestOnboardingQuotaKeepsConcurrentApproval(t *testing.T) {
	s := openMem(t)
	if err := s.PutGate("*", 1); err != nil {
		t.Fatal(err)
	}
	queueAdmission(t, s, "candidate", "*")
	queueAdmission(t, s, "other", "*")
	work := oneGateWork(t, s, time.Now())
	raced := NewOver(&onboardingRace{Store: s.obj, fire: func(context.Context) error { return s.ApproveOnboarding("other") }})
	ids, err := raced.AdmitGate(t.Context(), work, time.Now())
	if err != nil || len(ids) != 0 || admissionItem(t, s, "candidate").Attrs[attrOnbStatus] != statusQueued {
		t.Fatalf("quota race admitted %v: %v", ids, err)
	}
}

func TestPairingAcknowledgementKeepsNewEdge(t *testing.T) {
	s := openMem(t)
	if err := s.AddMembership("sender", "user", PairingAddedBy); err != nil {
		t.Fatal(err)
	}
	work, err := s.PairingWork(t.Context())
	if err != nil || len(work) != 1 {
		t.Fatalf("pairing: %v %v", work, err)
	}
	key, _ := membershipKey("sender", "user")
	if err := s.WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Delete, Entity: ACLMembership, Key: key}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMembership("sender", "user", PairingAddedBy); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgePairing(t.Context(), work[0]); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.PairingWork(t.Context())
	if err != nil || len(fresh) != 1 {
		t.Fatalf("old ack consumed new pairing: %v %v", fresh, err)
	}
	if err := s.AcknowledgePairing(t.Context(), fresh[0]); err != nil {
		t.Fatal(err)
	}
	c := &stateReadCounter{Store: s.obj}
	if work, err := NewOver(c).PairingWork(t.Context()); err != nil || len(work) != 0 || c.rows != 0 {
		t.Fatalf("acked pairing: %v %v rows=%d", work, err, c.rows)
	}
}

func TestExpiredCredentialCannotBeReplayed(t *testing.T) {
	s := openMem(t)
	m := core.Message{ID: "expired", ChatJID: "chat", Timestamp: time.Now().Add(-time.Hour), BotMsg: true}
	if _, err := s.putMessage(t.Context(), m, core.MessageStatusPending, messageOptions{exact: true, delivery: map[string]string{"expires_at": core.TS(time.Now().Add(-time.Minute)), "next_attempt_at": core.TS(time.Now().Add(time.Hour))}}); err != nil {
		t.Fatal(err)
	}
	due, err := s.PendingOutbound(t.Context(), time.Now(), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("expiry not due: %v %v", due, err)
	}
	if err := s.DeadLetter(t.Context(), m, 0); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ReplayDeadLetter(t.Context(), m); err == nil || ok {
		t.Fatalf("expired replay: %v %v", ok, err)
	}
}

func TestOnboardingWorkMigrationRunsOnce(t *testing.T) {
	s := openMem(t)
	now := time.Now()
	primary, err := onboardingPut(OnboardingRow{JID: "legacy", Sender: "sender", Status: statusQueued, Gate: "*", Created: onbodTS(now), QueuedAt: onbodTS(now)})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := gateUpsert("*", 1)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.obj.GuardedBatch(t.Context(), nil, []objstore.Write{primary, gate}); err != nil || !ok {
		t.Fatalf("legacy: %v %v", ok, err)
	}
	if err := s.MigrateOnboardingIndexes(t.Context()); err != nil {
		t.Fatal(err)
	}
	runGate(t, s, now, "legacy")
	c := &stateReadCounter{Store: s.obj}
	if err := NewOver(c).MigrateOnboardingIndexes(t.Context()); err != nil || c.rows != 1 {
		t.Fatalf("repeat migration %v rows=%d", err, c.rows)
	}
}

func TestOnboardingGateAcknowledgementKeepsQuotaRelease(t *testing.T) {
	s := openMem(t)
	now := time.Now()
	if err := s.PutGate("*", 1); err != nil {
		t.Fatal(err)
	}
	queueAdmission(t, s, "first", "*")
	queueAdmission(t, s, "second", "*")
	runGate(t, s, now, "first")
	work := oneGateWork(t, s, now)
	raced := NewOver(&onboardingRace{Store: s.obj, fire: func(context.Context) error { return s.DenyOnboarding("first") }})
	if ids, err := raced.AdmitGate(t.Context(), work, now); err != nil || len(ids) != 0 {
		t.Fatalf("old quota pass: %v %v", ids, err)
	}
	runGate(t, s, now, "second")
}

func TestOnboardingAdmissionKeepsChangedRow(t *testing.T) {
	s := openMem(t)
	if err := s.PutGate("*", 1); err != nil {
		t.Fatal(err)
	}
	queueAdmission(t, s, "candidate", "*")
	work := oneGateWork(t, s, time.Now())
	raced := NewOver(&onboardingRace{Store: s.obj, fire: func(ctx context.Context) error {
		// Change only the row, independently exercising its state guard.
		_, err := s.Obj().GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Update, Entity: Onboarding, Key: objstore.Key{Partition: OwnerOnbod, Sort: "candidate"}, Attrs: map[string]string{attrOnbStatus: statusRefused}}})
		return err
	}})
	if ids, err := raced.AdmitGate(t.Context(), work, time.Now()); err != nil || len(ids) != 0 {
		t.Fatalf("changed row admitted: %v %v", ids, err)
	}
	if state := admissionItem(t, s, "candidate").Attrs[attrOnbStatus]; state != statusRefused {
		t.Fatalf("operator state overwritten: %s", state)
	}
}

func TestOnboardingVerdictKeepsResolvedData(t *testing.T) {
	for _, paired := range []bool{false, true} {
		t.Run(fmt.Sprint(paired), func(t *testing.T) {
			s := openMem(t)
			if _, err := s.InsertOnboarding("chat", "sender"); err != nil {
				t.Fatal(err)
			}
			if paired {
				setAdmission(t, s, "chat", map[string]string{attrOnbUserSub: "original"})
			} else {
				setAdmission(t, s, "chat", map[string]string{attrOnbStatus: statusRefused})
			}
			if err := s.QueueOnboarding("chat", "replacement", "*"); err != nil {
				t.Fatal(err)
			}
			after := admissionItem(t, s, "chat")
			if paired && after.Attrs[attrOnbUserSub] != "original" || !paired && after.Attrs[attrOnbStatus] != statusRefused {
				t.Fatalf("resolved data overwritten: %v", after.Attrs)
			}
		})
	}
}

func TestOnboardingNoticeRetainsGenerationAndCancelsOnAdmission(t *testing.T) {
	s := openMem(t)
	queueAdmission(t, s, "chat", "*")
	first, err := s.ReadyOnboardingNotices(t.Context(), time.Now().Add(time.Second))
	if err != nil || len(first) != 1 {
		t.Fatalf("notice: %v %v", first, err)
	}
	if err := s.RepromptOnboarding("chat"); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueOnboarding("chat", "user", "*"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteOnboardingNotice(t.Context(), first[0], true); err != nil {
		t.Fatal(err)
	}
	next, err := s.ReadyOnboardingNotices(t.Context(), time.Now().Add(time.Second))
	if err != nil || len(next) != 1 || next[0].Attrs["_notice_id"] == first[0].Attrs["_notice_id"] {
		t.Fatalf("notice generation lost: %v %v", next, err)
	}
	if err := s.ApproveOnboarding("chat"); err != nil {
		t.Fatal(err)
	}
	c := &stateReadCounter{Store: s.obj}
	if work, err := NewOver(c).ReadyOnboardingNotices(t.Context(), time.Now().Add(time.Hour)); err != nil || len(work) != 0 || c.rows != 0 {
		t.Fatalf("obsolete notice: %v %v rows=%d", work, err, c.rows)
	}
}
