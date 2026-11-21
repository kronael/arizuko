package routd

import (
	"maps"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// ShellEnvSecrets is the third credential tier: a folder capability credential an
// explicit `shell:env:<KEY>` grant makes readable from the agent's own shell, so
// `gh` can authenticate. The tests below pin the two properties that keep it from
// becoming the bulk reader EnvProfileSecrets exists to deny — nothing without a
// grant, and never a model credential.

func shellEnvDB(t *testing.T) (*DB, *store.Store) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := seedSecretsStore(t, db, "shell-env-key")
	if err := s.PutSecretRow(store.ScopeFolder, "atlas", "NPM_TOKEN", "npm_folder"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSecretRow(store.ScopeFolder, "atlas", "BUILDKITE_TOKEN", "bkua_folder"); err != nil {
		t.Fatal(err)
	}
	return db, s
}

func shellEnv(t *testing.T, db *DB, folder, user string) map[string]string {
	t.Helper()
	got, err := db.ShellEnvSecrets(folder, user)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestShellEnvSecrets_NoGrantResolvesNothing(t *testing.T) {
	db, _ := shellEnvDB(t)
	if got := shellEnv(t, db, "atlas", "telegram:user/7"); len(got) != 0 {
		t.Fatalf("a folder with secrets but no shell:env grant resolved %v, want nothing", got)
	}
}

func TestShellEnvSecrets_GrantedKeyOnly(t *testing.T) {
	db, _ := shellEnvDB(t)
	if err := addACLRow(db, core.ACLRow{
		Principal: "folder:atlas", Action: auth.ShellEnvPrefix + "NPM_TOKEN",
		Scope: "atlas", Effect: "allow",
	}); err != nil {
		t.Fatalf("addACLRow: %v", err)
	}
	got := shellEnv(t, db, "atlas", "telegram:user/7")
	want := map[string]string{"NPM_TOKEN": "npm_folder"}
	if !maps.Equal(got, want) {
		t.Errorf("ShellEnvSecrets = %v, want exactly %v (BUILDKITE_TOKEN is ungranted)", got, want)
	}
}

// TestShellEnvSecrets_UserKeyWinsOverFolder mirrors the broker's BYOA rule: a
// person spends their OWN credential when they have one, so the shell tier must
// resolve through the same user-over-folder overlay rather than a second one.
func TestShellEnvSecrets_UserKeyWinsOverFolder(t *testing.T) {
	db, s := shellEnvDB(t)
	if err := s.PutSecretRow(store.ScopeUser, "telegram:user/7", "NPM_TOKEN", "npm_user7"); err != nil {
		t.Fatal(err)
	}
	if err := addACLRow(db, core.ACLRow{
		Principal: "folder:atlas", Action: auth.ShellEnvPrefix + "*",
		Scope: "atlas", Effect: "allow",
	}); err != nil {
		t.Fatalf("addACLRow: %v", err)
	}
	if got := shellEnv(t, db, "atlas", "telegram:user/7")["NPM_TOKEN"]; got != "npm_user7" {
		t.Errorf("NPM_TOKEN = %q, want the caller's own npm_user7", got)
	}
	if got := shellEnv(t, db, "atlas", "telegram:user/9")["NPM_TOKEN"]; got != "npm_folder" {
		t.Errorf("a user with no key of their own got %q, want the folder default", got)
	}
}

// TestShellEnvSecrets_ModelCredentialNeverCrosses is the containment that matters
// most. A `shell:env:*` row is the widest grant an operator can write, and it must
// still not hand the shell a model credential: those have their own path
// (EnvProfileSecrets) and the whole point of BUGS L131 is keeping them out of a
// shell the agent drives.
func TestShellEnvSecrets_ModelCredentialNeverCrosses(t *testing.T) {
	db, s := shellEnvDB(t)
	if err := s.PutSecretRow(store.ScopeUser, "telegram:user/7", "ANTHROPIC_API_KEY", "sk-user7"); err != nil {
		t.Fatal(err)
	}
	if err := addACLRow(db, core.ACLRow{
		Principal: "folder:atlas", Action: auth.ShellEnvPrefix + "*",
		Scope: "atlas", Effect: "allow",
	}); err != nil {
		t.Fatalf("addACLRow: %v", err)
	}
	got := shellEnv(t, db, "atlas", "telegram:user/7")
	for key := range store.EnvProfileKeys {
		if _, bad := got[key]; bad {
			t.Errorf("shell:env:* leaked model credential %s into the shell tier", key)
		}
	}
}

// GITHUB_TOKEN is gitd's `.env` token for every granted folder; a folder row of
// that name never reaches the shell.
func TestShellEnvSecrets_InstanceGitHubToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_instance")
	db, s := shellEnvDB(t)
	if err := s.PutSecretRow(store.ScopeFolder, "atlas", "GITHUB_TOKEN", "ghp_folder"); err != nil {
		t.Fatal(err)
	}
	if got := shellEnv(t, db, "beta", "telegram:user/7"); len(got) != 0 {
		t.Fatalf("an ungranted folder resolved %v, want nothing", got)
	}
	for _, f := range []string{"atlas", "beta"} {
		if err := addACLRow(db, core.ACLRow{
			Principal: "folder:" + f, Action: auth.ShellEnvPrefix + "GITHUB_TOKEN",
			Scope: f, Effect: "allow",
		}); err != nil {
			t.Fatalf("addACLRow: %v", err)
		}
		if got := shellEnv(t, db, f, "telegram:user/7")["GITHUB_TOKEN"]; got != "ghp_instance" {
			t.Errorf("%s GITHUB_TOKEN = %q, want the instance ghp_instance", f, got)
		}
	}
	t.Setenv("GITHUB_TOKEN", "")
	if got, ok := shellEnv(t, db, "atlas", "telegram:user/7")["GITHUB_TOKEN"]; ok {
		t.Errorf("with no instance token atlas got its folder row %q, want nothing", got)
	}
}
