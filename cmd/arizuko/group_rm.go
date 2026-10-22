package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

const groupRmUsage = "arizuko group <instance> rm [-f|--force] <folder>"

func parseGroupRm(args []string) (folder string, force bool, err error) {
	fs := flag.NewFlagSet("group rm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&force, "force", false, "remove the rows without stopping runs at runed")
	fs.BoolVar(&force, "f", false, "remove the rows without stopping runs at runed")
	if err := flexParse(fs, args); err != nil {
		return "", false, err
	}
	if fs.NArg() != 1 {
		return "", false, errors.New("want exactly one <folder>")
	}
	return fs.Arg(0), force, nil
}

func runedClient(dataDir string) (*runedv1.Client, error) {
	env, err := loadInstanceEnv(dataDir)
	if err != nil {
		return nil, fmt.Errorf("load .env: %w", err)
	}
	base := env["RUNED_URL"]
	if base == "" {
		base = "http://runed:8080"
	}
	return runedv1.NewClient(base, env["RUNED_TOKEN"], 30*time.Second), nil
}

func probeRuned(ctx context.Context, client *runedv1.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, client.BaseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := client.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET /health answered %d", resp.StatusCode)
	}
	return nil
}

// runGroupRm purges folder's subtree and then stops each removed folder's run at
// runed. Without force it probes runed first and removes nothing when runed is
// unreachable. With force the client is not used and no run is stopped.
func runGroupRm(ctx context.Context, s *store.Store, client *runedv1.Client, folder string, force bool, out io.Writer) error {
	stop := func(context.Context, string) error { return nil }
	if !force {
		if err := probeRuned(ctx, client); err != nil {
			return fmt.Errorf("cannot reach runed at %s, so a live run could outlive the group; "+
				"bring the instance up and set RUNED_URL (and RUNED_TOKEN if runed verifies bearers), "+
				"or stop the instance and pass -f/--force: %w", client.BaseURL, err)
		}
		stop = func(ctx context.Context, f string) error {
			_, err := client.StopFolder(ctx, f)
			return err
		}
	}
	removed, swept, err := s.DeleteGroupStopping(ctx, folder, stop)
	if removed == 0 && swept == 0 {
		if err != nil {
			return fmt.Errorf("remove group: %w", err)
		}
		return fmt.Errorf("no group at %s", folder)
	}
	auditCLI(s, "group rm", []string{folder})
	if removed == 0 {
		_, _ = fmt.Fprintf(out, "no group at %s — swept %d orphan row(s) under it\n", folder, swept)
	} else {
		_, _ = fmt.Fprintf(out, "removed %d group(s) under %s, with %d other row(s)\n", removed, folder, swept)
	}
	if err != nil {
		return fmt.Errorf("rows removed, but: %w", err)
	}
	return nil
}
