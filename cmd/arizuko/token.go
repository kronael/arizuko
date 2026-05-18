package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kronael/arizuko/store"
)

// cmdToken: arizuko token <instance> <subcommand> ...
//
//	arizuko token <instance> issue chat <folder> [<suffix>]
//	arizuko token <instance> issue webhook <folder> <label> [<suffix>]
//	arizuko token <instance> list <folder>
//	arizuko token <instance> revoke <jid>
func cmdToken(args []string) {
	if len(args) < 3 {
		die("usage: arizuko token <instance> <issue|list|revoke> ...")
	}
	instance, sub := args[0], args[1]

	dataDir := mustInstanceDir(instance)
	st, err := store.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		die("Failed: open db: %v", err)
	}
	defer st.Close()

	switch sub {
	case "issue":
		tokenIssue(st, args[2:])
	case "list":
		tokenList(st, args[2:])
	case "revoke":
		tokenRevoke(st, args[2:])
	default:
		die("unknown token subcommand: %s", sub)
	}
}

func tokenIssue(st *store.Store, args []string) {
	if len(args) < 2 {
		die("usage: arizuko token <instance> issue chat <folder> [<suffix>]\n" +
			"       arizuko token <instance> issue webhook <folder> <label> [<suffix>]")
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
	fmt.Printf("jid:   %s\ntoken: %s\n", jid, raw)
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
		die("usage: arizuko token <instance> revoke <jid>")
	}
	jid := args[0]
	// Owner folder: derived from the JID (web:<folder>/... or hook:<folder>/...).
	folder := jidFolder(jid)
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
	fmt.Println("revoked:", jid)
}

// jidFolder extracts the folder segment from a route-token JID.
func jidFolder(jid string) string {
	for _, prefix := range []string{"web:", "hook:"} {
		if strings.HasPrefix(jid, prefix) {
			rest := strings.TrimPrefix(jid, prefix)
			// folder is the first path segment
			if idx := strings.Index(rest, "/"); idx >= 0 {
				return rest[:idx]
			}
			return rest
		}
	}
	return ""
}
