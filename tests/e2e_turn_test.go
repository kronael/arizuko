package tests

// Real container turns against a fake model. routd, runed, the docker spawn,
// the arizuko-ant image, the agent runtime and the in-process MCP socket are
// the production code; the ONLY stand-in is the model endpoint, reached through
// MODEL_GATEWAY_URL — the seam every harness already redirects on (spec 6/23 §
// Wiring). No network, no model credential, no spend.
//
// Docker is driven as the test process can: directly, or through a `sudo -n`
// PATH shim, because every spawn site calls the bare `docker` binary
// (container.Bin). Skipped under -short and wherever the docker or the agent
// image is missing; `make test-turns` runs it deliberately.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/routd"
	routdv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/runed"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

const (
	antImage = "arizuko-ant:latest"
	// turnModel pins the price the CLI books: sonnet 4.5 lists at $3/M input,
	// $15/M output, so the fake's 100k/10k tokens cost $0.45 → 45 cents.
	turnModel   = "claude-sonnet-4-5"
	turnInTok   = 100_000
	turnOutTok  = 10_000
	turnCents   = 45
	turnFolder  = "demo"
	turnChat    = "slack:T/C/U"
	fakeAnswer  = "fake reply from the model"
	turnTimeout = 4 * time.Minute
)

// dockerForTurns returns the docker bridge gateway — the address a container
// sees the test process at — after making `docker` callable from this process,
// or skips.
func dockerForTurns(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("container turns run under make test-turns, not -short")
	}
	real, err := exec.LookPath("docker")
	if err != nil {
		t.Skipf("no docker binary: %v", err)
	}
	if exec.Command(real, "info").Run() != nil {
		if exec.Command("sudo", "-n", real, "info").Run() != nil {
			t.Skip("docker unreachable both directly and via sudo -n")
		}
		shim := t.TempDir()
		script := "#!/bin/sh\nexec sudo -n " + real + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(shim, "docker"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if out, err := exec.Command("docker", "image", "inspect", antImage).CombinedOutput(); err != nil {
		t.Skipf("%s missing (make agent): %s", antImage, strings.TrimSpace(string(out)))
	}
	out, err := exec.Command("docker", "network", "inspect", "bridge",
		"--format", "{{range .IPAM.Config}}{{.Gateway}}{{end}}").Output()
	if err != nil {
		t.Fatalf("docker network inspect bridge: %v", err)
	}
	gw := strings.TrimSpace(string(out))
	if gw == "" {
		t.Fatal("docker bridge has no gateway address")
	}
	return gw
}

// stageAnt mirrors what the arizuko image puts under /opt/arizuko/ant — the
// three trees runed reads when it seeds a group — so MaterializeAppSrc stages
// exactly what a release stages.
func stageAnt(t *testing.T, appsrc string) {
	t.Helper()
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(appsrc, "ant")
	for _, dir := range []string{"skills", "output-styles"} {
		if err := os.CopyFS(filepath.Join(dst, dir), os.DirFS(filepath.Join(repo, "ant", dir))); err != nil {
			t.Fatalf("stage ant/%s: %v", dir, err)
		}
	}
	md, err := os.ReadFile(filepath.Join(repo, "ant", "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "CLAUDE.md"), md, 0o644); err != nil {
		t.Fatal(err)
	}
}

// recDeliverer records what routd hands the adapter: the agent's reply and, on
// a refused turn, the budget notice that never spawns anything.
type recDeliverer struct {
	fedDeliverer
	mu    sync.Mutex
	texts []string
}

func (r *recDeliverer) Send(_, text, _, _, _, _ string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	return "platform-rec-1", nil
}

func (r *recDeliverer) sent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.texts...)
}

// turnStack is the federation plus what a real spawn needs: the data dir routd,
// runed and the container share, and the fake model behind the gateway URL.
type turnStack struct {
	*federation
	cfg   *core.Config
	llm   *testutils.FakeLLM
	sends *recDeliverer
}

func bootTurnStack(t *testing.T) *turnStack {
	t.Helper()
	bridge := dockerForTurns(t)
	// No model credential may reach the container from this process: with the
	// gateway pair set the harness needs none, and a host key would let the CLI
	// authenticate with something other than the virtual key under test.
	for k := range store.EnvProfileKeys {
		t.Setenv(k, "")
	}
	t.Setenv("ARIZUKO_DEV", "")

	root := t.TempDir()
	appsrc := filepath.Join(root, "appsrc")
	stageAnt(t, appsrc)
	inst := fmt.Sprintf("turn%d", os.Getpid())
	cfg := &core.Config{
		Name:            inst,
		Image:           antImage,
		Timezone:        "UTC",
		ProjectRoot:     root,
		HostProjectRoot: root,
		HostAppDir:      appsrc,
		AppSrcDir:       appsrc,
		StoreDir:        filepath.Join(root, "store"),
		GroupsDir:       filepath.Join(root, "groups"),
		IpcDir:          filepath.Join(root, "ipc"),
		WebDir:          filepath.Join(root, "web"),
		MaxContainers:   1,
		Timeout:         turnTimeout,
		IdleTimeout:     turnTimeout,
		ModelGatewayKey: "fake-virtual-key",
	}
	container.MaterializeAppSrc(cfg)

	llm := testutils.NewFakeLLM(t, testutils.LLMReply{
		Text: fakeAnswer, InputTokens: turnInTok, OutputTokens: turnOutTok,
		CostCents: turnCents,
	})
	cfg.ModelGatewayURL = llm.URLFor(bridge)

	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
	sends := &recDeliverer{}
	f := bootFederationWith(t, fedOpts{
		instance: inst,
		runtime:  func(*federation) runed.Runtime { return runed.NewDockerRuntime(cfg, folders) },
		deliver:  sends,
		runTTL:   turnTimeout,
		loop: []func(*routd.LoopConfig){func(lc *routd.LoopConfig) {
			lc.IpcDir = cfg.IpcDir
			lc.GroupsDir = cfg.GroupsDir
			lc.CostCapsEnabled = true
			// routd holds the master key and mints the per-turn key from it;
			// the container gets only what routd minted. Setting the pair on
			// core.Config alone arms container.Run's refusal without arming the
			// mint that satisfies it, and the turn never starts (spec 6/23).
			// routd runs in the test process on the host, so it reaches the
			// fake on loopback; the container reaches the same fake across the
			// docker bridge. One gateway, two routes to it — in a deployment
			// both sides are inside the compose network and the value is one.
			lc.GatewayURL = llm.URLFor("127.0.0.1")
			lc.GatewayKey = cfg.ModelGatewayKey
		}},
	})
	// A run the test abandoned (a failed wait) must not outlive it.
	t.Cleanup(func() { container.CleanupOrphans(inst, antImage) })

	if err := container.SetupGroup(cfg, turnFolder, ""); err != nil {
		t.Fatalf("setup group: %v", err)
	}
	if err := f.routdDB.PutGroup(core.Group{Folder: turnFolder, Model: turnModel}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	if _, err := f.routdDB.AddRoute(core.Route{Match: "platform=slack", Target: turnFolder}); err != nil {
		t.Fatalf("add route: %v", err)
	}
	return &turnStack{federation: f, cfg: cfg, llm: llm, sends: sends}
}

// inbound posts one adapter message; routd's poll loop takes it from there.
func (s *turnStack) inbound(t *testing.T, id, text string) {
	t.Helper()
	in := routdv1.Message{ID: id, ChatJID: turnChat, Sender: "u1", Content: text, Verb: "message"}
	rec := postBearer(t, s.routdTS.URL, "POST", "/v1/messages", s.authd.mintAdapter(t, "slakd"), "", in)
	if rec.StatusCode != 200 {
		t.Fatalf("ingest %s: status %d", id, rec.StatusCode)
	}
}

// spendCents is what the gateway has metered for the turn's folder. arizuko
// holds no spend, so this asks the meter — the same number its budget gate
// refuses on.
func (s *turnStack) spendCents() int { return s.llm.TeamSpendCents(turnFolder) }

func (s *turnStack) spawns(t *testing.T) int {
	t.Helper()
	return spawnCount(t, s.runedDB)
}

// awaitIdle waits until runed holds no live run: the reply lands on
// submit_turn, before the container exits, and the next step must start from a
// closed run rather than steer into this one.
func (s *turnStack) awaitIdle(t *testing.T) {
	t.Helper()
	waitFor(t, "runed to close the run", turnTimeout, func() bool {
		return liveSpawnCount(t, s.runedDB) == 0
	})
}

// costGet reads the turn through routd's REST face with a cost:read bearer.
func (s *turnStack) costGet(t *testing.T, turnID string) (int, routdv1.CostResponse) {
	t.Helper()
	tok := s.authd.mintService(t, "service:dashd", "cost:read")
	rec := getBearer(t, s.routdTS.URL, "/v1/cost?turn_id="+turnID, tok)
	var out routdv1.CostResponse
	if rec.StatusCode == 200 {
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return rec.StatusCode, out
}

func (s *turnStack) costSummary(t *testing.T) routdv1.CostSummaryResponse {
	t.Helper()
	tok := s.authd.mintService(t, "service:dashd", "cost:read")
	since := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)
	rec := getBearer(t, s.routdTS.URL, "/v1/cost/summary?since="+since, tok)
	if rec.StatusCode != 200 {
		t.Fatalf("cost summary: status %d", rec.StatusCode)
	}
	var out routdv1.CostSummaryResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func getBearer(t *testing.T, base, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestE2ETurn_CostAccounting drives three consecutive turns through one stack.
//
//  1. A completed turn: the container's harness talks to the fake through the
//     gateway pair, submit_turn lands, the reply reaches the chat, and the
//     GATEWAY carries the tokens the fake served — readable on both cost faces
//     dashd uses, and held nowhere in arizuko.
//  2. The folder cap set at that spend: the next message is refused at
//     dispatch with the budget notice, and neither runed nor the model sees it.
//  3. The cap lifted and the model failing: the agent submits no usage, and the
//     meter is unmoved by what the agent says about itself.
func TestE2ETurn_CostAccounting(t *testing.T) {
	s := bootTurnStack(t)

	t.Run("completed turn books cost", func(t *testing.T) {
		s.inbound(t, "wamid.1", "say hi")
		testutils.WaitForBotRow(t, store.NewOver(s.routdDB.Obj()), turnChat, turnTimeout)
		testutils.AssertMessage(t, store.NewOver(s.routdDB.Obj()), turnChat, fakeAnswer)

		calls := s.llm.Calls()
		t.Logf("model calls served: %+v", calls)
		if len(calls) == 0 {
			t.Fatal("the model endpoint saw no request: the harness did not use MODEL_GATEWAY_URL")
		}
		// routd mints a key per turn and hands THAT to the container, so the
		// bearer on the wire is the minted one, never the instance master key
		// the config carries (spec 6/23). A turn that presented the master key
		// would mean the mint was skipped and revocation reaches nothing.
		minted := s.llm.MintedKeys()
		if len(minted) == 0 {
			t.Fatal("the gateway minted no key: routd did not mint for this turn")
		}
		mintedBearers := map[string]bool{}
		for _, k := range minted {
			mintedBearers["Bearer "+k] = true
		}
		for _, c := range calls {
			if c.Bearer == "Bearer fake-virtual-key" {
				t.Errorf("model call carried the instance master key; the per-turn mint was skipped")
			} else if !mintedBearers[c.Bearer] {
				t.Errorf("model call bearer = %q, want a key the gateway minted for this turn", c.Bearer)
			}
			if c.Model != turnModel {
				t.Errorf("model call model = %q, want the group override %q", c.Model, turnModel)
			}
		}

		// The CLI decides how many requests one turn takes (the SDK path issued
		// two for this prompt where `claude -p` issued one); what accounting
		// owes is that every request the endpoint served is metered, at the
		// tokens it served and the CLI's list price for them.
		n := len(calls)
		wantIn, wantOut, wantCents := n*turnInTok, n*turnOutTok, n*turnCents
		// The turn's key was minted for THIS folder and THIS caller, which is
		// how the gateway attributes and caps. Nothing in arizuko records what
		// it cost.
		if got := s.spendCents(); got != wantCents {
			t.Fatalf("gateway metered %d cents against %s, want %d — the turn's key "+
				"was not charged to its folder", got, turnFolder, wantCents)
		}
		if status, got := s.costGet(t, "wamid.1"); status != 200 ||
			got.Folder != turnFolder || got.CostCents != wantCents || got.InputTokens != wantIn {
			t.Fatalf("GET /v1/cost = %d %+v, want 200 folder=%s cents=%d", status, got, turnFolder, wantCents)
		}
		sum := s.costSummary(t)
		if !sum.Metered {
			t.Fatal("GET /v1/cost/summary reported no meter on an instance with a gateway")
		}
		if len(sum.Folders) != 1 || sum.Folders[0].Folder != turnFolder ||
			sum.Folders[0].CostCents != wantCents || sum.Folders[0].Tokens != wantIn+wantOut {
			t.Fatalf("GET /v1/cost/summary = %+v, want one %s row with %d tokens, %d cents",
				sum.Folders, turnFolder, wantIn+wantOut, wantCents)
		}
		if n := s.spawns(t); n != 1 {
			t.Fatalf("runed spawns = %d, want 1", n)
		}
		s.awaitIdle(t)
	})
	if t.Failed() {
		return
	}

	t.Run("cap at today's spend refuses the next turn at dispatch", func(t *testing.T) {
		spent := s.spendCents()
		if spent == 0 {
			t.Fatal("the gateway metered nothing for the first turn; the cap would prove nothing")
		}
		if err := s.routdDB.SetFolderCap(turnFolder, spent); err != nil {
			t.Fatal(err)
		}
		calls, spawns := len(s.llm.Calls()), s.spawns(t)
		s.inbound(t, "wamid.2", "and again")
		waitFor(t, "the budget refusal to reach the adapter", 15*time.Second, func() bool {
			for _, txt := range s.sends.sent() {
				if strings.Contains(txt, "Budget reached for today") {
					return true
				}
			}
			return false
		})
		if n := s.spawns(t); n != spawns {
			t.Fatalf("runed spawns = %d, want %d: a refused turn must not spawn", n, spawns)
		}
		if n := len(s.llm.Calls()); n != calls {
			t.Fatalf("model calls = %d, want %d: a refused turn must not reach the model", n, calls)
		}
		if n := countBotRows(t, s.routdDB, turnChat); n != 1 {
			t.Fatalf("bot rows = %d, want 1: the refusal is a notice, not a reply row", n)
		}
	})
	if t.Failed() {
		return
	}

	t.Run("model failure moves the meter only by what it served", func(t *testing.T) {
		if err := s.routdDB.SetFolderCap(turnFolder, 0); err != nil {
			t.Fatal(err)
		}
		before := s.spendCents()
		// A refused call costs nothing, so the meter must not move — and the
		// agent's own report of the turn cannot move it either, because arizuko
		// keeps no number the agent could write to.
		s.llm.SetReply(testutils.LLMReply{Status: 400, Message: "fake says no", CostCents: 0})
		calls := len(s.llm.Calls())
		s.inbound(t, "wamid.3", "one more")
		waitFor(t, "the turn to close", turnTimeout, func() bool {
			return countBotRows(t, s.routdDB, turnChat) >= 2
		})
		if n := len(s.llm.Calls()); n <= calls {
			t.Fatal("the model endpoint saw no request for the third turn")
		}
		// The CLI reports the API failure as its result text; ant forwards it
		// as the turn's reply with no usage attached.
		testutils.AssertMessage(t, store.NewOver(s.routdDB.Obj()), turnChat, "API Error: 400")
		// The gateway writes a spend row for a refused call too — status
		// "failure", zero tokens, zero spend — so the turn IS metered and is
		// charged nothing. Reporting it as 404 would lose the difference between
		// a turn that reached the model and one that never did.
		status, got := s.costGet(t, "wamid.3")
		if status != 200 {
			t.Fatalf("GET /v1/cost for a refused turn = %d, want 200", status)
		}
		if got.CostCents != 0 || got.InputTokens != 0 || got.OutputTokens != 0 {
			t.Fatalf("GET /v1/cost for a refused turn = %+v, want all zero", got)
		}
		if got := s.spendCents(); got != before {
			t.Fatalf("meter after a failed turn = %d, want %d unchanged", got, before)
		}
		s.awaitIdle(t)
	})
}
