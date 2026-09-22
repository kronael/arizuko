package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/kronael/arizuko/routd"
)

// cmdBudget manages per-folder and per-user cost caps. Spec 11/19-cost-caps, 6/39.
//
// The cap is arizuko's policy and lives in ROUTD.DB
// (`groups.cost_cap_cents_per_day` / `user_profiles.cost_cap_cents_per_day`);
// routd's pre-spawn budgetGate pushes it to the model gateway, which meters the
// spend and refuses the request itself. The verb goes through *routd.DB so it
// writes the exact rows the gate reads; opening the frozen messages.db here made
// `budget set` a silent no-op (BUGS.md Q1).
//
// Spend is NOT reported here. It belongs to the gateway, and reaching it needs
// the master key this CLI deliberately does not hold — the metered surfaces are
// the dashboard and routd's GET /v1/cost/summary.
func cmdBudget(args []string) {
	need(args, 2, "arizuko budget <instance> <set|show> ...")
	instance, action := args[0], args[1]

	// mustInstanceDir also puts that instance's environment into this process,
	// which is what names the store the CLI then writes to.
	mustInstanceDir(instance)
	s := mustOpenRoutd()
	defer s.Close()

	switch action {
	case "set":
		scope, target, daily, err := parseBudgetSet(args[2:])
		if err != nil {
			die("usage: arizuko budget <instance> set <folder|user> <name|sub> --daily|-d N: %v", err)
		}
		if err := runBudgetSet(s, scope, target, daily, os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	case "show":
		need(args, 4, "arizuko budget <instance> show <folder|user> <name|sub>")
		if err := runBudgetShow(s, args[2], args[3], os.Stdout); err != nil {
			die("Failed: %v", err)
		}
	default:
		die("unknown budget action: %s", action)
	}
}

// parseBudgetSet parses `budget set` args via flexParse so --daily (-d) works in
// any position relative to the <scope> <target> positionals. It requires EXACTLY
// two positionals AND a daily value >= 0 (the cap is mandatory; 0 disables) —
// missing/extra positionals or an unset --daily error rather than silently
// dropping a misplaced flag.
func parseBudgetSet(args []string) (scope, target string, daily int, err error) {
	fs := flag.NewFlagSet("budget set", flag.ContinueOnError)
	fs.IntVar(&daily, "daily", -1, "daily cap in cents (0 = uncapped)")
	fs.IntVar(&daily, "d", -1, "daily cap in cents (0 = uncapped)")
	if err = flexParse(fs, args); err != nil {
		return "", "", 0, err
	}
	if fs.NArg() != 2 {
		return "", "", 0, fmt.Errorf("expected <folder|user> <name|sub>")
	}
	if daily < 0 {
		return "", "", 0, fmt.Errorf("--daily N required (cents; 0 disables)")
	}
	return fs.Arg(0), fs.Arg(1), daily, nil
}

func runBudgetSet(s *routd.DB, scope, target string, daily int, w io.Writer) error {
	switch scope {
	case "folder":
		if err := s.SetFolderCap(target, daily); err != nil {
			return err
		}
	case "user":
		if err := s.SetUserCap(target, daily); err != nil {
			return err
		}
	default:
		return fmt.Errorf("scope must be 'folder' or 'user', got %q", scope)
	}
	if daily == 0 {
		fmt.Fprintf(w, "OK: cap removed for %s %s (uncapped)\n", scope, target)
	} else {
		fmt.Fprintf(w, "OK: %s %s capped at %d cents/day\n", scope, target, daily)
	}
	return nil
}

func runBudgetShow(s *routd.DB, scope, target string, w io.Writer) error {
	var cap int
	var err error
	switch scope {
	case "folder":
		cap, err = s.FolderCap(target)
	case "user":
		cap, err = s.UserCap(target)
	default:
		return fmt.Errorf("scope must be 'folder' or 'user', got %q", scope)
	}
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "scope\t%s\n", scope)
	fmt.Fprintf(tw, "target\t%s\n", target)
	fmt.Fprintf(tw, "cap\t%s\n", formatCap(cap))
	fmt.Fprintf(tw, "spend\tmetered by the model gateway — see the dashboard usage page\n")
	return tw.Flush()
}

func formatCap(cents int) string {
	if cents == 0 {
		return "uncapped"
	}
	return fmt.Sprintf("%d cents/day", cents)
}
