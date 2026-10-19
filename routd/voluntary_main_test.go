package routd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/ipc"
)

// A follow-up accepted by steering has an authoritative routd turn, but the
// plain-text guest input loses it and ant invents seed:1. This drives the real
// entrypoint against the real broker, replacing only the model harness.
func runVoluntaryMainFixture(t *testing.T, queued bool) (*DB, *recDeliverer, []string, string) {
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
	fns := srv.buildGatedFns(bound)
	var mu sync.Mutex
	var submitted []string
	fns.SubmitTurn = func(folder string, result ipc.TurnResult) (ipc.TurnAck, error) {
		mu.Lock()
		submitted = append(submitted, result.TurnID)
		first := len(submitted) == 1
		mu.Unlock()
		ack, err := srv.mcpSubmitTurn(folder, result)
		if queued && first && err == nil {
			// This is the current guest input format: text, without turn_id.
			err = os.WriteFile(filepath.Join(input, "followup.json"), []byte(`{"type":"message","text":"second request"}`), 0o600)
		}
		return ack, err
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bun, "--preload", preload, filepath.Join(source, "index.ts"))
	mode := "submit-nudge"
	if queued {
		mode = "queued"
	}
	cmd.Env = append(os.Environ(), "VOLUNTARY_FIXTURE_ROOT="+root, "VOLUNTARY_FIXTURE_SOURCE="+source, "VOLUNTARY_FIXTURE_MODE="+mode)
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
	db, dl, submitted, output := runVoluntaryMainFixture(t, false)
	tc, _ := db.GetTurnContext("first")
	if len(submitted) != 2 || submitted[0] != "first" || submitted[1] != "first" || tc.Nudged != 1 || tc.State != "done" || !db.TurnResultRecorded("demo", "first") {
		t.Fatalf("submission nudge lost its binding: submissions=%q context=%+v\n%s", submitted, tc, output)
	}
	if len(dl.sends) != 1 || dl.sends[0].text != "first answer" {
		t.Fatalf("deliveries=%+v\n%s", dl.sends, output)
	}
}

func TestVoluntaryReplyQueuedInputKeepsAuthoritativeTurn(t *testing.T) {
	t.Skip("L458: plain-text steering loses the follow-up's bound turn identity; lifecycle policy needs sign-off")
	_, _, submitted, output := runVoluntaryMainFixture(t, true)
	if len(submitted) < 2 || submitted[1] != "second" {
		t.Fatalf("follow-up identity lost: submissions=%q; authoritative turn=second\n%s", submitted, output)
	}
}
