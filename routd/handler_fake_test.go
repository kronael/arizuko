package routd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/runed"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

// A group with `agent: fake` and no model gateway or credential runs a static
// Go handler built from testdata/fakehandler: runed renders the turn, the
// handler reads the stdin payload, replies and submits the turn over routd's
// real turn socket, and the turn ends as a success
// (specs/7/57-group-handler.md, acceptance 2). The runtime seam starts the
// binary with the rendered env and stdin, and the socket at the ipc mount's
// host path, where the guest's bridge would serve /run/ipc/gated.sock.
func TestFakeHandlerRepliesAndSubmitsTheTurn(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fake")
	build := exec.Command("go", "build", "-o", binary, "./testdata/fakehandler")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake handler: %v\n%s", err, out)
	}
	root := t.TempDir()
	cfg := &core.Config{Name: "test", Timezone: "UTC", ProjectRoot: root, HostAppDir: filepath.Join(root, "app"), GroupsDir: filepath.Join(root, "groups"), IpcDir: filepath.Join(root, "ipc"), WebDir: filepath.Join(root, "web")}
	for _, dir := range []string{"ant/output-styles", "ant/skills/migrate"} {
		if err := os.MkdirAll(filepath.Join(cfg.HostAppDir, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	manifest, skills, err := container.InstallPackage(root, "../ant", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutInstalledPackage(InstalledPackage{Name: manifest.Name, Source: "ant", Revision: "release", Manifest: map[string][]string{"skill": skills}}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := db.store().SetGroupSettings("demo", map[string]json.RawMessage{"agent": json.RawMessage(`"fake"`)}); err != nil {
		t.Fatal(err)
	}
	runDB, err := runed.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runDB.Close() })
	var ran bool
	var agent string
	runtime := runed.FakeRuntime{Fn: func(ctx context.Context, turn runed.Turn) runed.RunResult {
		ran, agent = true, turn.Env["ARIZUKO_AGENT"]
		for key := range turn.Env {
			if strings.HasPrefix(key, "MODEL_GATEWAY") || strings.HasSuffix(key, "_API_KEY") {
				t.Errorf("model credential %s in the handler's env", key)
			}
		}
		if bytes.Contains(turn.Stdin, []byte(`"secrets"`)) {
			t.Errorf("secrets in the handler's stdin: %s", turn.Stdin)
		}
		var ipcDir string
		for _, mount := range turn.Mounts {
			if mount.Container == "/run/ipc" {
				ipcDir = mount.Host
			}
		}
		cmd := exec.CommandContext(ctx, binary)
		cmd.Env = []string{"FAKE_MCP_SOCK=" + groupfolder.IpcSocket(ipcDir)}
		for key, value := range turn.Env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		cmd.Stdin = bytes.NewReader(turn.Stdin)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return runed.RunResult{Outcome: runedv1.OutcomeError, Error: err.Error() + ": " + stderr.String()}
		}
		return runed.RunResult{Outcome: runedv1.OutcomeOK}
	}}
	manager := runed.NewManager(runDB, runtime, runed.ManagerConfig{Instance: "test", Cfg: cfg, Folders: &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}})
	handler := runed.NewServer(manager, runDB, nil).Handler()
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	dl := &recDeliverer{}
	loop := NewLoop(db, runedv1.NewClient(httpServer.URL, "", time.Minute), LoopConfig{Deliver: dl, IpcDir: cfg.IpcDir, GroupsDir: cfg.GroupsDir})
	loop.StopQueue()
	loop.BindServer(NewServer(db, loop, dl, nil, 0, ""))
	pauseInput(t, db, "web:demo", "fake-turn", "u", "question", time.Now())
	processPauseChat(t, loop, "web:demo")

	if !ran || agent != "fake" {
		t.Fatalf("runtime ran=%v agent=%q, want agent fake", ran, agent)
	}
	sends := dl.sentMsgs()
	if len(sends) != 1 || sends[0].jid != "web:demo" || !strings.Contains(sends[0].text, "fake: ") || !strings.Contains(sends[0].text, "question") {
		t.Fatalf("replies = %+v", sends)
	}
	info, err := db.store().GetTurnResult("demo", "fake-turn")
	if err != nil || info.Status != "success" {
		t.Fatalf("turn result %+v, %v, want success", info, err)
	}
	tc, _ := db.GetTurnContext("fake-turn")
	if tc.State != store.TurnDone || tc.RetryCount != 0 {
		t.Fatalf("turn = %+v", tc)
	}
}
