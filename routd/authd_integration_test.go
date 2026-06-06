package routd

// Cross-section integration: authd token-validation + grant enforcement on
// routd's inbound dispatch and turn-callback surfaces, wired in-process with no
// docker, no mockagent, no LLM (specs/4/S-e2e-tests.md). A stub authd (httptest
// serving /v1/keys from an auth.SigningKey) issues ES256 user tokens; routd's
// Verifier verifies them over the same auth.FetchKeys RemoteKeySet path every
// backend runs. The stubRunner stands in for the agent so the round-trip closes
// without a real run.
//
// What this asserts that contract_test.go cannot: contract_test wires
// verify==nil (open mode), so the bearer gate and folder-bound grant checks are
// never exercised. Here the gate is live, against real authd-minted tokens.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// stubAuthd is a minimal authd: one ES256 signing key, /v1/keys serving its
// public JWKS, and a mint helper that signs a folder-bound user token exactly
// as authd.signMinted does (arz/folder in Extra, typ=user). routd verifies it
// over the network via auth.FetchKeys.
type stubAuthd struct {
	key *auth.SigningKey
	ts  *httptest.Server
}

func newStubAuthd(t *testing.T) *stubAuthd {
	t.Helper()
	key, err := auth.NewSigningKey("kid-test")
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
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
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &stubAuthd{key: key, ts: ts}
}

// mint signs a user token carrying scope and (when non-empty) the arz/folder
// claim — the shape authd issues for a brokered agent/adapter token.
func (a *stubAuthd) mint(t *testing.T, sub, folder string, scope ...string) string {
	t.Helper()
	c := auth.TokenClaims{Sub: sub, Typ: "user", Scope: scope}
	if folder != "" {
		c.Extra = map[string]string{"arz/folder": folder}
	}
	tok, err := a.key.Sign(c, time.Hour)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return tok
}

// verifierFunc adapts a func to routd.Verifier — the same adapter shape as
// routd/cmd/routd/main.go's keysetVerifier, inline for the test.
type verifierFunc func(r *http.Request) (sub string, scope []string, folder string, err error)

func (f verifierFunc) Verify(r *http.Request) (string, []string, string, error) { return f(r) }

// newVerifiedRoutd is newTestRoutd with a live Verifier pointed at authd's
// /v1/keys over the wire (cold RemoteKeySet), so authz/authzTurn run for real.
func newVerifiedRoutd(t *testing.T, a *stubAuthd) (*DB, *Server, *stubRunner) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ks, err := auth.FetchKeys(context.Background(), a.ts.URL)
	if err != nil {
		t.Fatalf("fetch keys: %v", err)
	}
	verify := verifierFunc(func(r *http.Request) (string, []string, string, error) {
		s, verr := auth.VerifyHTTP(r, ks)
		if verr != nil {
			return "", nil, "", verr
		}
		return s.Sub, s.Scope, s.Extra["arz/folder"], nil
	})
	runner := &stubRunner{}
	loop := NewLoop(db, runner, LoopConfig{})
	loop.StopQueue()
	srv := NewServer(db, loop, &fakeDeliverer{platformID: "1716.0042"}, verify, 0, "https://example.test")
	runner.srv = srv
	return db, srv, runner
}

// serveBearer issues a JSON request with an Authorization: Bearer header (which
// doJSONKey cannot set), so the live Verifier path runs against the real token.
func serveBearer(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	return serveBearerKey(t, h, method, path, token, "", body)
}

// serveBearerKey is serveBearer plus an X-Idempotency-Key (the turn-callback
// write surface requires one; ingress does not).
func serveBearerKey(t *testing.T, h http.Handler, method, path, token, idemKey string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idemKey != "" {
		req.Header.Set("X-Idempotency-Key", idemKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestInboundDispatchAuthorizedWithValidToken is the positive cross-section
// path: a real authd-minted user token bearing messages:write passes routd's
// ingress gate, the inbound is stored, and the loop dispatches the stub run.
// End-to-end through verify → ingest → route → dispatch, no LLM.
func TestInboundDispatchAuthorizedWithValidToken(t *testing.T) {
	authd := newStubAuthd(t)
	db, srv, runner := newVerifiedRoutd(t, authd)
	h := srv.Handler()

	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	if _, err := db.AddRoute(core.Route{Match: "platform=slack", Target: "demo"}); err != nil {
		t.Fatalf("add route: %v", err)
	}

	// adapter token: messages:write, bound to demo.
	adapterTok := authd.mint(t, "service:slakd", "demo", "messages:write")
	in := apiv1.Message{ID: "m1", ChatJID: "slack:T/C/U", Sender: "u1", Content: "hi", Verb: "message"}
	rec := serveBearer(t, h, "POST", "/v1/messages", adapterTok, in)
	if rec.Code != 200 {
		t.Fatalf("authorized ingest status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !db.MessageExists("m1") {
		t.Fatal("inbound row not stored after authorized ingest")
	}

	hadOutput, err := srv.loop.processGroupMessages("slack:T/C/U")
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if !hadOutput {
		t.Fatal("expected the run to report output")
	}
	if runner.gotTurn != "m1" {
		t.Fatalf("runed turn_id=%q want m1", runner.gotTurn)
	}
	if !strings.Contains(runner.gotBatch, "hi") {
		t.Fatalf("rendered batch missing trigger content: %q", runner.gotBatch)
	}
	if got := countBots(t, db, "slack:T/C/U"); got != 1 {
		t.Fatalf("bot rows=%d want 1 (callback must append exactly one)", got)
	}
}

// TestInboundDispatchDeniedMissingScope is the grant-enforcement negative the
// harness lacked: a valid, correctly-signed token that simply lacks
// messages:write is rejected at ingress with 403 and never stored or dispatched.
// This proves the gate enforces the capability, not merely token authenticity.
func TestInboundDispatchDeniedMissingScope(t *testing.T) {
	authd := newStubAuthd(t)
	db, srv, _ := newVerifiedRoutd(t, authd)
	h := srv.Handler()

	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	if _, err := db.AddRoute(core.Route{Match: "platform=slack", Target: "demo"}); err != nil {
		t.Fatalf("add route: %v", err)
	}

	// well-formed token, wrong capability: read-only, no messages:write.
	tok := authd.mint(t, "service:slakd", "demo", "tasks:read")
	in := apiv1.Message{ID: "m2", ChatJID: "slack:T/C/U", Sender: "u1", Content: "hi", Verb: "message"}
	rec := serveBearer(t, h, "POST", "/v1/messages", tok, in)
	if rec.Code != 403 {
		t.Fatalf("ingest with no messages:write must be 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if db.MessageExists("m2") {
		t.Fatal("a denied ingest must not store the inbound row")
	}
}

// TestInboundDispatchDeniedMissingToken: no bearer at all → 401, fail-closed.
func TestInboundDispatchDeniedMissingToken(t *testing.T) {
	authd := newStubAuthd(t)
	db, srv, _ := newVerifiedRoutd(t, authd)
	h := srv.Handler()

	in := apiv1.Message{ID: "m3", ChatJID: "slack:T/C/U", Sender: "u1", Content: "hi", Verb: "message"}
	rec := serveBearer(t, h, "POST", "/v1/messages", "", in)
	if rec.Code != 401 {
		t.Fatalf("ingest with no token must be 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if db.MessageExists("m3") {
		t.Fatal("an unauthenticated ingest must not store the inbound row")
	}
}

// TestInboundDispatchIngressFolderNotBound pins the CURRENT (permissive) ingress
// behavior: a messages:write token bound to folder "other" can ingest a message
// that ROUTES to folder "demo" — handleMessages discards the token's arz/folder
// claim (server.go: `sub, _, ok := s.authz(...)`). Folder-binding is enforced on
// the turn-callback surface (TestTurnCallbackDeniedForeignFolder), NOT at ingress.
//
// NOT a behavior change. Plausibly by-design (ingress routing is route-table-
// driven, the callback is where folder scope bites). bugs.md flags the intent
// question (routd ingress not folder-gated, 2026-05-30). If ingress SHOULD be
// folder-scoped, this expectation flips to 403 + no stored row.
func TestInboundDispatchIngressFolderNotBound(t *testing.T) {
	authd := newStubAuthd(t)
	db, srv, _ := newVerifiedRoutd(t, authd)
	h := srv.Handler()

	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	if _, err := db.AddRoute(core.Route{Match: "platform=slack", Target: "demo"}); err != nil {
		t.Fatalf("add route: %v", err)
	}

	// Token bound to "other", but the message routes to "demo".
	tok := authd.mint(t, "service:slakd", "other", "messages:write")
	in := apiv1.Message{ID: "m9", ChatJID: "slack:T/C/U", Sender: "u1", Content: "hi", Verb: "message"}
	rec := serveBearer(t, h, "POST", "/v1/messages", tok, in)
	if rec.Code != 200 {
		t.Fatalf("CURRENT: ingress ignores folder binding, want 200; got %d body=%s", rec.Code, rec.Body.String())
	}
	if !db.MessageExists("m9") {
		t.Fatal("CURRENT: message routing to a non-token folder is still stored at ingress")
	}
}

// TestTurnCallbackDeniedForeignFolder is the folder-bound grant enforcement on
// the callback surface (authzTurn → ownsFolder): a token whose arz/folder claim
// is for a DIFFERENT folder than the turn's cannot drive that turn's reply.
// This is where routd enforces folder-scoped grants end-to-end — a leaked agent
// token for folder "other" cannot append into "demo". 403, no bot row. The
// control asserts the same token scoped to the turn's own folder is allowed.
func TestTurnCallbackDeniedForeignFolder(t *testing.T) {
	authd := newStubAuthd(t)
	db, srv, _ := newVerifiedRoutd(t, authd)
	h := srv.Handler()

	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")

	foreignTok := authd.mint(t, "user:u9", "other", "messages:send:own_group")
	rec := serveBearer(t, h, "POST", "/v1/turns/t1/reply", foreignTok,
		apiv1.ReplyRequest{JID: "slack:T/C/U", Text: "answer"})
	if rec.Code != 403 {
		t.Fatalf("reply from a foreign-folder token must be 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if countBots(t, db, "slack:T/C/U") != 0 {
		t.Fatal("a denied callback must not append a bot row")
	}

	// control: the same shape token scoped to the turn's own folder is allowed.
	ownTok := authd.mint(t, "user:u1", "demo", "messages:send:own_group")
	rec2 := serveBearerKey(t, h, "POST", "/v1/turns/t1/reply", ownTok, "idem-1",
		apiv1.ReplyRequest{JID: "slack:T/C/U", Text: "answer"})
	if rec2.Code != 200 {
		t.Fatalf("control: own-folder token must be 200, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	if countBots(t, db, "slack:T/C/U") != 1 {
		t.Fatal("authorized own-folder callback must append exactly one bot row")
	}
}
