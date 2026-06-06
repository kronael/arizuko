package main

import (
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
// topology — spec 5/5) wrapped as a *store.Store, with the SECRETS_KEY keyring
// set so CLI-written secrets seal under the SAME key routd decrypts with at read
// time. The host-admin CLI writes here directly (filesystem access), no token
// plumbing — same discipline it used for messages.db before the split.
func openStoreWithKey(dataDir string) (*store.Store, error) {
	s, err := store.OpenRoutd(filepath.Join(dataDir, "store"))
	if err != nil {
		return nil, err
	}
	cfg, err := core.LoadConfigFrom(dataDir)
	if err == nil {
		if kr := core.SecretKeyring(cfg.SecretsKey); len(kr) > 0 {
			s.SetSecretKeys(kr...)
		}
	}
	return s, nil
}

// cmdSecret manages folder-scoped secrets (operator-only). User secrets
// live behind `arizuko user-secret`. Spec 7/Y.
func cmdSecret(args []string) {
	need(args, 2, "arizuko secret <instance> <set|list|delete> ...")
	instance, action := args[0], args[1]

	dataDir := mustInstanceDir(instance)
	s, err := openStoreWithKey(dataDir)
	if err != nil {
		die("Failed: open db: %v", err)
	}
	defer s.Close()

	switch action {
	case "set":
		fs := flag.NewFlagSet("secret set", flag.ExitOnError)
		value := fs.String("value", "", "secret value (required)")
		fs.Parse(args[2:])
		if fs.NArg() < 2 {
			die("usage: arizuko secret <instance> set <folder> KEY --value V")
		}
		if err := runSecretSet(s, store.ScopeFolder, fs.Arg(0), fs.Arg(1), *value, os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	case "list":
		need(args, 3, "arizuko secret <instance> list <folder>")
		if err := runSecretList(s, store.ScopeFolder, args[2], os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	case "delete":
		need(args, 4, "arizuko secret <instance> delete <folder> KEY")
		if err := runSecretDelete(s, store.ScopeFolder, args[2], args[3], os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	default:
		die("unknown secret action: %s", action)
	}
}

// cmdUserSecret manages user-scoped secrets (operator-only fallback for
// users who haven't logged in via /dash/me/secrets yet).
func cmdUserSecret(args []string) {
	need(args, 2, "arizuko user-secret <instance> <set|list|delete> ...")
	instance, action := args[0], args[1]

	dataDir := mustInstanceDir(instance)
	s, err := openStoreWithKey(dataDir)
	if err != nil {
		die("Failed: open db: %v", err)
	}
	defer s.Close()

	switch action {
	case "set":
		fs := flag.NewFlagSet("user-secret set", flag.ExitOnError)
		value := fs.String("value", "", "secret value (required)")
		fs.Parse(args[2:])
		if fs.NArg() < 2 {
			die("usage: arizuko user-secret <instance> set <user_sub> KEY --value V")
		}
		if err := runSecretSet(s, store.ScopeUser, fs.Arg(0), fs.Arg(1), *value, os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	case "list":
		need(args, 3, "arizuko user-secret <instance> list <user_sub>")
		if err := runSecretList(s, store.ScopeUser, args[2], os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	case "delete":
		need(args, 4, "arizuko user-secret <instance> delete <user_sub> KEY")
		if err := runSecretDelete(s, store.ScopeUser, args[2], args[3], os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	default:
		die("unknown user-secret action: %s", action)
	}
}

func runSecretSet(s *store.Store, scope store.SecretScope, scopeID, key, value string, w io.Writer) error {
	if value == "" {
		return fmt.Errorf("--value required")
	}
	if !keyValid(key) {
		return fmt.Errorf("key must match ^[A-Z][A-Z0-9_]*$")
	}
	// Audit-free: routd.db (the secrets owner now) has no audit_log table — same
	// discipline as routd's POST /v1/secrets endpoint.
	if err := s.PutSecretRow(scope, scopeID, key, value); err != nil {
		return err
	}
	fmt.Fprintf(w, "set %s/%s/%s\n", scope, scopeID, key)
	return nil
}

func runSecretList(s *store.Store, scope store.SecretScope, scopeID string, w io.Writer) error {
	rows, err := s.ListSecrets(scope, scopeID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "no secrets")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tCREATED_AT")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\n", r.Key, r.CreatedAt.Format("2006-01-02 15:04:05"))
	}
	return tw.Flush()
}

func runSecretDelete(s *store.Store, scope store.SecretScope, scopeID, key string, w io.Writer) error {
	if err := s.DeleteSecretRow(scope, scopeID, key); err != nil {
		return err
	}
	fmt.Fprintf(w, "deleted %s/%s/%s\n", scope, scopeID, key)
	return nil
}

// keyValid mirrors dashd/me_secrets.go keyPattern: uppercase ENV-style ids.
func keyValid(key string) bool {
	if key == "" {
		return false
	}
	for i, ch := range key {
		if i == 0 {
			if ch < 'A' || ch > 'Z' {
				return false
			}
			continue
		}
		if !(ch == '_' || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')) {
			return false
		}
	}
	return true
}
