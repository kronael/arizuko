package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
)

func skillsDash(t *testing.T) (*dash, *core.Config) {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	groupCoreSource(t)
	cfg := &core.Config{ProjectRoot: dataDir, GroupsDir: filepath.Join(dataDir, "groups"),
		AppSrcDir: os.Getenv("APP_SRC_DIR")}
	lock, err := container.LockPackages(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := container.EnsureCorePackage(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	return &dash{groupsDir: cfg.GroupsDir, appDir: t.TempDir()}, cfg
}

func TestStockSkills(t *testing.T) {
	d, cfg := skillsDash(t)
	lock, err := container.LockPackages(cfg.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	source := t.TempDir()
	for name, body := range map[string]string{
		"arizuko.toml":             "name = \"extra\"\nskills = [\"informational\"]\n",
		"skills/optional/SKILL.md": "optional skill",
	} {
		file := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := container.PreparePackage(cfg.ProjectRoot, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := container.SelectPackages(cfg.ProjectRoot, "mygroup", []container.PackageSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	got, err := d.stockSkills("mygroup")
	if err != nil || len(got.Skills) != 2 || got.Skills[0].Name != "base" || got.Skills[1].Name != "optional" {
		t.Fatalf("active skills = %+v, %v", got, err)
	}
	other, err := d.stockSkills("mygroup/child")
	if err != nil || len(other.Skills) != 1 || other.Skills[0].Name != "base" {
		t.Fatalf("parent selection inherited: %+v, %v", other, err)
	}
}

func TestStockSkillsMissingCore(t *testing.T) {
	d := &dash{groupsDir: filepath.Join(t.TempDir(), "groups")}
	if _, err := d.stockSkills("g"); err == nil || !strings.Contains(err.Error(), "core") {
		t.Fatalf("missing core error = %v", err)
	}
}

func TestSkillsDisabled(t *testing.T) {
	d, cfg := skillsDash(t)
	lock, err := container.LockPackages(cfg.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := d.setSkillDisabled("mygroup", "base", true); err != nil {
		t.Fatal(err)
	}
	inventory, err := d.stockSkills("mygroup")
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.skillsDisabled("mygroup", inventory)
	if err != nil || !got["base"] {
		t.Fatalf("disabled = %v, %v", got, err)
	}
	marker := filepath.Join(cfg.GroupsDir, "mygroup", ".claude/skills/base/.disabled")
	if err := os.WriteFile(marker, []byte("preserve marker bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(marker, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := d.setSkillDisabled("mygroup", "base", true); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "preserve marker bytes" {
		t.Fatalf("marker changed: %q, %v", body, err)
	}
	if err := d.setSkillDisabled("mygroup", "base", false); err != nil {
		t.Fatal(err)
	}
	if err := d.setSkillDisabled("mygroup", "base", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker remains: %v", err)
	}
}

func TestSetSkillDisabledRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{"bad name!", "symlink", "payload", "ancestor"} {
		t.Run(name, func(t *testing.T) {
			d, cfg := skillsDash(t)
			lock, err := container.LockPackages(cfg.ProjectRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if name == "bad name!" {
				if err := d.setSkillDisabled("g", name, true); err == nil {
					t.Fatal("invalid name accepted")
				}
				return
			}
			dir := filepath.Join(cfg.GroupsDir, "g", ".claude/skills/base")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			switch name {
			case "symlink":
				err = os.Symlink(filepath.Join(outside, "marker"), filepath.Join(dir, ".disabled"))
			case "payload":
				err = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("custom"), 0o644)
			case "ancestor":
				err = os.Symlink(outside, filepath.Join(cfg.GroupsDir, "bad"))
			}
			if err != nil {
				t.Fatal(err)
			}
			folder := "g"
			if name == "ancestor" {
				folder = "bad/child"
			}
			if err := d.setSkillDisabled(folder, "base", true); err == nil {
				t.Fatal("unsafe marker path accepted")
			}
		})
	}
}

func TestGroupSettingsActiveSkills(t *testing.T) {
	d, cfg := skillsDash(t)
	d.routd = routdDB(t)
	d.routdURL = stubRoutd(t, d.routd)
	seedFolder(t, d.routd, "mygroup")
	seedAdmin(t, d.routd, "alice@x", "mygroup")
	source := t.TempDir()
	for name, body := range map[string]string{"arizuko.toml": "name = \"extra\"\n",
		"skills/optional/SKILL.md": "old optional"} {
		file := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := container.LockPackages(cfg.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := container.PreparePackage(cfg.ProjectRoot, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := container.SelectPackages(cfg.ProjectRoot, "mygroup", []container.PackageSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	mux := newMux(d)
	get := httptest.NewRecorder()
	mux.ServeHTTP(get, adminReq("GET", "/dash/groups/mygroup/settings", "", "alice@x"))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `value="base"`) ||
		!strings.Contains(get.Body.String(), `value="optional"`) {
		t.Fatalf("picker = %d, %s", get.Code, get.Body.String())
	}
	post := httptest.NewRecorder()
	mux.ServeHTTP(post, adminReq("POST", "/dash/groups/mygroup/settings", "open=1", "alice@x"))
	if post.Code != http.StatusSeeOther {
		t.Fatalf("save = %d, %s", post.Code, post.Body.String())
	}
	marker := filepath.Join(cfg.GroupsDir, "mygroup", ".claude/skills/base/.disabled")
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	optionalMarker := filepath.Join(cfg.GroupsDir, "mygroup/.claude/skills/optional/.disabled")
	if _, err := os.Stat(optionalMarker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "skills/optional/SKILL.md"), []byte("current optional"), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err = container.LockPackages(cfg.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := container.PreparePackage(cfg.ProjectRoot, source)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded == snapshot {
		t.Fatal("upgrade retained old artifact")
	}
	if err := container.SelectPackages(cfg.ProjectRoot, "mygroup", []container.PackageSnapshot{upgraded}); err != nil {
		t.Fatal(err)
	}
	inventory, err := d.stockSkills("mygroup")
	if err != nil {
		t.Fatal(err)
	}
	disabled, _, err := container.PreparePackageMountTargets(cfg, "mygroup", inventory)
	if err != nil || !disabled["base"] || !disabled["optional"] {
		t.Fatalf("picker choices did not survive upgrade: %v, %v", disabled, err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	enable := httptest.NewRecorder()
	mux.ServeHTTP(enable, adminReq("POST", "/dash/groups/mygroup/settings",
		"open=1&skill_enabled=base&skill_enabled=optional", "alice@x"))
	if enable.Code != http.StatusSeeOther {
		t.Fatalf("enable = %d, %s", enable.Code, enable.Body.String())
	}
	for _, file := range []string{marker, optionalMarker} {
		if _, err := os.Lstat(file); !os.IsNotExist(err) {
			t.Fatalf("enable retained marker %s: %v", file, err)
		}
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(marker), "SKILL.md"), []byte("custom bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused := httptest.NewRecorder()
	mux.ServeHTTP(refused, adminReq("POST", "/dash/groups/mygroup/settings", "open=0", "alice@x"))
	if refused.Code != http.StatusInternalServerError || !strings.Contains(refused.Body.String(), "SKILL.md") {
		t.Fatalf("collision save = %d, %s", refused.Code, refused.Body.String())
	}
	failedGet := httptest.NewRecorder()
	mux.ServeHTTP(failedGet, adminReq("GET", "/dash/groups/mygroup/settings", "", "alice@x"))
	if !strings.Contains(failedGet.Body.String(), "SKILL.md") || !strings.Contains(failedGet.Body.String(), "packages install/upgrade") {
		t.Fatalf("picker hid collision: %s", failedGet.Body.String())
	}
	group, err := d.getGroup(t.Context(), "mygroup")
	if err != nil || !group.Open {
		t.Fatalf("settings wrote before preflight: %+v, %v", group, err)
	}
}
