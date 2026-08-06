package routd

// Tests for the spec 5/G engagement surface.
//
// The READ half: the deadline crossing the wire, and the list that lets a viewer
// find engaged chats instead of only looking one up. Before this,
// EngagementResponse never returned engaged_until — the one number a viewer
// exists to show — and no layer had a list query at all (BUGS F12a).
//
// The WRITE half (bottom of the file): the audit row the mutation leaves, and
// containment on the CLAIMING folder so the write face cannot reach a window the
// read face would not show.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// getEngagement GETs url through h and decodes a 200 body into out. Rides the
// package's existing getJSON recorder helper (scopes_test.go).
func getEngagement(t *testing.T, h http.Handler, url string, out any) int {
	t.Helper()
	rec := getJSON(t, h, url)
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("GET %s: decode %q: %v", url, rec.Body.String(), err)
		}
	}
	return rec.Code
}

// TestEngagementGet_ReturnsDeadline pins engaged_until onto the single-pair
// read. A live window reports its deadline; an idle pair reports an empty one
// but still carries the thread anchor, which outlives the window.
func TestEngagementGet_ReturnsDeadline(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "svc", scope: []string{"routes:read"}, folder: ""})

	if err := db.SetEngagement("slack:c/live", "", "alice", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := db.SetLastReply("slack:c/idle", "", "m-7", "alice"); err != nil {
		t.Fatal(err)
	}

	var live apiv1.EngagementResponse
	if code := getEngagement(t, h, "/v1/engagement?jid=slack:c/live", &live); code != 200 {
		t.Fatalf("live: status %d", code)
	}
	if live.EngagedUntil == "" {
		t.Fatal("live window returned no engaged_until — the deadline still does not cross the wire")
	}
	until, err := time.Parse(time.RFC3339Nano, live.EngagedUntil)
	if err != nil {
		t.Fatalf("engaged_until %q is not RFC3339Nano: %v", live.EngagedUntil, err)
	}
	if !until.After(time.Now()) {
		t.Errorf("engaged_until %v is not in the future", until)
	}
	if live.Folder != "alice" {
		t.Errorf("folder = %q, want alice", live.Folder)
	}

	var idle apiv1.EngagementResponse
	if code := getEngagement(t, h, "/v1/engagement?jid=slack:c/idle", &idle); code != 200 {
		t.Fatalf("idle: status %d", code)
	}
	if idle.EngagedUntil != "" {
		t.Errorf("idle pair reports engaged_until %q, want empty", idle.EngagedUntil)
	}
	if idle.LastReplyID != "m-7" {
		t.Errorf("idle last_reply_id = %q, want m-7 — the anchor outlives the window", idle.LastReplyID)
	}
}

// TestEngagementList_EnumeratesLiveWindows: GET /v1/engagement with no jid
// answers "who is engaged right now", which no query at any layer could answer
// before. An expired window is not live and must not appear.
func TestEngagementList_EnumeratesLiveWindows(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "svc", scope: []string{"routes:read"}, folder: ""})

	if err := db.SetEngagement("slack:c/one", "", "alice", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := db.SetEngagement("slack:c/two", "t-9", "bob", 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	// Expired: SetEngagement with a past deadline is exactly the disengage path.
	if err := db.SetEngagement("slack:c/gone", "", "alice", -time.Hour); err != nil {
		t.Fatal(err)
	}

	var got apiv1.EngagementListResponse
	if code := getEngagement(t, h, "/v1/engagement", &got); code != 200 {
		t.Fatalf("list: status %d", code)
	}
	if len(got.Engaged) != 2 {
		t.Fatalf("list returned %d windows, want 2 (the expired one must be excluded): %+v", len(got.Engaged), got.Engaged)
	}
	// Newest deadline first.
	if got.Engaged[0].JID != "slack:c/two" {
		t.Errorf("first = %q, want slack:c/two (newest deadline first)", got.Engaged[0].JID)
	}
	if got.Engaged[0].Topic != "t-9" {
		t.Errorf("topic = %q, want t-9 — a (jid, topic) PAIR is the engaged unit", got.Engaged[0].Topic)
	}
	if got.Engaged[0].Folder != "bob" || got.Engaged[1].Folder != "alice" {
		t.Errorf("folders = %q/%q, want bob/alice", got.Engaged[0].Folder, got.Engaged[1].Folder)
	}
	for _, e := range got.Engaged {
		if e.EngagedUntil == "" {
			t.Errorf("%s: listed with no engaged_until", e.JID)
		}
	}
	for _, e := range got.Engaged {
		if e.JID == "slack:c/gone" {
			t.Error("expired window slack:c/gone is listed as live")
		}
	}
}

// TestEngagementList_NoCrossFolderLeak is the content-level guard. A TOP-LEVEL
// tenant is the dangerous case: its folder claim is non-empty but shallow, so
// any widening keyed on depth rather than on an EMPTY claim hands it every
// sibling's chats — the leak the 5/16 REST folds had to close. The assertion is
// on the raw response BODY, not on a decoded count, so a jid reaching the
// caller through any field still fails.
func TestEngagementList_NoCrossFolderLeak(t *testing.T) {
	seed := func(db *DB) {
		for _, e := range []struct{ jid, folder string }{
			{"slack:c/alice-1", "alice"},
			{"slack:c/alice-2", "alice/support"},
			{"slack:c/bob-1", "bob"},
			{"slack:c/bob-2", "bob/support"},
			{"slack:c/unclaimed", ""},
		} {
			if err := db.SetEngagement(e.jid, "", e.folder, time.Hour); err != nil {
				t.Fatal(err)
			}
		}
	}

	db, h := authSrv(t, fakeVerifier{sub: "user:a", scope: []string{"routes:read:own_group"}, folder: "alice"})
	seed(db)

	rec := getJSON(t, h, "/v1/engagement")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, leaked := range []string{"slack:c/bob-1", "slack:c/bob-2", `"bob"`} {
		if strings.Contains(body, leaked) {
			t.Errorf("tenant alice's engagement list contains %s — cross-folder leak.\nbody: %s", leaked, body)
		}
	}
	// An unclaimed row (empty engaged_folder) must not be visible either:
	// containment is descendant(), not ownsFolder, which counts an empty target
	// as owned by everyone.
	if strings.Contains(body, "slack:c/unclaimed") {
		t.Errorf("tenant alice sees the unclaimed window — containment used the empty-target-is-owned rule.\nbody: %s", body)
	}

	var got apiv1.EngagementListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Engaged) != 2 {
		t.Fatalf("alice sees %d windows, want its own 2: %+v", len(got.Engaged), got.Engaged)
	}
	// Non-vacuity: the rows really are in the table, so an empty result would
	// have passed the leak assertions for the wrong reason.
	rootDB, rootH := authSrv(t, fakeVerifier{sub: "svc", scope: []string{"routes:read"}, folder: ""})
	seed(rootDB)
	var all apiv1.EngagementListResponse
	if code := getEngagement(t, rootH, "/v1/engagement", &all); code != 200 {
		t.Fatalf("root list: status %d", code)
	}
	if len(all.Engaged) != 5 {
		t.Fatalf("root sees %d windows, want all 5 — the leak test would otherwise pass vacuously: %+v",
			len(all.Engaged), all.Engaged)
	}
}

// TestDefaultEngagementTTL pins the number three operator doc pages cite
// (reference/env.html, concepts/engagement.html, routd/README.md). It had
// drifted to 20m on all three because a dead core.Config field carried that
// value while routd ran 30m (BUGS F12); the literal below is written out
// rather than compared against the constant, so bumping the constant fails
// here and the docs get revisited.
func TestDefaultEngagementTTL(t *testing.T) {
	if DefaultEngagementTTL != 30*time.Minute {
		t.Errorf("DefaultEngagementTTL = %v, want 30m — update the env.html/engagement.html/README defaults too",
			DefaultEngagementTTL)
	}

	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// A zero TTL means "unset" and must fall to the default...
	if got := NewServer(db, nil, &recDeliverer{}, nil, 0, "").engagementT; got != 30*time.Minute {
		t.Errorf("NewServer(ttl=0).engagementT = %v, want 30m", got)
	}
	// ...but an explicit ENGAGEMENT_TTL must survive, or the env var is dead.
	if got := NewServer(db, nil, &recDeliverer{}, nil, 90*time.Second, "").engagementT; got != 90*time.Second {
		t.Errorf("NewServer(ttl=90s).engagementT = %v, want 90s — explicit ENGAGEMENT_TTL ignored", got)
	}
}

// ---- the WRITE face: POST /v1/engagement (spec 5/G item 6) ----

// postEngagement POSTs body to /v1/engagement through h and returns the code.
func postEngagement(t *testing.T, h http.Handler, body apiv1.EngagementRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/engagement", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// engagementAudit reads the audit_log rows this surface writes, in order.
// Distinct from auditRows (migrations_0016_audit_test.go), which counts one
// action: the assertions below need the row CONTENTS, not just how many.
func engagementAudit(t *testing.T, db *DB) []struct{ Action, Actor, Resource, Folder, Params string } {
	t.Helper()
	rows, err := db.SQL().Query(
		`SELECT action, actor, COALESCE(resource,''), COALESCE(folder,''), COALESCE(params_summary,'')
		 FROM audit_log WHERE action LIKE 'engagement.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []struct{ Action, Actor, Resource, Folder, Params string }
	for rows.Next() {
		var r struct{ Action, Actor, Resource, Folder, Params string }
		if err := rows.Scan(&r.Action, &r.Actor, &r.Resource, &r.Folder, &r.Params); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestEngagementSet_AuditsTheMutation. An operator ending someone's
// conversation early appends no message and runs no turn, so this row is the
// only durable trace that it happened — and it had none at all before
// (DB.SetEngagement was a bare Exec).
//
// The row is COUNTED before its contents are read. audit.EmitInTx writes through
// the caller's tx and so does not depend on audit.Init, but a contents-only
// assertion would still pass vacuously against an empty table if the emit were
// dropped, which is exactly the mutation this test has to catch.
func TestEngagementSet_AuditsTheMutation(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"routes:write"}, folder: ""})

	if rec := postEngagement(t, h, apiv1.EngagementRequest{
		JID: "slack:c/one", Topic: "t-9", Folder: "corp/eng", TTLSeconds: 600,
	}); rec.Code != 200 {
		t.Fatalf("engage: status %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postEngagement(t, h, apiv1.EngagementRequest{
		JID: "slack:c/one", Topic: "t-9", Folder: "corp/eng", TTLSeconds: 0,
	}); rec.Code != 200 {
		t.Fatalf("disengage: status %d: %s", rec.Code, rec.Body.String())
	}

	got := engagementAudit(t, db)
	if len(got) != 2 {
		t.Fatalf("audit_log holds %d engagement rows, want 2 (one per write) — %+v", len(got), got)
	}
	if got[0].Action != "engagement.set" {
		t.Errorf("engage wrote action %q, want engagement.set", got[0].Action)
	}
	// The disengage is the row an operator greps for; a shared action name for
	// both writes would make "who ended it" unanswerable.
	if got[1].Action != "engagement.clear" {
		t.Errorf("disengage wrote action %q, want engagement.clear — the two writes are "+
			"indistinguishable in the trail", got[1].Action)
	}
	for i, r := range got {
		if r.Actor != "service:dashd" {
			t.Errorf("row %d actor = %q, want service:dashd — the row cannot answer WHO", i, r.Actor)
		}
		if r.Resource != "engagement/slack:c/one/t-9" {
			t.Errorf("row %d resource = %q, want engagement/slack:c/one/t-9", i, r.Resource)
		}
		if r.Folder != "corp/eng" {
			t.Errorf("row %d folder = %q, want corp/eng", i, r.Folder)
		}
		if !strings.Contains(r.Params, "slack:c/one") || !strings.Contains(r.Params, "t-9") {
			t.Errorf("row %d params = %q, want the (jid, topic) pair", i, r.Params)
		}
	}

	// The window really is gone — otherwise the rows above would describe a
	// mutation that never landed.
	if _, live := db.Engagement("slack:c/one", "t-9"); live {
		t.Error("the window is still live after ttl_seconds=0")
	}
}

// TestEngagementSet_AuditRowRidesTheWriteTransaction: a REJECTED write leaves
// NEITHER the row nor the mutation. Pins that the audit row is bound to the
// mutation rather than emitted beside it — an audit_log full of rows for writes
// that 403'd is worse than none.
func TestEngagementSet_AuditRowRidesTheWriteTransaction(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:a", scope: []string{"routes:write:own_group"}, folder: "alice"})
	if err := db.SetEngagement("web:bob/chat", "", "bob", time.Hour); err != nil {
		t.Fatal(err)
	}

	if rec := postEngagement(t, h, apiv1.EngagementRequest{
		JID: "web:bob/chat", Folder: "bob", TTLSeconds: 0,
	}); rec.Code != 403 {
		t.Fatalf("cross-folder write = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if got := engagementAudit(t, db); len(got) != 0 {
		t.Errorf("a denied write left %d audit rows: %+v", len(got), got)
	}
	if _, live := db.Engagement("web:bob/chat", ""); !live {
		t.Error("a denied write cleared the window anyway")
	}
}

// TestEngagementSet_NoCrossFolderWrite is the write twin of
// TestEngagementList_NoCrossFolderLeak, and guards the asymmetry the read alone
// left open.
//
// The request's `folder` is ATTACKER-CONTROLLED, and that is the whole trap.
// ownsJID resolves the jid's ROUTE TARGET; ownsFolder bounds only the folder the
// caller ASKS to write. Neither looks at who currently HOLDS the window — so
// alice, naming her own folder, passes both while clearing a window claimed by
// bob that GET /v1/engagement would never have shown her. The containment must
// therefore key on the LIVE window's owner, the same predicate ListEngaged
// applies per row.
//
// Every request below names `folder: "alice"`, the honest-looking value: sending
// "bob" would be stopped by the pre-existing ownsFolder check and would prove
// nothing about this one. The fixture holds two folders and both directions are
// asserted, so it cannot pass for want of anything to reach.
func TestEngagementSet_NoCrossFolderWrite(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:a", scope: []string{"routes:write:own_group"}, folder: "alice"})

	// Both chats live under alice by route (web:<folder> is the 1:1 binding
	// ownsJID falls back to), so ownsJID passes for BOTH — the route target is
	// not the containment being tested. Only the CLAIMING folder differs.
	if err := db.SetEngagement("web:alice/own", "", "alice", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := db.SetEngagement("web:alice/bobs", "", "bob", time.Hour); err != nil {
		t.Fatal(err)
	}

	if rec := postEngagement(t, h, apiv1.EngagementRequest{
		JID: "web:alice/bobs", Folder: "alice", TTLSeconds: 0,
	}); rec.Code != 403 {
		t.Errorf("alice cleared a window claimed by bob by naming her own folder: status %d %s",
			rec.Code, rec.Body.String())
	}
	if f, live := db.Engagement("web:alice/bobs", ""); !live || f.Folder != "bob" {
		t.Errorf("bob's window = %q,%v after alice's write; want bob,true", f.Folder, live)
	}

	// Non-vacuity: the IDENTICAL call shape on alice's OWN window succeeds, so
	// the 403 above is the claiming-folder check and not a handler that refuses
	// every write.
	if rec := postEngagement(t, h, apiv1.EngagementRequest{
		JID: "web:alice/own", Folder: "alice", TTLSeconds: 0,
	}); rec.Code != 200 {
		t.Fatalf("alice could not clear her own window: %d %s", rec.Code, rec.Body.String())
	}
	if _, live := db.Engagement("web:alice/own", ""); live {
		t.Error("alice's own window survived her own disengage")
	}

	// The operator path stays open: an EMPTY folder claim is the root/service
	// token, which is what /dash/engagement/ presents. If this 403'd, the
	// dashboard control would be dead.
	_, opH := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"routes:write"}, folder: ""})
	if rec := postEngagement(t, opH, apiv1.EngagementRequest{
		JID: "web:alice/bobs", Folder: "bob", TTLSeconds: 0,
	}); rec.Code != 200 {
		t.Errorf("the operator token cannot disengage: %d %s", rec.Code, rec.Body.String())
	}
}
