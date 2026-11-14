package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

func tokenFromLink(t *testing.T, body string) string {
	t.Helper()
	_, after, ok := strings.Cut(body, "/pair/")
	if !ok {
		t.Fatalf("no pairing link in outbound body: %q", body)
	}
	tok, _, _ := strings.Cut(after, `"`)
	return strings.TrimSpace(tok)
}

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

func newOnbodServer(t *testing.T) (*httptest.Server, *onbodInst, config) {
	t.Helper()
	inst := newOnbodInst(t)

	t.Setenv("DATA_DIR", inst.Tmp)
	t.Setenv("HOST_DATA_DIR", inst.Tmp)
	t.Setenv("ARIZUKO_DEV", "true")

	cfg := config{
		authBaseURL: "https://example.com",
		greeting:    "Hi!",
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
	return srv, inst, cfg
}

// TestOnboardingFlow walks the full onboarding state machine after the fold
// (spec 5/31-identity-pairing): awaiting_message -> prompt (a PAIRING token, outbound queued) ->
// RedeemPairing writes the edge -> the poll observer admits -> approved + group
// folder seeded on disk + groups + acl + routes rows.
func TestOnboardingFlow(t *testing.T) {
	srv, inst, cfg := newOnbodServer(t)

	jid := "telegram:42"
	seedAdmission(t, inst.Ob, store.ArchiveOnboardingRow{JID: jid, Status: "awaiting_message"})

	promptUnprompted(inst.X, inst.Ob, cfg, awaitingRows(t, inst.Ob))

	queued, err := inst.X.PendingOutbound(t.Context(), time.Now().Add(time.Second), 10)
	if err != nil || len(queued) != 1 || queued[0].ChatJID != jid {
		t.Fatalf("queued greeting: %v %v", queued, err)
	}
	token := tokenFromLink(t, queued[0].Content)
	if len(token) != 64 {
		t.Fatalf("want 64-char token, got %d", len(token))
	}
	st := inst.X
	pairs := pairingTokensFor(t, inst.X, jid)
	if len(pairs) != 1 {
		t.Fatalf("minted %d pairing rows for %s, want 1", len(pairs), jid)
	}

	if pairs[0].Key.Sort == token {
		t.Fatal("token stored in the clear — route_tokens must keep the hash, not the bearer")
	}

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

	status := admissionStatus(t, inst.Ob, jid)
	if status != "awaiting_message" {
		t.Errorf("redemption advanced admission on its own: status=%q", status)
	}
	observePairings(inst.X, inst.Ob, cfg, awaitingRows(t, inst.Ob))

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

	if folder := folderName(t, inst.X, "alice"); folder != "alice" {
		t.Errorf("groups row missing, got %q", folder)
	}

	if ug := firstScope(t, inst.X, sub); ug != "alice/**" {
		t.Errorf("creator grant should be subtree-scoped, got %q", ug)
	}

	if routeN := countRoutes(t, inst.X); routeN != 0 {
		t.Errorf("create_world wrote %d route(s) as a side effect; want 0", routeN)
	}

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

	groupDir := filepath.Join(inst.Tmp, "groups", "alice")
	if st, err := os.Stat(groupDir); err != nil || !st.IsDir() {
		t.Errorf("expected group dir %s on disk: err=%v", groupDir, err)
	}
}

// TestGateRateLimit: with a wildcard gate of 2/day, admitting 3 paired jids
// queues all 3 and admitFromQueue approves only 2, leaving the third queued.
func TestGateRateLimit(t *testing.T) {
	_, inst, _ := newOnbodServer(t)

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

	admitFromQueue(inst.Ob)
	approved = countAdmissions(t, inst.Ob, "approved")
	if approved != 2 {
		t.Errorf("after re-admit, approved = %d, want 2 (persisted limit)", approved)
	}
}
