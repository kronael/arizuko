package routd

// Tests for the spec 5/G engagement READ surface: the deadline crossing the
// wire, and the list that lets a viewer find engaged chats instead of only
// looking one up. Before this, EngagementResponse never returned engaged_until
// — the one number a viewer exists to show — and no layer had a list query at
// all (BUGS F12a).

import (
	"encoding/json"
	"net/http"
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
