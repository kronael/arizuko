package runed

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// renderWiring is what a Manager needs to render an agent turn: a config and a
// resolver over a temp tree, so the real renderer runs with no docker and
// without touching the repo. The staged `ant/` dirs exist but are empty, which
// is what keeps the per-spawn refresh quiet.
func renderWiring(t *testing.T) (*core.Config, *groupfolder.Resolver) {
	t.Helper()
	root := t.TempDir()
	appSrc := filepath.Join(root, "app")
	cfg := &core.Config{
		Name:        "test",
		Timezone:    "UTC",
		ProjectRoot: root,
		HostAppDir:  appSrc,
		GroupsDir:   filepath.Join(root, "groups"),
		IpcDir:      filepath.Join(root, "ipc"),
		WebDir:      filepath.Join(root, "web"),
		IdleTimeout: 30 * time.Second,
		Timeout:     60 * time.Second,
	}
	initializeRenderCore(t, cfg)
	return cfg, resolverFor(cfg)
}

func initializeRenderCore(t *testing.T, cfg *core.Config) {
	t.Helper()
	write(t, filepath.Join(cfg.ProjectRoot, "packages", "core", "arizuko.yml"), "name: core\n")
	write(t, filepath.Join(cfg.ProjectRoot, "packages", "core", "CLAUDE.md"), "core instructions\n")
	write(t, filepath.Join(cfg.ProjectRoot, "packages", "core", "skills", "base", "SKILL.md"), "base skill\n")
}

func selectedCore(t *testing.T, cfg *core.Config) string {
	t.Helper()
	return filepath.Join(cfg.ProjectRoot, "packages", "core")
}

func mustBuildMounts(t *testing.T, cfg *core.Config, folders *groupfolder.Resolver,
	req runedv1.RunRequest, gc core.GroupConfig, groupDir string) []core.Mount {
	t.Helper()
	initializeRenderCore(t, cfg)
	mounts, err := buildMounts(cfg, folders, req, gc, groupDir)
	if err != nil {
		t.Fatal(err)
	}
	return mounts
}

// resolverFor is the folder resolver for a config, the pairing every renderer
// call takes.
func resolverFor(cfg *core.Config) *groupfolder.Resolver {
	return &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
}

// turnFrame decodes the agent payload a rendered Turn carries — the same JSON
// ant/src/index.ts parses — so a test can assert what the guest would read.
func turnFrame(t *testing.T, turn Turn) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(turn.Stdin, &m); err != nil {
		t.Fatalf("turn stdin is not the agent payload: %v (%q)", err, turn.Stdin)
	}
	return m
}
