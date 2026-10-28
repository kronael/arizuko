package runed

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

func TestSelectedPackageHooksAndTools(t *testing.T) {
	cfg, folders := renderWiring(t)
	root := filepath.Join(cfg.ProjectRoot, "packages", "addon")
	write(t, filepath.Join(root, "arizuko.yml"), "name: addon\n")
	write(t, filepath.Join(root, "CLAUDE.md"), "addon operating instructions\n@/opt/arizuko/packages/addon/skills/extra/SKILL.md\n")
	write(t, filepath.Join(root, "skills", "extra", "SKILL.md"), "extra skill")
	write(t, filepath.Join(root, "skills", "base", "SKILL.md"), "package overlay")
	write(t, filepath.Join(root, "hooks", "native.sh"), "printf hook-fired")
	write(t, filepath.Join(root, "bin", "package-tool"), "#!/bin/sh\nprintf tool-fired\n")
	if err := os.Chmod(filepath.Join(root, "bin", "package-tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".claude/settings.json", ".codex/hooks.json"} {
		write(t, filepath.Join(root, rel), `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"printf hook-fired"}]}]}}`)
	}
	req := runedv1.RunRequest{Folder: "main", Packages: []string{"core", "addon"}}
	turn, err := renderTurn(cfg, folders, req, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if mount := findMount(turn.Mounts, core.ContainerHome+"/.claude/skills/base"); mount == nil || mount.Host != filepath.Join(root, "skills", "base") {
		t.Fatal("selected package did not overlay the core skill")
	}
	manual := findMount(turn.Mounts, core.ContainerHome+"/.claude/CLAUDE.md")
	data, err := os.ReadFile(manual.Host)
	if err != nil || !manual.RO || !strings.Contains(string(data), "addon operating instructions") || !strings.Contains(string(data), "@/opt/arizuko/packages/addon/") {
		t.Fatalf("package manual or native import missing: %s %v", data, err)
	}
	for _, guest := range []string{
		core.ContainerHome + "/.claude/skills/extra", "/opt/arizuko/packages/addon",
		core.ContainerHome + "/.claude/hooks/native.sh",
		"/usr/local/bin/package-tool", "/etc/codex/config.toml", "/etc/codex/hooks.json",
	} {
		if mount := findMount(turn.Mounts, guest); mount == nil || !mount.RO {
			t.Fatalf("read-only asset missing: %s", guest)
		}
	}
	if turn.Env["ARIZUKO_PACKAGE_BIN"] != "" || turn.Env["PATH"] != "" {
		t.Fatal("package rendering overrides the image PATH")
	}
	tool := findMount(turn.Mounts, "/usr/local/bin/package-tool")
	output, err := exec.Command(tool.Host).Output()
	if err != nil || string(output) != "tool-fired" {
		t.Fatalf("selected executable: %q %v", output, err)
	}
	for _, file := range []string{
		filepath.Join(cfg.GroupsDir, "main", ".claude/settings.json"),
		findMount(turn.Mounts, "/etc/codex/hooks.json").Host,
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
		command := config.Hooks["SessionStart"][0].Hooks[0].Command
		output, err := exec.Command("sh", "-c", command).Output()
		if err != nil || string(output) != "hook-fired" {
			t.Fatalf("native command %q: %q %v", command, output, err)
		}
	}
	req.Packages = []string{"core"}
	turn, err = renderTurn(cfg, folders, req, "next", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if findMount(turn.Mounts, core.ContainerHome+"/.claude/skills/extra") != nil || findMount(turn.Mounts, "/usr/local/bin/package-tool") != nil {
		t.Fatal("unselected package remains visible")
	}
	data, err = os.ReadFile(filepath.Join(cfg.GroupsDir, "main", ".claude/settings.json"))
	if err != nil || strings.Contains(string(data), "hook-fired") {
		t.Fatalf("removed package hook remains: %s %v", data, err)
	}
	data, err = os.ReadFile(findMount(turn.Mounts, "/etc/codex/hooks.json").Host)
	if err != nil || strings.Contains(string(data), "hook-fired") {
		t.Fatalf("removed native system hook remains: %s %v", data, err)
	}
}

func TestPackageAssetMountsOncePerGuestPath(t *testing.T) {
	cfg, folders := renderWiring(t)
	for _, name := range []string{"first", "second"} {
		root := filepath.Join(cfg.ProjectRoot, "packages", name)
		write(t, filepath.Join(root, "arizuko.yml"), "name: "+name+"\n")
		write(t, filepath.Join(root, "skills", "base", "SKILL.md"), name)
		write(t, filepath.Join(root, "hooks", "native.sh"), name)
		write(t, filepath.Join(root, "bin", "package-tool"), name)
	}
	for _, order := range [][]string{{"first", "second"}, {"second", "first"}} {
		turn, err := renderTurn(cfg, folders, runedv1.RunRequest{Folder: "main", Packages: append([]string{"core"}, order...)}, "test", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		winner := order[1]
		for guest, host := range map[string]string{
			core.ContainerHome + "/.claude/skills/base":     filepath.Join("skills", "base"),
			core.ContainerHome + "/.claude/hooks/native.sh": filepath.Join("hooks", "native.sh"),
			"/usr/local/bin/package-tool":                   filepath.Join("bin", "package-tool"),
		} {
			var hosts []string
			for _, mount := range turn.Mounts {
				if mount.Container == guest {
					hosts = append(hosts, mount.Host)
				}
			}
			if want := filepath.Join(cfg.ProjectRoot, "packages", winner, host); len(hosts) != 1 || hosts[0] != want {
				t.Fatalf("order %v: %s mounted from %v, want only %s", order, guest, hosts, want)
			}
		}
	}
}
