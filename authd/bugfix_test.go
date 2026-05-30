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
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	_ "modernc.org/sqlite"
)

// concurrentDB opens a real file-backed WAL DB so goroutines genuinely run in
// parallel (a shared-cache memory DB needs MaxOpenConns(1), which serializes
// callers and would hide a race). WAL + busy_timeout lets racing writers wait
// on the write lock rather than erroring, so the test observes the fix's
// invariant, not transient lock noise.
func concurrentDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(on)&_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// One refresh token redeemed by many goroutines must yield EXACTLY ONE
// successor; the losers are a reuse signal that kills the whole family. Without
// the atomic used_at compare-and-set, multiple goroutines lookupRefresh before
// any writes used_at and several rotate → forked live chains, reuse never
// fires.
func TestRefreshRotationRaceSingleWinner(t *testing.T) {
	db := concurrentDB(t)
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
	for i := 0; i < n; i++ {
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

// markRefreshUsed is the atomic primitive behind the race fix: N concurrent
// claims of one token, exactly one wins (rows-affected == 1). This is
// deterministic regardless of goroutine scheduling — the SQL `used_at IS NULL`
// guard makes it so. A regression to an unconditional UPDATE would let every
// caller "win".
func TestMarkRefreshUsedAtomicSingleWinner(t *testing.T) {
	db := concurrentDB(t)
	raw, err := issueRefresh(db, "user:1", []string{"a:read"}, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var wins int
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			won, err := markRefreshUsed(db, raw)
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

// Concurrent Rotate() must leave EXACTLY ONE active key. The retire+insert
// transaction plus the partial-unique index (migration 0002) prevent two
// active signers (split authority at the trust root).
func TestRotateConcurrentSingleActiveKey(t *testing.T) {
	db := concurrentDB(t)
	a, err := newAuthd(db, 15*time.Minute, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = a.Rotate() // a lost race may error; the invariant is checked below
		}()
	}
	wg.Wait()

	var active int
	if err := db.QueryRow(`SELECT COUNT(*) FROM signing_keys WHERE active = 1`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("exactly one active signing key required after concurrent rotate, got %d", active)
	}
	if err := a.reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.activeKey(); err != nil {
		t.Fatalf("authd must still have an active signer: %v", err)
	}
}

// The one-active-key index also rejects a second active row inserted directly —
// the structural guarantee the rotation tx relies on.
func TestOneActiveKeyIndexEnforced(t *testing.T) {
	db := testDB(t)
	if _, err := newAuthd(db, 15*time.Minute, time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
	k, _ := auth.NewSigningKey(genKid(time.Now()))
	privPEM, pubPEM, _ := encodeKey(k.Priv)
	_, err := db.Exec(
		`INSERT INTO signing_keys (kid, priv_pem, pub_pem, active, created_at) VALUES (?, ?, ?, 1, ?)`,
		k.Kid, privPEM, pubPEM, now())
	if err == nil {
		t.Fatal("inserting a second active key must violate the unique index")
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
	if _, err := db.Exec(`UPDATE signing_keys SET retired_at = ? WHERE kid = ?`,
		time.Now().Add(-5*time.Second).Format(time.RFC3339), stolen.Kid); err != nil {
		t.Fatal(err)
	}
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
	ts := httptest.NewServer(srv.mux())
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
	ts := httptest.NewServer(srv.mux())
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
