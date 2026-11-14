package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/retry"
)

func TestCapacityRetryTransitions(t *testing.T) {
	s := openTurnEntities(t)
	ctx := t.Context()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msg, err := s.AppendMessage(core.Message{ID: "first", ChatJID: "web:capacity", Timestamp: now})
	if err != nil {
		t.Fatal(err)
	}
	key, err := messageKeyOf(msg)
	if err != nil {
		t.Fatal(err)
	}
	input := MessageCursor(msg)
	opened, err := s.OpenTurnContext("capacity", "demo", "", msg.ChatJID, "", "", &TurnAdmission{
		AttemptID: "attempt", InputLast: input, InputKeys: []objstore.Key{key},
	})
	if err != nil || !opened {
		t.Fatalf("open: %v %v", opened, err)
	}
	read := func() TurnContext {
		t.Helper()
		tc, ok, err := s.ReadTurnContext(ctx, "capacity")
		if err != nil || !ok {
			t.Fatalf("read: %v %v", ok, err)
		}
		return tc
	}
	tc := read()
	decision := retry.Decision{Failures: 1, NextAt: now.Add(time.Minute)}
	stale := tc
	stale.AttemptID = "older"
	if ok, err := s.RecordCapacityFailure(ctx, stale, "", input, decision); err != nil || ok {
		t.Fatalf("stale attempt: %v %v", ok, err)
	}
	if ok, err := s.RecordCapacityFailure(ctx, tc, "", input, decision); err != nil || !ok {
		t.Fatalf("record: %v %v", ok, err)
	}
	if ok, err := s.RecordCapacityFailure(ctx, tc, "", input, decision); err != nil || ok {
		t.Fatalf("stale record: %v %v", ok, err)
	}
	tc = read()
	if tc.State != TurnWaitingCapacity || tc.CapacityFailures != 1 || tc.RetryCount != 0 || !tc.NextAttemptAt.Equal(decision.NextAt) {
		t.Fatalf("wait: %+v", tc)
	}
	if !IsActiveTurnState(tc.State) {
		t.Fatal("capacity wait is not active")
	}
	if recovered, err := s.RecoverableTurns(ctx); err != nil || len(recovered) != 0 {
		t.Fatalf("capacity wait is recoverable: %+v %v", recovered, err)
	}
	if count, err := s.SweepExpiredRunning(time.Nanosecond); err != nil || count != 0 {
		t.Fatalf("capacity wait expired: %d %v", count, err)
	}
	if due, err := s.DeferredTurns(ctx, now); err != nil || len(due) != 0 {
		t.Fatalf("early due: %+v %v", due, err)
	}
	if due, err := s.DeferredTurns(ctx, decision.NextAt); err != nil || len(due) != 1 {
		t.Fatalf("due: %+v %v", due, err)
	}
	if ok, err := s.ClaimDeferredTurn(ctx, tc, "early", now); err != nil || ok {
		t.Fatalf("early claim: %v %v", ok, err)
	}
	newer := MsgStamp(core.TS(now.Add(time.Second)), "new")
	if ok, err := s.WakeCapacityTurn(ctx, tc, newer, now); err != nil || !ok {
		t.Fatalf("wake: %v %v", ok, err)
	}
	if ok, err := s.WakeCapacityTurn(ctx, tc, newer, now); err != nil || ok {
		t.Fatalf("stale wake: %v %v", ok, err)
	}
	tc = read()
	if tc.CapacityFailures != 1 || tc.CapacityWakeCursor != newer || !tc.NextAttemptAt.Equal(now) {
		t.Fatalf("woken: %+v", tc)
	}
	if ok, err := s.WakeCapacityTurn(ctx, tc, input, now); err != nil || ok {
		t.Fatalf("older wake: %v %v", ok, err)
	}
	if ok, err := s.ClaimDeferredTurn(ctx, tc, "next", now); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	tc = read()
	if tc.State != TurnRunning || tc.AttemptID != "next" || tc.CapacityFailures != 1 || !tc.NextAttemptAt.IsZero() {
		t.Fatalf("claimed: %+v", tc)
	}
	for failures := 2; failures < 10; failures++ {
		decision := retry.Decision{Failures: failures, NextAt: now}
		if ok, err := s.RecordCapacityFailure(ctx, tc, "", newer, decision); err != nil || !ok {
			t.Fatalf("failure %d: %v %v", failures, ok, err)
		}
		tc = read()
		if ok, err := s.ClaimDeferredTurn(ctx, tc, "next", now); err != nil || !ok {
			t.Fatalf("claim %d: %v %v", failures, ok, err)
		}
		tc = read()
	}
	if ok, err := s.RecordCapacityFailure(ctx, tc, newer, newer, retry.Decision{Failures: 10, Stop: true}); err != nil || ok {
		t.Fatalf("cursor guard: %v %v", ok, err)
	}
	if ok, err := s.RecordCapacityFailure(ctx, tc, "", newer, retry.Decision{Failures: 10, Stop: true}); err != nil || !ok {
		t.Fatalf("stop: %v %v", ok, err)
	}
	tc = read()
	if tc.State != TurnDone || !tc.RunReturned || tc.CapacityFailures != 10 || tc.StatusOwed != "final" || !tc.NextAttemptAt.IsZero() {
		t.Fatalf("terminal: %+v", tc)
	}
	if cursor, err := s.AgentCursor(msg.ChatJID); err != nil || cursor != input {
		t.Fatalf("cursor: %q %v", cursor, err)
	}
	if due, err := s.DeferredTurns(ctx, now.Add(time.Hour)); err != nil || len(due) != 0 {
		t.Fatalf("terminal due: %+v %v", due, err)
	}
	if err := s.AcknowledgeTurnStatus(ctx, tc); err != nil {
		t.Fatal(err)
	}
	tc = read()
	if tc.CapacityFailures != 10 || tc.CapacityWakeCursor != newer || tc.StatusOwed != "" {
		t.Fatalf("archive: %+v", tc)
	}
}

func TestCapacityMetadataRejectsMalformed(t *testing.T) {
	for _, attrs := range []map[string]string{
		{"capacity_failures": "oops"}, {"capacity_failures": "0"}, {"capacity_wake_cursor": "bad"},
		{attrTurnState: TurnWaitingCapacity, "capacity_failures": "1"},
	} {
		if _, err := turnFrom(objstore.Item{Attrs: attrs}); err == nil {
			t.Fatalf("accepted %v", attrs)
		}
	}
	input := MsgStamp(core.TS(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), "input")
	keys, err := json.Marshal([]objstore.Key{{Partition: "web:1", Sort: input}})
	if err != nil {
		t.Fatal(err)
	}
	attrs := map[string]string{attrTurnState: TurnWaitingCapacity, attrTurnChatJID: "web:1",
		"capacity_failures": "1", "input_last": input, "input_keys": string(keys), "next_attempt_at": core.NowTS()}
	for _, field := range []string{attrTurnRunID, attrTurnRunReturned} {
		attrs[field] = "1"
		if _, err := turnFrom(objstore.Item{Attrs: attrs}); err == nil {
			t.Fatalf("accepted %s", field)
		}
		delete(attrs, field)
	}
}

func TestCapacitySaveAndSupersede(t *testing.T) {
	s := openTurnEntities(t)
	ctx := t.Context()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msg, err := s.AppendMessage(core.Message{ID: "input", ChatJID: "web:save", Timestamp: now})
	if err != nil {
		t.Fatal(err)
	}
	key, err := messageKeyOf(msg)
	if err != nil {
		t.Fatal(err)
	}
	input := MessageCursor(msg)
	for _, id := range []string{"save", "supersede"} {
		ok, err := s.OpenTurnContext(id, "demo", "", msg.ChatJID, "", "", &TurnAdmission{
			AttemptID: id, InputLast: input, InputKeys: []objstore.Key{key},
		})
		if err != nil || !ok {
			t.Fatalf("open %s: %v %v", id, ok, err)
		}
		tc, _, err := s.ReadTurnContext(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		ok, err = s.RecordCapacityFailure(ctx, tc, "", input, retry.Decision{Failures: 1, NextAt: now})
		if err != nil || !ok {
			t.Fatalf("wait %s: %v %v", id, ok, err)
		}
	}
	tc, _, err := s.ReadTurnContext(ctx, "save")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.RequeueDeferredTurn(ctx, tc, now, input); err != nil || ok {
		t.Fatalf("stale cursor: %v %v", ok, err)
	}
	if ok, err := s.RequeueDeferredTurn(ctx, tc, now, ""); err != nil || !ok {
		t.Fatalf("save: %v %v", ok, err)
	}
	tc, _, err = s.ReadTurnContext(ctx, "save")
	if err != nil || tc.State != TurnPendingRetry || tc.CapacityFailures != 1 || tc.InputLast != input {
		t.Fatalf("saved: %+v %v", tc, err)
	}
	tc, _, err = s.ReadTurnContext(ctx, "supersede")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SupersedeCapacityTurn(ctx, tc); err != nil || !ok {
		t.Fatalf("supersede: %v %v", ok, err)
	}
	if ok, err := s.SupersedeCapacityTurn(ctx, tc); err != nil || ok {
		t.Fatalf("stale supersede: %v %v", ok, err)
	}
	tc, _, err = s.ReadTurnContext(ctx, "supersede")
	if err != nil || tc.State != TurnDone || tc.CapacityFailures != 1 {
		t.Fatalf("archived: %+v %v", tc, err)
	}
	if cursor, err := s.AgentCursor(msg.ChatJID); err != nil || cursor != input {
		t.Fatalf("cursor: %q %v", cursor, err)
	}
}

func TestCapacityClaimReplacement(t *testing.T) {
	s := openTurnEntities(t)
	ctx := t.Context()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.PutGroup(core.Group{Folder: "next"}); err != nil {
		t.Fatal(err)
	}
	msg, err := s.AppendMessage(core.Message{ID: "input", ChatJID: "web:binding", Timestamp: now})
	if err != nil {
		t.Fatal(err)
	}
	key, err := messageKeyOf(msg)
	if err != nil {
		t.Fatal(err)
	}
	admission := &TurnAdmission{AttemptID: "first", InputLast: MessageCursor(msg), InputKeys: []objstore.Key{key}}
	if ok, err := s.OpenTurnContext("input", "demo", "old-topic", msg.ChatJID, "old", "old-return", admission); err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	observed, _, err := s.ReadTurnContext(ctx, "input")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.RecordCapacityFailure(ctx, observed, "", MessageCursor(msg), retry.Decision{Failures: 1, NextAt: now}); err != nil || !ok {
		t.Fatalf("busy: %v %v", ok, err)
	}
	observed, _, err = s.ReadTurnContext(ctx, "input")
	if err != nil {
		t.Fatal(err)
	}
	binding := TurnContext{TurnID: "input", Folder: "missing", ChatJID: msg.ChatJID, Trigger: "new"}
	next := &TurnAdmission{AttemptID: "second", InputLast: MessageCursor(msg), InputKeys: []objstore.Key{key}, AmbientKeys: []objstore.Key{}}
	if ok, err := s.ClaimControlledTurn(ctx, observed, &binding, next, now); err != nil || ok {
		t.Fatalf("missing destination: %v %v", ok, err)
	}
	if err := s.DeleteGroupRow("demo"); err != nil {
		t.Fatal(err)
	}
	binding.Folder = "next"
	for name, change := range map[string]func(*TurnContext){
		"attempt": func(tc *TurnContext) { tc.AttemptID = "stale" },
		"folder":  func(tc *TurnContext) { tc.Folder = "stale" },
		"count":   func(tc *TurnContext) { tc.CapacityFailures++ },
		"due":     func(tc *TurnContext) { tc.NextAttemptAt = now.Add(-time.Second) },
	} {
		stale := observed
		change(&stale)
		if ok, err := s.ClaimControlledTurn(ctx, stale, &binding, next, now); err != nil || ok {
			t.Fatalf("stale %s: %v %v", name, ok, err)
		}
	}
	chat, err := chatKey(msg.ChatJID)
	if err != nil {
		t.Fatal(err)
	}
	beforeChat, ok, err := s.itemAt(ctx, Chats, chat)
	if err != nil || !ok {
		t.Fatalf("read chat: %v %v", ok, err)
	}
	if ok, err := s.ClaimControlledTurn(ctx, observed, &binding, next, now); err != nil || !ok {
		t.Fatalf("replacement: %v %v", ok, err)
	}
	afterChat, ok, err := s.itemAt(ctx, Chats, chat)
	if err != nil || !ok || beforeChat.Attrs[objstore.IndexRevision] == afterChat.Attrs[objstore.IndexRevision] {
		t.Fatalf("chat ownership was not fenced: before=%+v after=%+v err=%v", beforeChat, afterChat, err)
	}
	got, ok, err := s.ReadTurnContext(ctx, "input")
	if err != nil || !ok || got.State != TurnRunning || got.Folder != "next" || got.Topic != "" || got.ReturnTo != "" || got.Trigger != "new" || got.AttemptID != "second" || got.CapacityFailures != 1 || got.CapacityWakeCursor != MessageCursor(msg) || got.InputLast != MessageCursor(msg) {
		t.Fatalf("binding: %+v %v %v", got, ok, err)
	}
	old, err := s.LiveTurnsInFolder(ctx, "demo")
	if err != nil || len(old) != 0 {
		t.Fatalf("old pointer: %+v %v", old, err)
	}
	current, err := s.LiveTurnsInFolder(ctx, "next")
	if err != nil || len(current) != 1 || current[0].TurnID != "input" {
		t.Fatalf("new pointer: %+v %v", current, err)
	}
}
