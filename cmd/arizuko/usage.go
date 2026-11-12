package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

const usageText = "arizuko usage [-i instance] [--since|-s YYYY-MM-DD] [--by|-b folder|day]"

// cmdUsage sums the per-turn usage records routd stores (spec 6/39 §"The turn
// usage record") per folder or per UTC day. It reads routd's store through
// *routd.DB, the reader the turn_usage resource serves.
func cmdUsage(args []string) {
	instance := args[0]
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	var since, by string
	fs.StringVar(&since, "since", "", "first UTC day to count")
	fs.StringVar(&since, "s", "", "first UTC day to count")
	fs.StringVar(&by, "by", "folder", "group by folder or day")
	fs.StringVar(&by, "b", "folder", "group by folder or day")
	if err := flexParse(fs, args[1:]); err != nil || fs.NArg() != 0 {
		usage(err, usageText)
	}
	var from time.Time
	if since != "" {
		t, err := time.Parse(time.DateOnly, since)
		if err != nil {
			usage(err, usageText)
		}
		from = t
	}
	if by != "folder" && by != "day" {
		usage(fmt.Errorf("--by must be folder or day, got %q", by), usageText)
	}

	// mustInstanceDir also puts that instance's environment into this process,
	// which is what names the store the CLI then reads.
	mustInstanceDir(instance)
	s := routd.OpenOver(mustOpen(store.OwnerRoutd).Obj())
	defer func() { _ = s.Close() }()
	if err := runUsage(s, from, by, os.Stdout); err != nil {
		die("Failed: %v", err)
	}
}

// usageSum is one group's totals. CostCents counts unmetered turns only: a
// metered turn's cost is the gateway's number, so its self-report is not
// added and the turn is counted in MeteredTurns instead.
type usageSum struct {
	Key          string
	Turns        int
	MeteredTurns int
	Input        int
	CacheRead    int
	CacheWrite   int
	Output       int
	CostCents    int
	CreditsSpent float64
}

func sumUsage(rows []resources.TurnUsageRow, by string) []usageSum {
	sums := map[string]*usageSum{}
	turns := map[string]bool{}
	for _, r := range rows {
		key := r.Folder
		if by == "day" {
			key = r.RecordedAt[:len(time.DateOnly)]
		}
		u := sums[key]
		if u == nil {
			u = &usageSum{Key: key}
			sums[key] = u
		}
		turn := r.Folder + "\x00" + r.TurnID
		if !turns[turn] {
			turns[turn] = true
			u.Turns++
			if r.Metered {
				u.MeteredTurns++
			}
		}
		u.Input += r.Input
		u.CacheRead += r.CacheRead
		u.CacheWrite += r.CacheWrite
		u.Output += r.Output
		if !r.Metered {
			u.CostCents += r.CostCents
		}
		if r.CreditsSpent != nil {
			u.CreditsSpent += *r.CreditsSpent
		}
	}
	out := make([]usageSum, 0, len(sums))
	for _, key := range slices.Sorted(maps.Keys(sums)) {
		out = append(out, *sums[key])
	}
	return out
}

func runUsage(s *routd.DB, since time.Time, by string, w io.Writer) error {
	rows, err := s.TurnUsage(context.Background(), since)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintf(tw, "%s\tturns\tmetered\tinput\tcache_read\tcache_write\toutput\tcost_cents\tcredits\t\n", by)
	for _, u := range sumUsage(rows, by) {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%.2f\t\n", u.Key, u.Turns, u.MeteredTurns,
			u.Input, u.CacheRead, u.CacheWrite, u.Output, u.CostCents, u.CreditsSpent)
	}
	return tw.Flush()
}
