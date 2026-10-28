package main

// Spec 5/8: manifest export, inspection and the shared apply engine.
// Sync and archive restore apply validated documents per owner subsystem;
// export emits one document per subsystem in a fixed order.

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources" // side-effect: register cold-tier resources
	"github.com/kronael/arizuko/store"
)

// subsystemOrder is the fixed, deterministic order export concatenates
// subsystem documents in and apply/plan report them in.
func subsystemOrder() []string {
	return []string{resreg.SubsystemRoutd, resreg.SubsystemOnbod}
}

// openSubsystemStores opens routd's and onbod's stores — the two owners the
// config manifest spans. Strict: store.OpenOwner errors if the owning daemon
// has never booted to make its tables (no silent divergent schema), matching
// this CLI's existing "die loud" posture.
func openSubsystemStores(dataDir string) (map[string]*store.Store, error) {
	routd, err := mustOpenOwner(store.OwnerRoutd, dataDir)
	if err != nil {
		return nil, fmt.Errorf("open routd's store: %w", err)
	}
	onbod, err := mustOpenOwner(store.OwnerOnbod, dataDir)
	if err != nil {
		routd.Close() //nolint:errcheck // the open already failed
		return nil, fmt.Errorf("open onbod's store: %w", err)
	}
	return map[string]*store.Store{
		resreg.SubsystemRoutd: routd,
		resreg.SubsystemOnbod: onbod,
	}, nil
}

func cliActor() string { return "cli:" + os.Getenv("USER") }

func closeStores(stores map[string]*store.Store) {
	for _, s := range stores {
		_ = s.Close()
	}
}

// parsedDoc is one manifest document after ParseYAML, kept so apply/plan can
// make TWO passes: parse-and-validate every document, and only then open the
// first transaction (spec 5/8 §"The missing-group rule": "refuses, before
// writing anything"). A one-pass loop could not do it — it would already have
// committed document 1 by the time it discovered document 2's bad reference.
type parsedDoc struct {
	subsystem string
	checksum  string
	manifest  map[string]any

	pruneScopes []string
}

// parseDocs splits and parses every document of a manifest file. Empty
// documents (a trailing `---`) carry nothing and are dropped. Returns an
// error rather than dying — `archive apply` composes the same helper and must
// unwind its own tar reader, not os.Exit out of it.
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

// preflightFolders is the missing-group rule's CLI half: every folder-scoped
// row across EVERY document must name a group that the document set declares
// or the target DB already has. Returns the offending references; the caller
// decides whether that is a refusal (apply, archive apply) or a report (plan).
//
// `groups` lives in exactly one owner DB and the resource declaration says
// which — identity is configured, never derived (root CLAUDE.md), so this
// reads Resource.DB rather than assuming routd.
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

// committed records one subsystem whose transaction already went through, so a
// LATER subsystem's failure can put it back (spec 5/8 §"Cross-subsystem apply").
// pre is the subsystem's projection as it stood BEFORE the forward apply, produced
// by the same renderer the CAS hash uses, so there is no second backup format.
// postSum is the rollback's own CAS token: if anything else moved the config in
// between, the rollback refuses instead of overwriting it.
type committed struct {
	subsystem string
	pre       map[string]any
	postSum   string
	scopes    []string
}

// applyRun is what one apply run carries beyond its documents: the two guards an
// operator may lower, and the three fields that reach the audit row.
//
// It is a struct rather than more parameters because `force` and `takeOver` are
// adjacent booleans that lower DIFFERENT guards — force ignores the checksum the
// manifest was planned against, takeOver adopts a claim an interrupted apply left
// — and a call site spelling them `false, false` says which is which to nobody.
type applyRun struct {
	force    bool
	takeOver bool
	actor    string
	digest   string
	extra    map[string]any
}

// applyDocs applies every document, one transaction per subsystem, and puts every
// already-committed subsystem back to its pre-apply projection if a later one
// fails.
//
// The pre-images are captured for ALL subsystems up front, before the first
// transaction opens: capturing lazily would still be valid but would make the
// ordering a silent correctness dependency instead of an obvious one.
//
// A SUBSYSTEM AN INTERRUPTED APPLY STILL HOLDS HAS NO PRE-IMAGE, and that is not
// a reason to refuse here. Its rows are part-written by definition, so a
// consistent read over them would describe a state no writer ever saw and
// resreg.ExportSnapshot says so rather than inventing one (BUGS L267). Refusing
// the whole run on it would put the operator's error one call too early: the
// apply itself knows whether this manifest may roll that scope forward, and
// its answer names the work the claim is holding. So this records that there is
// no pre-image, says it, and keeps the subsystem out of the rollback set — a
// scope with nothing to go back to must never be silently "restored" to an empty
// document.
//
// The rollback restores the manifest PROJECTION, not the database: a whole-DB swap
// would discard messages that arrived and memory a turn wrote during the apply
// window.
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

// rollback re-applies each committed subsystem's pre-image through the SAME
// resreg.Apply codepath, newest first. PruneScopes carries the forward
// manifest's own scopes so a folder the forward apply CREATED — one the
// pre-image never mentions, and therefore would not prune — does not survive.
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

func cmdExport(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko export [-i instance] [output.yml]")
		os.Exit(1)
	}
	instance := args[0]
	dataDir := mustInstanceDir(instance)
	stores, err := openSubsystemStores(dataDir)
	if err != nil {
		die("Failed: open store: %v", err)
	}
	defer closeStores(stores)
	var out []byte
	for _, subsystem := range subsystemOrder() {
		st := stores[subsystem]
		manifest, merr := resreg.Export(resreg.On(st.Obj()), subsystem)
		if merr != nil {
			die("Failed: export %s: %v", subsystem, merr)
		}
		sum, cerr := resreg.Checksum(resreg.On(st.Obj()), subsystem)
		if cerr != nil {
			die("Failed: checksum %s: %v", subsystem, cerr)
		}
		manifest["checksum"] = sum
		doc, eerr := resreg.EmitYAML(manifest)
		if eerr != nil {
			die("Failed: emit yaml %s: %v", subsystem, eerr)
		}
		if len(out) > 0 {
			out = append(out, []byte("---\n")...)
		}
		out = append(out, doc...)
	}
	if len(args) >= 2 {
		if err := os.WriteFile(args[1], out, 0o644); err != nil {
			die("Failed: write %s: %v", args[1], err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", args[1], len(out))
		return
	}
	_, _ = os.Stdout.Write(out)
}

// cmdPlan: non-mutating diff of a manifest vs live DB (spec 5/8 §"Surface").
// Parses + validates, prints the per-resource add/update/unchanged/remove
// delta per subsystem document, never opens a write tx.
func cmdPlan(args []string) {
	if len(args) < 2 {
		fmt.Println("usage: arizuko plan [-i instance] <manifest.yml>")
		os.Exit(1)
	}
	instance := args[0]
	file := args[1]
	dataDir := mustInstanceDir(instance)
	stores, err := openSubsystemStores(dataDir)
	if err != nil {
		die("Failed: open store: %v", err)
	}
	defer closeStores(stores)
	data, err := os.ReadFile(file)
	if err != nil {
		die("Failed: read %s: %v", file, err)
	}
	docs, perr := parseDocs(file, data)
	if perr != nil {
		die("Failed: %v", perr)
	}
	// plan never mutates, so it REPORTS the missing-group refusal apply would
	// raise instead of exiting on it — the operator sees the delta and the
	// blocker in one run.
	for _, e := range preflightFolders(stores, docs) {
		fmt.Printf("%v — apply would refuse\n", e)
	}
	for _, d := range docs {
		manifest, checksum, subsystem := d.manifest, d.checksum, d.subsystem
		st := stores[subsystem]
		dbSum, cerr := resreg.Checksum(resreg.On(st.Obj()), subsystem)
		if cerr != nil {
			die("Failed: checksum %s: %v", subsystem, cerr)
		}
		deltas, derr := resreg.Plan(resreg.On(st.Obj()), subsystem, manifest)
		if derr != nil {
			die("Failed: plan %s: %v", subsystem, derr)
		}
		fmt.Printf("--- %s ---\n", subsystem)
		printPlan(deltas)
		if checksum != dbSum {
			fmt.Printf("checksum: manifest=%s db=%s — apply would reject without --force\n", checksum, dbSum)
		} else {
			fmt.Printf("checksum: %s (match)\n", dbSum)
		}
	}
}

// printPlan renders the plan delta in catalog order. SkipApplyRebuild resources
// never mutate via apply, so they print as informational "set" rather than
// actionable deltas.
//
// The informational line names no command. FIVE resources carry the flag and each
// is managed by a different verb, so the line said "set via `arizuko secret set`"
// for four resources that verb cannot touch (BUGS R1-5). A per-resource command
// table here would be a second catalog beside resreg's.
func printPlan(deltas []resreg.ResourceDelta) {
	any := false
	for _, d := range deltas {
		if d.SkipApplyRebuild {
			// The count is the LIVE one. These resources are minted imperatively, so
			// what an operator wants from the line is how many exist — and adding the
			// PK lists up cannot say that for route_tokens or invites, whose identity
			// is a digest YAML never carries (BUGS L260).
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

// cmdGet: emit a live-DB manifest fragment for one resource (spec 5/8
// §"Round-trip honesty"). The fragment re-applies to a no-op — same
// shape `apply` accepts. Secret rows emit metadata only (the engine's
// SELECT omits the enc_value blob, which isn't in SecretsRow). Opens only
// the resource's OWN owner subsystem — resreg.Resource.DB says which.
func cmdGet(args []string) {
	if len(args) < 2 {
		fmt.Println("usage: arizuko get [-i instance] <resource>")
		os.Exit(1)
	}
	instance := args[0]
	resource := args[1]
	r := resreg.Lookup(resource)
	if r == nil {
		die("Failed: unknown resource %q", resource)
	}
	dataDir := mustInstanceDir(instance)
	stores, err := openSubsystemStores(dataDir)
	if err != nil {
		die("Failed: open store: %v", err)
	}
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
