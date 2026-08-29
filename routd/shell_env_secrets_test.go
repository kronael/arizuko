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
	if err := s.PutSecretRow(store.ScopeFolder, "atlas", "GITHUB_TOKEN", "ghp_folder"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSecretRow(store.ScopeFolder, "atlas", "BUILDKITE_TOKEN", "bkua_folder"); err != nil {
		t.Fatal(err)
	}
	return db, s
}

func TestShellEnvSecrets_NoGrantResolvesNothing(t *testing.T) {
	db, _ := shellEnvDB(t)
	if got := db.ShellEnvSecrets("atlas", "telegram:user/7"); len(got) != 0 {
		t.Fatalf("a folder with secrets but no shell:env grant resolved %v, want nothing", got)
	}
}

func TestShellEnvSecrets_GrantedKeyOnly(t *testing.T) {
	db, _ := shellEnvDB(t)
	if err := addACLRow(db, core.ACLRow{
		Principal: "folder:atlas", Action: auth.ShellEnvPrefix + "GITHUB_TOKEN",
		Scope: "atlas", Effect: "allow",
	}); err != nil {
		t.Fatalf("addACLRow: %v", err)
	}
	got := db.ShellEnvSecrets("atlas", "telegram:user/7")
	want := map[string]string{"GITHUB_TOKEN": "ghp_folder"}
	if !maps.Equal(got, want) {
		t.Errorf("ShellEnvSecrets = %v, want exactly %v (BUILDKITE_TOKEN is ungranted)", got, want)
	}
}

// TestShellEnvSecrets_UserKeyWinsOverFolder mirrors the broker's BYOA rule: a
// person spends their OWN credential when they have one, so the shell tier must
// resolve through the same user-over-folder overlay rather than a second one.
func TestShellEnvSecrets_UserKeyWinsOverFolder(t *testing.T) {
	db, s := shellEnvDB(t)
	if err := s.PutSecretRow(store.ScopeUser, "telegram:user/7", "GITHUB_TOKEN", "ghp_user7"); err != nil {
		t.Fatal(err)
	}
	if err := addACLRow(db, core.ACLRow{
		Principal: "folder:atlas", Action: auth.ShellEnvPrefix + "*",
		Scope: "atlas", Effect: "allow",
	}); err != nil {
		t.Fatalf("addACLRow: %v", err)
	}
	if got := db.ShellEnvSecrets("atlas", "telegram:user/7")["GITHUB_TOKEN"]; got != "ghp_user7" {
		t.Errorf("GITHUB_TOKEN = %q, want the caller's own ghp_user7", got)
	}
	if got := db.ShellEnvSecrets("atlas", "telegram:user/9")["GITHUB_TOKEN"]; got != "ghp_folder" {
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
	got := db.ShellEnvSecrets("atlas", "telegram:user/7")
	for key := range store.EnvProfileKeys {
		if _, bad := got[key]; bad {
			t.Errorf("shell:env:* leaked model credential %s into the shell tier", key)
		}
	}
}
