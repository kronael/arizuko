package authstore_test

// The storage layer's own bodies, run against the endpoint dyntest lends each
// test as its own namespace.
//
// They are about the invariants, not the coverage. A refresh token redeemed
// exactly once, a family whose death reaches everything it minted, and one
// signing key that signs — those are the three things authd would be dangerous
// without, and each has a body here that fails when the guard that holds it is
// removed.

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// newStore hands a test an empty authd store in a namespace of its own.
func newStore(t *testing.T) *authstore.Store {
	t.Helper()
	s := dyntest.Store(t)
	for _, entity := range authstore.Entities {
		if err := s.CreateEntity(t.Context(), entity); err != nil {
			t.Fatalf("create entity %s: %v", entity, err)
		}
	}
	return authstore.New(s)
}

// A fresh family reads back with neither tombstone, and the raw secret is the
// only way to address it.
func TestStartRefreshFamilyReadsBackLive(t *testing.T) {
	st := newStore(t)
	raw, err := st.StartRefreshFamily("google:alice", []string{"acme/**", "tasks:read"}, "web", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := st.RefreshTokenBySecret(raw)
	if !ok {
		t.Fatal("the token just issued is not in the store")
	}
	if r.Sub != "google:alice" || r.Aud != "web" {
		t.Fatalf("token reads back as %+v", r)
	}
	if strings.Join(r.Scope, ",") != "acme/**,tasks:read" {
		t.Fatalf("scope reads back as %v", r.Scope)
	}
	if r.Used || r.Revoked {
		t.Fatalf("a fresh token carries a tombstone: used=%v revoked=%v", r.Used, r.Revoked)
	}
	if time.Until(r.Expires) < 50*time.Minute {
		t.Fatalf("expiry %v is not the hour it was issued for", r.Expires)
	}
	if _, ok := st.RefreshTokenBySecret(raw + "x"); ok {
		t.Fatal("a secret that was never issued resolved to a token")
	}
}

// A spent token is REPORTED spent, never hidden. Refusing to find one would
// make reuse indistinguishable from a token that never existed, and those are
// different answers: the first kills a family, the second is a stranger
// presenting a guess.
func TestSpentAndRevokedTokensAreReportedRatherThanHidden(t *testing.T) {
	st := newStore(t)
	raw, err := st.StartRefreshFamily("alice", nil, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	won, err := st.ClaimRefreshToken(raw)
	if err != nil || !won {
		t.Fatalf("the first claim of an unspent token must win: won=%v err=%v", won, err)
	}
	r, ok := st.RefreshTokenBySecret(raw)
	if !ok {
		t.Fatal("a spent token must still be findable; hiding it deletes the evidence of reuse")
	}
	if !r.Used || r.Revoked {
		t.Fatalf("spent token reads used=%v revoked=%v, want used", r.Used, r.Revoked)
	}
	if _, err := st.RevokeFamily(r.Family); err != nil {
		t.Fatal(err)
	}
	r, ok = st.RefreshTokenBySecret(raw)
	if !ok || !r.Used || !r.Revoked {
		t.Fatalf("after the family is killed the token reads ok=%v used=%v revoked=%v",
			ok, r.Used, r.Revoked)
	}
}

// N concurrent redeems of one token elect exactly one winner, and the losers are
// not errors — "someone else claimed it" is the normal outcome of a contended
// claim (BUGS F36, first conjunct).
func TestClaimElectsOneWinnerUnderConcurrency(t *testing.T) {
	st := newStore(t)
	raw, err := st.StartRefreshFamily("alice", nil, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			won, err := st.ClaimRefreshToken(raw)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("a lost claim must be won=false, not an error: %v", err)
				return
			}
			if won {
				wins++
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d of %d concurrent redeems won; a one-time token has one winner", wins, n)
	}
}

// A token whose family died between the caller's lookup and the write must not
// be claimable (BUGS F36, second conjunct). The revoke commits BEFORE the claim
// opens, so it is not an interleave at all — the claim itself has to look.
func TestClaimRefusesATokenWhoseFamilyIsAlreadyDead(t *testing.T) {
	st := newStore(t)
	raw, err := st.StartRefreshFamily("alice", nil, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := st.RefreshTokenBySecret(raw)
	killed, err := st.RevokeFamily(r.Family)
	if err != nil || !killed {
		t.Fatalf("the first revoke of a live family must kill it: killed=%v err=%v", killed, err)
	}
	won, err := st.ClaimRefreshToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	if won {
		t.Fatal("a token in a killed family was claimed; the family conjunct is not holding")
	}
	newRaw, won, err := st.ClaimAndRotateRefresh(raw, r.Family, r.Sub, nil, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if won || newRaw != "" {
		t.Fatalf("a killed family minted a successor: won=%v raw=%q", won, newRaw)
	}
}

// Rotation spends the presented token and mints its successor together. The
// successor belongs to the same family, and the family's counters move with it.
func TestRotationSpendsAndMintsTogether(t *testing.T) {
	st := newStore(t)
	raw, err := st.StartRefreshFamily("alice", []string{"a:read"}, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := st.RefreshTokenBySecret(raw)
	next, won, err := st.ClaimAndRotateRefresh(raw, r.Family, r.Sub, []string{"b:read"}, "", time.Hour)
	if err != nil || !won {
		t.Fatalf("rotation of a live token must win: won=%v err=%v", won, err)
	}
	spent, ok := st.RefreshTokenBySecret(raw)
	if !ok || !spent.Used {
		t.Fatalf("the presented token was not spent: ok=%v used=%v", ok, spent.Used)
	}
	successor, ok := st.RefreshTokenBySecret(next)
	if !ok {
		t.Fatal("the successor is not in the store")
	}
	if successor.Family != r.Family {
		t.Fatalf("the successor joined family %s, not %s", successor.Family, r.Family)
	}
	if successor.Used || successor.Revoked {
		t.Fatal("a fresh successor carries a tombstone")
	}
	if strings.Join(successor.Scope, ",") != "b:read" {
		t.Fatalf("the successor kept scope %v; the rotation re-snapshots it", successor.Scope)
	}
	// Killing the family reaches the successor, because the successor IS a
	// member of it. That is what stops a credential outliving the kill.
	if _, err := st.RevokeFamily(r.Family); err != nil {
		t.Fatal(err)
	}
	if s2, _ := st.RefreshTokenBySecret(next); !s2.Revoked {
		t.Fatal("the successor survived the kill of the family it belongs to (BUGS F36)")
	}
}

// Revocation reports whether it KILLED something. Re-revoking a dead family
// legitimately kills nothing, and the callers turn that into a 404 rather than
// into a success an operator would misread during an incident.
func TestRevokeReportsWhetherItKilledAnything(t *testing.T) {
	st := newStore(t)
	raw, err := st.StartRefreshFamily("alice", nil, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := st.RefreshTokenBySecret(raw)
	if killed, err := st.RevokeFamily(r.Family); err != nil || !killed {
		t.Fatalf("first revoke: killed=%v err=%v", killed, err)
	}
	if killed, err := st.RevokeFamily(r.Family); err != nil || killed {
		t.Fatalf("re-revoking a dead family reported killed=%v err=%v, want false", killed, err)
	}
	if killed, err := st.RevokeFamily("no-such-family"); err != nil || killed {
		t.Fatalf("revoking a family that never existed reported killed=%v err=%v", killed, err)
	}
}

// One row per family however many times it has rotated, with the rotation count
// and the start instant the correlated subqueries used to compute. Newest
// renewal first, and the sub filter narrows without leaking the other principal.
func TestSessionsAreOneRowPerFamily(t *testing.T) {
	st := newStore(t)
	raw, err := st.StartRefreshFamily("alice", []string{"a:read"}, "web", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	fam, _ := st.RefreshTokenBySecret(raw)
	for i := range 3 {
		next, won, err := st.ClaimAndRotateRefresh(raw, fam.Family, "alice", []string{"a:read"}, "web", time.Hour)
		if err != nil || !won {
			t.Fatalf("rotation %d: won=%v err=%v", i, won, err)
		}
		raw = next
	}
	if _, err := st.StartRefreshFamily("bob", nil, "", time.Hour); err != nil {
		t.Fatal(err)
	}
	rows, err := st.SessionsByFamily("", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("2 logins and 3 rotations produced %d rows, want one per family: %+v", len(rows), rows)
	}
	var alice int
	for i, r := range rows {
		if r.Sub != "alice" {
			continue
		}
		alice = i
	}
	got := rows[alice]
	if got.FamilyID != fam.Family {
		t.Fatalf("alice's family is %q, want %q", got.FamilyID, fam.Family)
	}
	if got.Rotations != 4 {
		t.Fatalf("rotations = %d, want 4 (1 issue + 3 refreshes)", got.Rotations)
	}
	if got.StartedAt == "" || got.RenewedAt == "" || got.ExpiresAt == "" {
		t.Fatalf("lifecycle timestamps missing: %+v", got)
	}
	if got.StartedAt > got.RenewedAt {
		t.Fatalf("the family started at %s and was renewed at %s", got.StartedAt, got.RenewedAt)
	}
	if got.Status != "active" {
		t.Fatalf("status = %q, want active", got.Status)
	}
	only, err := st.SessionsByFamily("bob", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].Sub != "bob" {
		t.Fatalf("the sub filter returned %+v, want bob alone", only)
	}
	// Revoked outranks the clock: how a session ended is what an incident
	// review asks.
	if _, err := st.RevokeFamily(fam.Family); err != nil {
		t.Fatal(err)
	}
	rows, err = st.SessionsByFamily("alice", 0, time.Now())
	if err != nil || len(rows) != 1 {
		t.Fatalf("alice's family after the kill: %+v (%v)", rows, err)
	}
	if rows[0].Status != "revoked" {
		t.Fatalf("status = %q, want revoked", rows[0].Status)
	}
}

// The signing slot is the trust root: one item names one kid, so a rotation
// moves the signer and the outgoing key keeps verifying inside its window.
func TestRotationMovesTheSlotAndRetiresTheOutgoingKey(t *testing.T) {
	st := newStore(t)
	first, err := auth.NewSigningKey("1000000000-aaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RotateActiveKey(first, time.Now()); err != nil {
		t.Fatal(err)
	}
	second, _ := auth.NewSigningKey("2000000000-bbbbbbbb")
	at := time.Now()
	if err := st.RotateActiveKey(second, at); err != nil {
		t.Fatal(err)
	}
	keys, err := st.SigningKeysWithMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("two rotations left %d keys", len(keys))
	}
	active := map[string]bool{}
	for _, k := range keys {
		active[k.Key.Kid] = k.Active
		if k.Key.Kid == first.Kid && k.RetiredAt == nil {
			t.Fatal("the outgoing key was not retired")
		}
		if k.Key.Kid == second.Kid && k.RetiredAt != nil {
			t.Fatal("the incoming key was retired on arrival")
		}
	}
	if active[first.Kid] || !active[second.Kid] {
		t.Fatalf("the slot names the wrong key: %v", active)
	}

	// The metadata read answers the operator's question and leaves the private
	// half in the engine — the projection IS the boundary.
	rows, err := st.SigningKeyMetadata(time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Kid != second.Kid {
		t.Fatalf("metadata is %+v; kid order must be newest first", rows)
	}
	if rows[0].Status != "active" || rows[1].Status != "retiring" {
		t.Fatalf("statuses are %q and %q, want active then retiring", rows[0].Status, rows[1].Status)
	}
	long, err := st.SigningKeyMetadata(time.Hour, time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if long[1].Status != "retired" {
		t.Fatalf("past its window the retired key reads %q, want retired", long[1].Status)
	}
}

// Emergency revoke empties the slot and backdates the retirement, and the
// rotation that follows it installs a signer into the empty slot.
func TestRetireActiveKeysEmptiesTheSlot(t *testing.T) {
	st := newStore(t)
	k, _ := auth.NewSigningKey("1000000000-aaaaaaaa")
	if err := st.RotateActiveKey(k, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.RetireActiveKeys(time.Now().Add(-2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	keys, err := st.SigningKeysWithMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Active {
		t.Fatalf("after an emergency revoke the keys are %+v; none may be active", keys)
	}
	// Retiring again is a no-op rather than a failure: there is nothing left to
	// retire, which is the answer the set-wide UPDATE gave by touching no row.
	if err := st.RetireActiveKeys(time.Now()); err != nil {
		t.Fatalf("retiring an empty slot must not fail: %v", err)
	}
	next, _ := auth.NewSigningKey("2000000000-bbbbbbbb")
	if err := st.RotateActiveKey(next, time.Now()); err != nil {
		t.Fatalf("a rotation into an empty slot must succeed: %v", err)
	}
	keys, _ = st.SigningKeysWithMaterial()
	signers := 0
	for _, k := range keys {
		if k.Active {
			signers++
		}
	}
	if signers != 1 {
		t.Fatalf("%d keys are active, want exactly 1", signers)
	}
}

// A login resolves to the account that linked it, with every sub that account
// holds — and an identity another account already owns is refused rather than
// merged (spec 5/1 § Account linking).
func TestIdentityResolvesAndCannotBeStolen(t *testing.T) {
	st := newStore(t)
	if err := st.LinkOAuthIdentity("google:alice", "Alice", "google", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := st.LinkOAuthIdentity("google:alice", "Alice", "github", "a1"); err != nil {
		t.Fatal(err)
	}
	owner, name, created, subs, ok := st.OAuthIdentityForSub("github:a1")
	if !ok || owner != "google:alice" || name != "Alice" || created == "" {
		t.Fatalf("alias resolves to (%q, %q, %q, ok=%v)", owner, name, created, ok)
	}
	if strings.Join(subs, " ") != "github:a1 google:alice" {
		t.Fatalf("the account lists %v; the order is by provider", subs)
	}
	if _, _, _, _, ok := st.OAuthIdentityForSub("github:nobody"); ok {
		t.Fatal("an unlinked sub resolved to an account")
	}
	if _, _, _, _, ok := st.OAuthIdentityForSub("no-colon"); ok {
		t.Fatal("a sub that is not <provider>:<id> resolved to an account")
	}
	// Mallory cannot take Alice's github identity by linking it to her own
	// account.
	if err := st.LinkOAuthIdentity("google:mallory", "Mallory", "github", "a1"); err == nil {
		t.Fatal("linking an identity another account owns must fail, not merge")
	}
	owner, _, _, _, _ = st.OAuthIdentityForSub("github:a1")
	if owner != "google:alice" {
		t.Fatalf("the refused link moved the identity to %q", owner)
	}
}

// Import is the one-way move: the legacy rows become items, and the family's two
// numbers are derived from the tokens that used to be the only record of them.
func TestImportDerivesFamiliesFromTokens(t *testing.T) {
	st := newStore(t)
	k, _ := auth.NewSigningKey("1700000000-cafebabe")
	privPEM, pubPEM, err := authstore.EncodeKey(k.Priv)
	if err != nil {
		t.Fatal(err)
	}
	snap := authstore.Snapshot{
		Keys: []authstore.KeyRow{{
			Kid: k.Kid, PrivPEM: privPEM, PubPEM: pubPEM,
			CreatedAt: "2026-06-06T22:34:21Z", Active: true,
		}},
		Users: []authstore.UserRow{{
			UserID: "google:alice", Name: "Alice", CreatedAt: "2026-06-16T11:33:44Z",
		}},
		Identities: []authstore.IdentityRow{{
			UserID: "google:alice", Provider: "google", ProviderSub: "alice",
			LinkedAt: "2026-06-16T11:33:44Z",
		}},
		Tokens: []authstore.TokenRow{
			{
				TokenHash: "h1", FamilyID: "fam", Sub: "google:alice", Scope: "**",
				IssuedAt: "2026-06-16T15:32:02Z", ExpiresAt: "2026-07-16T15:32:02Z",
				UsedAt: "2026-06-16T15:32:11Z",
			},
			{
				TokenHash: "h2", FamilyID: "fam", Sub: "google:alice", Scope: "**",
				IssuedAt: "2026-06-16T15:32:11Z", ExpiresAt: "2026-07-16T15:32:11Z",
			},
		},
	}
	counts, err := st.Import(snap)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Keys != 1 || counts.Slot != 1 || counts.Users != 1 ||
		counts.Identities != 1 || counts.Tokens != 2 || counts.Families != 1 {
		t.Fatalf("import wrote %+v", counts)
	}
	rows, err := st.SessionsByFamily("", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("the two tokens produced %d sessions, want 1 family", len(rows))
	}
	got := rows[0]
	if got.Rotations != 2 {
		t.Fatalf("rotations = %d, want the 2 tokens the family holds", got.Rotations)
	}
	if got.StartedAt != "2026-06-16T15:32:02Z" {
		t.Fatalf("started_at = %q, want the earliest issue", got.StartedAt)
	}
	if got.RenewedAt != "2026-06-16T15:32:11Z" || got.ExpiresAt != "2026-07-16T15:32:11Z" {
		t.Fatalf("the family took its lifecycle from the wrong token: %+v", got)
	}
	// The imported family expired on 2026-07-16, and the status is derived from
	// the clock the caller hands in rather than from a stored word.
	if got.Status != "expired" {
		t.Fatalf("status at now = %q, want expired", got.Status)
	}
	early, err := st.SessionsByFamily("", 0, time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if early[0].Status != "active" {
		t.Fatalf("status inside its window = %q, want active", early[0].Status)
	}
	keys, err := st.SigningKeysWithMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || !keys[0].Active || keys[0].Key.Kid != k.Kid {
		t.Fatalf("the imported key is %+v", keys)
	}
	owner, _, _, _, ok := st.OAuthIdentityForSub("google:alice")
	if !ok || owner != "google:alice" {
		t.Fatalf("the imported identity resolves to %q (ok=%v)", owner, ok)
	}
	// A re-run rewrites the same items rather than failing, so an import that
	// died part way is finishable.
	if _, err := st.Import(snap); err != nil {
		t.Fatalf("a second import must be a no-op, not a failure: %v", err)
	}
}

// Import refuses what the entities cannot represent rather than choosing which
// credential to call live, or which key to call the signer. "0 rows matched" is
// a red flag in a rehearsal; so is a silently repaired one.
func TestImportRefusesWhatItCannotRepresent(t *testing.T) {
	st := newStore(t)
	twoSigners := authstore.Snapshot{Keys: []authstore.KeyRow{
		{Kid: "a", PrivPEM: "x", PubPEM: "y", CreatedAt: "t", Active: true},
		{Kid: "b", PrivPEM: "x", PubPEM: "y", CreatedAt: "t", Active: true},
	}}
	if _, err := st.Import(twoSigners); err == nil {
		t.Fatal("two active keys must be refused; the slot holds one")
	}
	forked := authstore.Snapshot{Tokens: []authstore.TokenRow{
		{TokenHash: "h1", FamilyID: "fam", Sub: "a", IssuedAt: "t1", ExpiresAt: "t9"},
		{TokenHash: "h2", FamilyID: "fam", Sub: "a", IssuedAt: "t2", ExpiresAt: "t9"},
	}}
	if _, err := st.Import(forked); err == nil {
		t.Fatal("a family with two unspent tokens must be refused; it has two live credentials")
	}
	allSpent := authstore.Snapshot{Tokens: []authstore.TokenRow{
		{TokenHash: "h1", FamilyID: "fam", Sub: "a", IssuedAt: "t1", ExpiresAt: "t9", UsedAt: "t2"},
	}}
	if _, err := st.Import(allSpent); err == nil {
		t.Fatal("a family with no unspent token must be refused; it has no current row")
	}
	// Anti-vacuity: a refusal writes nothing at all.
	rows, err := st.SessionsByFamily("", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a refused import left %d families behind", len(rows))
	}
}

// A refused import and a refused claim are different answers from a store that
// is working. A store that cannot answer at all is an error, and this is the
// body that says so: every read below runs against entities that do not exist.
func TestAMissingEntityIsAnErrorRatherThanAnEmptyAnswer(t *testing.T) {
	st := authstore.New(dyntest.Store(t))
	if _, err := st.SigningKeysWithMaterial(); err == nil {
		t.Fatal("reading keys from a store with no entities must fail")
	}
	if _, err := st.SessionsByFamily("", 0, time.Now()); err == nil {
		t.Fatal("listing sessions from a store with no entities must fail")
	}
	if _, err := st.StartRefreshFamily("alice", nil, "", time.Hour); err == nil {
		t.Fatal("issuing into a store with no entities must fail")
	}
	if _, _, _, _, ok := st.OAuthIdentityForSub("google:alice"); ok {
		t.Fatal("a store with no entities resolved an identity")
	}
	if _, ok := st.RefreshTokenBySecret("anything"); ok {
		t.Fatal("a store with no entities found a token")
	}
}
