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
	"time"

	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// tokenFromLink pulls the raw bearer out of an outbound "…/onboard?token=<raw>"
// prompt. Since Z3 the DB holds only hex(sha256(token)), so a test that needs
// the redeemable value must read it where the user does: the sent link.
func tokenFromLink(t *testing.T, body string) string {
	t.Helper()
	_, after, ok := strings.Cut(body, "/onboard?token=")
	if !ok {
		t.Fatalf("no onboard link in outbound body: %q", body)
	}
	tok, _, _ := strings.Cut(after, `"`)
	return strings.TrimSpace(tok)
}

// newOnbodServer wires the real mux against a migrated DB and a fake
// gated /v1/outbound endpoint. Returns server + a recorder of outbound calls.
func newOnbodServer(t *testing.T) (*httptest.Server, *testutils.Inst, *testutils.FakePlatform, config) {
	t.Helper()
	inst := testutils.NewInstance(t)

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
		handleOnboard(w, r, inst.DB, inst.DB, cfg)
	})
	mux.HandleFunc("POST /onboard", func(w http.ResponseWriter, r *http.Request) {
		handleOnboardPost(w, r, inst.DB, cfg)
	})
	mux.HandleFunc("GET /invite/{token}", func(w http.ResponseWriter, r *http.Request) {
		handleInvite(w, r, inst.DB, inst.DB, cfg)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, inst, fake, cfg
}

// TestOnboardingFlow walks the full onboarding state machine:
// awaiting_message → prompt (outbound fired) → token_used → approved +
// group folder seeded on disk + group + user_groups + routes rows.
func TestOnboardingFlow(t *testing.T) {
	srv, inst, fake, cfg := newOnbodServer(t)

	jid := "telegram:42"
	if _, err := inst.DB.Exec(
		`INSERT INTO onboarding (jid, status, created) VALUES (?, 'awaiting_message', ?)`,
		jid, time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	// Trigger the poll step: promptUnprompted issues a POST to gatedURL
	// and stamps token + prompted_at on the row.
	promptUnprompted(inst.DB, cfg)

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

	// The raw token exists ONLY in the link that just went out (Z3) — the DB
	// keeps hex(sha256) in token_ref — so recover it from the outbound body,
	// exactly as the user's chat client would.
	token := tokenFromLink(t, string(reqs[0].Body))
	var tokenRef, tokenExpires string
	if err := inst.DB.QueryRow(
		`SELECT token_ref, token_expires FROM onboarding WHERE jid = ?`, jid).Scan(&tokenRef, &tokenExpires); err != nil {
		t.Fatalf("read token_ref: %v", err)
	}
	if len(token) != 64 {
		t.Fatalf("want 64-char token, got %d", len(token))
	}
	if tokenRef != store.TokenRef(token) {
		t.Fatalf("token_ref = %q, want TokenRef(sent token)", tokenRef)
	}
	if tokenRef == token {
		t.Fatal("token stored in the clear — token_ref must be the hash, not the bearer")
	}
	// token_expires must be RFC3339 — SQL string comparison in
	// handleTokenLanding ('token_expires > now') breaks if writer uses
	// space-separated form (space < T) and reader uses RFC3339.
	if _, err := time.Parse(time.RFC3339, tokenExpires); err != nil {
		t.Errorf("token_expires not RFC3339: %q (%v)", tokenExpires, err)
	}

	// Consume the token. Use a client that does NOT follow redirects so
	// we can inspect the Set-Cookie and Location headers.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := c.Get(srv.URL + "/onboard?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("token landing: status = %d", resp.StatusCode)
	}
	var onboardJID *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == "onboard_jid" {
			onboardJID = ck
		}
	}
	if onboardJID == nil || onboardJID.Value != jid {
		t.Fatalf("onboard_jid cookie not set; cookies=%v", resp.Cookies())
	}
	// Token presentation is idempotent — status & token unchanged. The
	// claim happens at user_sub binding (post-OAuth dashboard hit) below.
	var status string
	inst.DB.QueryRow(`SELECT status FROM onboarding WHERE jid = ?`, jid).Scan(&status)
	if status != "awaiting_message" {
		t.Errorf("status = %q, want awaiting_message (presentation idempotent)", status)
	}

	// Seed the user_profiles row + POST create_world (fake proxyd by
	// setting X-User-Sub; CSRF cookie + form field double-submit).
	// Checked, not discarded: user_profiles.name is NOT NULL, so this seed
	// silently failed for as long as it omitted the column, and every later
	// assertion ran against a dashboard rendering "User not found" at 200.
	sub := "github:alice"
	if _, err := inst.DB.Exec(`INSERT INTO user_profiles (sub, username, name, created_at)
		VALUES (?, ?, ?, ?)`, sub, sub, "Alice", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed user_profiles: %v", err)
	}

	// First: GET /onboard so the dashboard handler links the JID (atomic
	// user_sub claim on onboarding row).
	reqG, _ := http.NewRequest("GET", srv.URL+"/onboard", nil)
	reqG.Header.Set("X-User-Sub", sub)
	reqG.AddCookie(onboardJID)
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

	// Confirm onboarding row now approved and user_sub bound.
	var gotSub, gotStatus string
	inst.DB.QueryRow(`SELECT user_sub, status FROM onboarding WHERE jid = ?`,
		jid).Scan(&gotSub, &gotStatus)
	if gotSub != sub {
		t.Errorf("onboarding.user_sub = %q, want %q", gotSub, sub)
	}
	if gotStatus != "approved" {
		t.Errorf("onboarding.status = %q, want approved (no gates)", gotStatus)
	}

	// POST create_world.
	form := url.Values{
		"action":   {"create_world"},
		"username": {"alice"},
		"csrf":     {csrf.Value},
	}
	reqP, _ := http.NewRequest("POST", srv.URL+"/onboard", strings.NewReader(form.Encode()))
	reqP.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqP.Header.Set("X-User-Sub", sub)
	reqP.AddCookie(csrf)
	reqP.AddCookie(&http.Cookie{Name: "pending_target", Value: "/"})
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
	var folder string
	inst.DB.QueryRow(`SELECT folder FROM groups WHERE folder = 'alice'`).Scan(&folder)
	if folder != "alice" {
		t.Errorf("groups row missing, got %q", folder)
	}
	var ug string
	inst.DB.QueryRow(`SELECT scope FROM acl WHERE principal = ?`, sub).Scan(&ug)
	// Subtree scope: an exact-folder grant authorizes the world and nothing
	// under it, so the creator could never reach their own subgroups (W1).
	if ug != "alice/**" {
		t.Errorf("creator grant should be subtree-scoped, got %q", ug)
	}
	// create_world itself writes no route (5/18 step 7) — the route is the act
	// the /onboard landing performs, and it names who did it.
	var routeN int
	inst.DB.QueryRow(`SELECT COUNT(*) FROM routes`).Scan(&routeN)
	if routeN != 0 {
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
	var addedBy, addedVia string
	if err := inst.DB.QueryRow(
		`SELECT COALESCE(added_by, ''), COALESCE(added_via, '') FROM routes
		 WHERE target = 'alice' AND match = 'room=42'`,
	).Scan(&addedBy, &addedVia); err != nil {
		t.Fatalf("expected the linked jid routed after the landing: %v; page: %s", err, rbody)
	}
	if addedBy != sub || addedVia != routeViaSoleWorld {
		t.Errorf("route attribution = (%q, %q), want (%q, %q)",
			addedBy, addedVia, sub, routeViaSoleWorld)
	}

	// SetupGroup side-effect: group folder + .claude dir exist on disk.
	if _, err := inst.DB.Exec(`SELECT 1`); err != nil {
		t.Fatal(err)
	}
	groupDir := filepath.Join(inst.Tmp, "groups", "alice")
	if st, err := os.Stat(groupDir); err != nil || !st.IsDir() {
		t.Errorf("expected group dir %s on disk: err=%v", groupDir, err)
	}
}

// TestOAuthCallback: after OAuth, proxyd forwards with X-User-Sub + the
// onboard_jid cookie from the token-landing step. The dashboard handler
// atomically claims onboarding.user_sub and writes user_jids. Simulate
// the cookie-bearing GET without going through OAuth itself (onbod has
// no OAuth endpoint — auth/ does).
func TestOAuthCallback(t *testing.T) {
	srv, inst, _, _ := newOnbodServer(t)

	jid := "discord:99"
	sub := "google:bob@example.com"
	// Row already past token landing (status=token_used).
	inst.DB.Exec(`INSERT INTO onboarding (jid, status, token_expires, created)
		VALUES (?, 'token_used', '2099-01-01T00:00:00Z', ?)`,
		jid, time.Now().Format(time.RFC3339))
	inst.DB.Exec(`INSERT INTO user_profiles (sub, username, created_at)
		VALUES (?, ?, ?)`, sub, sub, time.Now().Format(time.RFC3339))

	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, _ := http.NewRequest("GET", srv.URL+"/onboard", nil)
	req.Header.Set("X-User-Sub", sub)
	req.AddCookie(&http.Cookie{Name: "onboard_jid", Value: jid})

	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("oauth-returned dashboard: status=%d", resp.StatusCode)
	}

	var gotSub string
	inst.DB.QueryRow(`SELECT user_sub FROM onboarding WHERE jid = ?`, jid).Scan(&gotSub)
	if gotSub != sub {
		t.Errorf("onboarding.user_sub = %q, want %q", gotSub, sub)
	}
	var ujSub string
	inst.DB.QueryRow(`SELECT parent FROM acl_membership WHERE child = ?`, jid).Scan(&ujSub)
	if ujSub != sub {
		t.Errorf("user_jids.user_sub = %q, want %q", ujSub, sub)
	}
}

// TestGateRateLimit: with a wildcard gate of 2/day, linking 3 jids queues
// all 3 and admitFromQueue approves only 2, leaving the third queued.
func TestGateRateLimit(t *testing.T) {
	_, inst, _, _ := newOnbodServer(t)

	if _, err := inst.DB.Exec(
		`INSERT INTO onboarding_gates (gate, limit_per_day, enabled) VALUES ('*', 2, 1)`,
	); err != nil {
		t.Fatal(err)
	}

	now := time.Now().Format(time.RFC3339)
	for i, jid := range []string{"telegram:1", "telegram:2", "telegram:3"} {
		sub := "github:u" + string(rune('a'+i))
		inst.DB.Exec(`INSERT INTO onboarding (jid, status, created)
			VALUES (?, 'token_used', ?)`, jid, now)
		linkJID(inst.DB, inst.DB, jid, sub)
	}

	var queued int
	inst.DB.QueryRow(
		`SELECT COUNT(*) FROM onboarding WHERE status = 'queued'`).Scan(&queued)
	if queued != 3 {
		t.Fatalf("all 3 should be queued before admission, got %d", queued)
	}

	admitFromQueue(inst.DB)

	var approved, stillQueued int
	inst.DB.QueryRow(
		`SELECT COUNT(*) FROM onboarding WHERE status = 'approved'`).Scan(&approved)
	inst.DB.QueryRow(
		`SELECT COUNT(*) FROM onboarding WHERE status = 'queued'`).Scan(&stillQueued)
	if approved != 2 {
		t.Errorf("approved = %d, want 2 (rate limit)", approved)
	}
	if stillQueued != 1 {
		t.Errorf("still queued = %d, want 1 (rejected by limit)", stillQueued)
	}

	// Second admission pass same day must not exceed the limit.
	admitFromQueue(inst.DB)
	inst.DB.QueryRow(
		`SELECT COUNT(*) FROM onboarding WHERE status = 'approved'`).Scan(&approved)
	if approved != 2 {
		t.Errorf("after re-admit, approved = %d, want 2 (persisted limit)", approved)
	}
}
