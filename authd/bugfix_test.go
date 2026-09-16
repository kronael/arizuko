package main

// Regression tests for the authd/auth bug-hunt fixes (specs/5/1, bugs.md
// "Codex oracle deeper pass"). Each test fails if its fix is reverted:
//   - refresh rotation race  (atomic compare-and-set claim of a one-time token)
//   - rotation atomicity     (exactly one active signing key under concurrency)
//   - retired-key forgery    (iat > retired_at rejected even inside the window)
//   - kid scheme             (sortable "<unix>-<hex>", not uuid)
//   - unbounded request body (MaxBytesReader on the token endpoints)

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/authd/authstore"
)

// recordingGrants captures the principal FetchGrants is queried with so a test
// can assert the refresh path passes the full canonical sub, not a re-stripped
// one.
type recordingGrants struct {
	queried string
	snap    GrantsSnapshot
}

func (g *recordingGrants) FetchGrants(_ context.Context, principal string) (GrantsSnapshot, error) {
	g.queried = principal
	return g.snap, nil
}

// Bug 1: refresh for an OAuth sub "google:<id>" must query grants with the FULL
// canonical principal "google:<id>" (9-acl-unified.md), not a re-stripped "<id>".
// refresh_tokens stores the sub bare, and for an OAuth identity the bare
// canonical form already carries the "google:" provider prefix; re-stripping at
// ":" queried the wrong principal → empty scope on every refreshed token.
func TestRefreshUsesFullOAuthPrincipal(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	g := &recordingGrants{snap: GrantsSnapshot{Scope: []string{"tasks:read", "tasks:write"}}}
	a.grants = g

	// issueSession stores the bare canonical sub; for OAuth that is "google:<id>".
	const oauthSub = "google:114019283746"
	r0, err := a.IssueRefresh(oauthSub, []string{"tasks:read", "tasks:write"}, "")
	if err != nil {
		t.Fatal(err)
	}

	access, _, err := a.Refresh(context.Background(), r0)
	if err != nil {
		t.Fatal(err)
	}
	if g.queried != oauthSub {
		t.Fatalf("refresh must query grants with the full canonical principal %q, got %q", oauthSub, g.queried)
	}
	sub, err := auth.VerifyToken(access, a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if !auth.HasScope(sub.Scope, "tasks", "read") || !auth.HasScope(sub.Scope, "tasks", "write") {
		t.Fatalf("refreshed token must preserve the snapshot scope, got %v", sub.Scope)
	}
	if sub.Sub != "user:"+oauthSub {
		t.Fatalf("refreshed access sub = %q want user:%s", sub.Sub, oauthSub)
	}
}

// Bug 2: authd records into auth.db, which has the audit entity only because
// authd's own opener asks the driver for it — no migration in this daemon
// creates one. An emitted event must persist rather than being warned about and
// dropped.
func TestAuditLandsInAuthDB(t *testing.T) {
	db := testDB(t)
	audit.Init(db, "test")
	id := audit.Emit(context.Background(), audit.Event{
		Category: audit.CategorySystem,
		Action:   "daemon.start",
		Actor:    "system",
		Outcome:  audit.OutcomeOK,
	})
	if id == "" {
		t.Fatal("audit emit returned no id — the event was dropped")
	}
	rows, err := audit.Query(context.Background(), db, audit.Filter{})
	if err != nil {
		t.Fatalf("audit row not persisted in auth.db: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id || rows[0].Action != "daemon.start" {
		t.Fatalf("auth.db holds %+v, want the daemon.start row %s", rows, id)
	}
}

// One refresh token redeemed by many goroutines must yield EXACTLY ONE
// successor; the losers are a reuse signal that kills the whole family. Without
// the atomic used_at compare-and-set, multiple goroutines read the row before
// any writes used_at and several rotate → forked live chains, reuse never
// fires.
func TestRefreshRotationRaceSingleWinner(t *testing.T) {
	db := testDB(t)
	a, err := newAuthd(db, 15*time.Minute, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r0, err := a.IssueRefresh("user:1", []string{"tasks:read"}, "")
	if err != nil {
		t.Fatal(err)
	}

	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners int
	var successors []string
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			_, newRefresh, err := a.Refresh(context.Background(), r0)
			if err == nil {
				mu.Lock()
				winners++
				successors = append(successors, newRefresh)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if winners != 1 {
		t.Fatalf("exactly one concurrent redeem must win, got %d", winners)
	}
	// The lost races flagged reuse → the family (incl. the lone successor) is
	// revoked, so the successor is dead.
	if _, _, err := a.Refresh(context.Background(), successors[0]); err == nil {
		t.Fatal("successor must be revoked after a concurrent-reuse family kill")
	}
}

// ClaimRefreshToken is the atomic primitive behind the race fix: N concurrent
// claims of one token, exactly one wins (rows-affected == 1). This is
// deterministic regardless of goroutine scheduling — the SQL `used_at IS NULL`
// guard makes it so. A regression to an unconditional UPDATE would let every
// caller "win".
func TestMarkRefreshUsedAtomicSingleWinner(t *testing.T) {
	db := testDB(t)
	raw, err := newTestStore(db).StartRefreshFamily("user:1", []string{"a:read"}, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var wins int
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			won, err := newTestStore(db).ClaimRefreshToken(raw)
			if err != nil {
				return
			}
			if won {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("compare-and-set must elect exactly one winner, got %d", wins)
	}
}

// Concurrent Rotate() must leave EXACTLY ONE active key. The retire+install
// batch is conditioned on the kid the new key replaces, so a losing rotation
// writes nothing at all — no second signer, no split authority at the trust
// root.
func TestRotateConcurrentSingleActiveKey(t *testing.T) {
	db := testDB(t)
	a, err := newAuthd(db, 15*time.Minute, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			_ = a.Rotate() // a lost race may error; the invariant is checked below
		}()
	}
	wg.Wait()

	keys, err := newTestStore(db).SigningKeyMetadata(time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, k := range keys {
		if k.Active {
			active++
		}
	}
	if len(keys) < 2 {
		t.Fatalf("8 rotations left %d keys; at least one must have landed beside the boot key", len(keys))
	}
	if active != 1 {
		t.Fatalf("exactly one active signing key required after concurrent rotate, got %d of %d", active, len(keys))
	}
	// A losing rotation must write NOTHING, which is what the unique index gave
	// by rolling the loser's whole transaction back. The slot write is
	// conditioned on the kid it replaces, so a loser's new key never lands —
	// and the evidence is that every stored key is either the one in the slot
	// or a retired one. A key with neither is private key material nobody can
	// use and no retirement sweep will ever collect.
	for _, k := range keys {
		if !k.Active && k.RetiredAt == "" {
			t.Fatalf("key %s holds no slot and carries no retirement: a losing rotation "+
				"left its key material behind (%d keys total)", k.Kid, len(keys))
		}
	}
	if err := a.reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.activeKey(); err != nil {
		t.Fatalf("authd must still have an active signer: %v", err)
	}
}

// A second signing key written straight into the store cannot become a second
// signer. "Exactly one active key" is no longer a partial-unique index on a
// column a writer could set — it is the signing SLOT, one item naming one kid,
// so two active keys are not a state the store can hold. The adversarial write
// below is the strongest form: it bypasses every method authstore offers, and
// the key it plants still signs nothing.
func TestSecondKeyCannotBecomeASecondSigner(t *testing.T) {
	db := testDB(t)
	a, err := newAuthd(db, 15*time.Minute, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	booted, err := a.activeKey()
	if err != nil {
		t.Fatal(err)
	}
	k, _ := auth.NewSigningKey(genKid(time.Now().Add(time.Hour)))
	privPEM, pubPEM, _ := authstore.EncodeKey(k.Priv)
	putItem(t, db, authstore.SigningKeys, k.Kid, map[string]string{
		"priv_pem": privPEM, "pub_pem": pubPEM,
		"created_at": time.Now().Format(time.RFC3339),
	})

	// Anti-vacuity: the planted key IS in the store, so what follows is about
	// the slot rather than about a write that never landed.
	if _, ok := entityItem(t, db, authstore.SigningKeys, k.Kid); !ok {
		t.Fatal("the planted key is not in the store; the assertions below prove nothing")
	}
	if err := a.reload(); err != nil {
		t.Fatal(err)
	}
	if kid := activeKid(t, db); kid != booted.Kid {
		t.Fatalf("the slot names %q, want the boot key %q", kid, booted.Kid)
	}
	now, err := a.activeKey()
	if err != nil || now.Kid != booted.Kid {
		t.Fatalf("the signer moved to %v (%v); a planted key must not sign", now, err)
	}
	// It does not verify either: a key that holds no slot and carries no
	// retirement is outside the serving window, so it is not even a verifier.
	if _, serving := a.PublicKeys()[k.Kid]; serving {
		t.Fatal("a planted key entered the serving set")
	}
}

// A token minted by a key AFTER that key is retired (a thief with the stolen
// retired private key) must be rejected by authd's own verifier, even while the
// key is still inside its serving window for legit pre-retirement tokens. The
// retired_at is set to an hour ago directly so the test does not race the
// second-resolution clock.
func TestRetiredKeyForgeryRejectedThroughAuthd(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db) // maxAccessTTL = 1h, so a recently-retired key still serves

	stolen, _ := a.activeKey() // the key a thief steals
	if err := a.Rotate(); err != nil {
		t.Fatal(err)
	}
	// Backdate the now-retired key's retired_at a few seconds into the past:
	// inside the 1h serving window (still serves legit pre-retire tokens) yet
	// unambiguously before a token minted NOW, despite second-resolution times.
	mergeAttrs(t, db, authstore.SigningKeys, stolen.Kid, map[string]string{
		"retired_at": time.Now().Add(-5 * time.Second).Format(time.RFC3339),
	})
	if err := a.reload(); err != nil {
		t.Fatal(err)
	}
	if len(a.PublicKeys()) != 2 {
		t.Fatalf("expected old+new keys serving, got %d", len(a.PublicKeys()))
	}
	// Thief mints a FRESH token with the retired key (iat = now > retired_at).
	forged, err := stolen.Sign(auth.TokenClaims{Sub: "user:evil", Typ: "user", Scope: []string{"tasks:write"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.VerifyToken(forged, a.LocalKeySet()); err == nil {
		t.Fatal("token forged with a retired key (iat past retirement) must be rejected")
	}
}

// kid is the sortable "<created-unix>-<8 hex>" scheme: lexical order == creation
// order. A uuid kid (the regression) sorts randomly.
func TestKidSchemeSortableByCreation(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	k0 := genKid(base)
	k1 := genKid(base.Add(time.Second))
	k2 := genKid(base.Add(2 * time.Second))
	got := []string{k2, k0, k1}
	sort.Strings(got)
	if got[0] != k0 || got[1] != k1 || got[2] != k2 {
		t.Fatalf("kids must sort by creation time, got %v", got)
	}
	// Shape: "<unix>-<8 hex>".
	prefix, hexPart, ok := strings.Cut(k0, "-")
	if !ok || prefix != "1700000000" || len(hexPart) != 8 {
		t.Fatalf("kid not <unix>-<8hex>: %q", k0)
	}
}

// An oversized body on /v1/service-token is refused (MaxBytesReader), not
// buffered into memory.
func TestServiceTokenRejectsOversizedBody(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a, serviceSecrets: map[string]string{"boot": "service:timed"}}
	ts := httptest.NewServer(srv.mux(nil))
	defer ts.Close()

	huge := `{"key":"` + strings.Repeat("a", maxBodyBytes+1024) + `"}`
	resp, err := http.Post(ts.URL+"/v1/service-token", "application/json", bytes.NewReader([]byte(huge)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("oversized service-token body must be rejected, got %d", resp.StatusCode)
	}
}

// Same guard on /v1/refresh.
func TestRefreshRejectsOversizedBody(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux(nil))
	defer ts.Close()

	huge := `{"refresh_token":"` + strings.Repeat("a", maxBodyBytes+1024) + `"}`
	resp, err := http.Post(ts.URL+"/v1/refresh", "application/json", bytes.NewReader([]byte(huge)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("oversized refresh body must be rejected, got %d", resp.StatusCode)
	}
}
