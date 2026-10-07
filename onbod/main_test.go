package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

func TestMain(m *testing.M) {
	if err := dyntest.Setup(); err != nil {
		fmt.Fprintf(os.Stderr, "dyntest: %v\n", err)
		os.Exit(1)
	}
	os.Setenv("ARIZUKO_DEV", "true")
	os.Setenv("HOST_APP_DIR", "/tmp/test-app")
	code := m.Run()
	dyntest.Shutdown()
	os.Exit(code)
}

// migratedDB returns the two databases onbod runs against, in the order every
// handler takes them: xdb is routd.db (user_profiles, groups, route_tokens, acl),
// obdb is onbod.db (onboarding, onboarding_gates, invites). Each is built by the
// chain its owner ships, so a rename on either side reaches this test.
func migratedDB(t *testing.T) (xdb, obdb *store.Store) {
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
	return store.NewOver(rdb.Obj()), store.NewOver(ob)
}

// testDB fuses onbod's two owners into ONE handle, for the handlers these tests
// drive with a single store. Both halves are built by their owner's own door —
// routd's open for groups/routes/route_tokens/acl/acl_membership and the tables
// beside them, onbod's own prepareOwned for its three entities, its audit_log
// and its invite_redemptions — so a rename on either side reaches here. A
// declaration of its own would build a shape production does not have.
func testDB(t *testing.T) *store.Store {
	t.Helper()
	rdb, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	t.Cleanup(func() { rdb.Close() })
	pool, ok := rdb.Obj().(store.Driver)
	if !ok {
		t.Fatalf("routd's handle is %T, which makes no entities", rdb.Obj())
	}
	if err := prepareOwned(pool); err != nil {
		t.Fatalf("onbod migrate: %v", err)
	}
	return store.NewOver(pool)
}

// fixtureTS is the instant a fixture stamps when it does not care which. It is
// 20 bytes because created/prompted_at/queued_at/admitted_at are DECLARED
// ordered attributes and every driver refuses another width — a short value
// would sort into the wrong place instead of failing (store/onboarding.go).
const fixtureTS = "2026-01-01T00:00:00Z"

// seedAdmission stores one admission. It writes through the owner's own
// importer rather than a fixture of its own, so the item shape onbod reads is
// the one store writes. An unset Created gets fixtureTS: every read that orders
// admissions compares it.
func seedAdmission(t *testing.T, db *store.Store, r store.ArchiveOnboardingRow) {
	t.Helper()
	if r.Created == "" {
		r.Created = fixtureTS
	}
	if _, err := db.ImportOnboarding(t.Context(),
		[]store.ArchiveOnboardingRow{r}); err != nil {
		t.Fatalf("seed admission %s: %v", r.JID, err)
	}
}

// admission reads one admission back. The zero row when there is none, so a
// caller asserting on a status reads "" rather than failing on a missing row.
func admission(t *testing.T, db *store.Store, jid string) store.OnboardingRow {
	t.Helper()
	rows, err := db.ListOnboarding("")
	if err != nil {
		t.Fatalf("read admissions: %v", err)
	}
	for _, r := range rows {
		if r.JID == jid {
			return r
		}
	}
	return store.OnboardingRow{}
}

// admissionStatus is one admission's status, "" when there is none.
func admissionStatus(t *testing.T, db *store.Store, jid string) string {
	t.Helper()
	return admission(t, db, jid).Status
}

// countAdmissions counts the admissions in one status; an empty status counts
// every one.
func countAdmissions(t *testing.T, db *store.Store, status string) int {
	t.Helper()
	rows, err := db.ListOnboarding(status)
	if err != nil {
		t.Fatalf("count admissions: %v", err)
	}
	return len(rows)
}

// seedGateRow stores one gate at a chosen limit and enablement.
func seedGateRow(t *testing.T, db *store.Store, gate string, limit int, enabled bool) {
	t.Helper()
	st := db
	if err := st.PutGate(gate, limit); err != nil {
		t.Fatalf("seed gate %s: %v", gate, err)
	}
	if !enabled {
		if err := st.EnableGate(gate, false); err != nil {
			t.Fatalf("disable gate %s: %v", gate, err)
		}
	}
}

// seedInviteRow stores one invite at a chosen ref, for a fixture that has to
// name the coordinate it will assert on.
func seedInviteRow(t *testing.T, db *store.Store, r store.ArchiveInviteRow) {
	t.Helper()
	if r.IssuedAt == "" {
		r.IssuedAt = fixtureTS
	}
	if _, err := db.ImportInvites(t.Context(),
		[]store.ArchiveInviteRow{r}); err != nil {
		t.Fatalf("seed invite %s: %v", r.Ref, err)
	}
}

// setAdmissionAttr writes one attribute of one admission, for a fixture that
// has to build a state no accessor writes — a status the state machine cannot
// reach, or a marker only a second admission would overwrite.
func setAdmissionAttr(t *testing.T, db *store.Store, jid, attr, value string) {
	t.Helper()
	committed, err := db.Obj().GuardedBatch(t.Context(), nil, []objstore.Write{{
		Op: objstore.Update, Entity: store.Onboarding,
		Key:   objstore.Key{Partition: store.OwnerOnbod, Sort: jid},
		Attrs: map[string]string{attr: value},
	}})
	if err != nil {
		t.Fatalf("set %s %s: %v", jid, attr, err)
	}
	if !committed {
		t.Fatalf("set %s %s: refused, so there is no such admission", jid, attr)
	}
}

// setAdmissionStatus moves one admission to a status no accessor writes, which
// is what a stranded-row fixture needs.
func setAdmissionStatus(t *testing.T, db *store.Store, jid, status string) {
	t.Helper()
	setAdmissionAttr(t, db, jid, "status", status)
}

// allAdmissions is every admission, for a caller that counts by more than one
// attribute at once.
func allAdmissions(t *testing.T, db *store.Store) []store.OnboardingRow {
	t.Helper()
	rows, err := db.ListOnboarding("")
	if err != nil {
		t.Fatalf("read admissions: %v", err)
	}
	return rows
}

// invite reads one invite back by ref.
func invite(t *testing.T, db *store.Store, ref string) store.ArchiveInviteRow {
	t.Helper()
	rows, err := db.ExportInvites(t.Context())
	if err != nil {
		t.Fatalf("read invites: %v", err)
	}
	for _, r := range rows {
		if r.Ref == ref {
			return r
		}
	}
	t.Fatalf("no invite at ref %s", ref)
	return store.ArchiveInviteRow{}
}

// inviteUsed is one invite's redemption counter, by ref.
func inviteUsed(t *testing.T, db *store.Store, ref string) int {
	t.Helper()
	return invite(t, db, ref).UsedCount
}

// The greeting's credential is a PAIRING token in route_tokens, not a second
// onboarding token of onbod's own (spec 5/31-identity-pairing § the three token mechanisms).
// owner_folder is NULL: the greeting goes out before any human — and therefore
// any folder — is known, which is the constraint blocker 2 relaxed.
func TestPromptUnpromptedMintsPairingTokenWithNullOwner(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:1", Status: "awaiting_message"})

	cfg := config{greeting: "Welcome!", authBaseURL: "https://example.com"}
	promptUnprompted(db, db, cfg)

	// A pairing credential is the one a DELIVERY read does not answer, so the
	// two reads together say which kind was minted.
	pairs := pairingTokensFor(t, db, "telegram:1")
	if len(pairs) != 1 {
		t.Fatalf("the greeting minted %d pairing tokens for telegram:1, want 1", len(pairs))
	}
	if n := countRouteTokens(t, db); n != 1 {
		t.Errorf("the greeting left %d credentials in the table, want the 1 pairing token", n)
	}
	if owner, ok := pairs[0].Attrs["owner_folder"]; ok {
		t.Errorf("owner_folder = %q, want absent — no folder exists at greet time", owner)
	}

	if admission(t, db, "telegram:1").PromptedAt == "" {
		t.Error("expected prompted_at to be set")
	}
}

// The minted token is the one the chat receives, and it redeems as a pairing.
// A greeting whose link does not resolve is the silence spec 5/18 opens with.
func TestPromptUnpromptedLinkRedeemsAsAPairing(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:1", Status: "awaiting_message"})

	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Text string }
		json.NewDecoder(r.Body).Decode(&body)
		sent = body.Text
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cfg := config{
		authBaseURL: "https://example.com", gatedURL: srv.URL,
		svcToken: func(context.Context) (string, error) { return "t", nil },
	}
	promptUnprompted(db, db, cfg)

	_, raw, found := strings.Cut(sent, "https://example.com/pair/")
	if !found {
		t.Fatalf("greeting carried no /pair/ link: %q", sent)
	}
	jid, err := db.PeekPairing(strings.TrimSpace(raw))
	if err != nil || jid != "telegram:1" {
		t.Fatalf("the link the chat received does not redeem: PeekPairing = (%q, %v)", jid, err)
	}
}

func TestDashboardShowsUsernamePicker(t *testing.T) {
	db := testDB(t)
	if err := db.CreateUserProfile("github:new", "github:new", "New User"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}

	seedRedemption(t, db, "github:new", "/")

	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:new")
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "create_world") {
		t.Error("expected username picker form")
	}
}

// The rendered form and the handler's CSRF check must be tested against each
// other, not assumed to agree: a browser submits exactly the fields the
// renderer emitted, so this replays those (plus the cookies the GET set).
// postOnboard injects the double-submit pair itself, so no test built on it can
// catch a form that omits the field — which is how BUGS F1 shipped.
func TestUsernamePickerFormSatisfiesCSRFCheck(t *testing.T) {
	db := testDB(t)
	if err := db.CreateUserProfile("github:new", "newbie", "New User"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}

	seedRedemption(t, db, "github:new", "/")

	cfg := config{}
	get := httptest.NewRequest("GET", "/onboard", nil)
	get.Header.Set("X-User-Sub", "github:new")
	gw := httptest.NewRecorder()
	handleOnboard(gw, get, db, db, cfg)

	form := formInputs(gw.Body.String())
	if form.Get("action") != "create_world" {
		t.Fatalf("no create_world form rendered, got fields %v", form)
	}
	if form.Get(auth.CSRFField) == "" {
		t.Fatalf("rendered form carries no %q field, but handleOnboardPost requires one", auth.CSRFField)
	}

	post := httptest.NewRequest("POST", "/onboard", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("X-User-Sub", "github:new")
	for _, c := range gw.Result().Cookies() {
		post.AddCookie(c)
	}
	pw := httptest.NewRecorder()
	handleOnboardPost(pw, post, db, db, cfg)

	if pw.Code == http.StatusForbidden {
		t.Fatalf("submitting the rendered form is rejected: %s", strings.TrimSpace(pw.Body.String()))
	}
	if pw.Code != http.StatusSeeOther {
		t.Fatalf("want 303 from the rendered form, got %d: %s", pw.Code, pw.Body.String())
	}
}

var (
	inputTagRe = regexp.MustCompile(`<input\b[^>]*>`)
	attrRe     = regexp.MustCompile(`([a-zA-Z-]+)="([^"]*)"`)
)

// formInputs collects the name/value pairs of every <input> on a page — what a
// browser would submit from it. Radios and checkboxes count only when checked,
// as a browser submits exactly one of a radio group.
func formInputs(page string) url.Values {
	vals := url.Values{}
	for _, tag := range inputTagRe.FindAllString(page, -1) {
		attrs := map[string]string{}
		for _, m := range attrRe.FindAllStringSubmatch(tag, -1) {
			attrs[m[1]] = m[2]
		}
		switch attrs["type"] {
		case "radio", "checkbox":
			if !strings.Contains(tag, " checked") {
				continue
			}
		}
		if n := attrs["name"]; n != "" {
			vals.Set(n, html.UnescapeString(attrs["value"]))
		}
	}
	return vals
}

// twoWorldUser: alice administers two worlds and has just paired a JID that no
// route names — spec 5/18's step 6, where the choice is hers to make.
func twoWorldUser(t *testing.T) *store.Store {
	t.Helper()
	db := testDB(t)
	if err := db.CreateUserProfile("github:alice", "alice", "Alice"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	seedFolder(t, db, "acme")
	seedFolder(t, db, "beta")
	seedGrant(t, db, "github:alice", "admin", "acme")
	seedGrant(t, db, "github:alice", "admin", "beta")
	seedMembership(t, db, "telegram:42", "github:alice", "")
	return db
}

func getOnboard(db *store.Store, sub string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", sub)
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, db, config{})
	return w
}

// With two worlds there is no defensible auto-pick: the dashboard used to route
// the JID into whichever folder SQLite returned first, silently and without the
// owner ever seeing the other option. Every world must be offered, and nothing
// may be written until the owner submits.
func TestUnroutedJIDOffersEveryAdminWorld(t *testing.T) {
	db := twoWorldUser(t)

	w := getOnboard(db, "github:alice")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 picker, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, folder := range []string{"acme", "beta"} {
		if !strings.Contains(body, `value="`+folder+`"`) {
			t.Errorf("world %q is not offered as a choice", folder)
		}
	}
	if !strings.Contains(body, "telegram:42") {
		t.Error("picker must name the chat being routed")
	}

	n := countRoutes(t, db)
	if n != 0 {
		t.Errorf("auto-picked a world before the owner chose: %d routes written", n)
	}
}

// Same contract as TestUsernamePickerFormSatisfiesCSRFCheck, for the second
// form onbod renders: replay exactly the fields the page emits, with the
// cookies the GET set. postOnboard injects the double-submit pair itself, so
// only a test built from the rendered page can catch a missing csrf field —
// which is how BUGS F1 shipped.
func TestWorldPickerFormSatisfiesCSRFCheck(t *testing.T) {
	db := twoWorldUser(t)

	gw := getOnboard(db, "github:alice")
	form := formInputs(gw.Body.String())
	if form.Get("action") != "add_route" {
		t.Fatalf("no add_route form rendered, got fields %v", form)
	}
	if form.Get(auth.CSRFField) == "" {
		t.Fatalf("rendered form carries no %q field, but handleOnboardPost requires one", auth.CSRFField)
	}
	if form.Get("match") != "room=42" || form.Get("target") == "" {
		t.Fatalf("form does not bind the chat to a world: %v", form)
	}

	post := httptest.NewRequest("POST", "/onboard", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("X-User-Sub", "github:alice")
	for _, c := range gw.Result().Cookies() {
		post.AddCookie(c)
	}
	pw := httptest.NewRecorder()
	handleOnboardPost(pw, post, db, db, config{})

	if pw.Code == http.StatusForbidden {
		t.Fatalf("submitting the rendered form is rejected: %s", strings.TrimSpace(pw.Body.String()))
	}
	if pw.Code != http.StatusSeeOther {
		t.Fatalf("want 303 from the rendered form, got %d: %s", pw.Code, pw.Body.String())
	}

	target := routeTarget(t, db, "room=42")
	if target != form.Get("target") {
		t.Errorf("route target = %q, want the chosen %q", target, form.Get("target"))
	}
}

// One world is not a choice — asking would be a page with a single radio.
func TestSingleWorldRoutesWithoutAsking(t *testing.T) {
	db := twoWorldUser(t)
	deleteFolder(t, db, "beta")

	w := getOnboard(db, "github:alice")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 dashboard, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "add_route") {
		t.Error("asked the owner to choose between one world")
	}
	target := routeTarget(t, db, "room=42")
	if target != "acme" {
		t.Errorf("single world not routed automatically, target=%q", target)
	}
}

// An empty choice set is a terminal page, not a picker with no options and not
// a dashboard that quietly omits the chat: the owner must be told the chat has
// nowhere to go.
func TestNoAdminWorldIsAnExplicitDeadEnd(t *testing.T) {
	db := testDB(t)
	if err := db.CreateUserProfile("github:alice", "alice", "Alice"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	// alice holds a scope (so she is past the invite gate) but administers
	// nothing: her grant names a folder that is not a group.
	seedGrant(t, db, "github:alice", "admin", "ghost")
	seedMembership(t, db, "telegram:42", "github:alice", "")

	w := getOnboard(db, "github:alice")
	body := w.Body.String()
	if strings.Contains(body, "add_route") {
		t.Fatal("rendered an empty picker")
	}
	if !strings.Contains(body, "telegram:42") || !strings.Contains(body, "Nowhere to route") {
		t.Errorf("dead end not explained to the user, got: %s", body)
	}
}

func TestCreateWorldValidUsername(t *testing.T) {
	db := testDB(t)
	if err := db.CreateUserProfile("github:new", "github:new", "New User"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}

	seedRedemption(t, db, "github:new", "/")

	cfg := config{}
	form := url.Values{"action": {"create_world"}, "username": {"alice"}}
	form.Set("csrf", "c")
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:new")
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, db, cfg)

	if w.Code != http.StatusSeeOther {
		t.Errorf("want 303, got %d", w.Code)
	}

	folder := folderName(t, db, "alice")
	if folder != "alice" {
		t.Errorf("group not created")
	}

	ug := firstScope(t, db, "github:new")
	if ug != "alice/**" {
		t.Errorf("creator grant should be subtree-scoped, got %q", ug)
	}

	un, _, err := db.Username("github:new")
	if err != nil {
		t.Fatalf("read the username back: %v", err)
	}
	if un != "alice" {
		t.Errorf("username not updated, got %q", un)
	}
}

func TestCreateWorldInvalidUsername(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	form := url.Values{"action": {"create_world"}, "username": {"A!"}}
	form.Set("csrf", "c")
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:new")
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (error page), got %d", w.Code)
	}
}

func TestCreateWorldDuplicateUsername(t *testing.T) {
	db := testDB(t)
	seedFolder(t, db, "alice")
	cfg := config{}
	form := url.Values{"action": {"create_world"}, "username": {"alice"}}
	form.Set("csrf", "c")
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:new")
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, db, cfg)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (error page), got %d", w.Code)
	}
}

func TestUsernameValidation(t *testing.T) {
	valid := []string{"abc", "alice", "my-world", "a12", "abcdefghijklmnopqrstuvwxyz1234"}
	for _, u := range valid {
		if !usernameRe.MatchString(u) {
			t.Errorf("expected valid: %q", u)
		}
	}
	invalid := []string{"ab", "1abc", "ABC", "a!", "a b", ""}
	for _, u := range invalid {
		if usernameRe.MatchString(u) {
			t.Errorf("expected invalid: %q", u)
		}
	}
}

// admitJID writes NO membership edge — that is the whole point of the fold
// (spec 5/31-identity-pairing): RedeemPairing owns the edge, in routd's transaction, stamped
// 'pairing' so unpair can reach it. A second writer here is what P1b existed to
// remove, so the absence is asserted, not just the admission verdict.
func TestAdmitJIDApprovesAndWritesNoEdge(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:1", Status: "awaiting_message"})

	admitJID(db, config{}, "telegram:1", "github:alice")

	edges := parentCount(t, db, "telegram:1")
	if edges != 0 {
		t.Errorf("admitJID wrote %d acl_membership rows; the edge belongs to RedeemPairing alone", edges)
	}

	row := admission(t, db, "telegram:1")
	if row.Status != "approved" {
		t.Errorf("want approved, got %s", row.Status)
	}
	if row.UserSub != "github:alice" {
		t.Errorf("want github:alice, got %q", row.UserSub)
	}
}

// The observer is the admission trigger after the fold: it finds the edge webd's
// redemption committed and runs admission over it. Only edges stamped
// PairingAddedBy count — a role membership is not a pairing.
func TestObservePairingsAdmitsAPairedJID(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:1", Status: "awaiting_message"})
	seedMembership(t, db, "telegram:1", "github:alice", store.PairingAddedBy)

	observePairings(db, db, config{})

	row := admission(t, db, "telegram:1")
	if row.Status != "approved" || row.UserSub != "github:alice" {
		t.Errorf("observePairings left row at (%s, %s), want (approved, github:alice)",
			row.Status, row.UserSub)
	}
}

// An unpaired row is left exactly where it was: the observer must not invent an
// admission for a JID nobody has claimed.
func TestObservePairingsLeavesUnpairedRowsAlone(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:1", Status: "awaiting_message"})

	observePairings(db, db, config{})

	row := admission(t, db, "telegram:1")
	if row.Status != "awaiting_message" || row.UserSub != "" {
		t.Errorf("unpaired row advanced to (%s, %q)", row.Status, row.UserSub)
	}
}

// Dedup: user_sub is stamped by every admitJID branch, so a second tick over the
// same edge finds nothing to do. Without it the chat gets the outcome message
// once per poll interval.
func TestObservePairingsAdmitsOnlyOnce(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:1", Status: "awaiting_message"})
	seedMembership(t, db, "telegram:1", "github:alice", store.PairingAddedBy)

	observePairings(db, db, config{})
	first := admission(t, db, "telegram:1").AdmittedAt

	// A marker only a second admission would overwrite. It is a real 20-byte
	// instant because admitted_at is a DECLARED ordered attribute.
	const marker = "1999-12-31T23:59:59Z"
	setAdmissionAttr(t, db, "telegram:1", "admitted_at", marker)
	observePairings(db, db, config{})

	second := admission(t, db, "telegram:1").AdmittedAt
	if second != marker {
		t.Errorf("second tick re-admitted the row (admitted_at %q → %q)", first, second)
	}
}

// The observer's `user_sub IS NULL` guard, on the shape that would otherwise
// re-message a chat every tick: awaiting_message with a verdict's user_sub still
// on it. Nothing writes that shape today — RepromptOnboarding clears user_sub
// precisely so it cannot — but the guard is what stands between a partial write
// and a message every pollInterval, forever.
func TestObservePairingsSkipsARowThatAlreadyNamesAUser(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{
		JID: "telegram:1", Status: "awaiting_message", UserSub: "github:old"})
	seedMembership(t, db, "telegram:1", "github:alice", store.PairingAddedBy)

	observePairings(db, db, config{})

	row := admission(t, db, "telegram:1")
	if row.Status != "awaiting_message" || row.UserSub != "github:old" {
		t.Errorf("row already naming a user was re-admitted: (%s, %s)", row.Status, row.UserSub)
	}
}

// Reprompt is the operator's cooldown bypass, and it must leave a row the
// observer can still advance. Clearing the link without clearing the verdict
// re-greets the chat and then strands it — the O1 shape, freshly minted.
func TestRepromptLeavesAnAdmittableRow(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{
		JID: "telegram:1", Status: "approved", UserSub: "github:alice", Gate: "*",
		AdmittedAt: "2026-01-02T00:00:00Z", PromptedAt: "2026-01-02T00:00:00Z"})
	seedMembership(t, db, "telegram:1", "github:alice", store.PairingAddedBy)

	if err := db.RepromptOnboarding("telegram:1"); err != nil {
		t.Fatal(err)
	}
	observePairings(db, db, config{})

	row := admission(t, db, "telegram:1")
	if row.Status != "approved" || row.UserSub != "github:alice" {
		t.Errorf("reprompted row stalled at (%s, %q); the observer could not re-admit it",
			row.Status, row.UserSub)
	}
}

// Only a PAIRING edge admits. acl_membership also carries role membership and
// manifest/CLI-applied rows; a JID that holds one of those has consented to
// nothing, and admitting on it would stamp user_sub with whatever that row's
// parent happens to be — `role:operator`, on the instances that have it.
func TestObservePairingsIgnoresNonPairingEdges(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:1", Status: "awaiting_message"})
	seedMembership(t, db, "telegram:1", "role:operator", "migration-0053")

	observePairings(db, db, config{})

	row := admission(t, db, "telegram:1")
	if row.Status != "awaiting_message" || row.UserSub != "" {
		t.Errorf("a role membership admitted the row: (%s, %q)", row.Status, row.UserSub)
	}
}

// An agent-minted pairing (spec 5/31-identity-pairing's shipped path) has no onboarding row, so
// the observer never sees it. The scan is over `onboarding`, not over edges.
func TestObservePairingsIgnoresAgentMintedPairings(t *testing.T) {
	db := testDB(t)
	seedMembership(t, db, "telegram:9", "github:alice", store.PairingAddedBy)

	observePairings(db, db, config{})

	rows := countAdmissions(t, db, "")
	if rows != 0 {
		t.Errorf("observer created %d onboarding rows; it must only advance existing ones", rows)
	}
}

// Spec 5/18 step 7 inverts what this used to assert. Creating a world routed
// EVERY JID the sub had paired at it — two chats moved when the user asked for
// a world. create_world now writes no route; the /onboard landing it redirects
// to routes the ONE unrouted JID, and the next landing the next.
func TestCreateWorldRoutesNoJIDs(t *testing.T) {
	db := testDB(t)
	if err := db.CreateUserProfile("github:new", "github:new", "New User"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	seedMembership(t, db, "telegram:10", "github:new", "")
	seedMembership(t, db, "discord:20", "github:new", "")

	seedRedemption(t, db, "github:new", "/")

	cfg := config{}
	form := url.Values{"action": {"create_world"}, "username": {"newworld"}}
	form.Set("csrf", "c")
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:new")
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, db, cfg)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d; body: %s", w.Code, w.Body.String())
	}

	var n int
	n = countRoutes(t, db)
	if n != 0 {
		t.Fatalf("create_world wrote %d route(s) as a side effect; want 0", n)
	}

	// One landing, one route — not one per paired chat.
	getOnboard(db, "github:new")
	n = len(routesMatching(t, db, func(r resources.RoutesRow) bool { return r.Target == "newworld" }))
	if n != 1 {
		t.Fatalf("landing routed %d chats; the act concerns one", n)
	}
	// The other chat is not stranded: the next landing reaches it, one act each.
	getOnboard(db, "github:new")
	n = len(routesMatching(t, db, func(r resources.RoutesRow) bool { return r.Target == "newworld" }))
	if n != 2 {
		t.Errorf("second landing left %d routes; the remaining chat is stranded", n)
	}
}

func TestCreateWorldNoLinkedJIDs(t *testing.T) {
	db := testDB(t)
	if err := db.CreateUserProfile("github:lonely", "github:lonely", "Lonely"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}

	seedRedemption(t, db, "github:lonely", "/")

	cfg := config{}
	form := url.Values{"action": {"create_world"}, "username": {"lonely"}}
	form.Set("csrf", "c")
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:lonely")
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, db, cfg)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d; body: %s", w.Code, w.Body.String())
	}

	folder := folderName(t, db, "lonely")
	if folder != "lonely" {
		t.Error("group not created")
	}

	n := len(routesMatching(t, db, func(r resources.RoutesRow) bool { return r.Target == "lonely" }))
	if n != 0 {
		t.Errorf("expected 0 routes, got %d", n)
	}
}

// A caller holding a broad acl grant creates their world through the same
// authority as anyone else — a redeemed invite. The "**" row is deliberately
// present and deliberately inert: F50 removed the `pending_target` cookie that
// used to be the only thing making this pass, and an operator's grant was never
// the authority /onboard read. Operators create worlds through dashd or the CLI.
// The world is the assertion; routes are not written here at all (5/18 step 7).
func TestCreateWorldOperatorAllowed(t *testing.T) {
	db := testDB(t)
	if err := db.CreateUserProfile("github:op", "op", "Op"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	seedGrant(t, db, "github:op", "admin", "**")
	seedMembership(t, db, "telegram:99", "github:op", "")

	seedRedemption(t, db, "github:op", "/")

	cfg := config{}
	form := url.Values{"action": {"create_world"}, "username": {"opworld"}}
	form.Set("csrf", "c")
	req := httptest.NewRequest("POST", "/onboard", bytes.NewReader([]byte(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:op")
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "c"})
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, db, cfg)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d; body: %s", w.Code, w.Body.String())
	}
	folder := folderName(t, db, "opworld")
	if folder != "opworld" {
		t.Errorf("operator's world not created")
	}
	n := countRoutes(t, db)
	if n != 0 {
		t.Errorf("create_world wrote %d route(s) as a side effect; want 0", n)
	}
}

func TestMatchGate(t *testing.T) {
	gates := []gate{
		{kind: "github", param: "org=co", limitPerDay: 10},
		{kind: "google", param: "domain=co.com", limitPerDay: 20},
		{kind: "email", param: "domain=example.com", limitPerDay: 5},
		{kind: "*", param: "", limitPerDay: 50},
	}

	tests := []struct {
		sub  string
		want string // gateKey or "" for nil
	}{
		{"github:alice", "github:org=co"},
		{"google:alice@co.com", "google:domain=co.com"},
		{"google:alice@other.com", "*"},
		{"email:bob@example.com", "email:domain=example.com"},
	}
	for _, tc := range tests {
		g := matchGate(gates, tc.sub)
		var got string
		if g != nil {
			got = gateKey(*g)
		}
		if got != tc.want {
			t.Errorf("matchGate(%q) = %q, want %q", tc.sub, got, tc.want)
		}
	}
}

func TestMatchGateNoWildcard(t *testing.T) {
	gates := []gate{
		{kind: "github", param: "org=co", limitPerDay: 10},
	}
	g := matchGate(gates, "google:alice@co.com")
	if g != nil {
		t.Errorf("expected nil for unmatched sub, got %+v", *g)
	}
}

func TestAdmitJIDWithGatesQueues(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:1", Status: "awaiting_message"})
	seedGateRow(t, db, "github:org=co", 10, true)

	admitJID(db, config{}, "telegram:1", "github:alice")

	row := admission(t, db, "telegram:1")
	status, gateCol, queuedAt := row.Status, row.Gate, row.QueuedAt
	if status != "queued" {
		t.Errorf("want status=queued, got %s", status)
	}
	if gateCol != "github:org=co" {
		t.Errorf("want gate=github:org=co, got %s", gateCol)
	}
	if queuedAt == "" {
		t.Error("want queued_at set")
	}
}

// No gate matches: terminal `refused`, not the old silent stall at token_used.
// user_sub is stamped so the row stops matching the observer's scan and the
// chat is told exactly once.
func TestAdmitJIDWithGatesNoMatchIsRefusedTerminally(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:1", Status: "awaiting_message"})
	seedGateRow(t, db, "github:org=co", 10, true)

	admitJID(db, config{}, "telegram:1", "google:alice@other.com")

	row := admission(t, db, "telegram:1")
	status, sub := row.Status, row.UserSub
	if status != "refused" {
		t.Errorf("want status=refused, got %s", status)
	}
	if sub != "google:alice@other.com" {
		t.Errorf("want user_sub stamped so the refusal is sent once, got %q", sub)
	}
}

func TestAdmitFromQueue(t *testing.T) {
	db := testDB(t)
	now := "2026-04-17T10:00:00Z"
	// 3 queued users under the same gate, limit=2
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:1", Status: "queued",
		Gate: "github:org=co", QueuedAt: now, UserSub: "github:a"})
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:2", Status: "queued",
		Gate: "github:org=co", QueuedAt: "2026-04-17T10:01:00Z", UserSub: "github:b"})
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:3", Status: "queued",
		Gate: "github:org=co", QueuedAt: "2026-04-17T10:02:00Z", UserSub: "github:c"})

	seedGateRow(t, db, "github:org=co", 2, true)
	admitFromQueue(db)

	// First 2 should be approved, third still queued
	s1 := admissionStatus(t, db, "t:1")
	s2 := admissionStatus(t, db, "t:2")
	s3 := admissionStatus(t, db, "t:3")
	if s1 != "approved" {
		t.Errorf("t:1 want approved, got %s", s1)
	}
	if s2 != "approved" {
		t.Errorf("t:2 want approved, got %s", s2)
	}
	if s3 != "queued" {
		t.Errorf("t:3 want queued, got %s", s3)
	}
}

func TestAdmitFromQueueRespectsDaily(t *testing.T) {
	db := testDB(t)
	// Use the real clock's "today" — admitFromQueue uses time.Now() to scope
	// the per-day counter, so hardcoding a calendar date regresses the moment
	// the suite runs past that day.
	today := time.Now().UTC().Format("2006-01-02")
	// 1 already admitted today (counted by admitted_at)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:0", Status: "approved",
		Gate: "github:org=co", QueuedAt: today + "T08:00:00Z",
		AdmittedAt: today + "T08:00:00Z", UserSub: "github:z"})
	// 1 queued
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:1", Status: "queued",
		Gate: "github:org=co", QueuedAt: today + "T10:00:00Z", UserSub: "github:a"})

	seedGateRow(t, db, "github:org=co", 1, true)
	admitFromQueue(db)

	// Daily limit already hit, t:1 stays queued
	s := admissionStatus(t, db, "t:1")
	if s != "queued" {
		t.Errorf("t:1 want queued (daily limit hit), got %s", s)
	}
}

// TestAdmitFromQueueCapHoldsAcrossDays reproduces the cross-day backlog bug:
// rows queued yesterday, admitted today, must count toward today's quota. Under
// the old queued_at-counting code the second tick saw 0 admissions "today"
// (the backlog's queued_at is yesterday) and drained the whole backlog past the
// cap. Counting by admitted_at keeps the cap intact across poll ticks.
func TestAdmitFromQueueCapHoldsAcrossDays(t *testing.T) {
	db := testDB(t)
	yesterday := time.Now().Add(-24 * time.Hour).UTC().Format("2006-01-02")
	// 3 users queued yesterday, all under the same gate, limit=2.
	for i, jid := range []string{"t:1", "t:2", "t:3"} {
		seedAdmission(t, db, store.ArchiveOnboardingRow{JID: jid, Status: "queued",
			Gate:     "github:org=co",
			QueuedAt: yesterday + "T1" + strconv.Itoa(i) + ":00:00Z",
			UserSub:  "github:" + jid})
	}
	seedGateRow(t, db, "github:org=co", 2, true)

	// Two ticks in the same run; the second must not exceed today's cap.
	admitFromQueue(db)
	admitFromQueue(db)

	approved := 0
	stamped := 0
	// admitted rows must carry today's admitted_at so the count is on the right day.
	today := time.Now().UTC().Format("2006-01-02")
	for _, r := range allAdmissions(t, db) {
		if r.Status != "approved" {
			continue
		}
		if r.Gate == "github:org=co" {
			approved++
		}
		if strings.HasPrefix(r.AdmittedAt, today) {
			stamped++
		}
	}
	if approved != 2 {
		t.Errorf("want 2 admitted (daily cap), got %d — cap leaks across days", approved)
	}
	if stamped != 2 {
		t.Errorf("want 2 rows stamped admitted_at=today, got %d", stamped)
	}
}

func TestAdmitFromQueueNoGates(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:1", Status: "queued",
		Gate: "github:org=co", QueuedAt: "2026-04-17T10:00:00Z", UserSub: "github:a"})
	// No gates in DB → noop
	admitFromQueue(db)

	s := admissionStatus(t, db, "t:1")
	if s != "queued" {
		t.Errorf("want queued (no gates = noop), got %s", s)
	}
}

func TestQueuePositionRendering(t *testing.T) {
	db := testDB(t)
	// 2 users queued, test shows position for second one
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:1", Status: "queued",
		Gate: "*", QueuedAt: "2026-04-17T10:00:00Z", UserSub: "github:first"})
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:2", Status: "queued",
		Gate: "*", QueuedAt: "2026-04-17T10:01:00Z", UserSub: "github:second"})
	if err := db.CreateUserProfile("github:second", "second", "Second"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}

	seedGateRow(t, db, "*", 100, true)

	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:second")
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, db, cfg)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "#2") {
		t.Errorf("expected position #2 in body, got: %s", body)
	}
	if !strings.Contains(body, "queue") {
		t.Errorf("expected 'queue' in body")
	}
}

func TestNoGatesLegacyBehavior(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:1", Status: "awaiting_message"})

	// No gates → admitJID should set approved directly
	admitJID(db, config{}, "telegram:1", "github:alice")

	status := admissionStatus(t, db, "telegram:1")
	if status != "approved" {
		t.Errorf("want approved (legacy), got %s", status)
	}
}

// --- Invite tests ---

// createInvite is a test helper that mints an invite via the new store API,
// returning the raw bearer (I1: the DB stores only its hash).
func createInvite(t *testing.T, db *store.Store, targetGlob, issuedBy string, maxUses int) string {
	t.Helper()
	_, token, err := db.CreateInvite(targetGlob, issuedBy, maxUses, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	return token
}

func TestInviteCreation(t *testing.T) {
	db := testDB(t)
	token := createInvite(t, db, "alice", "telegram:1", 3)
	if len(token) != 64 {
		t.Errorf("expected 64-char hex token, got %d", len(token))
	}

	inv := invite(t, db, store.TokenRef(token))
	glob, issuedBy := inv.TargetGlob, inv.IssuedBySub
	maxUses, used := inv.MaxUses, inv.UsedCount
	if glob != "alice" {
		t.Errorf("want target_glob=alice, got %q", glob)
	}
	if issuedBy != "telegram:1" {
		t.Errorf("want issued_by_sub=telegram:1, got %q", issuedBy)
	}
	if maxUses != 3 {
		t.Errorf("want max_uses=3, got %d", maxUses)
	}
	if used != 0 {
		t.Errorf("want used_count=0, got %d", used)
	}
}

func TestInviteConsume(t *testing.T) {
	db := testDB(t)
	if err := db.CreateUserProfile("github:bob", "bob", "Bob"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	seedFolder(t, db, "alice")
	seedMembership(t, db, "telegram:99", "github:bob", "")

	token := createInvite(t, db, "alice", "telegram:1", 1)

	req := httptest.NewRequest("GET", "/invite/"+token, nil)
	req.SetPathValue("token", token)
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db)

	if w.Code != http.StatusSeeOther {
		t.Errorf("want 303, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/onboard" {
		t.Errorf("want redirect to /onboard, got %s", loc)
	}

	folder := firstScope(t, db, "github:bob")
	if folder != "alice" {
		t.Errorf("want user_groups folder=alice, got %q", folder)
	}

	// Redemption is a grant and nothing else (5/18 step 7). It used to route
	// every paired JID at the target; the /onboard it redirects to routes the
	// one unrouted chat, attributed.
	n := countRoutes(t, db)
	if n != 0 {
		t.Errorf("redemption wrote %d route(s) as a side effect; want 0", n)
	}

	used := inviteUsed(t, db, store.TokenRef(token))
	if used != 1 {
		t.Errorf("want used_count=1, got %d", used)
	}
}

func TestInviteExpired(t *testing.T) {
	db := testDB(t)
	past := "2020-01-01T00:00:00Z"
	seedInviteRow(t, db, store.ArchiveInviteRow{Ref: store.TokenRef("expired-tok"),
		TargetGlob: "alice", IssuedBySub: "telegram:1", MaxUses: 1, ExpiresAt: past})

	req := httptest.NewRequest("GET", "/invite/expired-tok", nil)
	req.SetPathValue("token", "expired-tok")
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (error page), got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "expired") && !strings.Contains(body, "Expired") {
		t.Errorf("expected expired message in body")
	}
}

func TestInviteMaxUses(t *testing.T) {
	db := testDB(t)
	seedInviteRow(t, db, store.ArchiveInviteRow{Ref: store.TokenRef("used-tok"),
		TargetGlob: "alice", IssuedBySub: "telegram:1", MaxUses: 1, UsedCount: 1})

	req := httptest.NewRequest("GET", "/invite/used-tok", nil)
	req.SetPathValue("token", "used-tok")
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (error page), got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "used") && !strings.Contains(body, "Used") {
		t.Errorf("expected max-uses message in body")
	}
}

func TestInviteAuthRequired(t *testing.T) {
	db := testDB(t)
	seedInviteRow(t, db, store.ArchiveInviteRow{Ref: store.TokenRef("auth-tok"),
		TargetGlob: "alice", IssuedBySub: "telegram:1", MaxUses: 1})

	req := httptest.NewRequest("GET", "/invite/auth-tok", nil)
	req.SetPathValue("token", "auth-tok")
	// No X-User-Sub header: the gate runs before the invite is even looked up,
	// so a redeemable token is still refused rather than spent.
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Errorf("onbod bounced the caller to %s; proxyd owns the login redirect", loc)
	}
}

func TestInviteInvalidToken(t *testing.T) {
	db := testDB(t)
	req := httptest.NewRequest("GET", "/invite/nonexistent", nil)
	req.SetPathValue("token", "nonexistent")
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db)

	if w.Code != http.StatusOK {
		t.Errorf("want 200 (error page), got %d", w.Code)
	}
}

func postOnboard(db *store.Store, cfg config, sub string,
	vals url.Values) *httptest.ResponseRecorder {
	// Double-submit CSRF: cookie and form field must match.
	const csrf = "test-csrf-token"
	if vals.Get("csrf") == "" {
		vals = cloneVals(vals)
		vals.Set("csrf", csrf)
	}
	body := vals.Encode()
	req := httptest.NewRequest("POST", "/onboard", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", sub)
	req.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: csrf})
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, db, cfg)
	return w
}

func cloneVals(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func TestDeleteRoute(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	seedGrant(t, db, "alice", "admin", "myroom")
	seedRoute(t, db, 0, "room=1", "myroom")

	id := routeIDInto(t, db, "myroom")

	w := postOnboard(db, cfg, "alice", url.Values{
		"action":   {"delete_route"},
		"route_id": {strconv.FormatInt(id, 10)},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d: %s", w.Code, w.Body.String())
	}

	n := boolCount(routeExists(t, db, id))
	if n != 0 {
		t.Error("route should be deleted")
	}
}

func TestDeleteRouteWrongUser(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	seedGrant(t, db, "bob", "admin", "bobroom")
	seedRoute(t, db, 0, "room=1", "aliceroom")

	id := routeIDInto(t, db, "aliceroom")

	w := postOnboard(db, cfg, "bob", url.Values{
		"action":   {"delete_route"},
		"route_id": {strconv.FormatInt(id, 10)},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}

	n := boolCount(routeExists(t, db, id))
	if n != 1 {
		t.Error("route should still exist")
	}
}

func TestAddRoute(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	seedGrant(t, db, "alice", "admin", "myroom")
	// Alice must own a JID whose room matches the add_route match pattern.
	seedMembership(t, db, "telegram:999", "alice", "")

	w := postOnboard(db, cfg, "alice", url.Values{
		"action": {"add_route"},
		"match":  {"room=999"},
		"target": {"myroom"},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d: %s", w.Code, w.Body.String())
	}

	rules := routesMatching(t, db, func(r resources.RoutesRow) bool { return r.Target == "myroom" })
	if len(rules) != 1 || rules[0].Match != "room=999" {
		t.Errorf("routes into myroom = %+v, want one matching room=999", rules)
	}
}

// Non-operator cannot add a route whose match refers to a JID they don't own,
// even if the target folder is theirs — prevents cross-tenant interception.
func TestAddRouteMatchNotOwned(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	seedGrant(t, db, "alice", "admin", "myroom")
	// alice has NO user_jids; attempt to claim victim's room.
	w := postOnboard(db, cfg, "alice", url.Values{
		"action": {"add_route"},
		"match":  {"room=victim-id"},
		"target": {"myroom"},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", w.Code, w.Body.String())
	}
	n := countRoutes(t, db)
	if n != 0 {
		t.Errorf("no route should have been created, got %d", n)
	}
}

// Reject malformed match pattern characters (wildcards, spaces).
func TestAddRouteInvalidMatchChars(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	seedGrant(t, db, "alice", "admin", "myroom")
	for _, bad := range []string{"room=* *", "room=a b", "** match"} {
		w := postOnboard(db, cfg, "alice", url.Values{
			"action": {"add_route"},
			"match":  {bad},
			"target": {"myroom"},
		})
		if w.Code != http.StatusBadRequest && w.Code != http.StatusForbidden {
			t.Errorf("bad match %q: want 400/403, got %d", bad, w.Code)
		}
	}
}

func TestCSRFRejected(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	seedGrant(t, db, "alice", "admin", "myroom")
	// No csrf cookie/field → must be rejected even with X-User-Sub.
	form := url.Values{"action": {"add_route"}, "match": {"room=1"}, "target": {"myroom"}}
	req := httptest.NewRequest("POST", "/onboard", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "alice")
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, db, cfg)
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403 (csrf), got %d", w.Code)
	}
}

// Invite consume is atomic: simulated concurrent redemption cannot exceed
// max_uses. The guard is inside the UPDATE — double-call with uses=max_uses
// after the first returns the "used" page.
func TestInviteAtomicConsume(t *testing.T) {
	db := testDB(t)
	if err := db.CreateUserProfile("github:bob", "bob", "Bob"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	seedFolder(t, db, "alice")
	tok := createInvite(t, db, "alice", "telegram:1", 1)

	// First consume.
	req := httptest.NewRequest("GET", "/invite/"+tok, nil)
	req.SetPathValue("token", tok)
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("first consume: want 303, got %d", w.Code)
	}

	// Second consume — uses is now 1 == max_uses; must fail.
	if err := db.CreateUserProfile("github:eve", "eve", "Eve"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	req2 := httptest.NewRequest("GET", "/invite/"+tok, nil)
	req2.SetPathValue("token", tok)
	req2.Header.Set("X-User-Sub", "github:eve")
	w2 := httptest.NewRecorder()
	handleInvite(w2, req2, db, db)
	if w2.Code != http.StatusOK {
		t.Errorf("second consume: want 200 error page, got %d", w2.Code)
	}
	// Eve should not have user_groups row.
	n := len(aclScopes(t, db, "github:eve"))
	if n != 0 {
		t.Errorf("eve must not be granted access, got %d rows", n)
	}
}

func TestAddRouteWrongTarget(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	seedGrant(t, db, "bob", "admin", "bobroom")

	w := postOnboard(db, cfg, "bob", url.Values{
		"action": {"add_route"},
		"match":  {"room=999"},
		"target": {"aliceroom"},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}

	n := countRoutes(t, db)
	if n != 0 {
		t.Error("no route should have been created")
	}
}

// --- Expanded coverage: schema, permissions, XSS, operator, flow ---

// renderDashboard must HTML-escape attacker-controlled username, sub, and jid.
func TestDashboardXSSEscape(t *testing.T) {
	db := testDB(t)
	attacker := `<script>alert(1)</script>`
	if err := db.CreateUserProfile(attacker, attacker, ""); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	seedGrant(t, db, attacker, "admin", "room")
	// The paired jid is attacker-controlled too, and the dashboard lists it.
	seedMembership(t, db, "telegram:"+attacker, attacker, "")

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", attacker)
	handleOnboard(w, req, db, db, config{})

	body := w.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("raw script tag leaked into dashboard HTML")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected HTML-escaped script in body")
	}
}

// Queue position render must escape the gate string (user-controlled via DB).
func TestQueuePositionXSSEscape(t *testing.T) {
	db := testDB(t)
	if err := db.CreateUserProfile("github:x", "x", "X"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	// gate value itself is stored, but the eta-msg is server-side formatted;
	// ensure no raw DB reflection occurs in HTML without escape.
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:1", Status: "queued",
		Gate: "<x>", QueuedAt: "2026-04-17T10:00:00Z", UserSub: "github:x"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:x")
	handleOnboard(w, req, db, db, config{})

	if strings.Contains(w.Body.String(), "<x>") {
		t.Error("unescaped gate value leaked into queue page")
	}
}

func TestHandleOnboardPostUnauthenticated(t *testing.T) {
	db := testDB(t)
	req := httptest.NewRequest("POST", "/onboard", nil)
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, db, config{})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestHandleOnboardPostUnknownAction(t *testing.T) {
	db := testDB(t)
	w := postOnboard(db, config{}, "github:alice", url.Values{"action": {"nope"}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestAddRouteMissingFields(t *testing.T) {
	db := testDB(t)
	seedGrant(t, db, "alice", "admin", "myroom")

	w := postOnboard(db, config{}, "alice", url.Values{"action": {"add_route"}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
	w2 := postOnboard(db, config{}, "alice", url.Values{
		"action": {"add_route"}, "match": {"room=1"},
	})
	if w2.Code != http.StatusBadRequest {
		t.Errorf("want 400 on missing target, got %d", w2.Code)
	}
}

func TestDeleteRouteInvalidID(t *testing.T) {
	db := testDB(t)
	seedGrant(t, db, "alice", "admin", "myroom")

	w := postOnboard(db, config{}, "alice", url.Values{
		"action": {"delete_route"}, "route_id": {"not-a-number"},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestDeleteRouteNotFound(t *testing.T) {
	db := testDB(t)
	seedGrant(t, db, "alice", "admin", "myroom")
	w := postOnboard(db, config{}, "alice", url.Values{
		"action": {"delete_route"}, "route_id": {"99999"},
	})
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

// Operator (user_groups has `**`) can add/delete any route — exercises
// auth.MatchGroups integration in folderAllowed.
func TestOperatorCanManageAnyRoute(t *testing.T) {
	db := testDB(t)
	seedGrant(t, db, "op", "admin", "**")
	seedRoute(t, db, 0, "room=X", "any-world")

	id := routeIDInto(t, db, "any-world")
	w := postOnboard(db, config{}, "op", url.Values{
		"action": {"delete_route"}, "route_id": {strconv.FormatInt(id, 10)},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("operator delete_route want 303, got %d: %s", w.Code, w.Body.String())
	}

	w2 := postOnboard(db, config{}, "op", url.Values{
		"action": {"add_route"}, "match": {"room=Y"}, "target": {"another-world"},
	})
	if w2.Code != http.StatusSeeOther {
		t.Fatalf("operator add_route want 303, got %d: %s", w2.Code, w2.Body.String())
	}
}

// Non-operator with zero grants (empty user_groups) must get 403.
func TestNonOperatorNoGrantsDenied(t *testing.T) {
	db := testDB(t)
	seedRoute(t, db, 0, "room=1", "someroom")

	id := routeIDInto(t, db, "someroom")
	// bob has no user_groups rows → userFolders returns nil → denied.
	// Operator is emergent only from a `**` grant row.
	w := postOnboard(db, config{}, "bob", url.Values{
		"action": {"delete_route"}, "route_id": {strconv.FormatInt(id, 10)},
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", w.Code)
	}
}

// Admission queue FIFO: oldest queued_at admitted first.
func TestAdmitFromQueueFIFO(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:late", Status: "queued",
		Gate: "*", QueuedAt: "2026-04-17T12:00:00Z", UserSub: "u:late"})
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:early", Status: "queued",
		Gate: "*", QueuedAt: "2026-04-17T09:00:00Z", UserSub: "u:early"})
	seedGateRow(t, db, "*", 1, true)
	admitFromQueue(db)

	early := admissionStatus(t, db, "t:early")
	late := admissionStatus(t, db, "t:late")
	if early != "approved" {
		t.Errorf("FIFO violated: early want approved, got %s", early)
	}
	if late != "queued" {
		t.Errorf("FIFO violated: late want queued, got %s", late)
	}
}

// Two gates with independent daily limits.
func TestAdmitFromQueueMultipleGates(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "g:1", Status: "queued",
		Gate: "github:org=co", QueuedAt: "2026-04-17T10:00:00Z", UserSub: "gh:1"})
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "*:1", Status: "queued",
		Gate: "*", QueuedAt: "2026-04-17T10:00:00Z", UserSub: "any:1"})
	seedGateRow(t, db, "github:org=co", 5, true)
	seedGateRow(t, db, "*", 5, true)
	admitFromQueue(db)

	a := admissionStatus(t, db, "g:1")
	b := admissionStatus(t, db, "*:1")
	if a != "approved" || b != "approved" {
		t.Errorf("both gates should admit: got github=%s, wildcard=%s", a, b)
	}
}

// Disabled gate is not loaded.
func TestLoadGatesEnabledFilter(t *testing.T) {
	db := testDB(t)
	seedGateRow(t, db, "github:org=co", 10, true)
	seedGateRow(t, db, "*", 5, false)

	gates := loadGates(db)
	if len(gates) != 1 {
		t.Fatalf("want 1 enabled gate, got %d", len(gates))
	}
	if gates[0].kind != "github" {
		t.Errorf("want kind=github, got %s", gates[0].kind)
	}
}

// End-to-end state machine on the canonical migrated schema, after the fold:
// awaiting_message → (prompt mints a pairing token in route_tokens) →
// (RedeemPairing writes the edge) → (observer) → queued (gated) →
// (admit) → approved. `token_used` is gone with the token it named.
func TestStateMachineMigrated(t *testing.T) {
	xdb, obdb := migratedDB(t)
	// Seed auth user + existing world for the JID to link to.
	if err := xdb.CreateUserProfile("github:alice", "alice", "Alice"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	seedGateRow(t, obdb, "*", 5, true)
	seedAdmission(t, obdb, store.ArchiveOnboardingRow{JID: "telegram:7", Status: "awaiting_message"})

	cfg := config{authBaseURL: "https://example.com"}

	// 1) prompt → a kind='pair' token exists for the jid, and the row is claimed
	promptUnprompted(xdb, obdb, cfg)
	if pairTokens := len(pairingTokensFor(t, xdb, "telegram:7")); pairTokens != 1 {
		t.Fatalf("prompt minted %d pairing tokens, want 1", pairTokens)
	}
	st := admissionStatus(t, obdb, "telegram:7")
	if st != "awaiting_message" {
		t.Fatalf("prompting must not advance status, got %s", st)
	}

	// 2) redemption writes the edge — in routd's DB, by RedeemPairing, not onbod
	seedMembership(t, xdb, "telegram:7", "github:alice", store.PairingAddedBy)

	// 3) the observer discovers it → queued (a gate is configured)
	observePairings(xdb, obdb, cfg)
	st = admissionStatus(t, obdb, "telegram:7")
	if st != "queued" {
		t.Fatalf("after observe+gate want queued, got %s", st)
	}

	// 4) admit from queue → approved
	admitFromQueue(obdb)
	st = admissionStatus(t, obdb, "telegram:7")
	if st != "approved" {
		t.Fatalf("after admit want approved, got %s", st)
	}
}

// paramVal / emailDomain edge cases.
func TestParamValAndEmailDomain(t *testing.T) {
	if paramVal("domain=co.com", "domain") != "co.com" {
		t.Error("paramVal should extract value")
	}
	if paramVal("other=x", "domain") != "" {
		t.Error("paramVal should return empty on mismatch")
	}
	if emailDomain("google:a@co.com") != "co.com" {
		t.Error("emailDomain extract")
	}
	if emailDomain("no-at-sign") != "" {
		t.Error("emailDomain should return empty when no @")
	}
}

func TestDashboardUnauthenticated(t *testing.T) {
	db := testDB(t)
	cfg := config{}
	req := httptest.NewRequest("GET", "/onboard", nil)
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, db, cfg)
	// No X-User-Sub: proxyd let through a caller it could not name, so say so.
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

// promptUnprompted POSTs the greeting + pairing link to routd's /v1/outbound,
// presenting the service:onbod bearer. A test HTTP server captures the wire
// format without mocking.
func TestPromptUnpromptedSendsGreeting(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:1", Status: "awaiting_message"})

	var gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = readAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config{gatedURL: srv.URL, authBaseURL: "https://ex.com",
		svcToken: func(context.Context) (string, error) { return "svc-jwt", nil }}
	promptUnprompted(db, db, cfg)

	if gotAuth != "Bearer svc-jwt" {
		t.Errorf("want Bearer service token, got %q", gotAuth)
	}
	if !strings.Contains(string(gotBody), "telegram:1") ||
		!strings.Contains(string(gotBody), "/pair/") {
		t.Errorf("unexpected outbound body: %s", gotBody)
	}
}

// sendReply gracefully handles non-2xx — the prompt still claims the row.
func TestSendReplyHandlesNon2xx(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:1", Status: "awaiting_message"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg := config{gatedURL: srv.URL, authBaseURL: "https://ex.com"}
	// Should not panic even when router returns 500.
	promptUnprompted(db, db, cfg)
	if admission(t, db, "t:1").PromptedAt == "" {
		t.Error("row should still be claimed after send failure")
	}
}

// sendReply swallows transport errors (unreachable URL).
func TestSendReplyHandlesTransportError(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:1", Status: "awaiting_message"})
	// Point at a closed port to force Do() to fail.
	cfg := config{gatedURL: "http://127.0.0.1:1", authBaseURL: "https://ex.com"}
	promptUnprompted(db, db, cfg) // must not panic
}

// ensureCSRFToken is idempotent: an incoming request that already carries the
// cookie must not receive a new Set-Cookie header.
func TestEnsureCSRFIdempotent(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/onboard", nil)
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "existing"})
	ensureCSRFToken(w, req, config{})
	for _, sc := range w.Result().Cookies() {
		if sc.Name == csrfCookieName {
			t.Errorf("unexpected re-set of csrf cookie when one already present")
		}
	}
}

// handleInvite with empty path value (defensive — router should never route
// empty, but guard in handler must still return a friendly page).
func TestInviteEmptyToken(t *testing.T) {
	db := testDB(t)
	req := httptest.NewRequest("GET", "/invite/", nil)
	req.SetPathValue("token", "")
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	handleInvite(w, req, db, db)
	if w.Code != http.StatusOK {
		t.Errorf("want 200 page, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Invalid Invite") {
		t.Error("want Invalid Invite page")
	}
}

// handleAddRoute: match/target longer than 256 chars is rejected before touching DB.
func TestAddRouteTooLong(t *testing.T) {
	db := testDB(t)
	seedGrant(t, db, "op", "admin", "**")
	long := strings.Repeat("a", 257)
	w := postOnboard(db, config{}, "op", url.Values{
		"action": {"add_route"}, "match": {"room=1"}, "target": {long},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 on long target, got %d", w.Code)
	}
	w2 := postOnboard(db, config{}, "op", url.Values{
		"action": {"add_route"}, "match": {long}, "target": {"myroom"},
	})
	if w2.Code != http.StatusBadRequest {
		t.Errorf("want 400 on long match, got %d", w2.Code)
	}
}

// handleAddRoute: invalid pattern characters (spaces, wildcards) are rejected.
func TestAddRouteInvalidMatchChars2(t *testing.T) {
	db := testDB(t)
	seedGrant(t, db, "op", "admin", "**")
	// Note: "room=a\n" (trailing newline) is accepted by the current regex —
	// Go's default `$` matches before a final \n. Logged in bugs.md.
	for _, bad := range []string{"room=a b", "room=*", "room=a%", "room=a\nb"} {
		w := postOnboard(db, config{}, "op", url.Values{
			"action": {"add_route"}, "match": {bad}, "target": {"x"},
		})
		if w.Code != http.StatusBadRequest {
			t.Errorf("match=%q: want 400, got %d", bad, w.Code)
		}
	}
}

// handleAddRoute: non-operator must own the room referenced in match.
func TestAddRouteNonOperatorForbiddenOnUnownedMatch(t *testing.T) {
	db := testDB(t)
	seedGrant(t, db, "alice", "admin", "alice")
	// alice has no user_jids → cannot claim any room=<id>.
	w := postOnboard(db, config{}, "alice", url.Values{
		"action": {"add_route"}, "match": {"room=9999"}, "target": {"alice"},
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d: %s", w.Code, w.Body.String())
	}
}

// userOwnsMatch: only canonical "room=<id>" is accepted; malformed prefixes
// return false.
func TestUserOwnsMatchRejectsNonRoomPrefix(t *testing.T) {
	db := testDB(t)
	seedMembership(t, db, "telegram:42", "alice", "")
	if userOwnsMatch(db, "alice", "peer=42") {
		t.Error("non-room prefix must not match")
	}
	if userOwnsMatch(db, "alice", "room=") {
		t.Error("empty room must not match")
	}
	if !userOwnsMatch(db, "alice", "room=42") {
		t.Error("valid room=42 should match")
	}
	if userOwnsMatch(db, "alice", "room=99") {
		t.Error("wrong room id must not match")
	}
}

// renderQueuePosition: with low daily limit + many queued, ETA must use
// "hours" wording (covers the >=60min branch).
func TestQueuePositionETAHours(t *testing.T) {
	db := testDB(t)
	if err := db.CreateUserProfile("github:x", "x", "X"); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}
	seedGateRow(t, db, "*", 1, true)
	// 4 earlier queued entries + the caller → position 5, 5*1440/1 = 7200 min.
	for i, jid := range []string{"t:a", "t:b", "t:c", "t:d"} {
		seedAdmission(t, db, store.ArchiveOnboardingRow{JID: jid, Status: "queued",
			Gate:     "*",
			QueuedAt: "2026-04-17T0" + strconv.Itoa(i) + ":00:00Z",
			UserSub:  "u:" + strconv.Itoa(i)})
	}
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:me", Status: "queued",
		Gate: "*", QueuedAt: "2026-04-17T09:00:00Z", UserSub: "github:x"})

	req := httptest.NewRequest("GET", "/onboard", nil)
	req.Header.Set("X-User-Sub", "github:x")
	w := httptest.NewRecorder()
	handleOnboard(w, req, db, db, config{})
	if !strings.Contains(w.Body.String(), "hours") {
		t.Errorf("expected 'hours' in ETA, body=%s", w.Body.String())
	}
}

// admitFromQueue respects already-admitted today's count and does not
// exceed limitPerDay even across repeated invocations.
func TestAdmitFromQueueDailyLimitPersists(t *testing.T) {
	db := testDB(t)
	today := time.Now().Format("2006-01-02")
	// Already-approved entry counts against today's budget (by admitted_at).
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:old", Status: "approved",
		Gate: "*", QueuedAt: today + "T02:00:00Z", AdmittedAt: today + "T02:00:00Z",
		UserSub: "u:old"})
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:wait", Status: "queued",
		Gate: "*", QueuedAt: today + "T03:00:00Z", UserSub: "u:wait"})
	seedGateRow(t, db, "*", 1, true)

	admitFromQueue(db)
	st := admissionStatus(t, db, "t:wait")
	if st != "queued" {
		t.Errorf("daily limit should block further admission, got %s", st)
	}

	// Second call is also a no-op (limit already hit).
	admitFromQueue(db)
	st = admissionStatus(t, db, "t:wait")
	if st != "queued" {
		t.Errorf("second admit should still be no-op, got %s", st)
	}
}

// Backlog queued on a prior day, admitted today, must count against today's
// limit. The buggy queued_at-scoped count saw 0 admitted today and drained the
// whole backlog every poll, making the daily limit unbounded across days.
func TestAdmitFromQueuePriorDayBacklogCounts(t *testing.T) {
	_, db := migratedDB(t)
	// Two entries queued yesterday, limit 1/day. First admit takes one.
	yesterday := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:1", Status: "queued",
		Gate: "*", QueuedAt: yesterday + "T01:00:00Z", UserSub: "u:1"})
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "t:2", Status: "queued",
		Gate: "*", QueuedAt: yesterday + "T02:00:00Z", UserSub: "u:2"})
	seedGateRow(t, db, "*", 1, true)

	admitFromQueue(db)
	s1 := admissionStatus(t, db, "t:1")
	s2 := admissionStatus(t, db, "t:2")
	if s1 != "approved" {
		t.Errorf("oldest backlog entry should be admitted, got %s", s1)
	}
	if s2 != "queued" {
		t.Errorf("second entry must wait: today's quota of 1 spent on t:1, got %s", s2)
	}

	// Re-poll: the admission stamped admitted_at=today, so today's count is 1,
	// quota exhausted; t:2 must stay queued instead of draining the backlog.
	admitFromQueue(db)
	s2 = admissionStatus(t, db, "t:2")
	if s2 != "queued" {
		t.Errorf("prior-day backlog must not evade today's limit, got %s", s2)
	}
}

// tokenHash returns empty string for empty input and a stable 8-char tag
// otherwise. Stability lets log pipelines correlate attempts.
func TestTokenHash(t *testing.T) {
	if chanlib.ShortHash("") != "" {
		t.Error("empty input must yield empty tag")
	}
	h := chanlib.ShortHash("abc123")
	if len(h) != 8 {
		t.Errorf("want 8-char tag, got %d", len(h))
	}
	if chanlib.ShortHash("abc123") != h {
		t.Error("tokenHash must be deterministic")
	}
	if chanlib.ShortHash("abc124") == h {
		t.Error("different inputs must produce different tags")
	}
}

// loadConfig happy path: populates fields from env, including
// pollInterval parsing.
func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	authd, _ := fakeAuthdToken(t)
	t.Setenv("DATA_DIR", dir)
	t.Setenv("ARIZUKO_DEV", "true")
	t.Setenv("ONBOARD_POLL_INTERVAL", "5s")
	t.Setenv("ONBOARDING_GREETING", "hi")
	t.Setenv("ROUTER_URL", "http://r:1")
	t.Setenv("AUTH_BASE_URL", "https://auth.example.com")
	t.Setenv("AUTHD_URL", authd.URL)
	t.Setenv("AUTHD_SERVICE_KEY", "boot-onbod")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.pollInterval != 5*time.Second {
		t.Errorf("want pollInterval=5s, got %v", cfg.pollInterval)
	}
	if cfg.greeting != "hi" {
		t.Errorf("greeting not propagated")
	}
	if cfg.gatedURL != "http://r:1" {
		t.Errorf("gatedURL not propagated, got %q", cfg.gatedURL)
	}
	if !cfg.secureCookie {
		t.Errorf("secureCookie should be true for https AUTH_BASE_URL")
	}
	if cfg.svcToken == nil {
		t.Errorf("svcToken must be set (AUTHD required)")
	}
}

// loadConfig with invalid poll interval falls back to default (10s).
func TestLoadConfigBadPollInterval(t *testing.T) {
	dir := t.TempDir()
	authd, _ := fakeAuthdToken(t)
	t.Setenv("DATA_DIR", dir)
	t.Setenv("ARIZUKO_DEV", "true")
	t.Setenv("ONBOARD_POLL_INTERVAL", "not-a-duration")
	t.Setenv("AUTHD_URL", authd.URL)
	t.Setenv("AUTHD_SERVICE_KEY", "boot-onbod")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.pollInterval != 10*time.Second {
		t.Errorf("want default 10s, got %v", cfg.pollInterval)
	}
}

// isOperator: operator emerges only from a `**` grant; nil/empty/other
// grants are not operator (no nil-sentinel — see specs/5/32-acl-unified.md).
func TestIsOperator(t *testing.T) {
	if !isOperator([]string{"alice", "**"}) {
		t.Error("** grant must be operator")
	}
	if isOperator(nil) {
		t.Error("nil folders must not be operator")
	}
	if isOperator([]string{"alice", "bob"}) {
		t.Error("plain folders must not be operator")
	}
	if isOperator([]string{}) {
		t.Error("empty slice must not be operator")
	}
}

// userFolders filters out empty-string rows (defensive — migration scars).
//
// The scar is written as a raw item, because store's own writer REFUSES an
// empty scope: a grant that names nothing has no coordinate (store/acl.go
// aclKey). What is under test is the reader's defence against a row no writer
// here produces.
func TestUserFoldersSkipsEmpty(t *testing.T) {
	db := testDB(t)
	seedGrant(t, db, "alice", "admin", "real")
	seedACLScar(t, db, core.ACLRow{Principal: "alice", Action: "admin", Effect: "allow"})
	got := userFolders(db, "alice")
	for _, f := range got {
		if f == "" {
			t.Errorf("empty folder leaked into result: %v", got)
		}
	}
}

// handleOnboardPost rejects when CSRF cookie set but no form value supplied.
func TestCSRFRejectedWhenFormMissing(t *testing.T) {
	db := testDB(t)
	req := httptest.NewRequest("POST", "/onboard",
		strings.NewReader(url.Values{"action": {"create_world"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:alice")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "abc"})
	w := httptest.NewRecorder()
	handleOnboardPost(w, req, db, db, config{})
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403 when csrf form value absent, got %d", w.Code)
	}
}

// promptUnprompted still claims strictly on `prompted_at IS NULL`. The cooldown
// that clears prompted_at lives in InsertOnboarding, where the inbound MISS that
// justifies a second greeting is (spec 5/31-identity-pairing § Deleting "greet once, ever"); a
// plain time predicate here would re-greet every stale row every tick.
func TestPromptUnprompted_DoesNotRegreetAPromptedRow(t *testing.T) {
	db := testDB(t)
	stale := time.Now().UTC().Add(-31 * time.Minute).Format(time.RFC3339)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:1",
		Status: "awaiting_message", PromptedAt: stale})

	cfg := config{authBaseURL: "https://example.com"}
	promptUnprompted(db, db, cfg)

	if minted := countRouteTokens(t, db); minted != 0 {
		t.Errorf("re-greeted a row already prompted %s ago: %d tokens minted", "31m", minted)
	}
	prompted := admission(t, db, "telegram:1").PromptedAt
	if prompted != stale {
		t.Errorf("prompted_at moved: %q -> %q", stale, prompted)
	}
}

// A row that has already been claimed by an admission verdict is not a greeting
// candidate at all, whatever its prompted_at says.
func TestPromptUnprompted_DoesNotResetClaimedRow(t *testing.T) {
	db := testDB(t)
	stale := time.Now().UTC().Add(-31 * time.Minute).Format(time.RFC3339)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:1", Status: "approved",
		UserSub: "github:alice", PromptedAt: stale})

	cfg := config{authBaseURL: "https://example.com"}
	promptUnprompted(db, db, cfg)

	if status := admissionStatus(t, db, "telegram:1"); status != "approved" {
		t.Errorf("claimed row was reset: status=%s", status)
	}
	if minted := countRouteTokens(t, db); minted != 0 {
		t.Errorf("claimed row got a link re-minted: %d tokens", minted)
	}
}

// Small helper: drain a body without importing io at the top of the file.
func readAll(r interface{ Read(p []byte) (int, error) }) ([]byte, error) {
	var out []byte
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return out, nil
			}
			return out, err
		}
	}
}

// W1: a creator's grant must reach the world's SUBGROUPS, and the reader that
// resolves "which folder may this user administer" must honour the pattern
// rather than compare strings. The old `JOIN acl a ON a.scope = g.folder`
// matched neither `acme` against `acme/eng` nor `acme/**` against anything.
func TestAdminFolders_HonoursSubtreeGrant(t *testing.T) {
	db := testDB(t)
	seedFolder(t, db, "acme")
	seedFolder(t, db, "acme/eng")
	seedGrant(t, db, "github:owner", "admin", "acme/**")

	// Both the world AND its subgroup are choices the owner may be offered.
	if got := adminFolders(db, "github:owner"); len(got) != 2 {
		t.Fatalf("subtree grant enumerated %v, want both acme and acme/eng", got)
	}
	if got := adminFolders(db, "github:stranger"); len(got) != 0 {
		t.Errorf("ungranted principal enumerated %v, want none", got)
	}
}

// O1: a row in a status no query selects is stranded — never prompted, queued
// or admitted, and its jid PRIMARY KEY blocks a fresh insert, so the user can
// never onboard from that chat again. Two such rows exist in production. The
// pipeline cannot repair them (picking a status is the operator's call) but it
// must not stay silent about them.
func TestKnownStatusesCoversEveryWriter(t *testing.T) {
	// Every status the code writes must be one the pipeline can advance,
	// otherwise we ship the O1 bug again with a new value.
	for _, s := range []string{"awaiting_message", "queued", "approved", "refused"} {
		if !knownStatuses[s] {
			t.Errorf("%q is written by the pipeline but not in knownStatuses", s)
		}
	}
	if knownStatuses["pending"] {
		t.Error("'pending' is not a status any writer produces; it must read as stranded")
	}
	// token_used went with the token it named (spec 5/31-identity-pairing § the fold). No writer
	// produces it, so a row still carrying it is stranded and must be shouted
	// about — the same treatment 'pending' gets.
	if knownStatuses["token_used"] {
		t.Error("'token_used' has no writer after the fold; it must read as stranded")
	}
}

// L61: the scan runs from promptUnprompted on a 10s poll, so an unrepairable
// row logged at ERROR forever — 6 lines a minute on two production instances,
// which buries real errors and makes a permanent condition read as a live
// incident. Once per (jid, status) per process is the whole fix.
func TestStrandedRowWarnsOncePerProcess(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:user/1", Status: "pending"})

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(prev)

	warnStrandedRows(db)
	warnStrandedRows(db)
	warnStrandedRows(db)

	if got := strings.Count(buf.String(), "stranded in an unknown status"); got != 1 {
		t.Errorf("three scans logged %d times, want exactly 1", got)
	}

	// A row that moves to a DIFFERENT unknown status is a new fact, so it
	// warns again — silence there would hide a real change.
	setAdmissionStatus(t, db, "telegram:user/1", "wedged")
	warnStrandedRows(db)
	if got := strings.Count(buf.String(), "stranded in an unknown status"); got != 2 {
		t.Errorf("a changed status logged %d times total, want 2", got)
	}
}

// L67: sendReply used to return nothing, so promptUnprompted logged
// "sent pairing link" on the line directly after "send reply non-2xx". The
// outcome must reach the caller; the claim deliberately survives (releasing it
// would re-mint a token every tick for a permanently unroutable jid).
func TestSendReplyReportsNon2xxToTheCaller(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	cfg := config{gatedURL: srv.URL, svcToken: func(context.Context) (string, error) { return "t", nil }}
	err := sendReply(cfg, "Coach", "hi")
	if err == nil {
		t.Fatal("a 502 must reach the caller; swallowing it is what logged success over the failure")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("the error must name the status, got %v", err)
	}
}

func TestSendReplyReportsMissingServiceToken(t *testing.T) {
	if err := sendReply(config{gatedURL: "http://127.0.0.1:1"}, "t:1", "hi"); err == nil {
		t.Fatal("a nil svcToken must be an error, not a silent drop")
	}
}
