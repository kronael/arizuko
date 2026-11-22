package main

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/routd"
)

func TestGroupSettingsReportsSkillFailure(t *testing.T) {
	db := routdDB(t)
	seedFolder(t, db, "team")
	seedAdmin(t, db, "alice", "team")
	if err := routd.OpenOver(db).PutInstalledPackage(routd.InstalledPackage{Name: "core", Manifest: map[string][]string{"skill": {"voice"}}}); err != nil {
		t.Fatal(err)
	}
	appDir := t.TempDir()
	core := filepath.Join(appDir, "packages", "core")
	if err := os.MkdirAll(filepath.Join(core, "skills", "voice"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(core, "arizuko.yml"), []byte("name: core\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(core, "skills", "voice", "SKILL.md"), []byte("voice"), 0o644); err != nil {
		t.Fatal(err)
	}
	groups := filepath.Join(appDir, "groups")
	marker := filepath.Join(groups, "team", ".claude", "skills", "voice", ".disabled")
	if err := os.MkdirAll(marker, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marker, "occupied"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	d := &dash{routd: db, groupsDir: groups}
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, adminReq("POST", "/dash/groups/team/settings", url.Values{"harness_default": {"1"}, "skill_enabled": {"voice"}}.Encode(), "alice"))
	if w.Code < 400 || !strings.Contains(w.Body.String(), "voice") {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}
