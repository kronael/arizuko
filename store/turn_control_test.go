package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

func TestControlledRefusalExpiry(t *testing.T) {
	s := openTurnEntities(t)
	for _, sample := range []struct{ id, marker, want string }{
		{"refused", "current", TurnRunning},
		{"old-refusal", "previous", TurnExpired},
		{"ordinary", "", TurnExpired},
	} {
		attrs := map[string]string{
			"folder": "demo", "chat_jid": "web:1", "state": TurnRunning,
			"started_at": core.TS(time.Now().Add(-24 * time.Hour)), "attempt_id": "current",
		}
		if sample.marker != "" {
			attrs["provider_failure_attempt"] = sample.marker
		}
		if err := s.SeedItem(t.Context(), TurnContexts, []string{sample.id}, attrs); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SweepExpiredRunning(time.Hour); err != nil {
			t.Fatal(err)
		}
		tc, _ := s.TurnContextOf(sample.id)
		if tc.State != sample.want {
			t.Fatalf("%s state=%s, want %s", sample.id, tc.State, sample.want)
		}
	}
}

func TestControlledEightConcurrentProbeClaims(t *testing.T) {
	s := openTurnEntities(t)
	now := time.Now().UTC()
	before := TurnControlState{Phase: "open", Episode: "outage", NextProbeAt: now}
	if ok, err := s.WriteTurnControl(t.Context(), nil, before); err != nil || !ok {
		t.Fatalf("open breaker: %v %v", ok, err)
	}
	start := make(chan struct{})
	results := make(chan bool, 8)
	for i := range 8 {
		id := "candidate-" + strconv.Itoa(i)
		if err := s.SeedItem(t.Context(), TurnContexts, []string{id}, map[string]string{
			"folder": "demo", "state": TurnPendingRetry, "attempt_id": "waiting",
			"next_attempt_at": core.TS(now),
		}); err != nil {
			t.Fatal(err)
		}
		go func() {
			<-start
			after := before
			after.Phase, after.ProbeTurnID, after.ProbeAttemptID = "probe", id, "probe"
			ok, err := s.ClaimControlledTurn(t.Context(), TurnContext{
				TurnID: id, Folder: "demo", AttemptID: "waiting",
			}, "probe", now, &before, &after)
			if err != nil {
				t.Errorf("claim %s: %v", id, err)
			}
			results <- ok
		}()
	}
	close(start)
	winners := 0
	for range 8 {
		if <-results {
			winners++
		}
	}
	control, _, err := s.ReadTurnControl(t.Context())
	if err != nil || winners != 1 || control.Phase != "probe" || control.ProbeTurnID == "" {
		t.Fatalf("probe winners=%d control=%+v err=%v", winners, control, err)
	}
}

func TestDeferredTurnGuards(t *testing.T) {
	ctx := context.Background()
	s := openTurnEntities(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msg, err := s.AppendMessage(core.Message{ID: "one", ChatJID: "web:1", Timestamp: now, Content: "input"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := messageKeyOf(msg)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.OpenTurnContext("turn", "demo", "topic", "web:1", "sender", "web:return", nil); err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	tc, ok := s.TurnContextOf("turn")
	if !ok {
		t.Fatal("missing turn")
	}
	tc.InputLast = MessageCursor(msg)
	tc.InputKeys = []objstore.Key{key}
	tc.NextAttemptAt = now.Add(time.Hour)
	tc.FailureCause = "failure"
	stale := tc
	stale.AttemptID = "stale"
	if ok, err := s.DeferTurn(ctx, DeferredTurn{Turn: stale}); err != nil || ok {
		t.Fatalf("stale defer: %v %v", ok, err)
	}
	if cursor, _ := s.AgentCursor(msg.ChatJID); cursor != "" {
		t.Fatalf("cursor advanced on refused defer: %q", cursor)
	}
	control := TurnControlState{Phase: "open", Episode: "episode", NextProbeAt: tc.NextAttemptAt}
	if ok, err := s.DeferTurn(ctx, DeferredTurn{Turn: tc, ControlAfter: &control}); err != nil || !ok {
		t.Fatalf("defer: %v %v", ok, err)
	}
	if cursor, _ := s.AgentCursor(msg.ChatJID); cursor != tc.InputLast {
		t.Fatalf("cursor %q", cursor)
	}
	saved, ok, err := s.ReadTurnContext(ctx, tc.TurnID)
	if err != nil || !ok || len(saved.InputKeys) != 1 || saved.InputKeys[0] != key || saved.ReturnTo != "web:return" {
		t.Fatalf("binding: %+v %v", saved, err)
	}
	messages, err := s.DeferredMessages(ctx, saved.InputKeys)
	if err != nil || len(messages) != 1 || messages[0].Content != "input" {
		t.Fatalf("messages: %+v %v", messages, err)
	}
	if ok, err := s.ClaimDeferredTurn(ctx, saved, "new", now); err != nil || ok {
		t.Fatalf("early claim: %v %v", ok, err)
	}
	if ok, err := s.ClaimDeferredTurn(ctx, saved, "new", tc.NextAttemptAt); err != nil || !ok {
		t.Fatalf("due claim: %v %v", ok, err)
	}
	if ok, err := s.ClaimDeferredTurn(ctx, saved, "other", tc.NextAttemptAt); err != nil || ok {
		t.Fatalf("duplicate claim: %v %v", ok, err)
	}
	got, exists, err := s.ReadTurnControl(ctx)
	if err != nil || !exists || got.Episode != control.Episode {
		t.Fatalf("control: %+v %v", got, err)
	}
	if ok, err := s.WriteTurnControl(ctx, nil, control); err != nil || ok {
		t.Fatalf("duplicate control: %v %v", ok, err)
	}
}

func TestSavePendingTurnGuards(t *testing.T) {
	ctx := t.Context()
	s := openTurnEntities(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msg, err := s.AppendMessage(core.Message{ID: "input", ChatJID: "web:pending", Timestamp: now})
	if err != nil {
		t.Fatal(err)
	}
	key, err := messageKeyOf(msg)
	if err != nil {
		t.Fatal(err)
	}
	tc := TurnContext{TurnID: "pending", Folder: "demo", ChatJID: msg.ChatJID,
		InputLast: MessageCursor(msg), InputKeys: []objstore.Key{key}, NextAttemptAt: now,
		RetryCount: 3, FailureCause: "ignored", ReturnTo: "web:return"}
	wrong := MsgStamp(core.TS(now), "wrong")
	if ok, err := s.SavePendingTurn(ctx, tc, wrong); err != nil || ok {
		t.Fatalf("cursor guard: %v %v", ok, err)
	}
	if _, exists := s.TurnContextOf(tc.TurnID); exists {
		t.Fatal("partial save")
	}
	if err := s.DeleteGroupRow("demo"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SavePendingTurn(ctx, tc, ""); err != nil || ok {
		t.Fatalf("group guard: %v %v", ok, err)
	}
	if err := s.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	prior := MsgStamp(core.TS(now.Add(time.Minute)), "later")
	if err := s.SetAgentCursor(msg.ChatJID, prior); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SavePendingTurn(ctx, tc, prior); err != nil || !ok {
		t.Fatalf("save: %v %v", ok, err)
	}
	saved, exists := s.TurnContextOf(tc.TurnID)
	if !exists || saved.State != TurnPendingRetry || saved.AttemptID == "" || saved.RetryCount != 0 ||
		saved.FailureCause != "" || saved.ReturnTo != tc.ReturnTo || len(saved.InputKeys) != 1 || saved.InputKeys[0] != key || !saved.NextAttemptAt.Equal(now) {
		t.Fatalf("binding: %+v", saved)
	}
	if cursor, _ := s.AgentCursor(msg.ChatJID); cursor != prior {
		t.Fatalf("cursor rewound: %q", cursor)
	}
	if ok, err := s.SavePendingTurn(ctx, tc, prior); err != nil || ok {
		t.Fatalf("duplicate save: %v %v", ok, err)
	}
}

func TestControlledProviderFailureGuards(t *testing.T) {
	ctx := t.Context()
	s := openTurnEntities(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if ok, err := s.OpenTurnContext("provider", "demo", "", "web:provider", "u", "", nil); err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	tc, _ := s.TurnContextOf("provider")
	tc.NextAttemptAt = now.Add(4 * time.Hour)
	before := TurnControlState{Phase: "closed", Episode: "one"}
	if ok, err := s.WriteTurnControl(ctx, nil, before); err != nil || !ok {
		t.Fatalf("control: %v %v", ok, err)
	}
	after := TurnControlState{Phase: "open", Episode: "two", NextProbeAt: now}
	pause := ModelPause{Folder: tc.Folder, Reason: "outage", Evidence: "outage", OpenedAt: core.TS(now), Attempts: 1}
	stale := before
	stale.Episode = "stale"
	if ok, err := s.RecordControlledProviderFailure(ctx, tc, nil, &pause, &stale, &after); err != nil || ok {
		t.Fatalf("singleton guard: %v %v", ok, err)
	}
	current, _ := s.TurnContextOf(tc.TurnID)
	p, err := s.ReadModelPause(ctx, tc.Folder)
	if err != nil || p != nil || current.ProviderFailureAttempt != "" {
		t.Fatalf("partial refusal: %+v %+v %v", current, p, err)
	}
	wrong := tc
	wrong.AttemptID = "stale"
	if ok, err := s.RecordControlledProviderFailure(ctx, wrong, nil, &pause, &before, &after); err != nil || ok {
		t.Fatalf("attempt guard: %v %v", ok, err)
	}
	if err := s.SetTurnState(tc.TurnID, TurnPendingRetry); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.RecordControlledProviderFailure(ctx, tc, nil, &pause, &before, &after); err != nil || ok {
		t.Fatalf("state guard: %v %v", ok, err)
	}
	if err := s.SetTurnState(tc.TurnID, TurnRunning); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteGroupRow(tc.Folder); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.RecordControlledProviderFailure(ctx, tc, nil, &pause, &before, &after); err != nil || ok {
		t.Fatalf("group guard: %v %v", ok, err)
	}
	if err := s.PutGroup(core.Group{Folder: tc.Folder}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.RecordControlledProviderFailure(ctx, tc, nil, &pause, &before, &after); err != nil || !ok {
		t.Fatalf("record: %v %v", ok, err)
	}
	current, _ = s.TurnContextOf(tc.TurnID)
	p, err = s.ReadModelPause(ctx, tc.Folder)
	control, exists, controlErr := s.ReadTurnControl(ctx)
	if err != nil || p == nil || p.Reason != "outage" || current.State != TurnRunning || current.ProviderFailureAttempt != tc.AttemptID || !current.NextAttemptAt.Equal(tc.NextAttemptAt) || controlErr != nil || !exists || control.Episode != after.Episode {
		t.Fatalf("refusal: %+v %+v %+v %v %v", current, p, control, err, controlErr)
	}
	if ok, err := s.RecordControlledProviderFailure(ctx, tc, &pause, &pause, &after, &after); err != nil || ok {
		t.Fatalf("duplicate guard: %v %v", ok, err)
	}
}

func TestControlledFinishGuards(t *testing.T) {
	ctx := t.Context()
	s := openTurnEntities(t)
	if ok, err := s.OpenTurnContext("finish", "demo", "", "web:finish", "u", "", nil); err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	tc, _ := s.TurnContextOf("finish")
	before := TurnControlState{Phase: "probe", Episode: "one", ProbeTurnID: tc.TurnID, ProbeAttemptID: tc.AttemptID}
	if ok, err := s.WriteTurnControl(ctx, nil, before); err != nil || !ok {
		t.Fatalf("control: %v %v", ok, err)
	}
	after := TurnControlState{Phase: "closed", Episode: "one"}
	stale := before
	stale.Episode = "stale"
	if ok, err := s.FinishControlledTurn(ctx, tc, &stale, &after); err != nil || ok {
		t.Fatalf("singleton guard: %v %v", ok, err)
	}
	current, _ := s.TurnContextOf(tc.TurnID)
	if current.State != TurnRunning {
		t.Fatalf("partial finish: %+v", current)
	}
	wrong := tc
	wrong.AttemptID = "stale"
	if ok, err := s.FinishControlledTurn(ctx, wrong, &before, &after); err != nil || ok {
		t.Fatalf("attempt guard: %v %v", ok, err)
	}
	wrong = tc
	wrong.State = TurnPendingRetry
	if ok, err := s.FinishControlledTurn(ctx, wrong, &before, &after); err != nil || ok {
		t.Fatalf("state guard: %v %v", ok, err)
	}
	if err := s.DeleteGroupRow(tc.Folder); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.FinishControlledTurn(ctx, tc, &before, &after); err != nil || ok {
		t.Fatalf("group guard: %v %v", ok, err)
	}
	if err := s.PutGroup(core.Group{Folder: tc.Folder}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.FinishControlledTurn(ctx, tc, &before, &after); err != nil || !ok {
		t.Fatalf("finish: %v %v", ok, err)
	}
	current, _ = s.TurnContextOf(tc.TurnID)
	control, exists, err := s.ReadTurnControl(ctx)
	if current.State != TurnDone || !current.RunReturned || err != nil || !exists || control.Phase != "closed" {
		t.Fatalf("finished: %+v %+v %v", current, control, err)
	}
	if ok, err := s.FinishControlledTurn(ctx, tc, &after, nil); err != nil || ok {
		t.Fatalf("duplicate finish: %v %v", ok, err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msg, err := s.AppendMessage(core.Message{ID: "input", ChatJID: "web:cancel", Timestamp: now})
	if err != nil {
		t.Fatal(err)
	}
	key, err := messageKeyOf(msg)
	if err != nil {
		t.Fatal(err)
	}
	pending := TurnContext{TurnID: "cancel", Folder: "demo", ChatJID: msg.ChatJID,
		InputLast: MessageCursor(msg), InputKeys: []objstore.Key{key}, NextAttemptAt: now}
	if ok, err := s.SavePendingTurn(ctx, pending, ""); err != nil || !ok {
		t.Fatalf("save: %v %v", ok, err)
	}
	pending, _ = s.TurnContextOf(pending.TurnID)
	if ok, err := s.FinishControlledTurn(ctx, pending, &after, nil); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	cancelled, _ := s.TurnContextOf(pending.TurnID)
	if cancelled.State != TurnDone || !cancelled.RunReturned || !cancelled.NextAttemptAt.IsZero() {
		t.Fatalf("cancelled: %+v", cancelled)
	}
	if ok, err := s.OpenTurnContext("submitted", "demo", "", "web:submitted", "u", "", nil); err != nil || !ok {
		t.Fatalf("open submitted: %v %v", ok, err)
	}
	if err := s.SetTurnState("submitted", TurnDone); err != nil {
		t.Fatal(err)
	}
	submitted, _ := s.TurnContextOf("submitted")
	if ok, err := s.FinishControlledTurn(ctx, submitted, &after, nil); err != nil || !ok {
		t.Fatalf("close submitted execution: %v %v", ok, err)
	}
	if ok, err := s.FinishControlledTurn(ctx, submitted, &after, nil); err != nil || ok {
		t.Fatalf("closed execution accepted duplicate finish: %v %v", ok, err)
	}
}

func TestDeferredTurnCursorNeverRewinds(t *testing.T) {
	ctx := t.Context()
	s := openTurnEntities(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msg, err := s.AppendMessage(core.Message{ID: "input", ChatJID: "web:cursor", Timestamp: now})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.OpenTurnContext("cursor", "demo", "", msg.ChatJID, "u", "", nil); err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	tc, _ := s.TurnContextOf("cursor")
	tc.InputLast, tc.NextAttemptAt = MessageCursor(msg), now.Add(time.Hour)
	prior := MsgStamp(core.TS(now.Add(time.Minute)), "later")
	if err := s.SetAgentCursor(msg.ChatJID, prior); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.DeferTurn(ctx, DeferredTurn{Turn: tc, PriorCursor: prior}); err != nil || !ok {
		t.Fatalf("defer: %v %v", ok, err)
	}
	if cursor, _ := s.AgentCursor(msg.ChatJID); cursor != prior {
		t.Fatalf("cursor rewound: got %q want %q", cursor, prior)
	}
}

func TestControlledAdmissionGuards(t *testing.T) {
	ctx := t.Context()
	s := openTurnEntities(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.AppendMessage(core.Message{ID: "last", ChatJID: "web:admit", Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	before := TurnControlState{Phase: "open", Episode: "one", NextProbeAt: now}
	if ok, err := s.WriteTurnControl(ctx, nil, before); err != nil || !ok {
		t.Fatalf("control: %v %v", ok, err)
	}
	stale := before
	stale.Episode = "stale"
	after := TurnControlState{Phase: "probe", Episode: "one", ProbeTurnID: "admit", ProbeAttemptID: "attempt"}
	admission := &TurnAdmission{Controlled: true, AttemptID: "attempt", InputLast: MsgStamp(core.TS(now), "last"),
		InputKeys:   []objstore.Key{{Partition: "input", Sort: "one"}},
		AmbientKeys: []objstore.Key{{Partition: "ambient", Sort: "one"}}, ControlBefore: &stale, ControlAfter: &after}
	if ok, _ := s.OpenTurnContext("admit", "demo", "", "web:admit", "u", "", admission); ok {
		t.Fatal("stale singleton admitted")
	}
	admission.ControlBefore = &before
	if ok, err := s.OpenTurnContext("admit", "demo", "", "web:admit", "u", "", admission); err != nil || !ok {
		t.Fatalf("admit: %v %v", ok, err)
	}
	tc, ok := s.TurnContextOf("admit")
	if !ok || len(tc.InputKeys) != 1 || tc.InputKeys[0] != admission.InputKeys[0] || len(tc.AmbientKeys) != 1 || tc.AmbientKeys[0] != admission.AmbientKeys[0] {
		t.Fatalf("binding: %+v", tc)
	}
	got, exists, err := s.ReadTurnControl(ctx)
	if err != nil || !exists || got.Phase != "probe" || got.ProbeAttemptID != "attempt" {
		t.Fatalf("probe: %+v %v", got, err)
	}
	tc.NextAttemptAt = now.Add(time.Hour)
	if ok, err := s.DeferTurn(ctx, DeferredTurn{Turn: tc}); err != nil || !ok {
		t.Fatalf("defer: %v %v", ok, err)
	}
	saved, _ := s.TurnContextOf(tc.TurnID)
	if ok, err := s.ClaimControlledTurn(ctx, saved, "next", tc.NextAttemptAt, &stale, nil); err != nil || ok {
		t.Fatalf("stale control claim: %v %v", ok, err)
	}
	if ok, err := s.ClaimControlledTurn(ctx, saved, "next", now, &after, nil); err != nil || ok {
		t.Fatalf("early claim: %v %v", ok, err)
	}
	if ok, err := s.ClaimControlledTurn(ctx, saved, "next", tc.NextAttemptAt, &after, nil); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
}

func TestDeferredTurnRetirementFencesLateWrites(t *testing.T) {
	ctx := t.Context()
	s := openTurnEntities(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msg, err := s.AppendMessage(core.Message{ID: "input", ChatJID: "web:retire", Timestamp: now})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.OpenTurnContext("retire", "demo", "", msg.ChatJID, "u", "", nil); err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	tc, _ := s.TurnContextOf("retire")
	tc.InputLast, tc.NextAttemptAt = MessageCursor(msg), now.Add(time.Hour)
	if ok, err := s.DeferTurn(ctx, DeferredTurn{Turn: tc}); err != nil || !ok {
		t.Fatalf("defer: %v %v", ok, err)
	}
	saved, _ := s.TurnContextOf(tc.TurnID)
	if _, _, err := s.DeleteGroup("demo"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimDeferredTurn(ctx, saved, "new", tc.NextAttemptAt); err != nil || ok {
		t.Fatalf("claim after deletion: %v %v", ok, err)
	}
	if err := s.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimDeferredTurn(ctx, saved, "new", tc.NextAttemptAt); err != nil || ok {
		t.Fatalf("claim after recreation: %v %v", ok, err)
	}
	late := tc
	late.RetryCount = saved.RetryCount
	if ok, err := s.DeferTurn(ctx, DeferredTurn{Turn: late, PriorCursor: tc.InputLast}); err != nil || ok {
		t.Fatalf("late defer: %v %v", ok, err)
	}
	retired, _ := s.TurnContextOf(tc.TurnID)
	if retired.State != TurnDone {
		t.Fatalf("retired turn: %+v", retired)
	}
}

func TestDeferredTurnSavedInputsAndAtomicRefusal(t *testing.T) {
	ctx := t.Context()
	cfg := dyntest.Config(t)
	cfg.Prefix = dynstore.TestPrefix()
	handle, err := dynstore.Dial(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{OwnerRoutd, OwnerOnbod} {
		if err := PrepareOwner(ctx, handle, owner); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		entities, err := handle.Entities(context.Background())
		if err != nil {
			t.Error(err)
			return
		}
		for _, entity := range entities {
			if err := handle.DropEntity(context.Background(), entity); err != nil {
				t.Error(err)
			}
		}
	})
	s := NewOver(handle)
	if err := s.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	routeID, err := s.AddRoute(core.Route{Match: "chat_jid=telegram:save", Target: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	input, err := s.AppendMessage(core.Message{ID: "input", ChatJID: "telegram:save", Timestamp: now, Content: "original", Attachments: `[{"url":"original"}]`})
	if err != nil {
		t.Fatal(err)
	}
	ambient, err := s.AppendMessage(core.Message{ID: "ambient", ChatJID: "web:save", Timestamp: now.Add(-time.Minute), Content: "context"})
	if err != nil {
		t.Fatal(err)
	}
	inputKey, _ := messageKeyOf(input)
	ambientKey, _ := messageKeyOf(ambient)
	if ok, err := s.OpenTurnContext("saved", "demo", "topic", input.ChatJID, "sender", "web:return", nil); err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	tc, _ := s.TurnContextOf("saved")
	tc.InputLast, tc.NextAttemptAt = MessageCursor(input), now.Add(time.Hour)
	tc.InputKeys, tc.AmbientKeys = []objstore.Key{inputKey}, []objstore.Key{ambientKey}
	control := TurnControlState{Phase: "open", Episode: "one"}
	if ok, err := s.WriteTurnControl(ctx, nil, control); err != nil || !ok {
		t.Fatalf("control: %v %v", ok, err)
	}
	stale := TurnControlState{Phase: "open", Episode: "stale"}
	if ok, err := s.DeferTurn(ctx, DeferredTurn{Turn: tc, ControlBefore: &stale, ControlAfter: &control}); err != nil || ok {
		t.Fatalf("stale control: %v %v", ok, err)
	}
	current, _ := s.TurnContextOf(tc.TurnID)
	cursor, _ := s.AgentCursor(input.ChatJID)
	if current.State != TurnRunning || cursor != "" {
		t.Fatalf("partial deferral: %+v cursor=%q", current, cursor)
	}
	if ok, err := s.DeferTurn(ctx, DeferredTurn{Turn: tc, ControlBefore: &control, ControlAfter: &control}); err != nil || !ok {
		t.Fatalf("defer: %v %v", ok, err)
	}
	if _, err := s.SweepExpiredRunning(0); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.OpenTurnContext(tc.TurnID, "demo", "changed", input.ChatJID, "other", "web:other", nil); (err != nil && !errors.Is(err, ErrModelPaused)) || ok {
		t.Fatalf("reopen deferred: %v %v", ok, err)
	}
	if _, err := s.UpdateRoute(routeID, core.Route{Match: "chat_jid=telegram:save", Target: "other"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.BumpEngagement(input.ChatJID, "topic", "demo", now); err != nil {
		t.Fatal(err)
	}
	reopenedHandle, err := dynstore.Dial(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reopened := NewOver(reopenedHandle)
	saved, ok, err := reopened.ReadTurnContext(ctx, tc.TurnID)
	if err != nil || !ok || saved.State != TurnPendingRetry || saved.Topic != "topic" || saved.Trigger != "sender" || saved.ReturnTo != "web:return" {
		t.Fatalf("binding: %+v %v", saved, err)
	}
	inputs, err := reopened.DeferredMessages(ctx, saved.InputKeys)
	if err != nil || len(inputs) != 1 || inputs[0].Content != input.Content || inputs[0].Attachments != input.Attachments {
		t.Fatalf("input: %+v %v", inputs, err)
	}
	contextMessages, err := reopened.DeferredMessages(ctx, saved.AmbientKeys)
	if err != nil || len(contextMessages) != 1 || contextMessages[0].Content != ambient.Content {
		t.Fatalf("ambient: %+v %v", contextMessages, err)
	}
	if _, err := reopened.DeferredMessages(ctx, []objstore.Key{{Partition: "missing", Sort: "missing"}}); err == nil {
		t.Fatal("missing input accepted")
	}
}

func TestDeferredTurnRejectsChangedCursorAndMissingGroup(t *testing.T) {
	ctx := t.Context()
	s := openTurnEntities(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msg, err := s.AppendMessage(core.Message{ID: "input", ChatJID: "web:guards", Timestamp: now})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.OpenTurnContext("guards", "demo", "", msg.ChatJID, "u", "", nil); err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	tc, _ := s.TurnContextOf("guards")
	tc.InputLast, tc.NextAttemptAt = MessageCursor(msg), now.Add(time.Hour)
	if ok, err := s.DeferTurn(ctx, DeferredTurn{Turn: tc, PriorCursor: MsgStamp(core.TS(now), "wrong")}); err != nil || ok {
		t.Fatalf("cursor guard: %v %v", ok, err)
	}
	if err := s.DeleteGroupRow("demo"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.DeferTurn(ctx, DeferredTurn{Turn: tc}); err != nil || ok {
		t.Fatalf("group guard: %v %v", ok, err)
	}
	current, _ := s.TurnContextOf(tc.TurnID)
	cursor, _ := s.AgentCursor(msg.ChatJID)
	if current.State != TurnRunning || cursor != "" {
		t.Fatalf("partial write: %+v cursor=%q", current, cursor)
	}
	if err := s.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.DeferTurn(ctx, DeferredTurn{Turn: tc}); err != nil || !ok {
		t.Fatalf("defer: %v %v", ok, err)
	}
	saved, _ := s.TurnContextOf(tc.TurnID)
	stale := saved
	stale.AttemptID = "stale"
	if ok, err := s.ClaimDeferredTurn(ctx, stale, "new", tc.NextAttemptAt); err != nil || ok {
		t.Fatalf("claim attempt guard: %v %v", ok, err)
	}
	if err := s.DeleteGroupRow("demo"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimDeferredTurn(ctx, saved, "new", tc.NextAttemptAt); err != nil || ok {
		t.Fatalf("claim group guard: %v %v", ok, err)
	}
}
