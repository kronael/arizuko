package tests

// Cross-daemon split-topology integration: boot authd + routd + runed in-process
// (httptest servers, real per-daemon sqlite) and drive a real end-to-end flow with
// everything wired but the LLM/container.
//
// authd signs and serves JWKS; routd owns the conversation plane and calls runed over
// the PINNED POST /v1/runs contract via the production runedv1.Client; runed is a pure
// spawner with FakeRuntime standing in for docker+agent. Tokens are real ES256, minted
// by the in-process authd and verified over auth.FetchKeys — the SAME path production
// runs.
//
// ONLY the LLM/container turn is faked. routd's own authd_integration_test wires an
// in-process stubRunner and drives processGroupMessages by hand; here routd→runed is
// real HTTP, routd's real poll loop dispatches, and the agent's reply rides the
// per-turn MCP socket routd serves — the same path a spawned agent drives.

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/routd"
	routdv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/runed"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

func installCoreFixture(t *testing.T, db *routd.DB, dataDir string) {
	t.Helper()
	manifest, skills, err := container.InstallPackage(dataDir, "../ant", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutInstalledPackage(routd.InstalledPackage{
		Name: manifest.Name, Source: "ant", Revision: "release",
		Manifest: map[string][]string{"skill": skills},
	}); err != nil {
		t.Fatal(err)
	}
}

// In the split, an adapter authenticates inbound on routd's /v1/messages with a
// service:<adapter> JWT exchanged from its AUTHD_SERVICE_KEY (spec 5/1) — NOT a
// shared CHANNEL_SECRET. fakeAuthd.mintAdapter mints that token exactly as authd
// does (service typ, the adapter's declared messages:write grant), so this test
// exercises the REAL A1 path: it would catch adapters 401ing on routd ingress.

// fakeAuthd is an in-process authd: one ES256 signing key, GET /v1/keys serving its
// JWKS, and a mint helper. mintUser resolves the FULL grant set for a sub — the token's
// scope is the resolved grants, not a narrow claim — and stamps the bare sub +
// arz/folder, exactly as authd.MintForSubject does minus the OAuth front-end.
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

func (fedDeliverer) Send(_, _, _, _, _, _ string) (string, error) { return "platform-fed-1", nil }
func (fedDeliverer) SendVia(_, _, _ string) (string, error)       { return "platform-fed-1", nil }
func (fedDeliverer) SendOptions(_, _ string, _ []chanlib.Option, _, _ string) (string, error) {
	return "platform-fed-1", nil
}
func (fedDeliverer) React(_, _, _ string) error      { return nil }
func (fedDeliverer) Edit(_, _, _, _ string) error    { return nil }
func (fedDeliverer) Delete(_, _ string) error        { return nil }
func (fedDeliverer) Pin(_, _ string) error           { return nil }
func (fedDeliverer) Unpin(_, _ string, _ bool) error { return nil }
func (fedDeliverer) Typing(_ string, _ bool) error   { return nil }
func (fedDeliverer) Document(_, _, _, _, _, _, _, _ string) (string, error) {
	return "platform-fed-doc", nil
}
func (fedDeliverer) SendVoice(_, _, _, _, _ string) (string, error)     { return "platform-fed-voice", nil }
func (fedDeliverer) Post(_, _ string, _ []string) (string, error)       { return "platform-fed-post", nil }
func (fedDeliverer) Forward(_, _, _ string) (string, error)             { return "", nil }
func (fedDeliverer) Quote(_, _, _ string) (string, error)               { return "", nil }
func (fedDeliverer) Repost(_, _ string) (string, error)                 { return "", nil }
func (fedDeliverer) Dislike(_, _ string) error                          { return nil }
func (fedDeliverer) SetSuggestions(_ string, _ []core.PanePrompt) error { return nil }
func (fedDeliverer) SetName(_, _ string) error                          { return nil }
func (fedDeliverer) HasCapability(_, _ string) bool                     { return false }
func (fedDeliverer) RoundDone(_, _, _, _ string) error                  { return nil }
func (fedDeliverer) FetchHistory(_ string, _ time.Time, _ int) ([]byte, error) {
	return nil, nil
}

// federation bundles the three booted daemons + the test wiring.
type federation struct {
	authd    *fakeAuthd
	routdDB  *routd.DB
	routdSrv *routd.Server
	routdTS  *httptest.Server
	loop     *routd.Loop
	runedDB  *runed.DB
	runedTS  *httptest.Server
	// ipcDir is where routd serves each turn's agent MCP socket; the fake agent
	// dials it there to reply.
	ipcDir string

	// agent observations: the folder routd dispatched each turn to and the
	// secret set it shipped for the container env.
	mu          sync.Mutex
	ranFolders  map[string]string            // turnID -> routd-dispatched folder
	ranSessions map[string]string            // turnID -> the resume session id the turn carried
	ranSecrets  map[string]map[string]string // folder -> the secrets the turn's payload carried
}

// fedOpts selects what stands in for the agent and for adapter egress. The zero
// value is the FakeRuntime agent and the no-op deliverer.
type fedOpts struct {
	// instance names the runed Manager; it prefixes every container name.
	// "" → fedtest.
	instance string
	// runtime builds runed's Runtime once f (the observation sink) exists.
	// nil → fakeAgentRuntime.
	runtime func(f *federation) runed.Runtime
	// deliver is routd's egress half. nil → fedDeliverer{}.
	deliver routd.Deliverer
	// runTTL bounds one run; zero keeps runed's default.
	runTTL time.Duration
	// cfg is what runed's Manager renders each turn's envelope from. nil → a
	// config over temp dirs, which is all the FakeRuntime agent needs.
	cfg  *core.Config
	loop []func(*routd.LoopConfig)
}

// bootFederation stands up authd, runed (real Manager + FakeRuntime), and routd (real
// Loop + real poller calling runed over HTTP). The FakeRuntime is the only fake: it
// simulates the agent by calling the reply tool over the turn's MCP socket. Returns
// f and the running loop's cancel.
func bootFederation(t *testing.T, cfgMods ...func(*routd.LoopConfig)) *federation {
	t.Helper()
	return bootFederationWith(t, fedOpts{loop: cfgMods})
}

// bootFederationWith is bootFederation with the agent side chosen by the caller:
// the FakeRuntime for the split-contract tests, runed's real docker Runtime for
// the container turns in e2e_turn_test.go.
func bootFederationWith(t *testing.T, o fedOpts) *federation {
	t.Helper()
	f := &federation{
		ipcDir:      t.TempDir(),
		ranFolders:  map[string]string{},
		ranSessions: map[string]string{},
		ranSecrets:  map[string]map[string]string{},
	}
	f.authd = newFakeAuthd(t)

	// --- runed (execution plane) ---
	rudb, err := runed.OpenMem()
	if err != nil {
		t.Fatalf("runed.OpenMem: %v", err)
	}
	t.Cleanup(func() { rudb.Close() })
	f.runedDB = rudb

	var rt runed.Runtime
	if o.runtime != nil {
		rt = o.runtime(f)
	} else {
		rt = fakeAgentRuntime(t, f)
	}
	if o.instance == "" {
		o.instance = "fedtest"
	}
	// No host model credential may leak into a rendered turn: the renderer
	// merges the operator anchors off the environment, and a developer's own key
	// would show up in the secret set these tests assert on.
	for k := range store.EnvProfileKeys {
		t.Setenv(k, "")
	}
	rcfg := o.cfg
	if rcfg == nil {
		root := t.TempDir()
		rcfg = &core.Config{
			Name: o.instance, Timezone: "UTC",
			ProjectRoot: root,
			HostAppDir:  filepath.Join(root, "app"),
			GroupsDir:   filepath.Join(root, "groups"),
			IpcDir:      f.ipcDir,
			WebDir:      filepath.Join(root, "web"),
			Timeout:     time.Hour, IdleTimeout: time.Hour,
		}
	}
	mgr := runed.NewManager(rudb, rt, runed.ManagerConfig{
		Instance: o.instance,
		RunTTL:   o.runTTL,
		Cfg:      rcfg,
		Folders:  &groupfolder.Resolver{GroupsDir: rcfg.GroupsDir, IpcDir: rcfg.IpcDir},
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
	installCoreFixture(t, rodb, rcfg.ProjectRoot)
	// routd refuses to start without SECRETS_KEY and the store refuses every
	// unsealed write (BUGS S2), so the federation carries a keyring like a real
	// instance does.
	rodb.SetSecretKeys([]byte("federation-secrets-key"))
	f.routdDB = rodb

	// routd → runed over REAL HTTP via the production client, carrying routd's
	// service token (runs:run). This is the PINNED POST /v1/runs contract.
	svcTok := f.authd.mintService(t, "service:routd", "runs:run", "runs:kill")
	// POST /v1/runs returns when the run ends, so the client waits out a whole
	// run: a shorter deadline re-dispatches a live turn and tears its MCP
	// socket down under the container.
	clientTimeout := 10 * time.Second
	if o.runTTL > 0 {
		clientTimeout = o.runTTL + 30*time.Second
	}
	runedClient := runedv1.NewClient(f.runedTS.URL, svcTok, clientTimeout)

	lc := routd.LoopConfig{
		InstanceName: "fedtest",
		PollEvery:    20 * time.Millisecond, // fast poll for the test
		IpcDir:       f.ipcDir,
	}
	for _, mod := range o.loop {
		mod(&lc)
	}
	var deliver routd.Deliverer = fedDeliverer{}
	if o.deliver != nil {
		deliver = o.deliver
		lc.Deliver = deliver
	}
	loop := routd.NewLoop(rodb, runedClient, lc)
	f.loop = loop
	// Inbound auth is the REAL split path: routd's verifier (newFedVerifier over
	// authd's JWKS) gates /v1/messages on a service:<adapter> messages:write JWT.
	// Adapters present a token from mintAdapter — see
	// TestSplitFederation_InboundViaServiceToken.
	f.routdSrv = routd.NewServer(rodb, loop, deliver, newFedVerifier(t, f.authd), 0, "https://fed.test")
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

// fakeAgentRuntime is the agent as a FakeRuntime: on each run it records the
// folder routd dispatched to, mints a folder-bound reply token from authd, and
// posts a reply back to routd over HTTP — the real agent→routd callback, just
// without an LLM deciding the text.
func fakeAgentRuntime(t *testing.T, f *federation) runed.Runtime {
	t.Helper()
	return runed.FakeRuntime{Fn: func(_ context.Context, turn runed.Turn) runed.RunResult {
		frame := decodeTurn(t, turn)
		f.mu.Lock()
		f.ranFolders[frame.MessageID] = turn.Folder
		f.ranSessions[frame.MessageID] = frame.SessionID
		f.ranSecrets[turn.Folder] = frame.Secrets
		f.mu.Unlock()
		// Reply over the turn's own MCP socket — the path a real agent takes.
		if err := f.agentReply(turn, frame, "agent says: ack "+frame.MessageID); err != nil {
			return runed.RunResult{Outcome: runedv1.OutcomeError, Error: err.Error()}
		}
		return runed.RunResult{Outcome: runedv1.OutcomeOK}
	}}
}

// turnFrame is the agent payload a rendered Turn carries on its stdin — the same
// JSON ant/src/index.ts parses. A federation test reads it because that is what
// the guest would read; runed's port carries bytes, not fields.
type turnFrame struct {
	Prompt    string            `json:"prompt"`
	SessionID string            `json:"sessionId"`
	ChatJID   string            `json:"chatJid"`
	Folder    string            `json:"groupFolder"`
	MessageID string            `json:"messageId"`
	Secrets   map[string]string `json:"secrets"`
}

func decodeTurn(t *testing.T, turn runed.Turn) turnFrame {
	t.Helper()
	var f turnFrame
	if err := json.Unmarshal(turn.Stdin, &f); err != nil {
		t.Fatalf("turn stdin is not the agent payload: %v (%q)", err, turn.Stdin)
	}
	return f
}
func (f *federation) dispatchedFolder(turnID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ranFolders[turnID]
}

// dispatchedSession is the resume session id the turn's payload carried — what
// the agent would hand to `claude --resume`.
func (f *federation) dispatchedSession(turnID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ranSessions[turnID]
}

// dispatchedSecrets is the secret map the turn's payload carried for that
// folder — verbatim what the guest receives.
func (f *federation) dispatchedSecrets(folder string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ranSecrets[folder]
}

// agentReply is the fake agent's reply tool: one JSON-RPC tools/call over the
// per-turn MCP socket routd serves under the folder's ipc dir. routd binds the
// socket before it calls runed and removes it when the run returns, so it is
// live for the whole FakeRuntime call.
func (f *federation) agentReply(turn runed.Turn, frame turnFrame, text string) error {
	ipcDir, err := (&groupfolder.Resolver{IpcDir: f.ipcDir}).IpcPath(turn.Folder)
	if err != nil {
		return err
	}
	conn, err := net.Dial("unix", groupfolder.IpcSocket(ipcDir))
	if err != nil {
		return err
	}
	defer conn.Close()
	req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "reply", "arguments": map[string]any{"chatJid": frame.ChatJID, "text": text}},
	})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	raw, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return err
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("reply rpc: %s", resp.Error.Message)
	}
	if resp.Result.IsError && len(resp.Result.Content) > 0 {
		return fmt.Errorf("reply tool: %s", resp.Result.Content[0].Text)
	}
	return nil
}

// ---- The end-to-end flow ----

// TestSplitFederation_InboundToTurnRoundTrip drives the full split path: authd mints
// tokens; an adapter posts an inbound to routd over HTTP; routd's REAL poll loop routes
// and dispatches to runed over the real contract; runed runs the (Fake) agent, which
// replies over the turn's MCP socket; routd appends exactly one bot row. Every hop is
// real but the LLM/container.
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
	if !messageExists(f.routdDB, "slack:T/C/U", "wamid.1") {
		t.Fatal("inbound not stored after authorized ingest")
	}

	// routd's poll loop routes the chat, dispatches the run to runed over HTTP,
	// runed runs the fake agent, which replies over the turn's socket. Wait for
	// the resulting bot row to land (the turn closing).
	testutils.WaitForBotRow(t, store.NewOver(f.routdDB.Obj()), "slack:T/C/U", 5*time.Second)

	if got := f.dispatchedFolder("wamid.1"); got != "demo" {
		t.Fatalf("routd dispatched turn to folder %q, want demo (routd→runed over HTTP)", got)
	}
	if got := countBotRows(t, f.routdDB, "slack:T/C/U"); got != 1 {
		t.Fatalf("bot rows = %d, want 1 (the agent's reply appended exactly once)", got)
	}
	// the appended bot row carries the agent's text.
	testutils.AssertMessage(t, store.NewOver(f.routdDB.Obj()), "slack:T/C/U", "ack wamid.1")

	// runed persisted the spawn — the execution plane saw a real run, not a stub.
	waitForSpawn(t, f.runedDB, 2*time.Second)
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
	if !messageExists(f.routdDB, "slack:T/C/U", "svc.1") {
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
	if messageExists(f.routdDB, "slack:T/C/U", "bad.1") {
		t.Fatal("a rejected ingest must not store the inbound row")
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

// TestSplitVsMonolithRoutingParity asserts the split (routd's real poll-loop dispatch)
// and the monolith (the shared router.ResolveRouteTarget) make the SAME routing decision
// for the same input. The split decision is observed end-to-end: which folder routd
// actually dispatched to runed.
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

			// In-process side: a second routd.db over the same routes, decided via
			// the shared router.ResolveRouteTarget every dispatch path calls.
			mono := testutils.NewRoutdStore(t)
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
	return testutils.BotRows(t, store.NewOver(db.Obj()), jid)
}

func containsScope(scope []string, want string) bool {
	return slices.Contains(scope, want)
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
