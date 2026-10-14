package routd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/queue"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// fakeGateway is litellm's admin surface as routd uses it. It answers the real
// paths with the real field names, the real status codes and the real nesting —
// /team/info wraps its row in "team_info", a duplicate /team/new is 400, an
// absent team is 404 — all measured against ghcr.io/berriai/litellm:main-latest
// on 2026-09-06. A rename on either side of the wire fails here rather than on
// the instance.
type fakeGateway struct {
	mu sync.Mutex
	// teams and users are the gateway's budget rows, in the gateway's own unit
	// (dollars). A nil maxBudget is an entity the gateway holds no ceiling for.
	teams map[string]*fakeBudget
	users map[string]*fakeBudget
	// minted is every /key/generate body, decoded.
	minted []map[string]any
	// deleted is every key named in a /key/delete body.
	deleted []string
	// spendFor answers /spend/logs?api_key=<hash>.
	spendFor map[string][]map[string]any
	// daily answers /team/daily/activity: one entry per day, each mapping a
	// team id to that day's metrics.
	daily []map[string]map[string]any
	// calls counts requests per path for read and mutation assertions.
	calls map[string]int
	// mintStatus / teamInfoStatus, when non-zero, is the status that path
	// answers with instead of doing its job.
	mintStatus     int
	teamInfoStatus int
	// nextKey is the key the next mint hands back.
	nextKey string
}

type fakeBudget struct {
	spend     float64
	maxBudget *float64
}

func newFakeGateway(t *testing.T) (*fakeGateway, string) {
	t.Helper()
	g := &fakeGateway{
		teams:    map[string]*fakeBudget{},
		users:    map[string]*fakeBudget{},
		spendFor: map[string][]map[string]any{},
		calls:    map[string]int{},
		nextKey:  "sk-minted-1",
	}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return g, srv.URL
}

// setTeam seeds a team the way the gateway would hold it: spend and cap in
// CENTS here, converted at the boundary, so a test never hand-writes the
// dollars-vs-cents conversion the code under test owns.
func (g *fakeGateway) setTeam(id string, spentCents, capCents int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.teams[id] = newFakeBudget(spentCents, capCents)
}

func (g *fakeGateway) setUser(id string, spentCents, capCents int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.users[id] = newFakeBudget(spentCents, capCents)
}

func newFakeBudget(spentCents, capCents int) *fakeBudget {
	b := &fakeBudget{spend: float64(spentCents) / 100}
	if capCents > 0 {
		d := float64(capCents) / 100
		b.maxBudget = &d
	}
	return b
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls[r.URL.Path]++
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

	case "/team/info":
		if g.teamInfoStatus != 0 {
			http.Error(w, `{"error":"database not connected"}`, g.teamInfoStatus)
			return
		}
		g.writeInfo(w, g.teams[r.URL.Query().Get("team_id")], "team_info")

	case "/user/info":
		g.writeInfo(w, g.users[r.URL.Query().Get("user_id")], "user_info")

	case "/team/new":
		g.upsert(w, r, g.teams, "team_id", true)

	case "/team/update":
		g.upsert(w, r, g.teams, "team_id", false)

	case "/user/new":
		g.upsert(w, r, g.users, "user_id", true)

	case "/user/update":
		g.upsert(w, r, g.users, "user_id", false)

	case "/spend/logs":
		rows := g.spendFor[r.URL.Query().Get("api_key")]
		if rows == nil {
			rows = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(rows)

	case "/team/daily/activity":
		results := []map[string]any{}
		for _, day := range g.daily {
			ents := map[string]any{}
			for team, m := range day {
				ents[team] = map[string]any{"metrics": m}
			}
			results = append(results, map[string]any{
				"date":      "2026-09-06",
				"metrics":   map[string]any{},
				"breakdown": map[string]any{"entities": ents},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})

	default:
		http.NotFound(w, r)
	}
}

// writeInfo answers the /team/info and /user/info shape: the row nested under
// its own key, or 404 when the gateway holds none.
func (g *fakeGateway) writeInfo(w http.ResponseWriter, b *fakeBudget, key string) {
	if b == nil {
		http.Error(w, `{"error":{"message":"not found","code":"404"}}`, http.StatusNotFound)
		return
	}
	info := map[string]any{"spend": b.spend, "max_budget": nil}
	if b.maxBudget != nil {
		info["max_budget"] = *b.maxBudget
	}
	_ = json.NewEncoder(w).Encode(map[string]any{key: info})
}

// upsert is the /team/new + /team/update pair (and the user twin). create=true
// refuses a duplicate with 400, which is what the real /team/new does and why
// the gate has to read before it writes.
func (g *fakeGateway) upsert(w http.ResponseWriter, r *http.Request, into map[string]*fakeBudget, idField string, create bool) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	id, _ := body[idField].(string)
	if create && into[id] != nil {
		http.Error(w, `{"error":"already exists"}`, http.StatusBadRequest)
		return
	}
	b := into[id]
	if b == nil {
		b = &fakeBudget{}
		into[id] = b
	}
	b.maxBudget = nil
	if v, ok := body["max_budget"].(float64); ok {
		b.maxBudget = &v
	}
	_ = json.NewEncoder(w).Encode(map[string]any{idField: id})
}

func (g *fakeGateway) snapshot() ([]map[string]any, []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]map[string]any(nil), g.minted...), append([]string(nil), g.deleted...)
}

// teamCap reports the cap the gateway now holds for a team, in cents, and
// whether it holds a team at all.
// userCap is teamCap for the user half. The two halves of the gate reconcile
// symmetrically, so the fake exposes both the same way (BUGS `L402`).
func (g *fakeGateway) userCap(id string) (int, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.users[id]
	if b == nil {
		return 0, false
	}
	if b.maxBudget == nil {
		return 0, true
	}
	return int(*b.maxBudget*100 + 0.5), true
}

func (g *fakeGateway) teamCap(id string) (int, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.teams[id]
	if b == nil {
		return 0, false
	}
	if b.maxBudget == nil {
		return 0, true
	}
	return int(*b.maxBudget*100 + 0.5), true
}

func (g *fakeGateway) callCount(path string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls[path]
}

func keyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
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
		GroupsDir:       t.TempDir(),
		RunTimeout:      20 * time.Minute,
		GatewayURL:      url,
		GatewayKey:      "sk-master",
		CostCapsEnabled: true,
	})
	loop.StopQueue()
	_ = db.PutGroup(core.Group{Folder: folder})
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:" + folder, Sender: "u",
		Content: "q", Timestamp: time.Now().UTC()})
	return db, loop, runner, fake
}

// The key a turn hands its container carries the two coordinates the meter
// attributes by — the folder as its team, the caller as its user. Both are set
// by routd on a credential the container only holds, which is what stops the
// spender choosing whose budget it spends.
func TestMintCarriesTheFolderAndTheCaller(t *testing.T) {
	fake, url := newFakeGateway(t)
	gw := newModelGateway(url, "sk-master")

	if _, err := gw.mint(context.Background(), "alias", "corp/eng", "t1", "user:alice", 20*time.Minute); err != nil {
		t.Fatalf("mint: %v", err)
	}
	minted, _ := fake.snapshot()
	if len(minted) != 1 {
		t.Fatalf("minted %d keys, want 1", len(minted))
	}
	if got := minted[0]["team_id"]; got != "corp/eng" {
		t.Errorf("key team_id = %v, want the folder corp/eng — the gateway attributes and caps by team", got)
	}
	if got := minted[0]["user_id"]; got != "user:alice" {
		t.Errorf("key user_id = %v, want the caller user:alice — per-person spend is attributed by it", got)
	}
	if got := minted[0]["duration"]; got != "1200s" {
		t.Errorf("key duration = %v, want 1200s — the key must not outlive the run ceiling", got)
	}
}

// The key carries no budget of its own: the folder's cap is the team's and the
// person's is the user's. A per-turn ceiling invented here would be a third
// policy the operator never set.
func TestMintCarriesNoBudgetOfItsOwn(t *testing.T) {
	fake, url := newFakeGateway(t)
	gw := newModelGateway(url, "sk-master")

	if _, err := gw.mint(context.Background(), "alias", "demo", "t1", "user:a", time.Minute); err != nil {
		t.Fatalf("mint: %v", err)
	}
	minted, _ := fake.snapshot()
	if _, ok := minted[0]["max_budget"]; ok {
		t.Errorf("the key carried max_budget=%v; the team enforces the folder's cap, "+
			"and a second ceiling here is a policy nobody set", minted[0]["max_budget"])
	}
}

// A caller with no sub is attributed to its team alone rather than to a user
// named "", which would collect every anonymous turn under one budget.
func TestMintOmitsAnEmptyCaller(t *testing.T) {
	fake, url := newFakeGateway(t)
	gw := newModelGateway(url, "sk-master")

	if _, err := gw.mint(context.Background(), "alias", "demo", "t1", "", time.Minute); err != nil {
		t.Fatalf("mint: %v", err)
	}
	minted, _ := fake.snapshot()
	if _, ok := minted[0]["user_id"]; ok {
		t.Errorf("key carried user_id=%v for a caller with no sub", minted[0]["user_id"])
	}
}

// A folder the gateway has never seen gets its team created, carrying the
// operator's cap. Without this every model call on the turn's key fails 404
// "Team doesn't exist in db" — the team is a precondition of the turn, not an
// optimisation for capped folders.
func TestGateCreatesTheTeamWhenTheGatewayHasNone(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "demo")
	if err := db.SetFolderCap("demo", 500); err != nil {
		t.Fatal(err)
	}

	refusal, err := loop.budgetGate(context.Background(), "demo", "user:a")
	if err != nil || refusal != "" {
		t.Fatalf("gate = (%q, %v), want the turn allowed", refusal, err)
	}
	cap, ok := fake.teamCap("demo")
	if !ok {
		t.Fatal("the gate did not create the folder's team; every model call on this turn's key would 404")
	}
	if cap != 500 {
		t.Errorf("created team cap = %d cents, want the operator's 500", cap)
	}
}

// The operator's cap is arizuko's policy and the gateway's job is only to
// enforce it, so a cap the operator changed moves the team's budget with it.
func TestGateConvergesTheTeamOnTheOperatorsCap(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "demo")
	fake.setTeam("demo", 0, 100)
	if err := db.SetFolderCap("demo", 900); err != nil {
		t.Fatal(err)
	}

	if _, err := loop.budgetGate(context.Background(), "demo", "user:a"); err != nil {
		t.Fatalf("gate: %v", err)
	}
	if cap, _ := fake.teamCap("demo"); cap != 900 {
		t.Errorf("team cap = %d cents, want 900 — the gateway kept enforcing a cap the operator replaced", cap)
	}
}

// Removing a cap has to CLEAR the gateway's, not merely stop arizuko checking
// it. A budget left standing would keep refusing turns the operator no longer
// caps, and no arizuko surface would show why.
func TestGateClearsTheGatewayCapWhenTheOperatorRemovesIt(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "demo")
	fake.setTeam("demo", 0, 400)
	if err := db.SetFolderCap("demo", 0); err != nil {
		t.Fatal(err)
	}

	if _, err := loop.budgetGate(context.Background(), "demo", "user:a"); err != nil {
		t.Fatalf("gate: %v", err)
	}
	cap, ok := fake.teamCap("demo")
	if !ok {
		t.Fatal("team vanished")
	}
	if cap != 0 {
		t.Errorf("team cap = %d cents after the operator removed the cap, want 0 (uncapped)", cap)
	}
}

// An unchanged cap costs no write. The gate runs on every turn, so a blind
// update would be one pointless round trip per dispatch.
func TestGateDoesNotRewriteAnUnchangedCap(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "demo")
	fake.setTeam("demo", 0, 500)
	if err := db.SetFolderCap("demo", 500); err != nil {
		t.Fatal(err)
	}

	if _, err := loop.budgetGate(context.Background(), "demo", "user:a"); err != nil {
		t.Fatalf("gate: %v", err)
	}
	if n := fake.callCount("/team/update"); n != 0 {
		t.Errorf("/team/update called %d times for an unchanged cap, want 0", n)
	}
}

// The refusal quotes the GATEWAY's numbers, because they are the only ones
// there are — arizuko holds no spend to disagree with them.
func TestGateRefusesOnTheGatewaysFolderMeter(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "demo")
	fake.setTeam("demo", 700, 500)
	if err := db.SetFolderCap("demo", 500); err != nil {
		t.Fatal(err)
	}

	refusal, err := loop.budgetGate(context.Background(), "demo", "user:a")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !strings.Contains(refusal, "channel spent 700 of 500 cents") {
		t.Errorf("refusal = %q, want the gateway's own spend and cap in it", refusal)
	}
}

// The person axis refuses on the same meter, so a caller who has spent their
// day's budget is stopped in a folder that still has headroom.
func TestGateRefusesOnTheGatewaysUserMeter(t *testing.T) {
	db, loop, _, fake := gatewayLoop(t, "demo")
	fake.setTeam("demo", 0, 0)
	fake.setUser("user:a", 300, 200)
	seedUser(t, db, "user:a", 200)

	refusal, err := loop.budgetGate(context.Background(), "demo", "user:a")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !strings.Contains(refusal, "you spent 300 of 200 cents") {
		t.Errorf("refusal = %q, want the gateway's per-person numbers", refusal)
	}
}

// A missing uncapped caller needs one lookup and no gateway row mutations.
func TestGateReadsAnUncappedCallerWithoutWriting(t *testing.T) {
	_, loop, _, fake := gatewayLoop(t, "demo")
	fake.setTeam("demo", 0, 0)

	if _, err := loop.budgetGate(context.Background(), "demo", "telegram:user/1"); err != nil {
		t.Fatalf("gate: %v", err)
	}
	if n := fake.callCount("/user/info"); n != 1 {
		t.Errorf("/user/info called %d times for an uncapped caller, want 1", n)
	}
	for _, path := range []string{"/user/new", "/user/update"} {
		if n := fake.callCount(path); n != 0 {
			t.Errorf("%s called %d times for a missing uncapped caller, want 0", path, n)
		}
	}
}

// A gateway that will not answer leaves the cap blind, and a turn dispatched
// against a blind meter is the second ledger this spec removes. It fails as a
// dependency outage so the batch re-feeds, exactly like a refused mint.
func TestGateRefusesToSpawnWhenTheGatewayIsSilent(t *testing.T) {
	db, loop, runner, fake := gatewayLoop(t, "demo")
	fake.teamInfoStatus = http.StatusInternalServerError
	if err := db.SetFolderCap("demo", 500); err != nil {
		t.Fatal(err)
	}

	_, err := loop.processGroupMessages("web:demo")
	if err == nil {
		t.Fatal("a silent gateway must fail the turn, not spawn against a meter nobody read")
	}
	if !errors.Is(err, queue.ErrDependencyDown) {
		t.Errorf("err = %v, want a dependency outage so the batch re-feeds "+
			"and the folder's breaker streak is left alone", err)
	}
	if runner.req.TurnID != "" {
		t.Fatalf("a run was dispatched anyway: %+v", runner.req)
	}
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
	minted, deleted := fake.snapshot()
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

// The turn records WHICH key metered it. That pointer is what GET /v1/cost
// resolves to ask the gateway what the turn cost, and it is the only thing
// arizuko keeps in place of the copy it used to write.
func TestTurnRecordsTheKeyThatMeteredIt(t *testing.T) {
	db, loop, runner, _ := gatewayLoop(t, "demo")

	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("process: %v", err)
	}
	got, err := db.TurnGatewayKey(runner.req.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if got != keyHash("sk-minted-1") {
		t.Errorf("turn's gateway_key_hash = %q, want sha256 of the minted key — "+
			"without it the turn's cost is unreadable", got)
	}
}

// An instance with no gateway has no meter. It still runs turns: the harness is
// supported without one, and refusing every turn would break it.
func TestNoGatewaySpawnsAndEnforcesNothing(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	runner := &keyRunner{}
	loop := NewLoop(db, runner, LoopConfig{GroupsDir: t.TempDir(), CostCapsEnabled: true})
	loop.StopQueue()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	_ = db.SetFolderCap("demo", 1)
	_ = db.PutMessage(core.Message{ID: "a", ChatJID: "web:demo", Sender: "u",
		Content: "q", Timestamp: time.Now().UTC()})

	refusal, err := loop.budgetGate(context.Background(), "demo", "user:a")
	if err != nil || refusal != "" {
		t.Fatalf("gate = (%q, %v); with no meter there is nothing to refuse on", refusal, err)
	}
	if _, err := loop.processGroupMessages("web:demo"); err != nil {
		t.Fatalf("an instance with no gateway must still run turns: %v", err)
	}
	if runner.req.ModelGatewayKey != "" {
		t.Errorf("run carried a gateway key %q on an instance with no gateway", runner.req.ModelGatewayKey)
	}
}

func TestNewGatewayIfConfigured(t *testing.T) {
	if gw := newGatewayIfConfigured("", "sk-master"); gw != nil {
		t.Error("no URL must yield no gateway: the instance runs the harness's own credential")
	}
	if gw := newGatewayIfConfigured("http://gw:4000/", "sk-master"); gw == nil || gw.baseURL != "http://gw:4000" {
		t.Errorf("newGatewayIfConfigured = %+v; the trailing slash must go or every path doubles it", gw)
	}
}

// A read for an entity the gateway does not hold is a distinct condition, not a
// failure: it is exactly what tells the gate to create one. Reporting it as an
// error would make every folder's first turn a dependency outage.
func TestAbsentEntityReadsAsNotFound(t *testing.T) {
	_, url := newFakeGateway(t)
	gw := newModelGateway(url, "sk-master")

	if _, err := gw.teamBudget(context.Background(), "nope"); !errors.Is(err, errGatewayNotFound) {
		t.Errorf("teamBudget for an absent team = %v, want errGatewayNotFound", err)
	}
	if _, err := gw.userBudget(context.Background(), "nobody"); !errors.Is(err, errGatewayNotFound) {
		t.Errorf("userBudget for an absent user = %v, want errGatewayNotFound", err)
	}
}

// The gateway speaks dollars and every arizuko surface speaks whole cents, so
// the conversion happens once at the boundary and rounds the TOTAL — a turn of
// many sub-cent calls reports what it cost rather than a column of zeroes.
func TestKeySpendSumsTheModelsAndRoundsTheTotalOnce(t *testing.T) {
	fake, url := newFakeGateway(t)
	gw := newModelGateway(url, "sk-master")
	fake.spendFor["h1"] = []map[string]any{
		{"model": "opus", "spend": 0.004, "prompt_tokens": 10, "completion_tokens": 2},
		{"model": "opus", "spend": 0.004, "prompt_tokens": 5, "completion_tokens": 3},
		{"model": "haiku", "spend": 0.004, "prompt_tokens": 1, "completion_tokens": 1},
	}

	got, err := gw.keySpend(context.Background(), "h1")
	if err != nil {
		t.Fatal(err)
	}
	// 3 × $0.004 = $0.012 → 1 cent. Rounded per row it would be 0.
	if got.CostCents != 1 {
		t.Errorf("cost = %d cents, want 1 — rounding each request instead of the "+
			"total reports a turn of sub-cent calls as free", got.CostCents)
	}
	if got.InputTokens != 16 || got.OutputTokens != 6 {
		t.Errorf("tokens = in %d out %d, want in 16 out 6", got.InputTokens, got.OutputTokens)
	}
	if got.Requests != 3 {
		t.Errorf("requests = %d, want 3 — an empty read is how an unflushed turn "+
			"is told apart from one that spent nothing", got.Requests)
	}
}

// The operator's per-folder read sums the gateway's days into one row per
// folder. The daily surface reports a row per DAY with each team's share
// nested under it, so a reader that took the day's top-level metrics would
// report every folder's spend as the instance's total.
func TestSpendByFolderSumsTheDaysPerFolder(t *testing.T) {
	fake, url := newFakeGateway(t)
	gw := newModelGateway(url, "sk-master")
	fake.daily = []map[string]map[string]any{
		{
			"corp/eng": {"spend": 0.10, "prompt_tokens": 100, "completion_tokens": 10},
			"solo":     {"spend": 0.01, "prompt_tokens": 5, "completion_tokens": 1},
		},
		{
			"corp/eng": {"spend": 0.20, "prompt_tokens": 200, "completion_tokens": 20},
		},
	}

	rows, err := gw.spendByFolder(context.Background(), "2026-09-01", "2026-09-07")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want one per folder", rows)
	}
	if rows[0].Folder != "corp/eng" || rows[0].CostCents != 30 || rows[0].Tokens != 330 {
		t.Errorf("corp/eng = %+v, want 30 cents and 330 tokens summed across both days", rows[0])
	}
	if rows[1].Folder != "solo" || rows[1].CostCents != 1 || rows[1].Tokens != 6 {
		t.Errorf("solo = %+v, want 1 cent and 6 tokens", rows[1])
	}
}

// An instance with no gateway answers /v1/cost/summary honestly: metered false,
// no folders. The flag is carried explicitly because an empty list would
// otherwise read as "nothing was spent", which is a claim an unmetered instance
// cannot make.
func TestCostSummarySaysWhenThereIsNoMeter(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"cost:read"}, folder: ""})

	rec := doJSON(t, h, "GET", "/v1/cost/summary?since=2026-09-01T00:00:00Z", "", nil)
	if rec.Code != 200 {
		t.Fatalf("cost summary = %d want 200: %s", rec.Code, rec.Body.String())
	}
	var got apiv1.CostSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Metered {
		t.Error("an instance with no gateway reported metered=true; nothing measures it")
	}
	if len(got.Folders) != 0 {
		t.Errorf("folders = %+v, want none", got.Folders)
	}
}

// The spend summary is a LIST-ALL read, and the gateway answers it for every
// folder on the instance. A folder-scoped token must therefore see only its own
// subtree: an empty folder claim is the service/root reach, and a scoped one is
// a containment boundary, not a hint. The same shape leaked cross-tenant on
// another list-all read once already.
func TestCostSummaryNarrowsToTheCallersSubtree(t *testing.T) {
	fake, url := newFakeGateway(t)
	fake.daily = []map[string]map[string]any{{
		"alice":     {"spend": 1.00, "prompt_tokens": 10, "completion_tokens": 0},
		"alice/eng": {"spend": 2.00, "prompt_tokens": 20, "completion_tokens": 0},
		"bob":       {"spend": 4.00, "prompt_tokens": 40, "completion_tokens": 0},
	}}

	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	loop := NewLoop(db, &keyRunner{}, LoopConfig{
		GroupsDir: t.TempDir(), GatewayURL: url, GatewayKey: "sk-master",
	})
	loop.StopQueue()
	srv := NewServer(db, nil, &recDeliverer{},
		fakeVerifier{sub: "user:u", scope: []string{"cost:read:own_group"}, folder: "alice"}, 0, "")
	srv.loop = loop
	h := srv.Handler()

	rec := doJSON(t, h, "GET", "/v1/cost/summary?since=2026-09-01T00:00:00Z", "", nil)
	if rec.Code != 200 {
		t.Fatalf("cost summary = %d want 200: %s", rec.Code, rec.Body.String())
	}
	var got apiv1.CostSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Metered {
		t.Fatal("a gateway is configured; the reply must report a meter")
	}
	seen := map[string]int{}
	for _, f := range got.Folders {
		seen[f.Folder] = f.CostCents
	}
	if _, leaked := seen["bob"]; leaked {
		t.Errorf("a token scoped to alice was handed bob's spend: %+v", got.Folders)
	}
	if seen["alice"] != 100 || seen["alice/eng"] != 200 {
		t.Errorf("own subtree = %+v, want alice 100 and alice/eng 200 cents", got.Folders)
	}
}
