package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// openStoreWithKey opens routd.db (which OWNS the secrets table in the split
// topology — spec 5/16-mcp-rest-unification) wrapped as a *store.Store, with the SECRETS_KEY keyring
// set so CLI-written secrets seal under the SAME key routd decrypts with at read
// time. The host-admin CLI writes here directly (filesystem access), no token
// plumbing — same discipline it used for messages.db before the split. AsCLI
// attributes the audit row to the invoking operator: the CLI has no X-User-*
// header to learn a sub from.
// The keyring is REQUIRED. Without it a write cannot seal and a read returns
// ciphertext, so the command refuses instead of degrading (BUGS S2).
func openStoreWithKey(dataDir string) (*store.Store, error) {
	s := mustOpen(store.OwnerRoutd)
	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil {
		return nil, err
	}
	kr, err := core.RequireSecretKeyring(cfg.SecretsKey)
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", err, filepath.Join(dataDir, ".env"))
	}
	s.SetSecretKeys(kr...)
	return s.AsCLI(os.Getenv("USER")), nil
}

// cmdSecret manages folder-scoped secrets (operator-only). User secrets
// live behind `arizuko user-secret`. Spec 5/13.
func cmdSecret(args []string) {
	cmdScopedSecret(args, "secret", "folder", store.ScopeFolder)
}

// cmdUserSecret manages user-scoped secrets (operator-only fallback for
// users who haven't logged in via /dash/me/secrets yet).
func cmdUserSecret(args []string) {
	cmdScopedSecret(args, "user-secret", "user_sub", store.ScopeUser)
}

func cmdScopedSecret(args []string, command, scopeName string, scope store.SecretScope) {
	instance, action := args[0], verb(args, fmt.Sprintf("arizuko %s [-i instance] <set|list|delete|seal-legacy> ...", command))

	dataDir := mustInstanceDir(instance)
	s, err := openStoreWithKey(dataDir)
	if err != nil {
		die("Failed: open db: %v", err)
	}
	defer func() { _ = s.Close() }()

	switch action {
	case "set":
		scopeID, key, value, err := parseSecretSet(command+" set", args[2:])
		if err != nil {
			usage(err, fmt.Sprintf("arizuko %s [-i instance] set <%s> KEY --value|-v V", command, scopeName))
		}
		if err := runSecretSet(s, scope, scopeID, key, value, os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	case "list":
		args = need(args, 3, 3, fmt.Sprintf("arizuko %s [-i instance] list <%s>", command, scopeName))
		if err := runSecretList(s, scope, args[2], os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	case "delete":
		args = need(args, 4, 4, fmt.Sprintf("arizuko %s [-i instance] delete <%s> KEY", command, scopeName))
		if err := runSecretDelete(s, scope, args[2], args[3], os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	case "seal-legacy":
		dryRun, err := parseSealLegacy(command+" seal-legacy", args[2:])
		if err != nil {
			usage(err, fmt.Sprintf("arizuko %s [-i instance] seal-legacy [--dry-run]", command))
		}
		if err := runSealLegacy(s, dryRun, os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	default:
		die("unknown %s action: %s", command, action)
	}
}

func parseSealLegacy(name string, args []string) (dryRun bool, err error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.BoolVar(&dryRun, "dry-run", false, "count the rows that would be sealed, change nothing")
	if err = flexParse(fs, args); err != nil {
		return false, err
	}
	if fs.NArg() != 0 {
		return false, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return dryRun, nil
}

// runSealLegacy seals rows written before encryption-at-rest landed. Readers
// already decrypt them correctly (`open` falls back for un-prefixed values), so
// nothing is broken — they are simply unencrypted at rest, which is what 5/14
// promises they are not.
//
// An explicit operator verb, NOT a boot hook (BUGS L16). Two reasons this file
// already paid for: the function this replaced, PurgeUnencryptedSecrets, was a
// boot-time mutation of this same table and wiped every secret on each startup;
// and a data migration that reports nothing is how `0023` left the whole fleet
// a member of nothing for nine hours. A verb prints the count before and after
// and runs when an operator says so, on one instance at a time.
//
// Rehearse with --dry-run against a read-only `.backup` copy first: this
// rewrites the column every credential lives in.
func runSealLegacy(s *store.Store, dryRun bool, w io.Writer) error {
	ctx := context.Background()
	before, err := s.CountPlaintextSecrets(ctx)
	if err != nil {
		return err
	}
	if dryRun {
		_, _ = fmt.Fprintf(w, "%d plaintext secret row(s) would be sealed (dry run, nothing changed)\n", before)
		return nil
	}
	if before == 0 {
		_, _ = fmt.Fprintln(w, "no plaintext secret rows: nothing to seal")
		return nil
	}
	sealed, err := s.EncryptPlaintextSecrets(ctx)
	if err != nil {
		return err
	}
	after, err := s.CountPlaintextSecrets(ctx)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "sealed %d of %d plaintext secret row(s); %d remaining\n", sealed, before, after)
	if after != 0 {
		return fmt.Errorf("%d row(s) still plaintext after sealing", after)
	}
	return nil
}

// parseSecretSet parses `secret set` / `user-secret set` args via flexParse so
// --value (-v) works in any position relative to the <scope_id> KEY positionals.
// It requires EXACTLY two positionals — extra or missing ones error rather than
// silently dropping a misplaced flag (the std-flag footgun). name labels the
// FlagSet for error messages (e.g. "secret set" vs "user-secret set").
func parseSecretSet(name string, args []string) (scopeID, key, value string, err error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&value, "value", "", "secret value (required)")
	fs.StringVar(&value, "v", "", "secret value (required)")
	if err = flexParse(fs, args); err != nil {
		return "", "", "", err
	}
	if fs.NArg() != 2 {
		return "", "", "", fmt.Errorf("expected <scope_id> KEY")
	}
	return fs.Arg(0), fs.Arg(1), value, nil
}

func runSecretSet(s *store.Store, scope store.SecretScope, scopeID, key, value string, w io.Writer) error {
	if value == "" {
		return fmt.Errorf("--value required")
	}
	if !store.ValidSecretKey(key) {
		return fmt.Errorf("key must match ^[A-Z][A-Z0-9_]*$")
	}
	// Audited: routd.db HAS had audit_log since routd migration 0016, and routd's
	// POST /v1/secrets writes through this same SetSecret. The row records the
	// scope/id/key and whether it sealed — NEVER the value (store/secrets.go).
	if err := s.SetSecret(scope, scopeID, key, value); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "set %s/%s/%s\n", scope, scopeID, key)
	return nil
}

func runSecretList(s *store.Store, scope store.SecretScope, scopeID string, w io.Writer) error {
	rows, err := s.ListSecrets(scope, scopeID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		_, _ = fmt.Fprintln(w, "no secrets")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "KEY\tCREATED_AT")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", r.Key, r.CreatedAt.Format("2006-01-02 15:04:05"))
	}
	return tw.Flush()
}

func runSecretDelete(s *store.Store, scope store.SecretScope, scopeID, key string, w io.Writer) error {
	if err := s.DeleteSecret(scope, scopeID, key); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "deleted %s/%s/%s\n", scope, scopeID, key)
	return nil
}
