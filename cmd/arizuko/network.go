package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/onvos/arizuko/store"
)

func cmdNetwork(args []string) {
	need(args, 2, "arizuko network <instance> <allow|deny|list> ...")
	instance, action := args[0], args[1]

	dataDir := mustInstanceDir(instance)
	s, err := store.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		die("Failed: open db: %v", err)
	}
	defer s.Close()

	switch action {
	case "allow":
		need(args, 4, "arizuko network <instance> allow <folder> <target>")
		folder, target := args[2], args[3]
		if err := s.AddNetworkRule(folder, target, "cli"); err != nil {
			die("Failed: add rule: %v", err)
		}
		fmt.Printf("rule added: folder=%q target=%q\n", folder, target)

	case "deny":
		need(args, 4, "arizuko network <instance> deny <folder> <target>")
		folder, target := args[2], args[3]
		if err := s.RemoveNetworkRule(folder, target); err != nil {
			die("Failed: rm rule: %v", err)
		}
		fmt.Printf("rule removed: folder=%q target=%q\n", folder, target)

	case "list":
		var rules []store.NetworkRule
		if len(args) >= 3 {
			rules, err = s.ListNetworkRules(args[2])
		} else {
			rules, err = s.AllNetworkRules()
		}
		if err != nil {
			die("Failed: list: %v", err)
		}
		if len(rules) == 0 {
			fmt.Println("no rules")
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "FOLDER\tTARGET\tCREATED_AT\tCREATED_BY")
		for _, r := range rules {
			folderLabel := r.Folder
			if folderLabel == "" {
				folderLabel = "(root)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
				folderLabel, r.Target, r.CreatedAt.Format("2006-01-02"), r.CreatedBy)
		}
		tw.Flush()

	case "resolve":
		need(args, 3, "arizuko network <instance> resolve <folder>")
		list, err := s.ResolveAllowlist(args[2])
		if err != nil {
			die("Failed: resolve: %v", err)
		}
		for _, t := range list {
			fmt.Println(t)
		}

	default:
		die("unknown network action: %s", action)
	}
}
