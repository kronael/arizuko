package runed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

func renderWiring(t *testing.T) (*core.Config, *groupfolder.Resolver) {
	t.Helper()
	root := t.TempDir()
	appSrc := filepath.Join(root, "app")
	cfg := &core.Config{
		Name:        "test",
		Timezone:    "UTC",
		ProjectRoot: root,
		HostAppDir:  appSrc,
		AppSrcDir:   appSrc,
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
	for name, body := range map[string]string{
		"arizuko.toml":           "name = \"core\"\n",
		"CLAUDE.md":              "resident manual\n",
		"skills/base/SKILL.md":   "---\nname: base\ndescription: Base\n---\nBase\n",
		"output-styles/brief.md": "brief style\n",
	} {
		file := filepath.Join(cfg.EffectiveAppSrcDir(), "ant", name)
		if _, err := os.Stat(file); os.IsNotExist(err) {
			write(t, file, body)
		}
	}
	lock, err := container.LockPackages(cfg.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := container.EnsureCorePackage(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, name := range []string{"packages", "package-selections"} {
			_ = filepath.WalkDir(filepath.Join(cfg.ProjectRoot, name), func(p string, e os.DirEntry, err error) error {
				if err != nil || !e.IsDir() {
					return err
				}
				return os.Chmod(p, 0o755)
			})
		}
	})
}

func selectRenderPackage(t *testing.T, cfg *core.Config, folder, name, skill, body string) (string, string) {
	t.Helper()
	source := t.TempDir()
	write(t, filepath.Join(source, "arizuko.toml"), "name = \""+name+"\"\n")
	write(t, filepath.Join(source, "skills", skill, "SKILL.md"), body)
	lock, err := container.LockPackages(cfg.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := container.PreparePackage(cfg.ProjectRoot, source)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := container.ReadPackageSelection(cfg.ProjectRoot, folder)
	if err != nil {
		t.Fatal(err)
	}
	var next []container.PackageSnapshot
	for _, previous := range selected {
		if previous.Name != name {
			next = append(next, previous)
		}
	}
	if err := container.SelectPackages(cfg.ProjectRoot, folder, append(next, snapshot)); err != nil {
		t.Fatal(err)
	}
	dir, err := container.ResolvePackage(cfg.ProjectRoot, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	return source, dir
}

func resolverFor(cfg *core.Config) *groupfolder.Resolver {
	return &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
}

func turnFrame(t *testing.T, turn Turn) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(turn.Stdin, &m); err != nil {
		t.Fatalf("turn stdin is not the agent payload: %v (%q)", err, turn.Stdin)
	}
	return m
}
