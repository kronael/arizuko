package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

// cmdToken: arizuko token <instance> <subcommand> ...
//
//	arizuko token <instance> issue chat <folder> [<suffix>]
//	arizuko token <instance> issue webhook <folder> <label> [<suffix>]
//	arizuko token <instance> issue bearer <folder> --scope s1,s2 [--ttl 1h] [--sub user:cli]
//	arizuko token <instance> list <folder>
//	arizuko token <instance> revoke <jid>
func cmdToken(args []string) {
	if len(args) < 3 {
		die("usage: arizuko token <instance> <issue|list|revoke> ...")
	}
	instance, sub := args[0], args[1]

	dataDir := mustInstanceDir(instance)
	// route_tokens + groups live in routd.db post-split — messages.db is the
	// frozen monolith routd never reads (same fix class as `arizuko network`,
	// 2dfa5670).
	st := mustOpenACL(dataDir)
	defer st.Close()

	switch sub {
	case "issue":
		tokenIssue(st, dataDir, args[2:])
	case "list":
		tokenList(st, args[2:])
	case "revoke":
		tokenRevoke(st, args[2:])
	default:
		die("unknown token subcommand: %s", sub)
	}
}

func tokenIssue(st *store.Store, dataDir string, args []string) {
	if len(args) < 2 {
		die("usage: arizuko token <instance> issue chat <folder> [<suffix>]\n" +
			"       arizuko token <instance> issue webhook <folder> <label> [<suffix>]\n" +
			"       arizuko token <instance> issue bearer <folder> --scope|-s s1,s2 [--ttl|-t 1h] [--sub SUB]")
	}
	if args[0] == "bearer" {
		tokenIssueBearer(st, dataDir, args[1:])
		return
	}
	kind, folder := args[0], args[1]

	var jid string
	switch kind {
	case "chat":
		suffix := ""
		if len(args) >= 3 {
			suffix = args[2]
		}
		if suffix != "" {
			jid = "web:" + folder + "/" + suffix
		} else {
			jid = "web:" + folder
		}
	case "webhook", "hook":
		if len(args) < 3 {
			die("usage: arizuko token <instance> issue webhook <folder> <label>")
		}
		label, suffix := args[2], ""
		if len(args) >= 4 {
			suffix = args[3]
		}
		if suffix != "" {
			jid = "hook:" + folder + "/" + label + "/" + suffix
		} else {
			jid = "hook:" + folder + "/" + label
		}
	default:
		die("unknown kind %q; use chat or webhook", kind)
	}

	if _, ok := st.GroupByFolder(folder); !ok {
		die("Failed: group %q not found", folder)
	}

	raw := store.GenRouteToken()
	rt := store.RouteToken{JID: jid, OwnerFolder: folder, CreatedAt: time.Now()}
	if err := st.InsertRouteToken(raw, rt); err != nil {
		die("Failed: insert token: %v", err)
	}
	auditCLI(st, "token issue", []string{kind, folder})
	fmt.Printf("jid:   %s\ntoken: %s\n", jid, raw)
}

// tokenIssueBearer mints a folder-scoped ES256 access JWT signed with authd's
// ACTIVE signing key, read directly from auth.db — the FS-mounted host-admin
// path (same discipline as `arizuko grant` writing routd.db). No new authd
// endpoint: the operator holding the data dir already holds the trust root.
// The minted claim shape matches authd's signMinted (typ=user, arz/folder),
// so every backend verifies it against the same JWKS.
func tokenIssueBearer(st *store.Store, dataDir string, args []string) {
	fs := flag.NewFlagSet("token issue bearer", flag.ContinueOnError)
	var scope, sub string
	fs.StringVar(&scope, "scope", "", "comma-separated scopes (required, e.g. messages:write,messages:read)")
	fs.StringVar(&scope, "s", "", "comma-separated scopes (required)")
	ttl := fs.Duration("ttl", time.Hour, "token lifetime")
	fs.DurationVar(ttl, "t", time.Hour, "token lifetime")
	fs.StringVar(&sub, "sub", "user:cli", "subject claim for the minted token")
	if err := flexParse(fs, args); err != nil || fs.NArg() != 1 {
		die("usage: arizuko token <instance> issue bearer <folder> --scope|-s s1,s2 [--ttl|-t 1h] [--sub SUB]")
	}
	folder := fs.Arg(0)
	if scope == "" {
		die("Failed: --scope is required (strict: no default scope)")
	}
	// Folder must exist in routd.db — groups live there post-split; a token
	// for a nonexistent folder would verify but own nothing.
	gs := mustOpenACL(dataDir)
	defer gs.Close()
	if _, ok := gs.GroupByFolder(folder); !ok {
		die("Failed: group %q not found", folder)
	}
	tok, exp, err := mintBearer(filepath.Join(dataDir, "store"), folder, sub, strings.Split(scope, ","), *ttl)
	if err != nil {
		die("Failed: %v", err)
	}
	auditCLI(st, "token issue bearer", []string{folder, scope, ttl.String()})
	fmt.Printf("sub:     %s\nfolder:  %s\nscope:   %s\nexpires: %s\ntoken:   %s\n",
		sub, folder, scope, exp.Format(time.RFC3339), tok)
}

// mintBearer loads authd's active signing key from auth.db and signs a
// folder-scoped user token. Split into a helper so tests can mint against a
// fixture auth.db and verify with the public half.
func mintBearer(storeDir, folder, sub string, scopes []string, ttl time.Duration) (string, time.Time, error) {
	dsn := filepath.Join(storeDir, "auth.db") + "?_pragma=busy_timeout(5000)"
	adb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", time.Time{}, err
	}
	// read-only single query; close error unactionable
	defer adb.Close() //nolint:errcheck
	var kid, privPEM string
	if err := adb.QueryRow(
		`SELECT kid, priv_pem FROM signing_keys WHERE active = 1`).Scan(&kid, &privPEM); err != nil {
		return "", time.Time{}, fmt.Errorf("load active signing key (has authd booted?): %w", err)
	}
	priv, err := auth.ParseECPrivateKeyPEM(privPEM)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("parse key %s: %w", kid, err)
	}
	key := &auth.SigningKey{Kid: kid, Priv: priv}
	tok, err := key.Sign(auth.TokenClaims{
		Sub: sub, Typ: "user", Scope: scopes,
		Extra: map[string]string{"arz/folder": folder},
	}, ttl)
	if err != nil {
		return "", time.Time{}, err
	}
	return tok, time.Now().Add(ttl), nil
}

func tokenList(st *store.Store, args []string) {
	if len(args) < 1 {
		die("usage: arizuko token <instance> list <folder>")
	}
	folder := args[0]
	tokens := st.ListRouteTokens(folder)
	if len(tokens) == 0 {
		fmt.Println("(no tokens)")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "JID\tKIND\tCREATED")
	for _, t := range tokens {
		kind := store.RouteTokenKind(t.JID)
		fmt.Fprintf(w, "%s\t%s\t%s\n", t.JID, kind, t.CreatedAt.Format(time.RFC3339))
	}
	w.Flush()
}

func tokenRevoke(st *store.Store, args []string) {
	if len(args) < 1 {
		die("usage: arizuko token <instance> revoke <jid> [<owner_folder>]")
	}
	jid := args[0]
	// owner_folder bounds revocation and may diverge from the JID's folder
	// (a higher-tier folder can mint on behalf of a descendant — see
	// store.RouteToken). Take it explicitly when given; otherwise fall back
	// to the JID's folder, which only holds for self-owned tokens.
	var folder string
	if len(args) >= 2 {
		folder = args[1]
	} else {
		folder = groupfolder.JidFolder(jid)
	}
	if folder == "" {
		die("Failed: unrecognised JID format %q", jid)
	}
	revoked, err := st.RevokeRouteToken(jid, folder)
	if err != nil {
		die("Failed: %v", err)
	}
	if !revoked {
		die("Failed: token not found or wrong owner for JID %q", jid)
	}
	auditCLI(st, "token revoke", []string{jid})
	fmt.Println("revoked:", jid)
}
