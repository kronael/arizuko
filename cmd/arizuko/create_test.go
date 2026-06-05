package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
