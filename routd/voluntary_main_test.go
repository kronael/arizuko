package routd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/runed"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

func runVoluntaryMainFixture(t *testing.T) (*DB, *recDeliverer, []string, string) {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is required for the entrypoint fixture")
	}
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	for _, id := range []string{"first", "second"} {
		if _, err := db.PutTurnContext(id, "demo", "", "tg:1", "u1", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.SetTurnAddressed(id, true); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	input := filepath.Join(root, "run/ipc/input")
	if err := os.MkdirAll(input, 0o700); err != nil {
		t.Fatal(err)
	}
	bound := turnMCP{folder: "demo", chatJID: "tg:1", turnID: "first", trigger: "u1"}
	fns := srv.buildRoutdFns(bound)
	var mu sync.Mutex
	var submitted []string
	fns.SubmitTurn = func(folder string, result ipc.TurnResult) (ipc.TurnAck, error) {
		mu.Lock()
		submitted = append(submitted, result.TurnID)
		mu.Unlock()
		return srv.mcpSubmitTurn(folder, result)
	}
	stop, err := ipc.ServeMCP(filepath.Join(root, "run/ipc/gated.sock"), fns, srv.buildStoreFns(bound), "demo", true, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	source, err := filepath.Abs("../ant/src")
	if err != nil {
		t.Fatal(err)
	}
	preload, err := filepath.Abs("../ant/testdata/voluntary-main.ts")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bun, "--preload", preload, filepath.Join(source, "index.ts"))
	cmd.Env = append(os.Environ(), "VOLUNTARY_FIXTURE_ROOT="+root, "VOLUNTARY_FIXTURE_SOURCE="+source, "VOLUNTARY_FIXTURE_MODE=submit-nudge")
	cmd.Stdin = strings.NewReader(`{"prompt":"first request","messageId":"first","groupFolder":"demo","chatJid":"tg:1"}`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("entrypoint: %v\n%s", err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	return db, dl, submitted, string(output)
}

// A successful empty submission must resume the real entrypoint with the same
// broker binding, deliver explicitly, and record exactly one final completion.
func TestVoluntaryReplyMainContinuesSubmissionNudge(t *testing.T) {
	db, dl, submitted, output := runVoluntaryMainFixture(t)
	tc, _ := db.GetTurnContext("first")
	if len(submitted) != 2 || submitted[0] != "first" || submitted[1] != "first" || tc.Nudged != 1 || tc.State != "done" || !db.TurnResultRecorded("demo", "first") {
		t.Fatalf("submission nudge lost its binding: submissions=%q context=%+v\n%s", submitted, tc, output)
	}
	if len(dl.sends) != 1 || dl.sends[0].text != "first answer" {
		t.Fatalf("deliveries=%+v\n%s", dl.sends, output)
	}
}

// Two inbound turns use separate manager admissions and exact broker bindings.
func TestVoluntaryReplyQueuedInputKeepsAuthoritativeTurn(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Fatal(err)
	}
	source, err := filepath.Abs("../ant/src")
	if err != nil {
		t.Fatal(err)
	}
	preload, err := filepath.Abs("../ant/testdata/voluntary-main.ts")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cfg := &core.Config{
		Name: "fixture", Timezone: "UTC", ProjectRoot: root,
		HostAppDir: filepath.Join(root, "app"),
		GroupsDir:  filepath.Join(root, "groups"),
		IpcDir:     filepath.Join(root, "ipc"), WebDir: filepath.Join(root, "web"),
	}
	for _, dir := range []string{"ant/output-styles", "ant/skills/migrate"} {
		if err := os.MkdirAll(filepath.Join(cfg.HostAppDir, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	runDB, err := runed.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runDB.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	var ids, outputs []string
	var steers int
	runtime := runed.FakeRuntime{Fn: func(ctx context.Context, turn runed.Turn) runed.RunResult {
		var frame struct {
			MessageID string `json:"messageId"`
		}
		if err := json.Unmarshal(turn.Stdin, &frame); err != nil {
			return runed.RunResult{Outcome: runedv1.OutcomeError, Error: err.Error(), Terminal: true}
		}
		ids = append(ids, frame.MessageID)
		input := filepath.Join(root, "run/ipc/input")
		if err := os.MkdirAll(input, 0o700); err != nil {
			return runed.RunResult{Outcome: runedv1.OutcomeError, Error: err.Error(), Terminal: true}
		}
		turn.RegisterSteer(func(batch string) bool {
			steers++
			raw, _ := json.Marshal(map[string]string{"type": "message", "text": batch})
			return os.WriteFile(filepath.Join(input, "followup.json"), raw, 0o600) == nil
		})
		if frame.MessageID == "first" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return runed.RunResult{Outcome: runedv1.OutcomeError, Error: ctx.Err().Error(), Terminal: true}
			}
		}
		cmd := exec.CommandContext(ctx, bun, "--preload", preload, filepath.Join(source, "index.ts"))
		cmd.Env = append(os.Environ(),
			"VOLUNTARY_FIXTURE_ROOT="+root, "VOLUNTARY_FIXTURE_SOURCE="+source,
			"VOLUNTARY_FIXTURE_SOCKET="+filepath.Join(cfg.IpcDir, "demo/gated.sock"),
			"VOLUNTARY_FIXTURE_MODE=lifecycle")
		cmd.Stdin = strings.NewReader(string(turn.Stdin))
		output, err := cmd.CombinedOutput()
		outputs = append(outputs, string(output))
		if err != nil {
			return runed.RunResult{Outcome: runedv1.OutcomeError, Error: err.Error(), Terminal: true}
		}
		return runed.RunResult{Outcome: runedv1.OutcomeOK}
	}}
	manager := runed.NewManager(runDB, runtime, runed.ManagerConfig{
		Instance: "fixture", Cfg: cfg, Folders: folders,
	})
	dl := &recDeliverer{}
	loop := NewLoop(db, manager, LoopConfig{
		GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir, Deliver: dl,
		RunTimeout: 60 * time.Second,
	})
	loop.StopQueue()
	srv := NewServer(db, loop, dl, nil, 0, "")
	loop.BindServer(srv)
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()
	doJSON(t, handler, "PUT", "/v1/routes", "", map[string]any{
		"routes": []apiv1.Route{{Match: "platform=telegram", Target: "demo"}},
	})
	ingest := func(id string) {
		rec := doJSON(t, handler, "POST", "/v1/messages", "", apiv1.Message{
			ID: id, ChatJID: "telegram:1", Sender: "u1", Content: id + " request", Verb: "mention",
		})
		if rec.Code != 200 {
			t.Fatalf("ingest %s: %d %s", id, rec.Code, rec.Body.String())
		}
	}
	ingest("first")
	done := make(chan error, 1)
	go func() {
		_, err := loop.processGroupMessages("telegram:1")
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("first turn never reached the runtime: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	ingest("second")
	busy, err := manager.Run(ctx, runedv1.RunRequest{
		Folder: "demo", ChatJID: "telegram:1", TurnID: "second", MessageBatch: "second request",
	})
	close(release)
	if firstErr := <-done; firstErr != nil {
		t.Fatal(firstErr)
	}
	if err != nil || !busy.Busy || busy.Steered || steers != 0 {
		t.Fatalf("fresh turn entered the active run: out=%+v err=%v steers=%d\n%s", busy, err, steers, outputs)
	}
	if !db.TurnResultRecorded("demo", "first") || db.TurnResultRecorded("demo", "second") {
		t.Fatalf("follow-up completed before refeed\n%s", outputs)
	}
	pending, err := db.NewMessagesForChat("telegram:1", db.GetAgentCursor("telegram:1"), pollBatch)
	latest := newestUserIdx(pending)
	if err != nil || latest < 0 || pending[latest].ID != "second" {
		t.Fatalf("follow-up not retained above the cursor: pending=%+v err=%v", pending, err)
	}
	if _, err := loop.processGroupMessages("telegram:1"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "first,second" {
		t.Fatalf("rendered identities=%q\n%s", ids, outputs)
	}
	for _, id := range []string{"first", "second"} {
		turn, ok := db.GetTurnContext(id)
		if !ok || turn.State != "done" || turn.Nudged != 1 || !db.TurnResultRecorded("demo", id) {
			t.Fatalf("turn %s did not close under its own binding: %+v\n%s", id, turn, outputs)
		}
	}
	first, _ := db.GetTurnContext("first")
	second, _ := db.GetTurnContext("second")
	if first.RunID == "" || second.RunID == "" || first.RunID == second.RunID {
		t.Fatalf("turns share a run: first=%+v second=%+v", first, second)
	}
	if len(dl.sends) != 2 || dl.sends[0].text != "first answer" || dl.sends[1].text != "second answer" {
		t.Fatalf("deliveries=%+v\n%s", dl.sends, outputs)
	}
	if strings.Contains(strings.Join(outputs, "\n"), "first:1") {
		t.Fatalf("guest synthesized a follow-up identity\n%s", outputs)
	}
}
