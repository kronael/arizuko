package mountsec

import (
	"os"
	"path/filepath"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestValidateNoAllowlist(t *testing.T) {
	al := Allowlist{}
	mounts := []AdditionalMount{{HostPath: "/tmp/test"}}
	got := ValidateAdditionalMounts(mounts, "test", true, al)
	if len(got) != 0 {
		t.Fatal("should reject all when no allowlist")
	}
}

func TestValidateAllowedMount(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "data")
	os.MkdirAll(sub, 0o755)

	al := Allowlist{
		AllowedRoots:    []AllowedRoot{{Path: dir, AllowReadWrite: true}},
		BlockedPatterns: defaultBlocked,
	}

	mounts := []AdditionalMount{{
		HostPath:      sub,
		ContainerPath: "mydata",
		Readonly:      boolPtr(false),
	}}
	got := ValidateAdditionalMounts(mounts, "main", true, al)
	if len(got) != 1 {
		t.Fatal("expected 1 valid mount")
	}
	if got[0].Readonly {
		t.Fatal("should be read-write")
	}
	if got[0].ContainerPath != "/workspace/extra/mydata" {
		t.Fatalf("unexpected container path: %s", got[0].ContainerPath)
	}
}

func TestValidateBlockedPattern(t *testing.T) {
	dir := t.TempDir()
	sshDir := filepath.Join(dir, ".ssh")
	os.MkdirAll(sshDir, 0o755)

	al := Allowlist{
		AllowedRoots:    []AllowedRoot{{Path: dir, AllowReadWrite: true}},
		BlockedPatterns: defaultBlocked,
	}

	mounts := []AdditionalMount{{HostPath: sshDir}}
	got := ValidateAdditionalMounts(mounts, "main", true, al)
	if len(got) != 0 {
		t.Fatal("should block .ssh mount")
	}
}

func TestValidateNonMainReadOnly(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "safe")
	os.MkdirAll(sub, 0o755)

	al := Allowlist{
		AllowedRoots:    []AllowedRoot{{Path: dir, AllowReadWrite: true}},
		BlockedPatterns: defaultBlocked,
		NonMainReadOnly: true,
	}

	mounts := []AdditionalMount{{
		HostPath: sub,
		Readonly: boolPtr(false),
	}}
	got := ValidateAdditionalMounts(mounts, "child", false, al)
	if len(got) != 1 {
		t.Fatal("expected 1 valid mount")
	}
	if !got[0].Readonly {
		t.Fatal("should force readonly for non-main")
	}
}

func TestValidateRootDisallowsRW(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "data")
	os.MkdirAll(sub, 0o755)

	al := Allowlist{
		AllowedRoots:    []AllowedRoot{{Path: dir, AllowReadWrite: false}},
		BlockedPatterns: defaultBlocked,
	}

	mounts := []AdditionalMount{{
		HostPath: sub,
		Readonly: boolPtr(false),
	}}
	got := ValidateAdditionalMounts(mounts, "main", true, al)
	if len(got) != 1 {
		t.Fatal("expected 1 mount")
	}
	if !got[0].Readonly {
		t.Fatal("should force readonly when root disallows rw")
	}
}

func TestValidatePathOutsideRoot(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	os.MkdirAll(outside, 0o755)

	al := Allowlist{
		AllowedRoots:    []AllowedRoot{{Path: filepath.Join(dir, "allowed")}},
		BlockedPatterns: defaultBlocked,
	}

	mounts := []AdditionalMount{{HostPath: outside}}
	got := ValidateAdditionalMounts(mounts, "main", true, al)
	if len(got) != 0 {
		t.Fatal("should reject path outside allowed root")
	}
}

func TestValidateRelativePath(t *testing.T) {
	al := Allowlist{
		AllowedRoots: []AllowedRoot{{Path: "/tmp"}},
	}
	mounts := []AdditionalMount{{HostPath: "relative/path"}}
	got := ValidateAdditionalMounts(mounts, "main", true, al)
	if len(got) != 0 {
		t.Fatal("should reject relative paths")
	}
}

func TestValidateInvalidContainerPath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "ok")
	os.MkdirAll(sub, 0o755)

	al := Allowlist{
		AllowedRoots: []AllowedRoot{{Path: dir}},
	}

	// "" defaults to basename so only test explicit invalids
	cases := []string{"/absolute", "../escape", " "}
	for _, cp := range cases {
		mounts := []AdditionalMount{{HostPath: sub, ContainerPath: cp}}
		got := ValidateAdditionalMounts(mounts, "main", true, al)
		if len(got) != 0 {
			t.Fatalf("should reject container path %q", cp)
		}
	}
}

func TestValidateContainerPathDefault(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "mydir")
	os.MkdirAll(sub, 0o755)

	al := Allowlist{
		AllowedRoots: []AllowedRoot{{Path: dir}},
	}

	mounts := []AdditionalMount{{HostPath: sub}}
	got := ValidateAdditionalMounts(mounts, "main", true, al)
	if len(got) != 1 {
		t.Fatal("expected 1 mount")
	}
	if got[0].ContainerPath != "/workspace/extra/mydir" {
		t.Fatalf("expected default container path from basename, got %s", got[0].ContainerPath)
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := expandHome("~/test"); got != filepath.Join(home, "test") {
		t.Fatalf("expected %s, got %s", filepath.Join(home, "test"), got)
	}
	if got := expandHome("~"); got != home {
		t.Fatalf("expected %s, got %s", home, got)
	}
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Fatalf("expected /abs/path, got %s", got)
	}
}

func TestMatchesBlocked(t *testing.T) {
	patterns := []string{".ssh", "credentials", ".env"}

	if got := matchesBlocked("/home/user/.ssh/id_rsa", patterns); got != ".ssh" {
		t.Fatalf("expected .ssh, got %q", got)
	}
	if got := matchesBlocked("/srv/data/store", patterns); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestValidContainerPath(t *testing.T) {
	if !validContainerPath("mydata") {
		t.Fatal("should accept simple name")
	}
	if validContainerPath("/absolute") {
		t.Fatal("should reject absolute")
	}
	if validContainerPath("..") {
		t.Fatal("should reject ..")
	}
	if validContainerPath("") {
		t.Fatal("should reject empty")
	}
	if validContainerPath(" ") {
		t.Fatal("should reject whitespace")
	}
}

func TestLoadAllowlistMissing(t *testing.T) {
	al := LoadAllowlist("/nonexistent/path.json")
	if len(al.AllowedRoots) != 0 {
		t.Fatal("should return empty for missing file")
	}
}

func TestLoadAllowlistMergesDefaults(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "allow.json")
	os.WriteFile(fp, []byte(`{
		"allowedRoots": [{"path": "/srv"}],
		"blockedPatterns": ["custom_secret"]
	}`), 0o644)

	al := LoadAllowlist(fp)
	if len(al.AllowedRoots) != 1 {
		t.Fatalf("expected 1 root, got %d", len(al.AllowedRoots))
	}

	hasDefault := false
	hasCustom := false
	for _, p := range al.BlockedPatterns {
		if p == ".ssh" {
			hasDefault = true
		}
		if p == "custom_secret" {
			hasCustom = true
		}
	}
	if !hasDefault {
		t.Fatal("should include default blocked patterns")
	}
	if !hasCustom {
		t.Fatal("should include custom blocked pattern")
	}
}
