package store

// merge_test.go is the store-side half of objstore's Merge conformance body.
// That one proves the DRIVER merges rather than replaces; these prove the four
// call sites ask for the merge they need, so a site that reaches for a Put —
// which looks equivalent and is a whole-item replace — names the attribute it
// erased.

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// TestSetSecretKeepsTheOAuthHalfItDoesNotName is the trap a review nearly
// shipped, at the call site rather than in the driver.
//
// A secrets item has TWO writers. secrets_oauth.go owns `provider`,
// `refresh_val`, `expires_at` and `scope_list`; SetSecret owns `value` and
// `created_at` and names neither of the other four. A Put here replaces the
// whole item, so an operator pasting a PAT over a surrogate-OAuth credential
// would silently strip the refresh token the broker renews with — and the
// failure surfaces hours later as a login that cannot refresh, nowhere near the
// write that caused it.
func TestSetSecretKeepsTheOAuthHalfItDoesNotName(t *testing.T) {
	s := oauthStore(t)
	exp := time.Now().Add(time.Hour).UTC()
	if err := s.PutOAuthSecret("github:alice", "GITHUB_TOKEN", "acc-tok", "github",
		"ref-tok", exp, "repo,read:user"); err != nil {
		t.Fatalf("seed the surrogate-OAuth half: %v", err)
	}

	if err := s.SetSecret(ScopeUser, "github:alice", "GITHUB_TOKEN", "pasted"); err != nil {
		t.Fatalf("SetSecret over the OAuth row: %v", err)
	}

	rows, err := s.UserOAuthSecrets("github:alice", []string{"GITHUB_TOKEN"})
	if err != nil {
		t.Fatalf("read the OAuth half back: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("the OAuth half reads back as %d rows, want 1: SetSecret named neither "+
			"`provider` nor `refresh_val`, so a whole-item replace took the row out of its "+
			"own projection", len(rows))
	}
	if rows[0].Refresh != "ref-tok" {
		t.Errorf("refresh_val is %q, want ref-tok: the metadata writer does not own it, and "+
			"the broker renews with it", rows[0].Refresh)
	}
	if rows[0].Scope != "repo,read:user" {
		t.Errorf("scope_list is %q, want repo,read:user", rows[0].Scope)
	}
	if rows[0].ExpiresAt.Sub(exp).Abs() > time.Second {
		t.Errorf("expires_at is %v, want %v", rows[0].ExpiresAt, exp)
	}
	// And the half SetSecret DOES own moved.
	got, err := s.GetSecret(ScopeUser, "github:alice", "GITHUB_TOKEN")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got.Value != "pasted" {
		t.Errorf("value is %q, want the pasted one", got.Value)
	}
}

// TestAnUpsertLeavesTheInstantTheFolderAppeared is putGroup's Init half.
//
// `added_at` is when the folder appeared, and every caller but the CLI passes
// time.Now(), so an upsert that merged it would overwrite that fact with the
// instant of the newest write — and the operator's group page, which reads it,
// would report every folder as created just now.
func TestAnUpsertLeavesTheInstantTheFolderAppeared(t *testing.T) {
	s := openEntities(t)
	born := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := s.PutGroup(core.Group{Folder: "atlas/eng", AddedAt: born}); err != nil {
		t.Fatalf("create the group: %v", err)
	}
	if err := s.PutGroup(core.Group{
		Folder: "atlas/eng", AddedAt: time.Now(), Product: "chat",
	}); err != nil {
		t.Fatalf("upsert the group: %v", err)
	}
	g, ok := s.GroupByFolder("atlas/eng")
	if !ok {
		t.Fatal("the group is gone after its own upsert")
	}
	if !g.AddedAt.Equal(born) {
		t.Errorf("added_at is %v, want %v: it is what the item is BORN with, so an upsert "+
			"states it on the create and never again", g.AddedAt, born)
	}
	if g.Product != "chat" {
		t.Errorf("product is %q, want chat: the merged half did not land", g.Product)
	}
}

// TestAnUpsertKeepsTheSettingsItDoesNotState is the other half of putGroup's
// one write: `config` holds ONE settings document, so a model override is folded
// into what the group already has rather than replacing it.
func TestAnUpsertKeepsTheSettingsItDoesNotState(t *testing.T) {
	s := openEntities(t)
	if err := s.PutGroup(core.Group{Folder: "atlas", AddedAt: time.Now()}); err != nil {
		t.Fatalf("create the group: %v", err)
	}
	if err := s.SetGroupOpen("atlas", false); err != nil {
		t.Fatalf("close the group: %v", err)
	}
	if err := s.PutGroup(core.Group{
		Folder: "atlas", AddedAt: time.Now(), Model: "opus",
	}); err != nil {
		t.Fatalf("upsert with a model: %v", err)
	}
	g, ok := s.GroupByFolder("atlas")
	if !ok {
		t.Fatal("the group is gone")
	}
	if g.Model != "opus" {
		t.Errorf("model is %q, want opus", g.Model)
	}
	if g.Open {
		t.Error("the group reads OPEN after an upsert that only named a model: the open flag " +
			"and the model live in ONE settings document, so the model is folded in rather " +
			"than written over it")
	}
}

// TestSetLastReplyNeverMovesTheEngagedFolder is the reply anchor's Init half.
//
// The folder is written on the row this CREATES and never again: a broadcast
// send into a chat some other folder is engaged with would otherwise move the
// engaged folder to the sender, and that folder's window would be hijacked by a
// message nobody routed to it. Only BumpEngagement moves it, because that is the
// write an engagement decision made.
func TestSetLastReplyNeverMovesTheEngagedFolder(t *testing.T) {
	s := openEntities(t)
	if err := s.SetLastReply("slack:T/C", "", "m-1", "atlas/eng"); err != nil {
		t.Fatalf("record the first anchor: %v", err)
	}
	if err := s.SetLastReply("slack:T/C", "", "m-2", "other/team"); err != nil {
		t.Fatalf("record the second anchor: %v", err)
	}
	if got := s.LastReplyID("slack:T/C", ""); got != "m-2" {
		t.Errorf("the anchor is %q, want m-2: it is the merged half and moves every time", got)
	}
	it, ok, err := s.replyStateItem(context.Background(), "slack:T/C", "")
	if err != nil || !ok {
		t.Fatalf("read the reply state back: ok=%v err=%v", ok, err)
	}
	if got := it.Attrs[AttrReplyEngagedFolder]; got != "atlas/eng" {
		t.Errorf("the engaged folder is %q, want atlas/eng: a later sender must not take a "+
			"conversation over by answering into it", got)
	}
	// The engagement decision DOES move it.
	if err := s.BumpEngagement("slack:T/C", "", "other/team", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("bump the window: %v", err)
	}
	e, live := s.Engagement("slack:T/C", "")
	if !live || e.Folder != "other/team" {
		t.Errorf("after a bump the window is %+v live=%v, want other/team", e, live)
	}
}

// TestADisengageTakesBothAttributesAwayInOneWrite is the merge that carries
// ONLY a removal — the operator's and the agent's disengage, which ends the
// window and un-claims the folder together.
//
// Both go, and they go by being TAKEN AWAY rather than written empty: absent is
// the only null this model has, so a cleared deadline still present would read
// as a window with an unparseable end, and an empty folder left behind would let
// the previous holder keep a window the operator just released. Five separate
// writes are what BUGS O1 cost the onboarding reset; this is one.
func TestADisengageTakesBothAttributesAwayInOneWrite(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	if err := s.BumpEngagement("slack:T/C", "", "atlas", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("claim the window: %v", err)
	}
	if _, live := s.Engagement("slack:T/C", ""); !live {
		t.Fatal("the window is not live after the claim")
	}
	if err := s.BumpEngagement("slack:T/C", "", "", time.Time{}); err != nil {
		t.Fatalf("disengage: %v", err)
	}
	it, ok, err := s.replyStateItem(ctx, "slack:T/C", "")
	if err != nil || !ok {
		t.Fatalf("the row is gone: ok=%v err=%v", ok, err)
	}
	for _, attr := range []string{AttrReplyEngagedUntil, AttrReplyEngagedFolder} {
		if v, has := it.Attrs[attr]; has {
			t.Errorf("%s is still there as %q after a disengage; absent is the only null",
				attr, v)
		}
	}
	// The same write against a pair with NO row creates one and refuses nothing,
	// which is what lets a disengage run before any reply ever landed.
	if err := s.BumpEngagement("slack:T/NEW", "", "", time.Time{}); err != nil {
		t.Fatalf("disengage a pair that has no row: %v", err)
	}
}

// TestAMergeOpensTheChatWithoutRefusingTheBatch is putMessage's chat-open.
//
// `chats` is the partition list the poll loop and the archive both walk, so a
// message in a chat it does not name is a message nothing ever feeds and nothing
// ever exports. The open used to sit OUTSIDE the message's batch because a
// create-if-absent refuses on the chat's second message and a refusal takes the
// whole batch with it; a Merge has nothing to refuse, so the chat, its inbound
// flag and the message now land together.
func TestAMergeOpensTheChatWithoutRefusingTheBatch(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	for i, m := range []core.Message{
		{ID: "m-1", ChatJID: "slack:T/C", Sender: "alice", Timestamp: time.Now(), Verb: "message"},
		{ID: "m-2", ChatJID: "slack:T/C", Sender: "alice", Timestamp: time.Now().Add(time.Second), Verb: "message"},
	} {
		if err := s.PutMessage(m); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}
	jids, err := s.ChatJIDs(ctx)
	if err != nil {
		t.Fatalf("read the chat list: %v", err)
	}
	if len(jids) != 1 || jids[0] != "slack:T/C" {
		t.Fatalf("the chat list is %v, want one slack:T/C", jids)
	}
	proactive, err := s.ProactiveChats(ctx)
	if err != nil {
		t.Fatalf("read the proactive chats: %v", err)
	}
	if len(proactive) != 1 {
		t.Errorf("%d chats carry has_inbound, want 1: the flag rides the chat's own merge",
			len(proactive))
	}
	// A bot row opens the chat and does NOT flag it.
	if err := s.PutMessage(core.Message{
		ID: "m-3", ChatJID: "slack:T/D", Sender: "agent", BotMsg: true,
		Timestamp: time.Now(), Verb: "message",
	}); err != nil {
		t.Fatalf("append the bot row: %v", err)
	}
	jids, err = s.ChatJIDs(ctx)
	if err != nil {
		t.Fatalf("read the chat list: %v", err)
	}
	if len(jids) != 2 {
		t.Errorf("the chat list is %v, want both chats: the open is unconditional", jids)
	}
	proactive, err = s.ProactiveChats(ctx)
	if err != nil {
		t.Fatalf("read the proactive chats: %v", err)
	}
	if len(proactive) != 1 {
		t.Errorf("%d chats carry has_inbound, want 1: the agent's own output is not traffic "+
			"a person sent", len(proactive))
	}
}

// TestFireProactiveStampsTheCooldownWithTheRowItFires is the atomicity
// FireProactive's transaction gave, now given by one batch.
//
// The caller dispatches the run only after this commits, so the synthetic row
// and the cooldown have to land together: a row with no cooldown fires again on
// the next tick, and a cooldown with no row silences a chat for a turn nobody
// ran.
func TestFireProactiveStampsTheCooldownWithTheRowItFires(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	before := time.Now().Add(-time.Second)
	if err := s.FireProactive(ctx, "slack:T/C", "turn-1"); err != nil {
		t.Fatalf("fire: %v", err)
	}
	if fired := s.ProactiveLastFired("slack:T/C"); !fired.After(before) {
		t.Errorf("the cooldown is %v, want an instant after %v", fired, before)
	}
	msgs, err := s.NewMessagesForChat(ctx, "slack:T/C", "", 10)
	if err != nil {
		t.Fatalf("read the chat: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != "turn-1" {
		t.Fatalf("the chat holds %d messages, want the synthetic trigger", len(msgs))
	}
	// The synthetic row is not traffic a person sent, so it must not make the
	// chat eligible for the NEXT proactive turn.
	proactive, err := s.ProactiveChats(ctx)
	if err != nil {
		t.Fatalf("read the proactive chats: %v", err)
	}
	if len(proactive) != 0 {
		t.Errorf("%d chats carry has_inbound after a proactive fire, want 0: the trigger must "+
			"not feed itself", len(proactive))
	}
}

// TestACreateOnlyMergeIsIdempotentOnTheEdgeItDraws is membershipPut's shape:
// the write lands an edge and leaves an existing one exactly as it was, without
// making its row count an answer — which is what lets it ride in a batch some
// other write's condition decides.
func TestACreateOnlyMergeIsIdempotentOnTheEdgeItDraws(t *testing.T) {
	s := openEntities(t)
	first, err := membershipPut("folder:atlas", RoleMember, "system:first")
	if err != nil {
		t.Fatalf("render the edge: %v", err)
	}
	if err := s.WriteEntities(context.Background(), nil, first); err != nil {
		t.Fatalf("draw the edge: %v", err)
	}
	second, err := membershipPut("folder:atlas", RoleMember, "system:second")
	if err != nil {
		t.Fatalf("render the edge again: %v", err)
	}
	if second.Op != objstore.Merge {
		t.Fatalf("the edge write is a %s; a create-if-absent Put REFUSES the batch it rides "+
			"in, which is why the four edge writers each held a transaction", second.Op)
	}
	if err := s.WriteEntities(context.Background(), nil, second); err != nil {
		t.Fatalf("re-draw the edge: %v", err)
	}
	edges, err := s.MemberEdges(RoleMember)
	if err != nil {
		t.Fatalf("read the edges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("the graph holds %d edges, want 1", len(edges))
	}
	if edges[0].AddedBy != "system:first" {
		t.Errorf("added_by is %q, want system:first: re-adding an edge that is there must not "+
			"restamp who drew it", edges[0].AddedBy)
	}
}
