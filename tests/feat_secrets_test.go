package tests

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// TestFeature_Secrets covers the credential plane end to end at the federation
// level: what a turn RESOLVES, whose value wins, whether a caller can reach
// another folder's, and whether a resolved value can escape into a log.
//
// Deliberately NOT covered here: that a row inserts and reads back. That is
// visible from `store/secrets.go` and is already unit-tested. Every subtest
// below asserts something a reader of one file cannot conclude — a cross-file
// resolution rule, a containment boundary, or a leak guard.
func TestFeature_Secrets(t *testing.T) {
	const key = "EVAL_PROVIDER_KEY"

	// The BYOA rule (routd/dispatch.go: FolderSecretsForUser at turn build).
	// A user-scoped secret shadows the folder default FOR THAT CALLER ONLY.
	// Getting this backwards would hand one tenant's key to another's turn, and
	// no single file states the precedence — folder and user rows are peers in
	// the table.
	t.Run("user-secret-shadows-folder-for-that-caller-only", func(t *testing.T) {
		f := bootFederation(t)
		if err := f.routdDB.PutGroup(core.Group{Folder: "acme"}); err != nil {
			t.Fatal(err)
		}
		if err := f.routdDB.SetSecret(store.ScopeFolder, "acme", key, "folder-value"); err != nil {
			t.Fatal(err)
		}
		if err := f.routdDB.SetSecret(store.ScopeUser, "google:alice", key, "alice-value"); err != nil {
			t.Fatal(err)
		}

		if got := f.routdDB.FolderSecretsForUser("acme", "google:alice")[key]; got != "alice-value" {
			t.Errorf("alice's turn resolved %q, want her own key to shadow the folder default", got)
		}
		if got := f.routdDB.FolderSecretsForUser("acme", "google:bob")[key]; got != "folder-value" {
			t.Errorf("bob's turn resolved %q, want the folder default — alice's key must not leak to him", got)
		}
		if got := f.routdDB.FolderSecretsForUser("acme", "")[key]; got != "folder-value" {
			t.Errorf("an unattributed turn resolved %q, want the folder default", got)
		}
	})

	// Folder secrets DESCEND the ancestry (deepest wins) and stop at the
	// subtree edge. That is the opposite of group-scoped FILES, which never
	// inherit — so the direction cannot be inferred from the repo's general
	// rule and has to be pinned here. Getting the boundary wrong is a
	// cross-tenant credential leak, not a config annoyance.
	t.Run("folder-secret-descends-the-subtree-but-not-sideways", func(t *testing.T) {
		f := bootFederation(t)
		for _, folder := range []string{"acme", "acme/eng", "acme/eng/sre", "other"} {
			if err := f.routdDB.PutGroup(core.Group{Folder: folder}); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.routdDB.SetSecret(store.ScopeFolder, "acme", key, "acme-value"); err != nil {
			t.Fatal(err)
		}
		if got := f.routdDB.FolderSecretsForUser("acme/eng/sre", "")[key]; got != "acme-value" {
			t.Errorf("descendant resolved %q, want the ancestor's value — folder secrets cascade", got)
		}
		if got := f.routdDB.FolderSecretsForUser("other", "")[key]; got != "" {
			t.Errorf("sibling tenant resolved %q; the cascade must stop at the subtree edge", got)
		}
	})

	// Deepest-wins: a child's own value overrides the ancestor's for that child
	// and leaves the ancestor untouched. Both halves matter — an override that
	// also changed the parent would silently repoint every other descendant.
	t.Run("deeper-folder-value-overrides-the-ancestor", func(t *testing.T) {
		f := bootFederation(t)
		for _, folder := range []string{"acme", "acme/eng"} {
			if err := f.routdDB.PutGroup(core.Group{Folder: folder}); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.routdDB.SetSecret(store.ScopeFolder, "acme", key, "acme-value"); err != nil {
			t.Fatal(err)
		}
		if err := f.routdDB.SetSecret(store.ScopeFolder, "acme/eng", key, "eng-value"); err != nil {
			t.Fatal(err)
		}
		if got := f.routdDB.FolderSecretsForUser("acme/eng", "")[key]; got != "eng-value" {
			t.Errorf("child resolved %q, want its own value to win over the ancestor's", got)
		}
		if got := f.routdDB.FolderSecretsForUser("acme", "")[key]; got != "acme-value" {
			t.Errorf("ancestor resolved %q; a child's override must not rewrite the parent", got)
		}
	})

	// Deleting the user override must FALL BACK to the folder default, not to
	// nothing. A delete that emptied the resolution would silently break every
	// turn for that user while the folder row sat there looking healthy.
	t.Run("deleting-the-user-override-falls-back-to-the-folder-default", func(t *testing.T) {
		f := bootFederation(t)
		if err := f.routdDB.PutGroup(core.Group{Folder: "acme"}); err != nil {
			t.Fatal(err)
		}
		if err := f.routdDB.SetSecret(store.ScopeFolder, "acme", key, "folder-value"); err != nil {
			t.Fatal(err)
		}
		if err := f.routdDB.SetSecret(store.ScopeUser, "google:alice", key, "alice-value"); err != nil {
			t.Fatal(err)
		}
		if err := f.routdDB.DeleteSecret(store.ScopeUser, "google:alice", key); err != nil {
			t.Fatal(err)
		}
		if got := f.routdDB.FolderSecretsForUser("acme", "google:alice")[key]; got != "folder-value" {
			t.Errorf("after deleting her override alice resolved %q, want the folder default", got)
		}
	})

	// Sealing at rest. A resolved secret is plaintext in memory for the turn;
	// this asserts the value is not ALSO sitting in the row. The regression is
	// invisible from the outside — reads keep working either way, because the
	// read path decrypts only when a key is set. The keyring must be seeded
	// here: with no SECRETS_KEY the store writes plaintext BY DESIGN, so a
	// fixture that forgot it would assert nothing.
	t.Run("stored-row-does-not-hold-the-plaintext", func(t *testing.T) {
		f := bootFederation(t)
		f.routdDB.SetSecretKeys([]byte("test-secrets-key-material"))
		if err := f.routdDB.PutGroup(core.Group{Folder: "acme"}); err != nil {
			t.Fatal(err)
		}
		const plaintext = "sk-live-do-not-store-me"
		if err := f.routdDB.SetSecret(store.ScopeFolder, "acme", key, plaintext); err != nil {
			t.Fatal(err)
		}
		if got := f.routdDB.FolderSecretsForUser("acme", "")[key]; got != plaintext {
			t.Fatalf("precondition: resolution returned %q, want the plaintext back", got)
		}

		var stored string
		if err := f.routdDB.SQL().QueryRow(
			`SELECT value FROM secrets WHERE scope_id = ? AND key = ?`, "acme", key,
		).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stored, plaintext) {
			t.Error("the secrets row holds the plaintext; SECRETS_KEY sealing is not engaged")
		}
	})
}
