package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// cmdRoute manages the route table (operator-only). Routes live alongside acl
// in routd.db (split topology) or messages.db (monolith), so it reuses
// mustOpenACL's dual-path opener. Audit-free writes: routd.db has no audit_log
// table, so we use PutRouteRow / DeleteRouteRow (the audit-free twins) — same
// discipline as `arizuko grant` and `arizuko secret`.
func cmdRoute(args []string) {
	need(args, 2, "arizuko route <instance> <list|add|rm> ...")
	instance, action := args[0], args[1]

	dataDir := mustInstanceDir(instance)
	s := mustOpenACL(dataDir)
	defer s.Close()

	switch action {
	case "list":
		if err := runRouteList(s, os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	case "add":
		seq, match, target, err := parseRouteAdd(args[2:])
		if err != nil {
			die("usage: arizuko route <instance> add <match> <target> [--seq N]: %v", err)
		}
		if err := runRouteAdd(s, seq, match, target, os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	case "rm":
		need(args, 3, "arizuko route <instance> rm <id>")
		id, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			die("Failed: invalid id %q", args[2])
		}
		if err := runRouteRm(s, id, os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	default:
		die("unknown route action: %s", action)
	}
}

// parseRouteAdd parses `route add` args via flexParse so --seq works in any
// position (before, between, or after the match/target positionals) with a -s
// short alias. It requires EXACTLY two positionals — extra or missing ones
// error rather than silently dropping a misplaced --seq (the std-flag footgun).
func parseRouteAdd(args []string) (seq int, match, target string, err error) {
	fs := flag.NewFlagSet("route add", flag.ContinueOnError)
	fs.IntVar(&seq, "seq", 0, "match priority (lower wins)")
	fs.IntVar(&seq, "s", 0, "match priority (lower wins)")
	if err = flexParse(fs, args); err != nil {
		return 0, "", "", err
	}
	if fs.NArg() != 2 {
		return 0, "", "", fmt.Errorf("expected <match> <target>")
	}
	return seq, fs.Arg(0), fs.Arg(1), nil
}

func runRouteList(s *store.Store, w io.Writer) error {
	routes := s.AllRoutes()
	if len(routes) == 0 {
		fmt.Fprintln(w, "no routes")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSEQ\tMATCH\tTARGET")
	for _, r := range routes {
		match := r.Match
		if match == "" {
			match = "*" // empty match = catch-all; render so it's not blank
		}
		fmt.Fprintf(tw, "%d\t%d\t%s\t%s\n", r.ID, r.Seq, match, r.Target)
	}
	return tw.Flush()
}

// runRouteAdd inserts a route. "*" and "" both mean catch-all; the router
// represents catch-all as an empty Match (store/routes.go ListRoutes), so a
// literal "*" is normalized to "" to match how the matcher reads it.
func runRouteAdd(s *store.Store, seq int, match, target string, w io.Writer) error {
	if target == "" {
		return fmt.Errorf("target required")
	}
	if match == "*" {
		match = ""
	}
	id, err := s.PutRouteRow(core.Route{Seq: seq, Match: match, Target: target})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "added route %d\n", id)
	return nil
}

func runRouteRm(s *store.Store, id int64, w io.Writer) error {
	n, err := s.DeleteRouteRow(id)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("no route %d", id)
	}
	fmt.Fprintf(w, "removed route %d\n", id)
	return nil
}
