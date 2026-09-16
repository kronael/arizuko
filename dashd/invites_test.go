package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// testDBWithInvites is a migrated routd.db plus onbod's entities, because these
// handlers hold BOTH files and this fixture fuses them into one. `invites` is
// an objstore entity, so it is asked of the driver rather than created by a
// CREATE TABLE that would build a shape production does not have — and it is
// asked for UNDER ITS OWNER, which is what keeps routd.db's own opener from
// growing it (store/entities.go). acl, acl_membership and audit_log are routd's
// own, from routd's migrations (BUGS F44).
func testDBWithInvites(t *testing.T) objstore.Store {
	t.Helper()
	db := routdDB(t)
	d, ok := db.(store.Driver)
	if !ok {
		t.Fatalf("routd's handle is %T, which makes no entities", db)
	}
	if err := store.EnsureEntities(t.Context(), d, store.OwnerOnbod); err != nil {
		t.Fatalf("onbod entities: %v", err)
	}
	return db
}

func TestHandleInvitesEmpty(t *testing.T) {
	db := testDBWithInvites(t)
	d := &dash{routd: db, onbod: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/invites/", nil)
	req.Header.Set("X-User-Sub", "github:operator")
	req.Header.Set("X-User-Groups", `["**"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "No invites") {
		t.Errorf("expected empty-state message, got: %q", body)
	}
	if !strings.Contains(body, "Create invite") {
		t.Errorf("expected create form, got: %q", body)
	}
}

func TestHandleInviteCreateAndList(t *testing.T) {
	db := testDBWithInvites(t)
	// Grant operator (**) admin so requireAdmin passes.
	seedGrant(t, db, core.ACLRow{
		Principal: "github:operator", Action: "admin", Scope: "**",
		Effect: "allow", GrantedBy: "test",
	})

	d := &dash{routd: db, onbod: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	form := url.Values{}
	form.Set("target_glob", "alice")
	form.Set("max_uses", "3")
	req := httptest.NewRequest("POST", "/dash/invites/",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:operator")
	// No Origin header — requireSameOrigin allows missing-origin (curl / server-to-server).
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Create renders the bearer once instead of redirecting — this is the only
	// surface that ever shows it. The DB stores only its hash (I1), so recover
	// it from the rendered link rather than querying for it.
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %q", w.Code, w.Body.String())
	}
	body0 := w.Body.String()
	const marker = "<code>/invite/"
	i := strings.Index(body0, marker)
	if i == -1 {
		t.Fatalf("create page has no invite link: %q", body0)
	}
	rest := body0[i+len(marker):]
	token := rest[:strings.Index(rest, "<")]
	if token == "" {
		t.Fatalf("could not extract token from create page: %q", body0)
	}

	// List page shows the invite by ref, never the bearer.
	req2 := httptest.NewRequest("GET", "/dash/invites/", nil)
	req2.Header.Set("X-User-Sub", "github:operator")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != 200 {
		t.Fatalf("list status = %d", w2.Code)
	}
	body := w2.Body.String()
	if !strings.Contains(body, "alice") {
		t.Errorf("invite target not in list: %q", body)
	}
	if strings.Contains(body, "No invites") {
		t.Errorf("expected non-empty list, got empty-state message")
	}
	if strings.Contains(body, token) {
		t.Errorf("invite bearer rendered on the list page: %q", body)
	}
	if !strings.Contains(body, store.TokenRef(token)) {
		t.Errorf("invite ref missing from the list page: %q", body)
	}
}

func TestHandleInviteCreateForbiddenWithoutAdminGrant(t *testing.T) {
	db := testDBWithInvites(t)
	// No acl row → requireAdmin denies.
	d := &dash{routd: db, onbod: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	form := url.Values{}
	form.Set("target_glob", "bob")
	form.Set("max_uses", "1")
	req := httptest.NewRequest("POST", "/dash/invites/",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:regular")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestHandleInviteRevokeAndGone(t *testing.T) {
	db := testDBWithInvites(t)
	seedGrant(t, db, core.ACLRow{
		Principal: "github:operator", Action: "admin", Scope: "**",
		Effect: "allow", GrantedBy: "test",
	})
	// Seed one invite through its owner, keyed by ref (I1) — token here is just
	// the would-be bearer, never itself persisted (long enough for the [:8]
	// display slice).
	const token = "tok-abcdef0123456789abcdef0123456789"
	if _, err := store.NewOver(db).ImportInvites(t.Context(), []store.ArchiveInviteRow{{
		Ref: store.TokenRef(token), TargetGlob: "carol", IssuedBySub: "github:operator",
		IssuedAt: time.Now().UTC().Format(time.RFC3339), MaxUses: 1,
	}}); err != nil {
		t.Fatal(err)
	}

	d := &dash{routd: db, onbod: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	// The revoke form must address the opaque ref, never the raw bearer token —
	// the token in a URL would leak to request / proxy logs.
	req0 := httptest.NewRequest("GET", "/dash/invites/", nil)
	req0.Header.Set("X-User-Sub", "github:operator")
	w0 := httptest.NewRecorder()
	mux.ServeHTTP(w0, req0)
	ref := store.TokenRef(token)
	list := w0.Body.String()
	if !strings.Contains(list, `action="/dash/invites/`+ref+`/revoke"`) {
		t.Errorf("revoke form does not use the opaque ref: %q", list)
	}
	if strings.Contains(list, `action="/dash/invites/`+token+`/revoke"`) {
		t.Errorf("raw token leaked into the revoke form action")
	}

	req := httptest.NewRequest("POST", "/dash/invites/"+ref+"/revoke", nil)
	req.Header.Set("X-User-Sub", "github:operator")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("revoke status = %d, body = %q", w.Code, w.Body.String())
	}

	// List should be empty.
	req2 := httptest.NewRequest("GET", "/dash/invites/", nil)
	req2.Header.Set("X-User-Sub", "github:operator")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	body := w2.Body.String()
	if strings.Contains(body, token) {
		t.Errorf("revoked token still visible: %q", body)
	}
	if !strings.Contains(body, "No invites") {
		t.Errorf("expected empty-state after revoke: %q", body)
	}
}

// Both invite WRITERS answer 503 when onbod's store is absent. The list page
// banners, and these two took the same handle and called a method on it — so an
// instance running without onboarding met a nil deref on a POST rather than the
// unavailable the read side has always reported.
func TestInviteWritesRefuseWithoutTheOnbodStore(t *testing.T) {
	db := testDBWithInvites(t)
	// routd is there and onbod is not: onboarding is an optional profile, so
	// this is an ordinary instance, not a broken one.
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
	seedAdmin(t, db, "github:operator", "**")
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	for _, c := range []struct{ name, method, path, body string }{
		{"create", "POST", "/dash/invites/", url.Values{"target_glob": {"alice"}}.Encode()},
		{"revoke", "POST", "/dash/invites/deadbeef/revoke", ""},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		if c.body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("X-User-Sub", "github:operator")
		req.Header.Set("X-User-Groups", `["**"]`)
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set("Host", "example.com")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503 — body %q", c.name, w.Code, w.Body.String())
		}
	}
}
