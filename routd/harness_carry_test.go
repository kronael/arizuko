package routd

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/runed"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

func TestHarnessCarry(t *testing.T) {
	ctx := context.Background()
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
	const harness = `{ "z" : {"unknown": [9007199254740993, "\\u0061"]}, "backend" : "codex" }`
	if err := db.store().WriteEntities(ctx, nil, objstore.Write{Op: objstore.Merge, Entity: store.Groups, Key: objstore.Key{Partition: store.OwnerRoutd, Sort: "demo"}, Attrs: map[string]string{"config": `{"harness":` + harness + `}`}}); err != nil {
		t.Fatal(err)
	}
	runDB, err := runed.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runDB.Close() })
	var got string
	runtime := runed.FakeRuntime{Fn: func(_ context.Context, turn runed.Turn) runed.RunResult {
		got = turn.Env["ARIZUKO_HARNESS"]
		return runed.RunResult{Outcome: runedv1.OutcomeOK}
	}}
	manager := runed.NewManager(runDB, runtime, runed.ManagerConfig{Instance: "test", Cfg: cfg, Folders: &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}})
	server := httptest.NewServer(runed.NewServer(manager, runDB, nil).Handler())
	defer server.Close()
	loop := NewLoop(db, runedv1.NewClient(server.URL, "", 0), LoopConfig{})
	loop.StopQueue()
	check := func() {
		t.Helper()
		got = ""
		out, err := loop.dispatchRun(ctx, "demo", "topic", "web:demo", "turn", "timed-isolated:test", "hello", false, false, "")
		if err != nil || out.Outcome != runedv1.OutcomeOK {
			t.Fatalf("dispatch: %+v %v", out, err)
		}
		if got != harness {
			t.Fatalf("harness = %q, want %q", got, harness)
		}
	}
	check()
	if err := db.store().SetGroupOpen("demo", false); err != nil {
		t.Fatal(err)
	}
	check()
	if err := db.store().SetGroupObserveWindow("demo", 10, 20); err != nil {
		t.Fatal(err)
	}
	check()
	if err := db.PutGroup(core.Group{Folder: "demo", Model: "example"}); err != nil {
		t.Fatal(err)
	}
	check()
	be := resreg.On(db.Obj())
	_, rows := routdManifest(t, be)
	sum, err := resreg.Checksum(be, resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resreg.Apply(ctx, be, resreg.SubsystemRoutd, sum, false, rows, nil); err != nil {
		t.Fatal(err)
	}
	check()
}
