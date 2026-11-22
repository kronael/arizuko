package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

func subsystemOrder() []string {
	return []string{resreg.SubsystemRoutd, resreg.SubsystemOnbod}
}

func openSubsystemStores() map[string]*store.Store {
	return map[string]*store.Store{
		resreg.SubsystemRoutd: mustOpen(store.OwnerRoutd),
		resreg.SubsystemOnbod: mustOpen(store.OwnerOnbod),
	}
}

func cliActor() string { return "cli:" + os.Getenv("USER") }

func closeStores(stores map[string]*store.Store) {
	for _, s := range stores {
		_ = s.Close()
	}
}

type parsedDoc struct {
	subsystem string
	checksum  string
	manifest  map[string]any

	pruneScopes []string
}

func parseDocs(file string, data []byte) ([]parsedDoc, error) {
	docs, err := resreg.SplitDocuments(data)
	if err != nil {
		return nil, fmt.Errorf("split %s: %w", file, err)
	}
	var out []parsedDoc
	for _, doc := range docs {
		manifest, checksum, perr := resreg.ParseYAML(doc)
		if perr != nil {
			return nil, fmt.Errorf("parse %s: %w", file, perr)
		}
		if len(manifest) == 0 {
			continue
		}
		subsystem, serr := resreg.SubsystemOf(manifest)
		if serr != nil {
			return nil, serr
		}
		out = append(out, parsedDoc{subsystem: subsystem, checksum: checksum, manifest: manifest})
	}
	return out, nil
}

func preflightFolders(stores map[string]*store.Store, docs []parsedDoc) []error {
	g := resreg.Lookup(resreg.GroupsResource)
	if g == nil {
		return []error{fmt.Errorf("%s resource is not registered", resreg.GroupsResource)}
	}
	owner, ok := stores[g.DB]
	if !ok {
		return []error{fmt.Errorf("%s has no configured owner subsystem %q", resreg.GroupsResource, g.DB)}
	}
	manifests := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		manifests = append(manifests, d.manifest)
	}
	known, err := resreg.KnownFolders(resreg.On(owner.Obj()), manifests...)
	if err != nil {
		return []error{fmt.Errorf("read known folders: %w", err)}
	}
	var out []error
	for _, d := range docs {
		if verr := resreg.ValidateFolderRefs(d.subsystem, d.manifest, known); verr != nil {
			out = append(out, verr)
		}
	}
	return out
}

type committed struct {
	subsystem string
	pre       map[string]any
	postSum   string
	scopes    []string
}

type applyRun struct {
	force    bool
	takeOver bool
	actor    string
	digest   string
	extra    map[string]any
}

func applyDocs(ctx context.Context, stores map[string]*store.Store, docs []parsedDoc,
	run applyRun) ([]string, error) {
	pre := map[string]map[string]any{}
	for _, d := range docs {
		st, ok := stores[d.subsystem]
		if !ok {
			return nil, fmt.Errorf("no store for subsystem %q", d.subsystem)
		}
		snapshot, _, _, err := resreg.ExportSnapshot(ctx, resreg.On(st.Obj()), d.subsystem)
		if errors.Is(err, objstore.ErrScopeHeld) {
			fmt.Fprintf(os.Stderr, "%s is part-applied and still claimed (%v), so it has no "+
				"pre-image: it cannot be rolled back if a later subsystem fails\n", d.subsystem, err)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("pre-image %s: %w", d.subsystem, err)
		}
		pre[d.subsystem] = snapshot
	}

	var report []string
	var done []committed
	for _, d := range docs {
		st := stores[d.subsystem]
		newSum, aerr := resreg.Apply(ctx, resreg.On(st.Obj()), d.subsystem, d.checksum, run.force, d.manifest,
			&resreg.ApplyOpts{Actor: run.actor, ManifestDigest: run.digest, Extra: run.extra,
				PruneScopes: d.pruneScopes, TakeOver: run.takeOver})
		if aerr != nil {
			if rerr := rollback(ctx, stores, done, run.actor); rerr != nil {
				return report, fmt.Errorf("apply %s: %w; ROLLBACK ALSO FAILED, the instance is half-applied: %v",
					d.subsystem, aerr, rerr)
			}
			return report, fmt.Errorf("apply %s: %w", d.subsystem, aerr)
		}
		if snapshot, ok := pre[d.subsystem]; ok {
			done = append(done, committed{
				subsystem: d.subsystem,
				pre:       snapshot,
				postSum:   newSum,
				scopes:    resreg.ManifestScopes(d.subsystem, d.manifest),
			})
		}
		report = append(report, fmt.Sprintf("%s: applied (checksum %s -> %s)", d.subsystem, d.checksum, newSum))
	}
	return report, nil
}

func rollback(ctx context.Context, stores map[string]*store.Store, done []committed, actor string) error {
	var errs []error
	for i := len(done) - 1; i >= 0; i-- {
		c := done[i]
		if _, err := resreg.Apply(ctx, resreg.On(stores[c.subsystem].Obj()), c.subsystem, c.postSum, false, c.pre,
			&resreg.ApplyOpts{Actor: actor + " (rollback)", PruneScopes: c.scopes}); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", c.subsystem, err))
		}
	}
	return errors.Join(errs...)
}

func groupFolders(docs []parsedDoc) []string {
	var out []string
	for _, d := range docs {
		if rows, ok := d.manifest[resreg.GroupsResource].([]resources.GroupsRow); ok {
			for _, g := range rows {
				out = append(out, g.Folder)
			}
		}
	}
	return out
}

func mergeLive(ctx context.Context, stores map[string]*store.Store, docs []parsedDoc) ([]parsedDoc, error) {
	seen := map[string]bool{}
	out := make([]parsedDoc, 0, len(docs))
	for _, d := range docs {
		st, ok := stores[d.subsystem]
		if !ok {
			return nil, fmt.Errorf("no store for subsystem %q", d.subsystem)
		}
		if seen[d.subsystem] {
			return nil, fmt.Errorf("two %s documents: a rows file holds one per subsystem", d.subsystem)
		}
		seen[d.subsystem] = true
		live, checksum, _, err := resreg.ExportSnapshot(ctx, resreg.On(st.Obj()), d.subsystem)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", d.subsystem, err)
		}
		merged := map[string]any{}
		for name, rows := range d.manifest {
			if merged[name], err = resreg.Lookup(name).MergeRows(live[name], rows); err != nil {
				return nil, err
			}
		}
		out = append(out, parsedDoc{subsystem: d.subsystem, checksum: checksum, manifest: merged})
	}
	return out, nil
}

func applyRows(ctx context.Context, stores map[string]*store.Store, dataDir, file string, data []byte) ([]string, error) {
	docs, err := parseDocs(file, data)
	if err != nil {
		return nil, err
	}
	if bad := preflightFolders(stores, docs); len(bad) > 0 {
		return nil, errors.Join(bad...)
	}
	merged, err := mergeLive(ctx, stores, docs)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	lines, err := applyDocs(ctx, stores, merged, applyRun{actor: cliActor(), digest: "sha256:" + hex.EncodeToString(sum[:])})
	if err != nil {
		return lines, err
	}
	return lines, setupMissingGroupDirs(dataDir, groupFolders(docs))
}

func isTar(data []byte) bool {
	return len(data) >= 262 && string(data[257:262]) == "ustar"
}

const applyUsage = "arizuko apply [-i instance] <dump.tar|rows.yml> [--force|-f] [--stopped]"

func cmdApply(args []string) {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	var force, stopped bool
	fs.BoolVar(&force, "force", false, "restore over a part-applied dump")
	fs.BoolVar(&force, "f", false, "restore over a part-applied dump")
	fs.BoolVar(&stopped, "stopped", false, "skip the run-slot hold")
	if err := flexParse(fs, args); err != nil || fs.NArg() != 2 {
		usage(err, applyUsage)
	}
	instance, file := fs.Arg(0), fs.Arg(1)
	dataDir := mustInstanceDir(instance)
	stores := openSubsystemStores()
	defer closeStores(stores)
	data, err := os.ReadFile(file)
	if err != nil {
		die("Failed: read %s: %v", file, err)
	}
	ctx := context.Background()
	if isTar(data) {
		restoreDump(ctx, stores, dataDir, data, force, stopped)
		return
	}
	if force || stopped {
		die("Failed: --force and --stopped apply to a dump tar; %s is a rows file", file)
	}
	lines, err := applyRows(ctx, stores, dataDir, file, data)
	for _, l := range lines {
		fmt.Println(l)
	}
	if err != nil {
		die("Failed: apply: %v", err)
	}
}

func cmdPlan(args []string) {
	args = need(args, 2, 2, "arizuko plan [-i instance] <rows.yml>")
	instance := args[0]
	file := args[1]
	mustInstanceDir(instance)
	stores := openSubsystemStores()
	defer closeStores(stores)
	data, err := os.ReadFile(file)
	if err != nil {
		die("Failed: read %s: %v", file, err)
	}
	docs, perr := parseDocs(file, data)
	if perr != nil {
		die("Failed: %v", perr)
	}
	for _, e := range preflightFolders(stores, docs) {
		fmt.Printf("%v — apply would refuse\n", e)
	}
	merged, merr := mergeLive(context.Background(), stores, docs)
	if merr != nil {
		die("Failed: %v", merr)
	}
	for _, d := range merged {
		deltas, derr := resreg.Plan(resreg.On(stores[d.subsystem].Obj()), d.subsystem, d.manifest)
		if derr != nil {
			die("Failed: plan %s: %v", d.subsystem, derr)
		}
		fmt.Printf("--- %s ---\n", d.subsystem)
		printPlan(deltas)
	}
}

func printPlan(deltas []resreg.ResourceDelta) {
	any := false
	for _, d := range deltas {
		if d.SkipApplyRebuild {
			if d.Live > 0 {
				any = true
				fmt.Printf("%s: %d set (not applied — managed imperatively, never via apply)\n", d.Resource, d.Live)
			}
			continue
		}
		if !d.Changed() {
			continue
		}
		any = true
		fmt.Printf("%s:\n", d.Resource)
		for _, pk := range d.Add {
			fmt.Printf("  + %s\n", pk)
		}
		for _, pk := range d.Update {
			fmt.Printf("  ~ %s\n", pk)
		}
		for _, pk := range d.Remove {
			fmt.Printf("  - %s\n", pk)
		}
	}
	if !any {
		fmt.Println("no changes")
	}
}

func cmdGet(args []string) {
	args = need(args, 2, 2, "arizuko get [-i instance] <resource>")
	instance := args[0]
	resource := args[1]
	r := resreg.Lookup(resource)
	if r == nil {
		die("Failed: unknown resource %q", resource)
	}
	mustInstanceDir(instance)
	stores := openSubsystemStores()
	defer closeStores(stores)
	st, ok := stores[r.DB]
	if !ok {
		die("Failed: resource %q has no configured owner subsystem %q", resource, r.DB)
	}
	frag, err := resreg.GetResource(resreg.On(st.Obj()), resource)
	if err != nil {
		die("Failed: get %s: %v", resource, err)
	}
	out, err := resreg.EmitYAML(frag)
	if err != nil {
		die("Failed: emit yaml: %v", err)
	}
	_, _ = os.Stdout.Write(out)
}
