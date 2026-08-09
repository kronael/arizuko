package routd

import (
	"context"
	"maps"
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

	got := db.folderSecrets("main/trading")
	if got["GITHUB_TOKEN"] != "ghp_plaintext42" {
		t.Errorf("FolderSecrets decrypt = %q, want ghp_plaintext42", got["GITHUB_TOKEN"])
	}

	// ConnectorSecrets narrows to the declared set and decrypts.
	cs, _, _ := db.ConnectorSecrets("main/trading", "", []string{"GITHUB_TOKEN"})
	if cs["GITHUB_TOKEN"] != "ghp_plaintext42" {
		t.Errorf("ConnectorSecrets = %q, want ghp_plaintext42", cs["GITHUB_TOKEN"])
	}
	// A non-declared key never surfaces even if present in the folder set.
	other, _, _ := db.ConnectorSecrets("main/trading", "", []string{"OTHER"})
	if _, ok := other["GITHUB_TOKEN"]; ok {
		t.Error("ConnectorSecrets leaked an undeclared key")
	}
}

// TestEnvProfileSecrets_OnlyUserModelCredentials pins what dispatchRun ships as
// RunRequest.Secrets — i.e. what lands in the agent's container env (BUGS X1,
// spec 5/13 §Trust model). Only the caller's OWN model credentials qualify. A
// folder capability credential must not appear, nor a user's own capability
// credential, nor a folder row for a model key restored past validateScope by
// ValidateAndImportSecrets (seeded here with raw SQL, which is the only way that
// row shape exists).
func TestEnvProfileSecrets_OnlyUserModelCredentials(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := seedSecretsStore(t, db, "byoa-routd-key")

	if err := s.PutSecretRow(store.ScopeFolder, "atlas", "GITHUB_TOKEN", "ghp_folder"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSecretRow(store.ScopeUser, "telegram:user/7", "GITHUB_TOKEN", "ghp_user7"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSecretRow(store.ScopeUser, "telegram:user/7", "ANTHROPIC_API_KEY", "sk-user7"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(
		`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at)
		 VALUES ('folder', 'atlas', 'ANTHROPIC_API_KEY', 'sk-folder', '2026-08-09T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}

	got := db.EnvProfileSecrets("telegram:user/7")
	want := map[string]string{"ANTHROPIC_API_KEY": "sk-user7"}
	if !maps.Equal(got, want) {
		t.Errorf("EnvProfileSecrets = %v, want exactly %v", got, want)
	}
	if got := db.EnvProfileSecrets("telegram:user/9"); len(got) != 0 {
		t.Errorf("a user with no rows resolved %v, want nothing", got)
	}
	if got := db.EnvProfileSecrets(""); len(got) != 0 {
		t.Errorf("an unattributed turn resolved %v, want nothing", got)
	}

	// The same folder credential the container never sees IS still reachable by a
	// connector that declares it — the broker is the one path (spec 5/13).
	brokered, _, err := db.ConnectorSecrets("atlas", "telegram:user/9", []string{"GITHUB_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if brokered["GITHUB_TOKEN"] != "ghp_folder" {
		t.Errorf("broker resolved GITHUB_TOKEN = %q, want ghp_folder", brokered["GITHUB_TOKEN"])
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
	if got := db.folderSecrets("main"); len(got) != 0 {
		t.Errorf("no secret seeded must resolve empty (reads routd.db), got %v", got)
	}

	// A key in routd's OWN db DOES resolve, decrypted.
	own := seedSecretsStore(t, db, "k")
	if err := own.PutSecretRow(store.ScopeFolder, "main", "GITHUB_TOKEN", "ghp_own"); err != nil {
		t.Fatalf("seed own secret: %v", err)
	}
	if got := db.folderSecrets("main")["GITHUB_TOKEN"]; got != "ghp_own" {
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
	// Connector tool visibility is db.Authorize-gated on the folder principal, and
	// the role:member floor carries only the messaging verbs — so grant it explicitly.
	grantMCPTools(t, db, "main", "fake_echo_env")

	srv := NewServer(db, nil, nil, nil, 0, "")
	srv.SetConnectors(tools)

	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	stop, err := ipc.ServeMCP(sock, srv.buildGatedFns(turnMCP{folder: "main"}),
		srv.buildStoreFns(turnMCP{folder: "main"}), "main", false, 0, "")
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
	if got, _, _ := db.ConnectorSecrets("main", "", []string{"GITHUB_TOKEN"}); len(got) != 0 {
		t.Errorf("empty table: want empty, got %v", got)
	}
	if got := db.folderSecrets("main"); len(got) != 0 {
		t.Errorf("empty table FolderSecrets: want empty, got %v", got)
	}

	// Arm 2: a v2: row in routd.db, NO keyring → reads as ciphertext, not
	// plaintext, and no panic. Seed with a key, then clear routd's keyring.
	seed := seedSecretsStore(t, db, "seed-key")
	if err := seed.PutSecretRow(store.ScopeFolder, "main", "GITHUB_TOKEN", "ghp_secret"); err != nil {
		t.Fatalf("PutSecretRow: %v", err)
	}
	db.SetSecretKeys() // clear the keyring routd uses for reads
	got := db.folderSecrets("main")
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

// TestConnectorSecrets_UserBYOAWins: a user's user-scoped GITHUB_TOKEN must
// reach the connector subprocess (win over the folder default). Before the fix,
// ConnectorSecrets called FolderSecrets (folder scope only) — the user BYOA key
// was silently invisible to the connector.
func TestConnectorSecrets_UserBYOAWins(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetSecretKeys([]byte("connector-byoa-key"))

	seed := seedSecretsStore(t, db, "connector-byoa-key")
	// folder default
	if err := seed.PutSecretRow(store.ScopeFolder, "main", "GITHUB_TOKEN", "ghp_folder"); err != nil {
		t.Fatal(err)
	}
	// alice's personal override
	if err := seed.PutSecretRow(store.ScopeUser, "github:alice", "GITHUB_TOKEN", "ghp_alice"); err != nil {
		t.Fatal(err)
	}

	// alice's callerSub → her key wins
	got, _, _ := db.ConnectorSecrets("main", "github:alice", []string{"GITHUB_TOKEN"})
	if got["GITHUB_TOKEN"] != "ghp_alice" {
		t.Errorf("alice callerSub: GITHUB_TOKEN = %q, want ghp_alice (BYOA override)", got["GITHUB_TOKEN"])
	}

	// bob has no override → falls back to folder default
	got2, _, _ := db.ConnectorSecrets("main", "github:bob", []string{"GITHUB_TOKEN"})
	if got2["GITHUB_TOKEN"] != "ghp_folder" {
		t.Errorf("bob callerSub: GITHUB_TOKEN = %q, want ghp_folder (folder fallback)", got2["GITHUB_TOKEN"])
	}

	// empty callerSub (service:routd / cron) → folder default
	got3, _, _ := db.ConnectorSecrets("main", "", []string{"GITHUB_TOKEN"})
	if got3["GITHUB_TOKEN"] != "ghp_folder" {
		t.Errorf("empty callerSub: GITHUB_TOKEN = %q, want ghp_folder", got3["GITHUB_TOKEN"])
	}
}

// F68 / spec 5/13 § Audit: every secret_use_log row must name the scope the key
// ACTUALLY resolved from ({user, folder, missing}) and the tool that asked. The
// writer hardcoded "folder" for anything it found and never set tool, so the one
// question the table exists to answer — "who used which credential, from where"
// — could not distinguish a user's own BYO key from the folder default.
//
// Drives the real writer: buildStoreFns' ResolveConnectorSecrets closure, which
// is what ipc calls per connector/ext tool invocation.
func TestSecretUseLogRecordsScopeAndTool(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := seedSecretsStore(t, db, "test-secrets-key-0123456789")

	// SHARED lives only at folder scope; MINE exists at BOTH and the user row
	// must win (BYOA) — that is the shadowing case the old code mislabelled.
	if err := s.PutSecretRow(store.ScopeFolder, "main", "SHARED", "folder-shared"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSecretRow(store.ScopeFolder, "main", "MINE", "folder-mine"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSecretRow(store.ScopeUser, "google:alice", "MINE", "alice-mine"); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, nil, nil, nil, 0, "")
	fns := srv.buildStoreFns(turnMCP{folder: "main", trigger: "google:alice", turnID: "turn-77"})

	got, err := fns.ResolveConnectorSecrets("main", "gh_create_pull_request",
		[]string{"SHARED", "MINE", "ABSENT"})
	if err != nil {
		t.Fatalf("ResolveConnectorSecrets: %v", err)
	}
	if got["MINE"] != "alice-mine" {
		t.Fatalf("MINE = %q, want the user row to shadow the folder default", got["MINE"])
	}

	rows, err := db.SQL().Query(`SELECT key, scope, tool, caller_sub, spawn_id FROM secret_use_log ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct{ scope, tool, caller, spawn string }
	seen := map[string]row{}
	for rows.Next() {
		var k string
		var r row
		if err := rows.Scan(&k, &r.scope, &r.tool, &r.caller, &r.spawn); err != nil {
			t.Fatal(err)
		}
		seen[k] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 {
		t.Fatalf("secret_use_log rows = %d (%v), want one per required key", len(seen), seen)
	}
	want := map[string]string{"SHARED": "folder", "MINE": "user", "ABSENT": "missing"}
	for k, wantScope := range want {
		r, ok := seen[k]
		if !ok {
			t.Fatalf("no secret_use_log row for %s", k)
		}
		if r.scope != wantScope {
			t.Errorf("%s scope = %q, want %q", k, r.scope, wantScope)
		}
		if r.tool != "gh_create_pull_request" {
			t.Errorf("%s tool = %q, want the calling tool name", k, r.tool)
		}
		if r.caller != "google:alice" || r.spawn != "turn-77" {
			t.Errorf("%s caller/spawn = %q/%q, want google:alice/turn-77", k, r.caller, r.spawn)
		}
	}
}
