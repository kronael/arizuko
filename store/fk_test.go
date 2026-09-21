package store

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"

	"github.com/kronael/arizuko/audit"
)

// What a group delete takes away, and what it leaves.
//
// AN ENTITY HAS NO FOREIGN KEY, so nothing here rides on a cascade: every row a
// delete removes is removed by store/folder_scope.go's purge, explicitly, by
// the same containment predicate for every scope. web_routes and route_tokens
// are two of the scopes it names (BUGS L253).

// seedWebRoute writes one web_route item. routd owns every write of this
// entity (web_routes.go says so), so the fixture addresses the coordinate and
// the attribute names that file EXPORTS rather than minting a second spelling.
func seedWebRoute(t *testing.T, s *Store, prefix, folder string) {
	t.Helper()
	key, err := WebRouteKey(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Put, Entity: WebRoutes, Key: key,
		Attrs: map[string]string{
			AttrWebAccess:    "public",
			AttrWebFolder:    folder,
			AttrWebCreatedAt: core.NowTS(),
		},
	}); err != nil {
		t.Fatalf("seed web route %s: %v", prefix, err)
	}
}

func countWebRoutes(t *testing.T, s *Store, folder string) int {
	t.Helper()
	all, err := s.AllWebRoutes()
	if err != nil {
		t.Fatalf("AllWebRoutes: %v", err)
	}
	n := 0
	for _, wr := range all {
		if wr.Folder == folder {
			n++
		}
	}
	return n
}

// TestWebRoutes_PurgedOnGroupDelete: deleting a group removes its web_routes.
func TestWebRoutes_PurgedOnGroupDelete(t *testing.T) {
	s := openEntities(t)
	if err := s.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}
	seedWebRoute(t, s, "/pub/acme/", "acme")
	if got := countWebRoutes(t, s, "acme"); got != 1 {
		t.Fatalf("seed: want 1 web_route, got %d", got)
	}

	if _, _, err := s.DeleteGroup("acme"); err != nil {
		t.Fatal(err)
	}
	if got := countWebRoutes(t, s, "acme"); got != 0 {
		t.Fatalf("after group delete: want 0 web_routes, got %d", got)
	}
}

// TestRouteTokens_PurgedOnGroupDelete: deleting a group removes the delivery
// bearers it owns, so a revoked folder's token stops resolving.
func TestRouteTokens_PurgedOnGroupDelete(t *testing.T) {
	s := openEntities(t)
	if err := s.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}
	raw := GenRouteToken()
	if err := s.InsertRouteToken(raw, RouteToken{
		JID:         "web:acme",
		OwnerFolder: "acme",
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.ListRouteTokens("acme"); len(got) != 1 {
		t.Fatalf("seed: want 1 route_token, got %d", len(got))
	}

	if _, _, err := s.DeleteGroup("acme"); err != nil {
		t.Fatal(err)
	}
	if got := s.ListRouteTokens("acme"); len(got) != 0 {
		t.Fatalf("after group delete: want 0 route_tokens, got %d", len(got))
	}
	if _, ok := s.LookupRouteToken(raw); ok {
		t.Fatal("after group delete: token still resolves (the purge did not fire)")
	}
}

// TestFK_HistoryTablesStrandOnGroupDelete verifies that history tables
// (messages, audit_log) are NOT deleted on group removal — per spec 5/8 they
// must strand. This pins the absence of a FK on these tables.
func TestFK_HistoryTablesStrandOnGroupDelete(t *testing.T) {
	s := openEntities(t)
	if err := s.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMessage(core.Message{
		ID: "m1", ChatJID: "acme", Sender: "u", Content: "hi", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := audit.EmitTo(t.Context(), s.Obj(), audit.Event{
		Category: "mutation", Action: "group.test", Actor: "system",
		Folder: "acme", Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DeleteGroup("acme"); err != nil {
		t.Fatal(err)
	}
	// History strands by spec.
	msgs, err := s.MessagesSince("acme", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	n := len(msgs)
	if n != 1 {
		t.Fatalf("messages stranded count: want 1, got %d", n)
	}
	stranded := 0
	for _, r := range auditRowsFor(t, s, "group.test") {
		if r.Folder == "acme" {
			stranded++
		}
	}
	if stranded != 1 {
		t.Fatalf("stranded audit rows: want 1, got %d", stranded)
	}
}

// TestHotTier_PurgedOnGroupDelete pins the four verdicts the hot tier's move
// changed, and the one it did not.
//
// Three of them are a straight purge: a session resumes the last tenant's agent
// transcript, a reply state hands it a live engagement window, and a queued
// system event arrives in its first prompt. `system_messages` is the one whose
// verdict CHANGED — its blocker was "the column is `folder` in routd.db and
// `group_id` in messages.db", and an item has no column (BUGS L253's shape).
//
// `chats` is the fourth and it is the one that is CLEARED rather than deleted:
// the chat outlives the folder because it carries its own dispatch cursor, so
// what goes is the binding and not the item. A purge that deleted it would throw
// the cursor away and the next inbound would re-feed the chat's whole history.
func TestHotTier_PurgedOnGroupDelete(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	if err := s.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}
	// A sibling sharing the leading characters: a green result must mean the
	// subtree went, not the table.
	if err := s.PutGroup(core.Group{Folder: "acmeworks"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSession("acme/inbox", "#deploy", "sess-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSession("acmeworks", "", "sess-2"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastReply("web:acme", "", "m-1", "acme"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastReply("web:other", "", "m-2", "acmeworks"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueSystemMessage("acme/inbox", "system", "note", "queued"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueSystemMessage("acmeworks", "system", "note", "kept"); err != nil {
		t.Fatal(err)
	}
	const jid = "slack:T/C/A"
	if err := s.SetStickyGroup(jid, "acme/inbox"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStickyTopic(jid, "#deploy"); err != nil {
		t.Fatal(err)
	}
	cursor := core.NowTS()
	if err := s.SetAgentCursor(jid, MsgStamp(cursor, "m1")); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.DeleteGroup("acme"); err != nil {
		t.Fatal(err)
	}

	if id, ok := s.GetSession("acme/inbox", "#deploy"); ok {
		t.Errorf("the deleted subtree's session survived (%q): the next tenant resumes the "+
			"last one's transcript", id)
	}
	if got := s.LastReplyID("web:acme", ""); got != "" {
		t.Errorf("the deleted folder's reply state survived: %q", got)
	}
	queued, err := s.QueuedSystemMessages(ctx, "acme/inbox")
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Errorf("%d queued system events survived the delete; they arrive in the next "+
			"tenant's first prompt", len(queued))
	}

	group, topic, err := s.StickyPins(jid)
	if err != nil {
		t.Fatal(err)
	}
	if group != "" || topic != "" {
		t.Errorf("the chat still points at the deleted folder: (%q, %q)", group, topic)
	}
	want := MsgStamp(cursor, "m1")
	if got, _ := s.AgentCursor(jid); got != want {
		t.Errorf("the chat's dispatch cursor = %q, want %q — the purge deleted the item "+
			"instead of clearing the binding, and the chat re-feeds its whole history",
			got, want)
	}

	// The sibling is untouched, or the purge widened onto the whole table.
	if _, ok := s.GetSession("acmeworks", ""); !ok {
		t.Error("the purge widened onto the sibling folder's session")
	}
	if got := s.LastReplyID("web:other", ""); got != "m-2" {
		t.Errorf("the purge widened onto the sibling folder's reply state: %q", got)
	}
	kept, err := s.QueuedSystemMessages(ctx, "acmeworks")
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Errorf("the purge widened onto the sibling folder's queue: %d events left", len(kept))
	}
}

// Deleting a folder empties the feed partition of every folder UNDER it, not
// only the named one. The subtree is read off `groups`, and the purge deletes
// those very rows, so a read taken after it names nothing and each child's
// partition survives for the next tenant of that path to inherit.
func TestMessageFolders_ChildPartitionPurgedOnGroupDelete(t *testing.T) {
	s := openEntities(t)
	for _, f := range []string{"acme", "acme/support"} {
		if err := s.PutGroup(core.Group{Folder: f}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	for _, f := range []string{"acme", "acme/support"} {
		if err := s.WriteEntities(ctx, nil, objstore.Write{
			Op: objstore.Put, Entity: MessageFolders,
			Key:   objstore.Key{Partition: f, Sort: "2026-09-12T00:00:00.000000000Z\x1fm1"},
			Attrs: map[string]string{"chat_jid": "telegram:1"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.DeleteGroup("acme"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	for _, f := range []string{"acme", "acme/support"} {
		items, err := s.Obj().Query(ctx, objstore.Query{Entity: MessageFolders, Partition: f})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 0 {
			t.Errorf("%s kept %d feed items after its tree was deleted", f, len(items))
		}
	}
}
