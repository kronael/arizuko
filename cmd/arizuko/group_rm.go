package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

const groupRmUsage = "arizuko group <instance> rm [-f|--force] <folder>"

func parseGroupRm(args []string) (folder string, force bool, err error) {
	fs := flag.NewFlagSet("group rm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&force, "force", false, "remove rows and files without stopping runs at runed")
	fs.BoolVar(&force, "f", false, "remove rows and files without stopping runs at runed")
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

func runGroupRm(
	ctx context.Context, s *store.Store, cfg *core.Config,
	client *runedv1.Client, folder string, force bool, out io.Writer,
) error {
	lock, err := container.LockPackages(cfg.ProjectRoot)
	if err != nil {
		return err
	}
	// The deletion owns selections until registered identities disappear.
	defer lock.Close() //nolint:errcheck
	registered, err := s.GroupFolders(0)
	if err != nil {
		return err
	}
	folders := []string{folder}
	for _, registered := range registered {
		if registered != folder && groupfolder.Contains(folder, registered) {
			folders = append(folders, registered)
		}
	}
	slices.SortFunc(folders, func(a, b string) int { return len(b) - len(a) })
	for _, folder := range folders {
		if !groupfolder.IsValidFolder(folder) || filepath.Clean(folder) != folder || folder == "." {
			return fmt.Errorf("invalid group folder %q", folder)
		}
	}
	instance, err := os.OpenRoot(cfg.ProjectRoot)
	if err != nil {
		return err
	}
	// Closing read-only directory handles cannot change the removal result.
	defer instance.Close() //nolint:errcheck
	type removalPath struct {
		root   *os.Root
		folder string
		path   string
	}
	var paths []removalPath
	for _, base := range []string{
		cfg.GroupsDir,
		filepath.Join(cfg.WebDir, "pub"),
		filepath.Join(cfg.WebDir, "priv"),
	} {
		relative, err := filepath.Rel(cfg.ProjectRoot, base)
		if err != nil {
			return err
		}
		root, err := instance.OpenRoot(relative)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("validate %s: %w", base, err)
		}
		// Closing read-only directory handles cannot change the removal result.
		defer root.Close() //nolint:errcheck
		resolver := groupfolder.Resolver{GroupsDir: base}
		for _, folder := range folders {
			path, err := resolver.GroupPath(folder)
			if err != nil {
				return err
			}
			_, err = root.Stat(folder)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("validate %s: %w", path, err)
			}
			paths = append(paths, removalPath{root, folder, path})
		}
	}
	stop := func(context.Context, string) error { return nil }
	if !force {
		if err := client.Health(ctx); err != nil {
			return fmt.Errorf("cannot reach runed at %s, so a live run could outlive the group; "+
				"bring the instance up and set RUNED_URL (and RUNED_TOKEN if runed verifies bearers), "+
				"or stop the instance and pass -f/--force: %w", client.BaseURL, err)
		}
		stop = func(ctx context.Context, f string) error {
			_, err := client.StopFolder(ctx, f)
			return err
		}
	}
	if err := container.WithdrawGroupPackages(cfg.ProjectRoot, s, folders); err != nil {
		return err
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
	for _, path := range paths {
		if _, err := path.root.Lstat(path.folder); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect %s: %w", path.path, err)
		}
		if err := path.root.RemoveAll(path.folder); err != nil {
			return fmt.Errorf("remove %s: %w", path.path, err)
		}
		_, _ = fmt.Fprintf(out, "removed %s\n", path.path)
	}
	return nil
}
