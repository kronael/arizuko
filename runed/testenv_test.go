package runed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

// renderWiring is what a Manager needs to render an agent turn: a config and a
// resolver over a temp tree, so the real renderer runs with no docker and
// without touching the repo. The staged `ant/` dirs exist but are empty, which
// is what keeps the per-spawn refresh quiet.
func renderWiring(t *testing.T) (*core.Config, *groupfolder.Resolver) {
	t.Helper()
	root := t.TempDir()
	appSrc := filepath.Join(root, "app")
	for _, d := range []string{
		filepath.Join(appSrc, "ant", "output-styles"),
		filepath.Join(appSrc, "ant", "skills", "migrate"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &core.Config{
		Name:        "test",
		Image:       "arizuko-ant:test",
		Timezone:    "UTC",
		ProjectRoot: root,
		HostAppDir:  appSrc,
		GroupsDir:   filepath.Join(root, "groups"),
		IpcDir:      filepath.Join(root, "ipc"),
		WebDir:      filepath.Join(root, "web"),
		IdleTimeout: 30 * time.Second,
		Timeout:     60 * time.Second,
	}
	return cfg, resolverFor(cfg)
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
