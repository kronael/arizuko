package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

// cmdGithub: arizuko github <instance> <subcommand> ...
//
//	arizuko github <instance> add <repo|org|app> <name> <folder>
//	arizuko github <instance> list [<folder>]
//	arizuko github <instance> rm <kind>/<name> <folder>
//
// add mints the webhook secret, seals it into routd.db and seeds the route
// rows that deliver the source's events to <folder>. The URL + secret print
// once; paste both into GitHub (content type: application/json).
func cmdGithub(args []string) {
	need(args, 2, "arizuko github <instance> <add|list|rm> ...")
	instance, sub := args[0], args[1]
	dataDir := mustInstanceDir(instance)
	s, err := openStoreWithKey(dataDir)
	if err != nil {
		die("Failed: open routd.db: %v", err)
	}
	defer func() { _ = s.Close() }()

	switch sub {
	case "add":
		need(args, 5, "arizuko github <instance> add <repo|org|app> <name> <folder>")
		kind, name, folder := args[2], args[3], args[4]
		if !groupfolder.IsValidFolder(folder) {
			die("Failed: invalid folder %q", folder)
		}
		if _, ok := s.GroupByFolder(folder); !ok {
			die("Failed: group %q not found", folder)
		}
		key, err := store.GitHubSourceKey(kind, name)
		if err != nil {
			die("Failed: %v", err)
		}
		secret, err := s.AddGitHubSource(store.GitHubSource{Source: key, OwnerFolder: folder}, folder)
		if err != nil {
			die("Failed: register %s: %v", key, err)
		}
		auditCLI(s, "github add", []string{key, folder})
		cfg, err := core.LoadConfigFrom(dataDir)
		if err != nil {
			die("Failed: load config: %v", err)
		}
		url := "<WEB_HOST unset>/github/" + key
		if base := core.PublicBaseURL(cfg.WebHost); base != "" {
			url = base + "/github/" + key
		}
		fmt.Printf("source: %s\nurl:    %s\nsecret: %s\n", key, url, secret)
		fmt.Printf("paste url + secret into GitHub (content type: application/json); events route to %s\n", folder)

	case "list":
		folder := ""
		if len(args) > 2 {
			folder = args[2]
		}
		rows, err := s.ListGitHubSources(folder)
		if err != nil {
			die("Failed: list: %v", err)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "SOURCE\tOWNER\tCREATED")
		for _, r := range rows {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", r.Source, r.OwnerFolder, r.CreatedAt.UTC().Format(time.RFC3339))
		}
		_ = w.Flush()

	case "rm":
		need(args, 4, "arizuko github <instance> rm <kind>/<name> <folder>")
		key, folder := args[2], args[3]
		hit, err := s.RemoveGitHubSource(key, folder)
		if err != nil {
			die("Failed: revoke %s: %v", key, err)
		}
		if !hit {
			die("Failed: no source %s owned by %s", key, folder)
		}
		auditCLI(s, "github rm", []string{key, folder})
		fmt.Printf("revoked %s (deliveries now 404; seeded routes removed)\n", key)

	default:
		die("unknown github subcommand: %s", sub)
	}
}
