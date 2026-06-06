package routd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

// buildFakeMCP compiles ipc/testdata/fakemcp into a temp binary. fakemcp is a
// stdio MCP server that echoes the env var named by FAKEMCP_KEY — the same
// fixture ipc/connector_test.go uses to assert env injection + result scrub.
func buildFakeMCP(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakemcp")
	cmd := exec.Command("go", "build", "-o", bin, "../ipc/testdata/fakemcp/")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakemcp: %v\n%s", err, out)
	}
	return bin
}

// seedSecretsStore wraps routd's OWN routd.db handle as a keyring-matched
// *store.Store for seeding encrypted secret rows the way the operator would —
// routd OWNS the secrets table now (spec 5/5), so reads come from there, NOT a
// sibling messages.db. Sets routd's decrypt keyring too so db.FolderSecrets
// resolves them.
func seedSecretsStore(t *testing.T, d *DB, key string) *store.Store {
	t.Helper()
	if key != "" {
		d.SetSecretKeys([]byte(key))
	}
	s := store.New(d.db)
	if key != "" {
		s.SetSecretKeys([]byte(key))
	}
	return s
}

func writeConnectorsTOML(t *testing.T, dir, bin string) {
	t.Helper()
	toml := `[[mcp_connector]]
name = "fake"
command = ["` + bin + `"]
secrets = ["GITHUB_TOKEN"]
scope = "per_call"

[mcp_connector.env_template]
FAKEMCP_KEY = "GITHUB_TOKEN"
GITHUB_TOKEN = "{secret:GITHUB_TOKEN}"
`
	if err := os.WriteFile(filepath.Join(dir, "connectors.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write connectors.toml: %v", err)
	}
}

// TestLoadConnectors registers a connector tool from a test connectors.toml and
// asserts the namespaced LocalName + declared secrets travel through.
func TestLoadConnectors(t *testing.T) {
	bin := buildFakeMCP(t)
	dir := t.TempDir()
	writeConnectorsTOML(t, dir, bin)

	tools, err := LoadConnectors(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadConnectors: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	if tools[0].LocalName != "fake_echo_env" {
		t.Errorf("LocalName = %q, want fake_echo_env", tools[0].LocalName)
	}
	if got := tools[0].Connector.Secrets; len(got) != 1 || got[0] != "GITHUB_TOKEN" {
		t.Errorf("Secrets = %v, want [GITHUB_TOKEN]", got)
	}
}

// TestLoadConnectors_MissingFileIsNil: no connectors.toml → nil, no error.
func TestLoadConnectors_MissingFileIsNil(t *testing.T) {
	tools, err := LoadConnectors(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("LoadConnectors: %v", err)
	}
	if tools != nil {
		t.Errorf("expected nil tools, got %v", tools)
	}
}

// TestFolderSecrets_DecryptsV2: a folder secret seeded as a `v2:` encrypted row
// resolves back to plaintext via the sibling RO read + SECRETS_KEY decrypt.
func TestFolderSecrets_DecryptsV2(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := seedSecretsStore(t, db, "test-secrets-key-0123456789")

	// PutSecretRow with a keyring seals it as "v2:..." — verify it really is.
	if err := s.PutSecretRow(store.ScopeFolder, "main/trading", "GITHUB_TOKEN", "ghp_plaintext42"); err != nil {
		t.Fatalf("PutSecretRow: %v", err)
	}
	var raw string
	if err := db.SQL().QueryRow(
		`SELECT value FROM secrets WHERE scope_kind='folder' AND scope_id='main/trading' AND key='GITHUB_TOKEN'`,
	).Scan(&raw); err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if !strings.HasPrefix(raw, "v2:") {
		t.Fatalf("stored value not encrypted: %q", raw)
	}

	got := db.FolderSecrets("main/trading")
	if got["GITHUB_TOKEN"] != "ghp_plaintext42" {
		t.Errorf("FolderSecrets decrypt = %q, want ghp_plaintext42", got["GITHUB_TOKEN"])
	}

	// ConnectorSecrets narrows to the declared set and decrypts.
	cs := db.ConnectorSecrets("main/trading", []string{"GITHUB_TOKEN"})
	if cs["GITHUB_TOKEN"] != "ghp_plaintext42" {
		t.Errorf("ConnectorSecrets = %q, want ghp_plaintext42", cs["GITHUB_TOKEN"])
	}
	// A non-declared key never surfaces even if present in the folder set.
	if _, ok := db.ConnectorSecrets("main/trading", []string{"OTHER"})["GITHUB_TOKEN"]; ok {
		t.Error("ConnectorSecrets leaked an undeclared key")
	}
}

// TestSecretsReadOwnDB proves routd resolves secrets from its OWN routd.db:
// with nothing seeded the resolved set is empty; a key seeded in routd.db DOES
// resolve (decrypted). routd opens NO sibling messages.db. Mirrors
// TestACLReadsOwnDB.
func TestSecretsReadOwnDB(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// No secret seeded → nothing resolves (routd reads only routd.db; it opens
	// NO sibling messages.db, so there is no cross-DB secret to leak).
	db.SetSecretKeys([]byte("k")) // routd's read keyring
	if got := db.FolderSecrets("main"); len(got) != 0 {
		t.Errorf("no secret seeded must resolve empty (reads routd.db), got %v", got)
	}

	// A key in routd's OWN db DOES resolve, decrypted.
	own := seedSecretsStore(t, db, "k")
	if err := own.PutSecretRow(store.ScopeFolder, "main", "GITHUB_TOKEN", "ghp_own"); err != nil {
		t.Fatalf("seed own secret: %v", err)
	}
	if got := db.FolderSecrets("main")["GITHUB_TOKEN"]; got != "ghp_own" {
		t.Errorf("routd.db secret = %q, want ghp_own", got)
	}
}

// TestConnectorCall_ReceivesResolvedSecret: end-to-end through the per-turn MCP
// socket — the connector call receives the resolved secret (env injection) and
// the result is scrubbed (proving the secret map reached CallConnectorTool, not
// nil). The fakemcp echoes env[GITHUB_TOKEN]=<value>; the scrubber replaces the
// raw value with the redaction marker.
func TestConnectorCall_ReceivesResolvedSecret(t *testing.T) {
	bin := buildFakeMCP(t)
	dir := t.TempDir()
	writeConnectorsTOML(t, dir, bin)
	tools, err := LoadConnectors(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadConnectors: %v", err)
	}

	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := seedSecretsStore(t, db, "test-secrets-key-0123456789")
	if err := s.PutSecretRow(store.ScopeFolder, "main", "GITHUB_TOKEN", "ghp_livetoken"); err != nil {
		t.Fatalf("PutSecretRow: %v", err)
	}

	srv := NewServer(db, nil, nil, nil, 0, "")
	srv.SetConnectors(tools)

	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	rules := deriveFolderGrants(db, "main")
	stop, err := ipc.ServeMCP(sock, srv.buildGatedFns(turnMCP{folder: "main"}),
		srv.buildStoreFns(turnMCP{folder: "main"}), "main", rules, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	deadline := time.Now().Add(2 * time.Second)
	for !fileExists(sock) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	text, errText := callToolText(t, sock, "fake_echo_env", map[string]any{})
	if errText != "" {
		t.Fatalf("tool error: %s", errText)
	}
	// The connector echoed the injected env; the scrubber redacted the value.
	if strings.Contains(text, "ghp_livetoken") {
		t.Errorf("raw secret leaked unscrubbed: %q", text)
	}
	if !strings.Contains(text, "«redacted»") {
		t.Errorf("secret not injected/scrubbed (got nil secrets?): %q", text)
	}
}

// TestConnectorSecrets_GracefulWhenUnset: no SECRETS_KEY → empty / ciphertext
// passthrough, no panic. Reads come from routd's OWN secrets table (always
// present). Two arms: empty table, and a v2: row read with no keyring.
func TestConnectorSecrets_GracefulWhenUnset(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// Arm 1: empty secrets table, no keyring → empty.
	if got := db.ConnectorSecrets("main", []string{"GITHUB_TOKEN"}); len(got) != 0 {
		t.Errorf("empty table: want empty, got %v", got)
	}
	if got := db.FolderSecrets("main"); len(got) != 0 {
		t.Errorf("empty table FolderSecrets: want empty, got %v", got)
	}

	// Arm 2: a v2: row in routd.db, NO keyring → reads as ciphertext, not
	// plaintext, and no panic. Seed with a key, then clear routd's keyring.
	seed := seedSecretsStore(t, db, "seed-key")
	if err := seed.PutSecretRow(store.ScopeFolder, "main", "GITHUB_TOKEN", "ghp_secret"); err != nil {
		t.Fatalf("PutSecretRow: %v", err)
	}
	db.SetSecretKeys() // clear the keyring routd uses for reads
	got := db.FolderSecrets("main")
	if got["GITHUB_TOKEN"] == "ghp_secret" {
		t.Error("decrypted without a keyring (plaintext leak)")
	}
	if v, ok := got["GITHUB_TOKEN"]; ok && !strings.HasPrefix(v, "v2:") {
		t.Errorf("expected ciphertext passthrough, got %q", v)
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
