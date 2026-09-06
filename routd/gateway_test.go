package routd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// turnResultWithModels is a submit_turn payload carrying a per-model cost the
// agent computed for itself — the number 6/23 Fault 2 is about.
func turnResultWithModels() apiv1.TurnResult {
	return apiv1.TurnResult{
		Status: "success",
		Models: map[string]apiv1.ModelCost{
			"claude-opus-4": {Input: 10, Output: 5, CostCents: 7},
		},
	}
}

// fakeGateway is litellm's admin surface as routd uses it: mint, read spend,
// delete. It answers the real paths with the real field names, so a rename on
// either side of the wire fails here rather than on the instance.
type fakeGateway struct {
	mu sync.Mutex
	// minted is every /key/generate body, decoded.
	minted []map[string]any
	// deleted is every key named in a /key/delete body.
	deleted []string
	// spendFor answers /spend/logs?api_key=<hash>. A hash absent from the map
	// answers an empty list, which is what an unflushed turn looks like.
	spendFor map[string][]map[string]any
	// mintStatus, when non-zero, is the status /key/generate answers with.
	mintStatus int
	// spendStatus, when non-zero, is the status /spend/logs answers with.
	spendStatus int
	// spendReads counts /spend/logs requests, so a poll can be observed.
	spendReads int
	// bearers is every Authorization header the surface saw.
	bearers []string
	// nextKey is the key the next mint hands back.
	nextKey string
}

func newFakeGateway(t *testing.T) (*fakeGateway, string) {
	t.Helper()
	g := &fakeGateway{spendFor: map[string][]map[string]any{}, nextKey: "sk-minted-1"}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return g, srv.URL
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.bearers = append(g.bearers, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/key/generate":
		if g.mintStatus != 0 {
			http.Error(w, `{"error":"no database"}`, g.mintStatus)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.minted = append(g.minted, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"key": g.nextKey})
	case "/key/delete":
		var body struct {
			Keys []string `json:"keys"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.deleted = append(g.deleted, body.Keys...)
		_ = json.NewEncoder(w).Encode(map[string]any{"deleted_keys": body.Keys})
	case "/spend/logs":
		g.spendReads++
		if g.spendStatus != 0 {
			http.Error(w, `{"error":"database not connected"}`, g.spendStatus)
			return
		}
		rows := g.spendFor[r.URL.Query().Get("api_key")]
		if rows == nil {
			rows = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(rows)
	default:
		http.NotFound(w, r)
	}
}

func (g *fakeGateway) snapshot() ([]map[string]any, []string, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]map[string]any(nil), g.minted...), append([]string(nil), g.deleted...), g.spendReads
}

func keyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// The key a turn hands its container is scoped in both dimensions the spec
// promises: a TTL taken from the run ceiling and a budget taken from what the
// folder may still spend today. Without them the container holds a credential
// that outlives the turn and buys as much as the operator's own key.
func TestGatewayMintCarriesTTLAndBudget(t *testing.T) {
	fake, url := newFakeGateway(t)
	gw := newModelGateway(url, "sk-master")

	key, err := gw.mint(context.Background(), "arizuko-turn-1-ab12", "atlas/support", "turn-1",
		20*time.Minute, 250)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if key.Key != "sk-minted-1" || key.Hash != keyHash("sk-minted-1") {
		t.Fatalf("mint returned %+v; Hash must be sha256(Key), which is how /spend/logs names it", key)
	}
	minted, _, _ := fake.snapshot()
	if len(minted) != 1 {
		t.Fatalf("mint sent %d requests, want 1", len(minted))
	}
	body := minted[0]
	if body["duration"] != "1200s" {
		t.Errorf("duration = %v, want 1200s (the run ceiling) — a key with no TTL outlives its turn", body["duration"])
	}
	if body["max_budget"] != 2.5 {
		t.Errorf("max_budget = %v, want 2.5 (250 cents in dollars) — the folder's remaining cap "+
			"is what stops a runaway turn at the boundary", body["max_budget"])
	}
	if body["key_alias"] != "arizuko-turn-1-ab12" {
		t.Errorf("key_alias = %v; the alias is how an operator finds this turn's key", body["key_alias"])
	}
	meta, _ := body["metadata"].(map[string]any)
	if meta["folder"] != "atlas/support" || meta["turn_id"] != "turn-1" {
		t.Errorf("metadata = %v, want the folder and turn this key was minted for", meta)
	}
	if got := fake.bearers[0]; got != "Bearer sk-master" {
		t.Errorf("mint authorised with %q, want the master key", got)
	}
}

// An uncapped folder has no budget to hand the key. Sending one anyway would
// be a per-turn ceiling the operator never set, and the first turn over it
// would fail with no rule behind the refusal.
func TestGatewayMintOmitsBudgetWhenTheFolderIsUncapped(t *testing.T) {
	fake, url := newFakeGateway(t)
	gw := newModelGateway(url, "sk-master")

	if _, err := gw.mint(context.Background(), "a", "atlas", "turn-1", 0, 0); err != nil {
		t.Fatalf("mint: %v", err)
	}
	minted, _, _ := fake.snapshot()
	if _, ok := minted[0]["max_budget"]; ok {
		t.Errorf("mint sent max_budget for an uncapped folder: %v", minted[0])
	}
	if _, ok := minted[0]["duration"]; ok {
		t.Errorf("mint sent a duration with no run ceiling configured: %v", minted[0])
	}
}

// The gateway's own log is the source cost_log takes: per model, tokens summed
// and dollars rounded to cents ONCE over the model's total. Rounding each
// request would book a turn of sub-cent calls as zero.
func TestGatewaySpendAggregatesPerModel(t *testing.T) {
	fake, url := newFakeGateway(t)
	gw := newModelGateway(url, "sk-master")
	hash := keyHash("sk-minted-1")
	fake.spendFor[hash] = []map[string]any{
		{"model": "claude-opus-4", "spend": 0.004, "prompt_tokens": 100, "completion_tokens": 20},
		{"model": "claude-opus-4", "spend": 0.004, "prompt_tokens": 50, "completion_tokens": 10},
		{"model": "claude-haiku-4", "spend": 0.031, "prompt_tokens": 7, "completion_tokens": 3},
	}

	got, err := gw.spend(context.Background(), hash)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	opus := got["claude-opus-4"]
	if opus.Input != 150 || opus.Output != 30 {
		t.Errorf("opus tokens = %+v, want 150 in / 30 out summed across its requests", opus)
	}
	if opus.CostCents != 1 {
		t.Errorf("opus cost = %d cents, want 1: $0.008 rounds once over the model's total, "+
			"not per request (which would book 0)", opus.CostCents)
	}
	if haiku := got["claude-haiku-4"]; haiku.CostCents != 3 {
		t.Errorf("haiku cost = %d cents, want 3", haiku.CostCents)
	}
}

// litellm writes spend rows on a batch timer, so the read taken the instant a
// container exits sees nothing. The wait is what makes the turn boundary a
// usable place to read from at all.
func TestGatewayWaitForSpendPollsUntilTheFlushLands(t *testing.T) {
	fake, url := newFakeGateway(t)
	gw := newModelGateway(url, "sk-master")
	gw.spendFlush = 3 * time.Second
	hash := keyHash("sk-minted-1")
	go func() {
		time.Sleep(1500 * time.Millisecond)
		fake.mu.Lock()
		fake.spendFor[hash] = []map[string]any{
			{"model": "m", "spend": 0.02, "prompt_tokens": 1, "completion_tokens": 1},
		}
		fake.mu.Unlock()
	}()

	got, err := gw.waitForSpend(context.Background(), hash)
	if err != nil {
		t.Fatalf("waitForSpend: %v", err)
	}
	if got["m"].CostCents != 2 {
		t.Fatalf("waitForSpend = %+v; it gave up before the gateway flushed the turn's usage", got)
	}
	if _, _, reads := fake.snapshot(); reads < 2 {
		t.Errorf("waitForSpend read once (%d); an unflushed first read must be retried, not believed", reads)
	}
}

// A read that fails and a turn that spent nothing must never look the same:
// only the first one means the folder's cap has stopped counting.
func TestGatewayWaitForSpendReportsAFailedReadAsAnError(t *testing.T) {
	fake, url := newFakeGateway(t)
	fake.spendStatus = http.StatusInternalServerError
	gw := newModelGateway(url, "sk-master")
	gw.spendFlush = 0

	got, err := gw.waitForSpend(context.Background(), keyHash("sk-minted-1"))
	if err == nil {
		t.Fatalf("waitForSpend = %+v, nil; a 500 must not read as a zero-cost turn", got)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not carry the gateway's status", err)
	}
}

// Revocation is deleting the key — the spec's whole claim that a leaked
// credential is scoped to one turn rests on this call happening.
func TestGatewayRevokeDeletesTheKey(t *testing.T) {
	fake, url := newFakeGateway(t)
	gw := newModelGateway(url, "sk-master")

	if err := gw.revoke(context.Background(), "sk-minted-1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, deleted, _ := fake.snapshot(); len(deleted) != 1 || deleted[0] != "sk-minted-1" {
		t.Fatalf("revoke deleted %v, want [sk-minted-1]", deleted)
	}
}

// A gateway URL with no master key mints nothing; the pair is the declaration,
// and the URL alone is what decides whether the instance has a gateway.
func TestNewGatewayIfConfigured(t *testing.T) {
	if gw := newGatewayIfConfigured("", "sk-master"); gw != nil {
		t.Error("no URL must yield no gateway: the instance runs the harness's own credential")
	}
	if gw := newGatewayIfConfigured("http://gw:4000/", "sk-master"); gw == nil || gw.baseURL != "http://gw:4000" {
		t.Errorf("newGatewayIfConfigured = %+v; the trailing slash must go or every path doubles it", gw)
	}
}

// keyRunner records the run request and answers a clean turn boundary.
type keyRunner struct{ req runedv1.RunRequest }

func (r *keyRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	r.req = req
	return runedv1.RunOutcome{RunID: "r1", Outcome: runedv1.OutcomeOK}, nil
}

// gatewayLoop builds a Loop whose gateway is the fake, with one registered
// group and one inbound message ready to dispatch.
func gatewayLoop(t *testing.T, folder string) (*DB, *Loop, *keyRunner, *fakeGateway) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	fake, url := newFakeGateway(t)
	runner := &keyRunner{}
	loop := NewLoop(db, runner, LoopConfig{
		GroupsDir:  t.TempDir(),
		RunTimeout: 20 * time.Minute,
		GatewayURL: url,
		GatewayKey: "sk-master",
	})
	loop.StopQueue()
	loop.gateway.spendFlush = 0
	_ = db.PutGroup(core.Group{Folder: folder})
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:" + folder, Sender: "u",
		Content: "q", Timestamp: time.Now().UTC()})
	return db, loop, runner, fake
}

// The container gets a key minted for THIS turn and the master key stays in
// routd. One shared non-expiring key handed to every spawn is exactly the
// fault 6/23 names.
func TestTurnMintsItsOwnKeyAndRevokesItAfter(t *testing.T) {
	_, loop, runner, fake := gatewayLoop(t, "demo")

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process: %v", err)
	}

	if runner.req.ModelGatewayKey != "sk-minted-1" {
		t.Fatalf("run carried gateway key %q, want the minted sk-minted-1", runner.req.ModelGatewayKey)
	}
	if runner.req.ModelGatewayKey == "sk-master" {
		t.Fatal("the run carried the MASTER key; it authorises the mint and never leaves routd")
	}
	minted, deleted, _ := fake.snapshot()
	if len(minted) != 1 {
		t.Fatalf("turn minted %d keys, want exactly 1", len(minted))
	}
	if len(deleted) != 1 || deleted[0] != "sk-minted-1" {
		t.Fatalf("turn deleted %v; the key must not outlive the spawn it was minted for", deleted)
	}
}

// A gateway that will not mint fails the turn as a dependency outage rather
// than spawning a container that holds neither a virtual key nor the model
// credential spawnSecrets already withheld from it.
func TestTurnRefusesToSpawnWhenTheMintFails(t *testing.T) {
	_, loop, runner, fake := gatewayLoop(t, "demo")
	fake.mintStatus = http.StatusInternalServerError

	if _, err := loop.processGroupMessages("web:demo"); err == nil {
		t.Fatal("a refused mint must fail the turn, not spawn without a key")
	}
	if runner.req.TurnID != "" {
		t.Fatalf("a run was dispatched with no minted key: %+v", runner.req)
	}
}

// cost_log takes the gateway's number. The agent's own submit_turn report is
// not the row, so an agent that under-reports moves nothing.
func TestGatewaySpendBecomesTheCostLogRow(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "demo")
	fake.spendFor[keyHash("sk-minted-1")] = []map[string]any{
		{"model": "claude-opus-4", "spend": 0.42, "prompt_tokens": 900, "completion_tokens": 120},
	}

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process: %v", err)
	}

	var model string
	var in, out, cents int
	err := db.SQL().QueryRow(
		`SELECT model, input_tokens, output_tokens, cost_cents FROM cost_log WHERE folder='demo'`).
		Scan(&model, &in, &out, &cents)
	if err != nil {
		t.Fatalf("cost_log has no row for the turn: %v", err)
	}
	if model != "claude-opus-4" || in != 900 || out != 120 || cents != 42 {
		t.Errorf("cost_log row = %s %d/%d %dc, want claude-opus-4 900/120 42c from the gateway",
			model, in, out, cents)
	}
}

// The agent's self-report is corroboration on a gateway instance, never the
// row. Writing it would let a turn book its own number into the primary key
// the gateway's measurement needs.
func TestAgentReportedCostIsNotWrittenWhenTheGatewayMeters(t *testing.T) {
	db, loop, _, _ := gatewayLoop(t, "demo")
	srv := NewServer(db, loop, nil, nil, time.Minute, "")
	loop.BindServer(srv)
	if _, err := db.PutTurnContext("turn-x", "demo", "", "web:demo", "u", ""); err != nil {
		t.Fatalf("put turn context: %v", err)
	}

	if _, err := srv.recordTurnResult("turn-x", turnResultWithModels()); err != nil {
		t.Fatalf("recordTurnResult: %v", err)
	}

	var rows int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM cost_log WHERE turn_id='turn-x'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("submit_turn wrote %d cost_log rows on a gateway instance; the spender's own "+
			"number must never become the row", rows)
	}
}

// The mirror: with no gateway the agent's report is the ONLY number there is,
// so it stays the row.
func TestAgentReportedCostIsTheRowWithNoGateway(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	loop := NewLoop(db, &keyRunner{}, LoopConfig{GroupsDir: t.TempDir()})
	loop.StopQueue()
	srv := NewServer(db, loop, nil, nil, time.Minute, "")
	loop.BindServer(srv)
	_ = db.PutGroup(core.Group{Folder: "demo"})
	if _, err := db.PutTurnContext("turn-y", "demo", "", "web:demo", "u", ""); err != nil {
		t.Fatalf("put turn context: %v", err)
	}

	if _, err := srv.recordTurnResult("turn-y", turnResultWithModels()); err != nil {
		t.Fatalf("recordTurnResult: %v", err)
	}

	var cents int
	if err := db.SQL().QueryRow(
		`SELECT cost_cents FROM cost_log WHERE turn_id='turn-y'`).Scan(&cents); err != nil {
		t.Fatalf("no cost_log row from the agent's report: %v", err)
	}
	if cents != 7 {
		t.Errorf("cost_cents = %d, want 7 from the agent's submit_turn report", cents)
	}
}
