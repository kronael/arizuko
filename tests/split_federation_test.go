package tests

// Cross-daemon split-topology integration: boot authd + routd + runed
// in-process (httptest servers, real per-daemon sqlite) and drive a real
// end-to-end flow with everything wired but the LLM/container.
//
// Topology (project_split_topology): authd is the auth authority (signs +
// serves JWKS); routd owns the conversation plane (routd.db, ingress, routing,
// turn callbacks, the poll loop) and calls runed over the PINNED POST /v1/runs
// contract via the production runedv1.Client; runed is a pure spawner
// (runed.db, Manager, FakeRuntime stands in for docker+agent). Tokens are real
// ES256, minted by the in-process authd and verified by each backend over
// auth.FetchKeys — the SAME path production runs.
//
// What is faked: ONLY the LLM/container turn (FakeRuntime). Everything else —
// JWKS verification, grant resolution, HTTP between routd↔runed, routd's real
// poll loop, the agent's reply callback into routd, sqlite per daemon — is the
// real code.
//
// This closes the coverage gap flagged by the test plan: there was no
// cross-daemon split test booting the federation end-to-end. routd's own
// authd_integration_test wires a stubRunner (an in-process fake that calls
// back directly) and drives processGroupMessages by hand; here routd→runed is
// real HTTP, routd's real poll loop dispatches, and the run callback rides a
// real authd-brokered token back to routd.

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/routd"
	routdv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/runed"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
	"github.com/kronael/arizuko/types"
)

// In the split, an adapter authenticates inbound on routd's /v1/messages with a
// service:<adapter> JWT exchanged from its AUTHD_SERVICE_KEY (spec 5/1) — NOT a
// shared CHANNEL_SECRET. fakeAuthd.mintAdapter mints that token exactly as authd
// does (service typ, the adapter's declared messages:write grant), so this test
// exercises the REAL A1 path: it would catch adapters 401ing on routd ingress.

// fakeAuthd is an in-process authd: one ES256 signing key, GET /v1/keys
// serving its JWKS, and a mint helper. mintUser resolves the FULL grant set
// for a sub from the grants map (the flip-blocker behavior: the token's scope
// is the resolved grants, not a narrow claim) and stamps the bare sub +
// arz/folder. This is exactly what authd.MintForSubject / signMinted do, minus
// the OAuth front-end (authd is package main, so we rebuild the signing
// surface from the importable auth package — the same primitives authd uses).
type fakeAuthd struct {
	key    *auth.SigningKey
	ts     *httptest.Server
	grants map[string][]string // bareSub -> resolved grant scopes (the DB-resolved set)
}

func newFakeAuthd(t *testing.T) *fakeAuthd {
	t.Helper()
	key, err := auth.NewSigningKey("kid-fed")
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	a := &fakeAuthd{key: key, grants: map[string][]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/keys", func(w http.ResponseWriter, _ *http.Request) {
		doc, err := auth.PublicJWKS(key)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(doc)
	})
	a.ts = httptest.NewServer(mux)
	t.Cleanup(a.ts.Close)
	return a
}

func (a *fakeAuthd) URL() string { return a.ts.URL }

// grant records the resolved grant set for a bare sub (the DB resolver's job).
func (a *fakeAuthd) grant(bareSub string, scopes ...string) { a.grants[bareSub] = scopes }

// mintService signs a service token for routd→runed (runs:run/runs:kill);
// service tokens carry no folder claim (unscoped, like routd's own).
func (a *fakeAuthd) mintService(t *testing.T, sub string, scope ...string) string {
	t.Helper()
	tok, err := a.key.Sign(auth.TokenClaims{Sub: sub, Typ: "service", Scope: scope}, time.Hour)
	if err != nil {
		t.Fatalf("sign service token: %v", err)
	}
	return tok
}

// mintAdapter signs a service:<adapter> token carrying messages:write — the
// token a channel adapter exchanges its AUTHD_SERVICE_KEY for (spec 5/1) and
// presents on routd's /v1/messages ingress. Same shape as mintService; named
// for the inbound role so the test reads as the real adapter path.
func (a *fakeAuthd) mintAdapter(t *testing.T, adapter string) string {
	t.Helper()
	return a.mintService(t, "service:"+adapter, "messages:write")
}

// mintUser signs a folder-bound user/agent token. scope is the FULL grant set
// resolved for the sub (flip-blocker: resolve from the DB, not a narrow claim);
// the arz/folder claim narrows the binding but never shrinks the grant set.
func (a *fakeAuthd) mintUser(t *testing.T, sub, folder string) string {
	t.Helper()
	c := auth.TokenClaims{Sub: sub, Typ: "user", Scope: a.grants[sub]}
	if folder != "" {
		c.Extra = map[string]string{"arz/folder": folder}
	}
	tok, err := a.key.Sign(c, time.Hour)
	if err != nil {
		t.Fatalf("sign user token: %v", err)
	}
	return tok
}

// fedVerifier is a routd/runed Verifier pointed at authd's /v1/keys over the
// wire (cold RemoteKeySet) — the exact path keysetVerifier runs in prod.
type fedVerifier struct{ ks *auth.KeySet }

func (v fedVerifier) Verify(r *http.Request) (string, []string, string, error) {
	s, err := auth.VerifyHTTP(r, v.ks)
	if err != nil {
		return "", nil, "", err
	}
	return s.Sub, s.Scope, s.Extra["arz/folder"], nil
}

func newFedVerifier(t *testing.T, a *fakeAuthd) fedVerifier {
	t.Helper()
	ks, err := auth.FetchKeys(context.Background(), a.URL())
	if err != nil {
		t.Fatalf("fetch keys: %v", err)
	}
	return fedVerifier{ks: ks}
}

// fedDeliverer is a no-op egress (the loop's redelivery half). The flow under
// test closes via the agent's reply callback, not adapter egress.
type fedDeliverer struct{}

func (fedDeliverer) Send(_, _, _, _, _ string) (string, error)         { return "platform-fed-1", nil }
func (fedDeliverer) React(_, _, _ string) error                        { return nil }
func (fedDeliverer) Edit(_, _, _ string) error                         { return nil }
func (fedDeliverer) Delete(_, _ string) error                          { return nil }
func (fedDeliverer) Pin(_, _ string) error                             { return nil }
func (fedDeliverer) Unpin(_, _ string, _ bool) error                   { return nil }
func (fedDeliverer) Document(_, _, _, _, _, _ string) (string, error)  { return "platform-fed-doc", nil }
func (fedDeliverer) SendVoice(_, _, _, _ string) (string, error)       { return "platform-fed-voice", nil }
func (fedDeliverer) Post(_, _ string, _ []string) (string, error)      { return "platform-fed-post", nil }
func (fedDeliverer) Forward(_, _, _ string) (string, error)            { return "", nil }
func (fedDeliverer) Quote(_, _, _ string) (string, error)              { return "", nil }
func (fedDeliverer) Repost(_, _ string) (string, error)                { return "", nil }
func (fedDeliverer) Dislike(_, _ string) error                         { return nil }
func (fedDeliverer) SetSuggestions(_ string, _ []core.PanePrompt) error { return nil }
func (fedDeliverer) SetName(_, _ string) error                         { return nil }
func (fedDeliverer) RoundDone(_, _, _, _ string) error                 { return nil }
func (fedDeliverer) FetchHistory(_ string, _ time.Time, _ int) ([]byte, error) {
	return nil, nil
}

// federation bundles the three booted daemons + the test wiring.
type federation struct {
	authd    *fakeAuthd
	routdDB  *routd.DB
	routdSrv *routd.Server
	routdTS  *httptest.Server
	runedDB  *runed.DB
	runedTS  *httptest.Server

	// agent observations: the folder routd dispatched each turn to, and the
	// status routd returned to the agent's reply callback.
	mu          sync.Mutex
	ranFolders  map[string]string // turnID -> routd-dispatched folder
	replyStatus map[string]int    // turnID -> routd reply status
}

// bootFederation stands up authd, runed (real Manager + FakeRuntime), and
// routd (real Loop + real poller calling runed over HTTP via runedv1.Client).
// The FakeRuntime is the only fake: it simulates the agent by posting a reply
// back into routd with a real authd-minted folder-bound token, closing the
// turn end-to-end. Returns f and the running loop's cancel.
func bootFederation(t *testing.T) *federation {
	t.Helper()
	f := &federation{ranFolders: map[string]string{}, replyStatus: map[string]int{}}
	f.authd = newFakeAuthd(t)
	// Grant resolution (the DB resolver authd consults): the agent identity
	// gets the full reply grant set for its folder.
	f.authd.grant("user:agent", "messages:send:own_group", "chats:read:own_group")

	// --- runed (execution plane) ---
	rudb, err := runed.OpenMem()
	if err != nil {
		t.Fatalf("runed.OpenMem: %v", err)
	}
	t.Cleanup(func() { rudb.Close() })
	f.runedDB = rudb

	// The FakeRuntime is the agent: on each run it records the folder routd
	// dispatched to, mints a folder-bound reply token from authd, and posts a
	// reply back to routd over HTTP — the real agent→routd callback, just
	// without an LLM deciding the text.
	rt := runed.FakeRuntime{Fn: func(_ context.Context, spec runed.RunSpec) runed.RunResult {
		f.mu.Lock()
		f.ranFolders[spec.TurnID] = spec.Folder
		f.mu.Unlock()
		replyTok := f.authd.mintUser(t, "user:agent", spec.Folder)
		body, _ := json.Marshal(routdv1.ReplyRequest{JID: spec.ChatJID, Text: "agent says: ack " + spec.TurnID})
		req, _ := http.NewRequest("POST", f.routdTS.URL+"/v1/turns/"+spec.TurnID+"/reply", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+replyTok)
		req.Header.Set("X-Idempotency-Key", "agent-"+spec.TurnID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return runed.RunResult{Outcome: runedv1.OutcomeError, Error: err.Error()}
		}
		defer resp.Body.Close()
		f.mu.Lock()
		f.replyStatus[spec.TurnID] = resp.StatusCode
		f.mu.Unlock()
		return runed.RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "sess-" + spec.TurnID}
	}}
	broker := runed.NewStaticBroker("fed.jws", "jti-fed")
	mgr := runed.NewManager(rudb, rt, broker, runed.ManagerConfig{
		Scopes:   []types.Scope{"messages:send:own_group", "chats:read:own_group"},
		Instance: "fedtest",
	})
	runedSrv := runed.NewServer(mgr, rudb, newFedVerifier(t, f.authd))
	f.runedTS = httptest.NewServer(runedSrv.Handler())
	t.Cleanup(f.runedTS.Close)

	// --- routd (conversation plane) ---
	rodb, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	t.Cleanup(func() { rodb.Close() })
	f.routdDB = rodb

	// routd → runed over REAL HTTP via the production client, carrying routd's
	// service token (runs:run). This is the PINNED POST /v1/runs contract.
	svcTok := f.authd.mintService(t, "service:routd", "runs:run", "runs:kill")
	runedClient := runedv1.NewClient(f.runedTS.URL, svcTok, 10*time.Second)

	loop := routd.NewLoop(rodb, runedClient, routd.LoopConfig{
		InstanceName: "fedtest",
		PollEvery:    20 * time.Millisecond, // fast poll for the test
		RunScopes:    []types.Scope{"messages:send:own_group", "chats:read:own_group"},
	})
	// Inbound auth is the REAL split path: routd's verifier (newFedVerifier over
	// authd's JWKS) gates /v1/messages on a service:<adapter> messages:write JWT.
	// Adapters present a token from mintAdapter — see
	// TestSplitFederation_InboundViaServiceToken.
	f.routdSrv = routd.NewServer(rodb, loop, fedDeliverer{}, newFedVerifier(t, f.authd), 0, "https://fed.test")
	loop.BindServer(f.routdSrv)
	f.routdTS = httptest.NewServer(f.routdSrv.Handler())
	t.Cleanup(f.routdTS.Close)

	// Start routd's REAL poll loop — the production dispatch path (pollOnce →
	// queue → processGroupMessages → runner). Cancelled on test cleanup.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go loop.Run(ctx)
	return f
}

func (f *federation) dispatchedFolder(turnID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ranFolders[turnID]
}

func (f *federation) replyCode(turnID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.replyStatus[turnID]
}

// ---- The end-to-end flow ----

// TestSplitFederation_InboundToTurnRoundTrip drives the full split path:
// authd mints tokens; an adapter posts an inbound to routd over HTTP (real
// JWKS verify + grant gate); routd's REAL poll loop routes + dispatches a run
// to runed over the real POST /v1/runs contract; runed brokers a token, runs
// the (Fake) agent, which posts a reply BACK to routd carrying a real
// authd-minted folder-bound token; routd's turn-callback gate accepts it and
// appends exactly one bot row. Every hop is real but the LLM/container.
func TestSplitFederation_InboundToTurnRoundTrip(t *testing.T) {
	f := bootFederation(t)

	if err := f.routdDB.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	if _, err := f.routdDB.AddRoute(core.Route{Match: "platform=slack", Target: "demo"}); err != nil {
		t.Fatalf("add route: %v", err)
	}

	// Adapter ingests an inbound over HTTP with its service:slakd messages:write
	// JWT — the REAL split path (the adapter exchanges its AUTHD_SERVICE_KEY for
	// this token, spec 5/1). routd's authz gate accepts it as a service caller.
	in := routdv1.Message{ID: "wamid.1", ChatJID: "slack:T/C/U", Sender: "u1", Content: "hello federation", Verb: "message"}
	rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", f.authd.mintAdapter(t, "slakd"), "", in)
	if rec.StatusCode != 200 {
		t.Fatalf("authorized ingest status=%d", rec.StatusCode)
	}
	if !f.routdDB.MessageExists("wamid.1") {
		t.Fatal("inbound not stored after authorized ingest")
	}

	// routd's poll loop routes the chat, dispatches the run to runed over HTTP,
	// runed runs the fake agent, which replies back into routd. Wait for the
	// resulting bot row to land (the turn closing).
	testutils.WaitForRow(t, f.routdDB.SQL(),
		`SELECT COUNT(*) FROM messages WHERE chat_jid=? AND is_bot_message=1`,
		[]any{"slack:T/C/U"}, 5*time.Second)

	if got := f.dispatchedFolder("wamid.1"); got != "demo" {
		t.Fatalf("routd dispatched turn to folder %q, want demo (routd→runed over HTTP)", got)
	}
	if code := f.replyCode("wamid.1"); code != 200 {
		t.Fatalf("agent reply callback into routd = %d, want 200 (folder-bound token accepted)", code)
	}
	if got := countBotRows(t, f.routdDB, "slack:T/C/U"); got != 1 {
		t.Fatalf("bot rows = %d, want 1 (the agent's reply appended exactly once)", got)
	}
	// the appended bot row carries the agent's text.
	testutils.AssertMessage(t, f.routdDB.SQL(), "slack:T/C/U", "ack wamid.1")

	// runed persisted the spawn — the execution plane saw a real run, not a stub.
	testutils.WaitForRow(t, f.runedDB.SQL(), `SELECT COUNT(*) FROM spawns`, nil, 2*time.Second)
}

// TestSplitFederation_InboundViaServiceToken is the A1 POSITIVE: a channel
// adapter's service:<adapter> messages:write JWT is accepted on routd's
// /v1/messages ingress and the inbound is stored. This is the path that 401'd in
// the live split (adapters had no service token); it would catch A1's return.
func TestSplitFederation_InboundViaServiceToken(t *testing.T) {
	f := bootFederation(t)
	if err := f.routdDB.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatalf("put group: %v", err)
	}

	in := routdv1.Message{ID: "svc.1", ChatJID: "slack:T/C/U", Sender: "u1", Content: "via service token", Verb: "message"}
	rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", f.authd.mintAdapter(t, "teled"), "", in)
	if rec.StatusCode != 200 {
		t.Fatalf("ingest with service:teled token = %d, want 200", rec.StatusCode)
	}
	if !f.routdDB.MessageExists("svc.1") {
		t.Fatal("inbound not stored after authorized service-token ingest")
	}
}

// TestSplitFederation_InboundRejectsBadToken is the A1 NEGATIVE across the real
// federation: an inbound POST /v1/messages with a non-JWT bearer, with none, and
// with a well-formed token LACKING messages:write is rejected (401/403) and
// never stored. Only a valid service:<adapter> messages:write JWT gets in.
func TestSplitFederation_InboundRejectsBadToken(t *testing.T) {
	f := bootFederation(t)

	in := routdv1.Message{ID: "bad.1", ChatJID: "slack:T/C/U", Sender: "u1", Content: "no creds", Verb: "message"}
	if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", "not-a-jwt", "", in); rec.StatusCode != 401 {
		t.Fatalf("ingest with bogus bearer = %d, want 401", rec.StatusCode)
	}
	if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", "", "", in); rec.StatusCode != 401 {
		t.Fatalf("ingest with no token = %d, want 401", rec.StatusCode)
	}
	// Well-formed token, wrong scope (no messages:write) → 403, fail-closed.
	weak := f.authd.mintService(t, "service:slakd", "chats:read")
	if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", weak, "", in); rec.StatusCode != 403 {
		t.Fatalf("ingest without messages:write = %d, want 403", rec.StatusCode)
	}
	if f.routdDB.MessageExists("bad.1") {
		t.Fatal("a rejected ingest must not store the inbound row")
	}
}

// TestSplitFederation_ForeignFolderTokenRejected proves the folder-bound grant
// gate bites across the real federation: a leaked agent token for a DIFFERENT
// folder than the turn's cannot drive that turn's reply. authd mints both
// tokens; routd verifies them against the SAME JWKS; only the own-folder token
// is accepted. This is the flip-blocker enforcement end-to-end.
func TestSplitFederation_ForeignFolderTokenRejected(t *testing.T) {
	f := bootFederation(t)

	if _, err := f.routdDB.PutTurnContext("turn-x", "demo", "", "slack:T/C/U", "u1", ""); err != nil {
		t.Fatalf("put turn context: %v", err)
	}
	f.authd.grant("user:agent", "messages:send:own_group")

	// Foreign-folder token: well-formed, correct scope, WRONG folder → 403.
	foreign := f.authd.mintUser(t, "user:agent", "other")
	rec := postBearer(t, f.routdTS.URL, "POST", "/v1/turns/turn-x/reply", foreign, "fk-1",
		routdv1.ReplyRequest{JID: "slack:T/C/U", Text: "leaked"})
	if rec.StatusCode != 403 {
		t.Fatalf("foreign-folder reply status=%d, want 403", rec.StatusCode)
	}
	if countBotRows(t, f.routdDB, "slack:T/C/U") != 0 {
		t.Fatal("a denied callback must not append a bot row")
	}

	// Control: the same identity scoped to the turn's own folder is accepted.
	own := f.authd.mintUser(t, "user:agent", "demo")
	rec2 := postBearer(t, f.routdTS.URL, "POST", "/v1/turns/turn-x/reply", own, "ok-1",
		routdv1.ReplyRequest{JID: "slack:T/C/U", Text: "legit"})
	if rec2.StatusCode != 200 {
		t.Fatalf("own-folder reply status=%d, want 200", rec2.StatusCode)
	}
	if countBotRows(t, f.routdDB, "slack:T/C/U") != 1 {
		t.Fatal("authorized own-folder callback must append exactly one bot row")
	}
}

// TestSplitFederation_RunsDispatchScopeGated proves runed's POST /v1/runs gate
// is live across the federation: routd's service token (runs:run) is accepted,
// but a token lacking runs:run is rejected by runed at 403 — runed is not a
// bare-bearer endpoint. Both verified against the same authd JWKS.
func TestSplitFederation_RunsDispatchScopeGated(t *testing.T) {
	f := bootFederation(t)

	body, _ := json.Marshal(runedv1.RunRequest{
		Folder: "demo", ChatJID: "slack:T/C/U", TurnID: "t-scope",
		MessageBatch: "rendered", CallerSub: "user:agent",
	})

	// (a) routd's real service token (runs:run) is accepted.
	svc := f.authd.mintService(t, "service:routd", "runs:run")
	if rec := postRaw(t, f.runedTS.URL, "/v1/runs", svc, body); rec.StatusCode != 200 {
		t.Fatalf("runs with runs:run = %d, want 200", rec.StatusCode)
	}
	// (b) a well-formed token without runs:run is rejected at runed.
	weak := f.authd.mintService(t, "service:rogue", "messages:write")
	if rec := postRaw(t, f.runedTS.URL, "/v1/runs", weak, body); rec.StatusCode != 403 {
		t.Fatalf("runs without runs:run = %d, want 403 (runed scope gate)", rec.StatusCode)
	}
	// (c) no token at all → 401, fail-closed.
	if rec := postRaw(t, f.runedTS.URL, "/v1/runs", "", body); rec.StatusCode != 401 {
		t.Fatalf("runs with no token = %d, want 401", rec.StatusCode)
	}
}

// TestSplitFederation_StampResolvesFullGrants ties to the auth flip-blocker:
// an ES256 token whose arz/folder claim is one narrow folder still resolves the
// FULL grant set on verify — the token's Scope (authd's resolved grants), not
// the folder claim, defines the capability set. Verified over the real JWKS.
func TestSplitFederation_StampResolvesFullGrants(t *testing.T) {
	f := bootFederation(t)
	v := newFedVerifier(t, f.authd)

	f.authd.grant("user:dev", "messages:send:own_group", "chats:read:own_group", "tasks:read")
	tok := f.authd.mintUser(t, "user:dev", "corp/eng/x") // narrow folder claim

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	sub, scope, folder, err := v.Verify(req)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if sub != "user:dev" {
		t.Fatalf("sub = %q, want user:dev", sub)
	}
	if folder != "corp/eng/x" {
		t.Fatalf("folder claim = %q, want corp/eng/x", folder)
	}
	// The full resolved grant set survives verbatim — NOT shrunk to the folder
	// claim (the flip-blocker: authd's resolved grants define the scope set).
	for _, want := range []string{"messages:send:own_group", "chats:read:own_group", "tasks:read"} {
		if !containsScope(scope, want) {
			t.Errorf("resolved scope %v missing %q (token shrunk to folder claim?)", scope, want)
		}
	}
	// And messages:send:own_group is honored by the scope matcher routd's gate
	// uses (auth.HasScope splits resource:verb and matches the qualified held).
	if !auth.HasScope(scope, "messages", "send:own_group") {
		t.Errorf("HasScope did not honor messages:send:own_group in %v", scope)
	}
}

// TestSplitVsMonolithRoutingParity asserts the split (routd's real poll-loop
// dispatch) and the monolith (gateway via the shared router.ResolveRouteTarget
// over store.AllRoutes — gateway.go:2030) make the SAME routing decision for
// the same input. The split decision is observed end-to-end: which folder
// routd actually dispatched to runed (captured in the FakeRuntime). This pins
// that the two routing paths cannot drift for the canonical cases.
func TestSplitVsMonolithRoutingParity(t *testing.T) {
	cases := []struct {
		name    string
		routes  []core.Route
		chatJID string
		want    string // "" = no route → no dispatch
	}{
		{
			name:    "catch-all",
			routes:  []core.Route{{Match: "", Target: "inbox"}},
			chatJID: "slack:T/C/U",
			want:    "inbox",
		},
		{
			name:    "platform match wins over later catch-all",
			routes:  []core.Route{{Seq: 0, Match: "platform=slack", Target: "eng"}, {Seq: 1, Match: "", Target: "inbox"}},
			chatJID: "slack:T/C/U",
			want:    "eng",
		},
		{
			name:    "room match",
			routes:  []core.Route{{Match: "room=42", Target: "sales"}},
			chatJID: "telegram:42",
			want:    "sales",
		},
		{
			name:    "no matching route",
			routes:  []core.Route{{Match: "platform=discord", Target: "gaming"}},
			chatJID: "slack:T/C/U",
			want:    "", // routd dispatches nothing
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := bootFederation(t)

			// Monolith side: the gateway store over the same routes, decided via
			// the shared router.ResolveRouteTarget (the exact gateway.go call).
			mono := mustMonolithDB(t)
			for _, target := range distinctTargets(tc.routes) {
				if err := f.routdDB.PutGroup(core.Group{Folder: target}); err != nil {
					t.Fatalf("routd put group: %v", err)
				}
				if err := mono.PutGroup(core.Group{Folder: target}); err != nil {
					t.Fatalf("mono put group: %v", err)
				}
			}
			for _, r := range tc.routes {
				if _, err := f.routdDB.AddRoute(r); err != nil {
					t.Fatalf("routd add route: %v", err)
				}
				if _, err := mono.AddRoute(r); err != nil {
					t.Fatalf("mono add route: %v", err)
				}
			}
			monoMsg := core.Message{ChatJID: tc.chatJID, Verb: "message"}
			monoTarget := router.ResolveRouteTarget(monoMsg, mono.AllRoutes()).Folder
			if monoTarget != tc.want {
				t.Fatalf("monolith routed to %q, want %q", monoTarget, tc.want)
			}

			// Split side: ingest over HTTP via the real adapter service-token path,
			// let routd's real poll loop dispatch.
			turnID := "parity-" + tc.name
			in := routdv1.Message{ID: turnID, ChatJID: tc.chatJID, Sender: "u1", Content: "route me", Verb: "message"}
			rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", f.authd.mintAdapter(t, "slakd"), "", in)
			if rec.StatusCode != 200 {
				t.Fatalf("ingest status=%d", rec.StatusCode)
			}

			if tc.want != "" {
				// Expect a dispatch to the parity folder.
				deadline := time.Now().Add(5 * time.Second)
				for f.dispatchedFolder(turnID) == "" && time.Now().Before(deadline) {
					time.Sleep(20 * time.Millisecond)
				}
				splitTarget := f.dispatchedFolder(turnID)
				if splitTarget != tc.want {
					t.Errorf("split dispatched to %q, want %q", splitTarget, tc.want)
				}
				if splitTarget != monoTarget {
					t.Errorf("ROUTING DRIFT: monolith=%q split=%q", monoTarget, splitTarget)
				}
			} else {
				// No route: routd must dispatch nothing. Give the poller a few
				// cycles, then assert no run fired for this turn.
				time.Sleep(200 * time.Millisecond)
				if got := f.dispatchedFolder(turnID); got != "" {
					t.Errorf("split dispatched to %q on a route MISS, want no dispatch (monolith=%q)", got, monoTarget)
				}
			}
		})
	}
}

// --- helpers ---

func postBearer(t *testing.T, base, method, path, token, idemKey string, body any) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(method, base+path, strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idemKey != "" {
		req.Header.Set("X-Idempotency-Key", idemKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func postRaw(t *testing.T, base, path, token string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", base+path, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func countBotRows(t *testing.T, db *routd.DB, jid string) int {
	t.Helper()
	var n int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM messages WHERE chat_jid = ? AND is_bot_message = 1`, jid).Scan(&n); err != nil {
		t.Fatalf("count bot rows: %v", err)
	}
	return n
}

// mustMonolithDB opens an in-memory migrated messages.db for the monolith
// (gateway) side of the parity check.
func mustMonolithDB(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.OpenMem()
	if err != nil {
		t.Fatalf("store.OpenMem: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func containsScope(scope []string, want string) bool {
	for _, s := range scope {
		if s == want {
			return true
		}
	}
	return false
}

func distinctTargets(routes []core.Route) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range routes {
		if r.Target != "" && !seen[r.Target] {
			seen[r.Target] = true
			out = append(out, r.Target)
		}
	}
	return out
}

// anchor the ecdsa import (auth.SigningKey holds *ecdsa.PrivateKey).
var _ = (*ecdsa.PrivateKey)(nil)
