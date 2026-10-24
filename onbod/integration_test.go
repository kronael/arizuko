package main

// Integration tests for onbod: exercise the HTTP server end-to-end
// through a real httptest.Server with a FakePlatform standing in for
// gated's /v1/outbound. The existing main_test.go covers handler-level
// logic; these tests wire the whole mux together.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// tokenFromLink pulls the raw bearer out of an outbound "…/pair/<raw>" prompt.
// route_tokens holds only sha256(token), so a test that needs the redeemable
// value must read it where the user does: the sent link.
func tokenFromLink(t *testing.T, body string) string {
	t.Helper()
	_, after, ok := strings.Cut(body, "/pair/")
	if !ok {
		t.Fatalf("no pairing link in outbound body: %q", body)
	}
	tok, _, _ := strings.Cut(after, `"`)
	return strings.TrimSpace(tok)
}

// onbodInst is the pair of databases onbod runs against: X is routd.db
// (user_profiles, groups, acl, acl_membership, routes, route_tokens), Ob is
// onbod.db (onboarding, onboarding_gates, invites, invite_redemptions). Each is
// built by the chain its owner ships and migrates, so a rename on either side
// reaches these tests instead of passing against a single stand-in schema.
type onbodInst struct {
	X   *store.Store
	Ob  *store.Store
	Tmp string
}

func newOnbodInst(t *testing.T) *onbodInst {
	t.Helper()
	rdb, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	t.Cleanup(func() { rdb.Close() })
	ob := dyntest.Store(t)
	if err := prepareOwned(ob); err != nil {
		t.Fatalf("onbod migrate: %v", err)
	}
	return &onbodInst{X: store.NewOver(rdb.Obj()), Ob: store.NewOver(ob), Tmp: t.TempDir()}
}

// newOnbodServer wires the real mux against onbod's two databases and a fake
// gated /v1/outbound endpoint. Returns server + a recorder of outbound calls.
func newOnbodServer(t *testing.T) (*httptest.Server, *onbodInst, *testutils.FakePlatform, config) {
	t.Helper()
	inst := newOnbodInst(t)

	fake := testutils.NewFakePlatform()
	t.Cleanup(fake.Close)
	fake.On("POST /v1/outbound", func(_ testutils.PlatformReq) (int, any) {
		return http.StatusOK, map[string]string{"ok": "1"}
	})

	// Give core.LoadConfig() a sane sandbox so handleCreateWorld's
	// SetupGroup fallback doesn't pollute cwd.
	t.Setenv("DATA_DIR", inst.Tmp)
	t.Setenv("HOST_DATA_DIR", inst.Tmp)
	t.Setenv("ARIZUKO_DEV", "true")

	cfg := config{
		gatedURL:    fake.URL(),
		authBaseURL: "https://example.com",
		greeting:    "Hi!",
		svcToken:    func(context.Context) (string, error) { return "test-svc-token", nil },
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /onboard", func(w http.ResponseWriter, r *http.Request) {
		handleOnboard(w, r, inst.X, inst.Ob, cfg)
	})
	mux.HandleFunc("POST /onboard", func(w http.ResponseWriter, r *http.Request) {
		handleOnboardPost(w, r, inst.X, inst.Ob, cfg)
	})
	mux.HandleFunc("GET /invite/{token}", func(w http.ResponseWriter, r *http.Request) {
		handleInvite(w, r, inst.X, inst.Ob)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, inst, fake, cfg
}

// TestOnboardingFlow walks the full onboarding state machine after the fold
// (spec 5/31-identity-pairing): awaiting_message -> prompt (a PAIRING token, outbound fired) ->
// RedeemPairing writes the edge -> the poll observer admits -> approved + group
// folder seeded on disk + groups + acl + routes rows.
func TestOnboardingFlow(t *testing.T) {
	srv, inst, fake, cfg := newOnbodServer(t)

	jid := "telegram:42"
	seedAdmission(t, inst.Ob, store.ArchiveOnboardingRow{JID: jid, Status: "awaiting_message"})
	seedInbound(t, inst.X, jid, jid)

	// Trigger the poll step: promptUnprompted mints the pairing token, issues a
	// POST to gatedURL and stamps prompted_at on the row.
	promptUnprompted(inst.X, inst.Ob, cfg)

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 outbound to gated, got %d", len(reqs))
	}
	if reqs[0].Path != "/v1/outbound" {
		t.Errorf("outbound path = %q", reqs[0].Path)
	}
	if !strings.Contains(string(reqs[0].Body), jid) {
		t.Errorf("outbound body missing jid: %q", reqs[0].Body)
	}

	// The raw token exists ONLY in the link that just went out — route_tokens
	// keeps sha256(token) — so recover it from the outbound body, exactly as the
	// user's chat client would.
	token := tokenFromLink(t, string(reqs[0].Body))
	if len(token) != 64 {
		t.Fatalf("want 64-char token, got %d", len(token))
	}
	st := inst.X
	pairs := pairingTokensFor(t, inst.X, jid)
	if len(pairs) != 1 {
		t.Fatalf("minted %d pairing rows for %s, want 1", len(pairs), jid)
	}
	// The coordinate IS the verifier, so a table that kept the bearer would
	// address the item by it.
	if pairs[0].Key.Sort == token {
		t.Fatal("token stored in the clear — route_tokens must keep the hash, not the bearer")
	}
	// GET /pair is webd's and is side-effect-free; the redemption POST it fronts
	// is store.RedeemPairing, which is what writes the edge.
	sub := "github:alice"
	if err := inst.X.CreateUserProfile(sub, sub, "Alice"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	if peeked, err := st.PeekPairing(token); err != nil || peeked != jid {
		t.Fatalf("PeekPairing = (%q, %v), want (%q, nil)", peeked, err, jid)
	}
	if paired, err := st.RedeemPairing(token, sub); err != nil || paired != jid {
		t.Fatalf("RedeemPairing = (%q, %v)", paired, err)
	}
	if addedBy := edgeAddedBy(t, inst.X, jid); addedBy != store.PairingAddedBy {
		t.Errorf("edge added_by = %q, want %q — one writer, reachable by unpair",
			addedBy, store.PairingAddedBy)
	}

	// Admission is a poll now, not part of redemption.
	status := admissionStatus(t, inst.Ob, jid)
	if status != "awaiting_message" {
		t.Errorf("redemption advanced admission on its own: status=%q", status)
	}
	observePairings(inst.X, inst.Ob, cfg)

	// Confirm onboarding row now approved and user_sub bound.
	admitted := admission(t, inst.Ob, jid)
	gotSub, gotStatus := admitted.UserSub, admitted.Status
	if gotSub != sub {
		t.Errorf("onboarding.user_sub = %q, want %q", gotSub, sub)
	}
	if gotStatus != "approved" {
		t.Errorf("onboarding.status = %q, want approved (no gates)", gotStatus)
	}

	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	// The pair success page sends the user here; /onboard is where the world
	// choice happens (spec 5/18 step 6).
	reqG, _ := http.NewRequest("GET", srv.URL+"/onboard", nil)
	reqG.Header.Set("X-User-Sub", sub)
	respG, err := c.Do(reqG)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(respG.Body)
	respG.Body.Close()
	if respG.StatusCode != http.StatusOK {
		t.Fatalf("dashboard GET: status=%d body=%s", respG.StatusCode, body)
	}
	var csrf *http.Cookie
	for _, ck := range respG.Cookies() {
		if ck.Name == csrfCookieName {
			csrf = ck
		}
	}
	if csrf == nil {
		t.Fatal("csrf cookie not set by dashboard GET")
	}

	// POST create_world. The authority is a redeemed top-level invite, recorded
	// in invite_redemptions — there is no cookie to bring along (BUGS F50).
	seedRedemption(t, inst.Ob, sub, "/")
	form := url.Values{
		"action":   {"create_world"},
		"username": {"alice"},
		"csrf":     {csrf.Value},
	}
	reqP, _ := http.NewRequest("POST", srv.URL+"/onboard", strings.NewReader(form.Encode()))
	reqP.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqP.Header.Set("X-User-Sub", sub)
	reqP.AddCookie(csrf)
	respP, err := c.Do(reqP)
	if err != nil {
		t.Fatal(err)
	}
	pbody, _ := io.ReadAll(respP.Body)
	respP.Body.Close()
	if respP.StatusCode != http.StatusSeeOther {
		t.Fatalf("create_world: status=%d body=%s", respP.StatusCode, pbody)
	}

	// Group row, user_groups row, and route present.
	if folder := folderName(t, inst.X, "alice"); folder != "alice" {
		t.Errorf("groups row missing, got %q", folder)
	}
	// Subtree scope: an exact-folder grant authorizes the world and nothing
	// under it, so the creator could never reach their own subgroups (W1).
	if ug := firstScope(t, inst.X, sub); ug != "alice/**" {
		t.Errorf("creator grant should be subtree-scoped, got %q", ug)
	}
	// create_world itself writes no route (5/18 step 7) — the route is the act
	// the /onboard landing performs, and it names who did it.
	if routeN := countRoutes(t, inst.X); routeN != 0 {
		t.Errorf("create_world wrote %d route(s) as a side effect; want 0", routeN)
	}

	// Follow the redirect the POST returned; that landing routes the chat.
	reqR, _ := http.NewRequest("GET", srv.URL+"/onboard", nil)
	reqR.Header.Set("X-User-Sub", sub)
	respR, err := c.Do(reqR)
	if err != nil {
		t.Fatal(err)
	}
	rbody, _ := io.ReadAll(respR.Body)
	respR.Body.Close()
	if respR.StatusCode != http.StatusOK {
		t.Fatalf("post-create landing: status=%d body=%s", respR.StatusCode, rbody)
	}
	routeAddedBy, addedVia, ok := routeAttribution(t, inst.X, "room=42")
	if !ok {
		t.Fatalf("expected the linked jid routed after the landing; page: %s", rbody)
	}
	if routeAddedBy != sub || addedVia != routeViaSoleWorld {
		t.Errorf("route attribution = (%q, %q), want (%q, %q)",
			routeAddedBy, addedVia, sub, routeViaSoleWorld)
	}

	// SetupGroup side-effect: group folder + .claude dir exist on disk.
	groupDir := filepath.Join(inst.Tmp, "groups", "alice")
	if st, err := os.Stat(groupDir); err != nil || !st.IsDir() {
		t.Errorf("expected group dir %s on disk: err=%v", groupDir, err)
	}
}

// TestGateRateLimit: with a wildcard gate of 2/day, admitting 3 paired jids
// queues all 3 and admitFromQueue approves only 2, leaving the third queued.
func TestGateRateLimit(t *testing.T) {
	_, inst, _, _ := newOnbodServer(t)

	seedGateRow(t, inst.Ob, "*", 2, true)

	for i, jid := range []string{"telegram:1", "telegram:2", "telegram:3"} {
		sub := "github:u" + string(rune('a'+i))
		seedAdmission(t, inst.Ob, store.ArchiveOnboardingRow{JID: jid, Status: "awaiting_message"})
		admitJID(inst.Ob, config{}, jid, sub)
	}

	queued := countAdmissions(t, inst.Ob, "queued")
	if queued != 3 {
		t.Fatalf("all 3 should be queued before admission, got %d", queued)
	}

	admitFromQueue(inst.Ob)

	approved := countAdmissions(t, inst.Ob, "approved")
	stillQueued := countAdmissions(t, inst.Ob, "queued")
	if approved != 2 {
		t.Errorf("approved = %d, want 2 (rate limit)", approved)
	}
	if stillQueued != 1 {
		t.Errorf("still queued = %d, want 1 (rejected by limit)", stillQueued)
	}

	// Second admission pass same day must not exceed the limit.
	admitFromQueue(inst.Ob)
	approved = countAdmissions(t, inst.Ob, "approved")
	if approved != 2 {
		t.Errorf("after re-admit, approved = %d, want 2 (persisted limit)", approved)
	}
}
