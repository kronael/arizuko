package store

// coordinates_test.go pins the two rules that make a composite coordinate safe,
// on THIS package's writers.
//
// resreg has its own copies of both and its own tests for them, and those tests
// do not reach here: `resreg/entity.go` renders an item from a manifest row and
// the files beside this one render one from a daemon's call, so breaking either
// guard leaves the other's tests green. That is what a mutation showed — the
// separator refusal and the padded ordering were both mutable in `store` with
// every package still passing.

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// TestCoordinateRefusesTheSeparatorInAValue: a write whose value carries the
// byte that joins the coordinate is REFUSED, not joined.
//
// Joined, two different rows address one item: the last write wins and the read
// that splits the key apart answers a tuple neither row states. The write is the
// only end that can see it — the read gets a key that splits cleanly into the
// wrong parts.
func TestCoordinateRefusesTheSeparatorInAValue(t *testing.T) {
	s := openEntities(t)
	bad := "corp" + KeySep + "eng"

	t.Run("acl", func(t *testing.T) {
		err := s.PutACLRow(core.ACLRow{Principal: bad, Action: "mcp:send", Scope: "**"})
		requireSeparatorRefusal(t, err)
	})
	t.Run("acl_membership", func(t *testing.T) {
		requireSeparatorRefusal(t, s.PutMembership("folder:"+bad, RoleMember, "test"))
	})
	t.Run("network_rules", func(t *testing.T) {
		requireSeparatorRefusal(t, s.AddNetworkRule(bad, "example.com", "test"))
	})
	t.Run("routes", func(t *testing.T) {
		_, err := s.PutRouteRow(core.Route{Seq: 0, Target: bad, Match: "*"})
		requireSeparatorRefusal(t, err)
	})
	t.Run("secrets", func(t *testing.T) {
		// The keyring first: storeValue refuses a plaintext write before the
		// coordinate is ever rendered, so without it this would pass on the
		// wrong refusal.
		sealed := openEntities(t)
		sealed.SetSecretKeys([]byte("coordinate-test-key"))
		requireSeparatorRefusal(t, sealed.SetSecret(SecretScopeFolder, bad, "API_KEY", "v"))
	})

	// The hot tier's three composite coordinates. A folder, a topic and a chat
	// jid are all operator- or platform-supplied, so each is a value the byte
	// can arrive in.
	t.Run("sessions/folder", func(t *testing.T) {
		requireSeparatorRefusal(t, s.SetSession(bad, "#deploy", "sess-1"))
	})
	t.Run("sessions/topic", func(t *testing.T) {
		requireSeparatorRefusal(t, s.SetSession("team", bad, "sess-1"))
	})
	t.Run("chat_reply_state/jid", func(t *testing.T) {
		requireSeparatorRefusal(t, s.SetLastReply(bad, "", "m1", "team"))
	})
	t.Run("chat_reply_state/topic", func(t *testing.T) {
		requireSeparatorRefusal(t, s.SetLastReply("slack:c/1", bad, "m1", "team"))
	})
	t.Run("system_messages/folder", func(t *testing.T) {
		requireSeparatorRefusal(t, s.EnqueueSystemMessage(bad, "system", "note", "hi"))
	})
}

// TestTheHotTierRoundTripsEveryAttributeItCarries reads each of the five back
// through the method its daemon calls, because a Put replaces an item's whole
// attribute set: a writer that forgot a field would report success and lose it,
// and only a read through the real method sees that.
func TestTheHotTierRoundTripsEveryAttributeItCarries(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()

	t.Run("chats", func(t *testing.T) {
		const jid = "slack:T/C/U"
		if err := s.SetStickyGroup(jid, "corp/eng"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetStickyTopic(jid, "#deploy"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetChatIsGroup(jid, true); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkChatErrored(jid); err != nil {
			t.Fatal(err)
		}
		if flagged, _ := s.ErroredChatJIDs(); len(flagged) != 1 || flagged[0] != jid {
			t.Fatalf("precondition: errored chats = %v, want [%s]", flagged, jid)
		}
		if err := s.SetAgentCursor(jid, MsgStamp(core.NowTS(), "m1")); err != nil {
			t.Fatal(err)
		}
		group, topic, err := s.StickyPins(jid)
		if err != nil || group != "corp/eng" || topic != "#deploy" {
			t.Errorf("pins = (%q, %q), err %v — a later write replaced the item", group, topic, err)
		}
		if is, _ := s.ChatIsGroup(jid); !is {
			t.Error("the multi-party flag did not survive the writes after it")
		}
		if flagged, _ := s.ErroredChatJIDs(); len(flagged) != 1 || flagged[0] != jid {
			t.Errorf("errored chats = %v, want [%s]", flagged, jid)
		}
		if cur, _ := s.AgentCursor(jid); cur == "" {
			t.Error("the dispatch cursor did not survive")
		}
		// The false flag is ABSENT and not "0", which is the only null this
		// model has.
		if err := s.SetChatIsGroup(jid, false); err != nil {
			t.Fatal(err)
		}
		if is, _ := s.ChatIsGroup(jid); is {
			t.Error("a chat set back to direct still reads as a group")
		}
	})

	t.Run("sessions", func(t *testing.T) {
		if err := s.ForkTopic("team", "", "#deploy", "child-uuid", false); err != nil {
			t.Fatal(err)
		}
		if err := s.SetSession("team", "#deploy", "resumed-uuid"); err != nil {
			t.Fatal(err)
		}
		line, ok := s.TopicLineage("team", "#deploy")
		if !ok {
			t.Fatal("the forked topic has no lineage")
		}
		if line.ParentTopic == nil || *line.ParentTopic != "" {
			t.Errorf("parent topic = %v, want the main timeline's empty string: absent is "+
				"the only null, so an empty parent needs the fork instant to say it is one",
				line.ParentTopic)
		}
		if line.ForkedAt == "" || line.ObservedCursor == "" {
			t.Errorf("fork instant %q / cursor %q: SetSession replaced the item instead of "+
				"merging into it", line.ForkedAt, line.ObservedCursor)
		}
		if id, _ := s.GetSession("team", "#deploy"); id != "resumed-uuid" {
			t.Errorf("session id = %q, want resumed-uuid", id)
		}
	})

	t.Run("chat_reply_state", func(t *testing.T) {
		const jid = "slack:T/C/E"
		until := time.Now().Add(time.Hour)
		if err := s.SetLastReply(jid, "", "m-1", "team"); err != nil {
			t.Fatal(err)
		}
		if err := s.BumpEngagement(jid, "", "team", until); err != nil {
			t.Fatal(err)
		}
		if got := s.LastReplyID(jid, ""); got != "m-1" {
			t.Errorf("reply anchor = %q, want m-1 — the engagement write replaced the item", got)
		}
		e, live := s.Engagement(jid, "")
		if !live || e.Folder != "team" {
			t.Errorf("engagement = %+v live=%v, want team", e, live)
		}
		// Ending it takes the deadline away and leaves the anchor.
		if err := s.BumpEngagement(jid, "", "team", time.Time{}); err != nil {
			t.Fatal(err)
		}
		if _, live := s.Engagement(jid, ""); live {
			t.Error("the window is still live after it was ended")
		}
		if got := s.LastReplyID(jid, ""); got != "m-1" {
			t.Errorf("reply anchor = %q after the window ended, want m-1", got)
		}
	})

	t.Run("chat_proactive", func(t *testing.T) {
		fired := time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC)
		if err := s.RecordProactiveFire(ctx, "slack:T/C/P", fired); err != nil {
			t.Fatal(err)
		}
		if got := s.ProactiveLastFired("slack:T/C/P"); !got.Equal(fired) {
			t.Errorf("cooldown = %v, want %v", got, fired)
		}
		rows, err := s.ProactiveCooldowns(10)
		if err != nil || len(rows) != 1 || rows[0].JID != "slack:T/C/P" {
			t.Errorf("cooldown panel = %v, err %v", rows, err)
		}
	})

	t.Run("system_messages", func(t *testing.T) {
		for _, body := range []string{"first", "second", "third"} {
			if err := s.EnqueueSystemMessage("team", "system", "note", body); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.EnqueueSystemMessage("other", "system", "note", "not ours"); err != nil {
			t.Fatal(err)
		}
		queued, err := s.QueuedSystemMessages(ctx, "team")
		if err != nil {
			t.Fatal(err)
		}
		if len(queued) != 3 {
			t.Fatalf("the queue holds %d events for team, want 3 — the folder prefix took "+
				"the wrong set", len(queued))
		}
		// The queue is read in the order it was written, which is what the
		// AUTOINCREMENT id it replaced gave.
		for i, want := range []string{"first", "second", "third"} {
			if queued[i].Body != want {
				t.Errorf("event %d = %q, want %q: the queue position does not order", i,
					queued[i].Body, want)
			}
		}
		if n, _ := s.CountSystemMessages(ctx); n != 4 {
			t.Errorf("the entity holds %d events, want 4", n)
		}
	})
}

// requireSeparatorRefusal insists the write failed AND said why. A refusal with
// some other reason would pass a bare `err != nil` while the coordinate went in
// split.
func requireSeparatorRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("the write joined a value carrying the separator, so two rows now " +
			"address one item and the last one wins")
	}
	if !strings.Contains(err.Error(), "separat") && !strings.Contains(err.Error(), "coordinate") {
		t.Errorf("err = %v, want it to name the separator or the coordinate", err)
	}
}

// TestRouteSeqSortsNumerically: the route table reads back in precedence order.
//
// A sort key is compared as TEXT, so an unpadded number puts 10 before 2 — and
// the whole meaning of the table is "lower seq wins, first match". The read
// sorts by (seq, id) afterwards, so this asserts on the KEY: the items come out
// of the driver in the order the coordinate puts them, and a wrong rendering
// there is a wrong answer everywhere the raw order is used.
func TestRouteSeqSortsNumerically(t *testing.T) {
	s := openEntities(t)
	for _, seq := range []int{100, 2, 10, 0} {
		if _, err := s.PutRouteRow(core.Route{Seq: seq, Target: "main", Match: "s"}); err != nil {
			t.Fatalf("seed seq %d: %v", seq, err)
		}
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Routes, Partition: OwnerRoutd,
	})
	if err != nil {
		t.Fatalf("read the route items: %v", err)
	}
	var keys []string
	var seqs []int
	for _, it := range items {
		keys = append(keys, it.Key.Sort)
		n, aerr := strconv.Atoi(strings.SplitN(it.Key.Sort, KeySep, 2)[0])
		if aerr != nil {
			t.Fatalf("route key %q carries a seq that is not a number", it.Key.Sort)
		}
		seqs = append(seqs, n)
	}
	want := []int{0, 2, 10, 100}
	if len(seqs) != len(want) {
		t.Fatalf("read %d routes, want %d: %v", len(seqs), len(want), keys)
	}
	for i, n := range want {
		if seqs[i] != n {
			t.Fatalf("coordinate order = %v, want %v — the key is compared as text, so "+
				"the number has to be rendered at a fixed width", seqs, want)
		}
	}
}

// TestRouteSeqRefusesWhatItCannotOrder: a value the padding cannot order is
// refused rather than written, at the write, which is the only end that can see
// it.
func TestRouteSeqRefusesWhatItCannotOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		seq  int
		want string
	}{
		{"negative", -1, "is negative"},
		{"wider than the padding", 12345678901, "wider than the 10 digits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := openEntities(t).PutRouteRow(core.Route{Seq: tc.seq, Target: "main", Match: "s"})
			if err == nil {
				t.Fatal("the write rendered a seq that does not sort in numeric order")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
