package main

import (
	"bytes"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// chownStore must chown only store/ to the container uid (1000) so uid-1000
// daemons can open their SQLite DBs in a root-owned tree. When the caller is
// not root, chowning to uid 1000 is EPERM — chownStore must WARN (with the
// `sudo chown` fix hint) and NOT fail, so `arizuko create` still succeeds on a
// user-writable /srv/data.
func TestChownStore_NonRootWarnsNotFatal(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "messages.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	chownStore(storeDir) // must not panic / exit

	if os.Geteuid() == 0 {
		// root can chown to 1000; no warning expected.
		if strings.Contains(buf.String(), "could not chown") {
			t.Errorf("root chown should succeed, got WARN: %s", buf.String())
		}
		return
	}
	out := buf.String()
	if !strings.Contains(out, "could not chown store dir") {
		t.Errorf("expected chown WARN when non-root, got: %q", out)
	}
	if !strings.Contains(out, "sudo chown -R 1000:1000") {
		t.Errorf("WARN must carry the sudo-chown fix hint, got: %q", out)
	}
}

func TestCreatePackagesWritableByContainer(t *testing.T) {
	if os.Geteuid() != 0 && os.Geteuid() != containerUID {
		t.Skip("requires root or container uid")
	}
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("DATA_DIR", "")
	cmdCreate([]string{"packages-test"})
	packages := filepath.Join(base, "arizuko_packages-test", "packages")
	info, err := os.Stat(packages)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Uid != containerUID || info.Mode().Perm()&0o200 == 0 {
		t.Fatalf("packages owner = %d, mode = %v; container uid must write", stat.Uid, info.Mode())
	}
}

func TestCreateRuntimeDirectoriesWritable(t *testing.T) {
	if root := os.Getenv("ARIZUKO_TEST_RUNTIME_ROOT"); root != "" {
		for _, dir := range []string{"ipc/new-group", "groups/new-group", "web/pub/new-group", "web/priv/new-group"} {
			if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		sock, err := net.Listen("unix", filepath.Join(root, "ipc/new-group/gated.sock"))
		if err != nil {
			t.Fatal(err)
		}
		if err := sock.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root to run as container uid")
	}
	base := t.TempDir()
	for _, dir := range []string{filepath.Dir(base), base} {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("DATA_DIR", "")
	cmdCreate([]string{"runtime"})
	cmd := exec.Command(os.Args[0], "-test.run=^TestCreateRuntimeDirectoriesWritable$")
	cmd.Env = append(os.Environ(), "ARIZUKO_TEST_RUNTIME_ROOT="+filepath.Join(base, "arizuko_runtime"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: containerUID, Gid: containerGID}}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("container uid cannot create runtime paths: %v\n%s", err, output)
	}
}
