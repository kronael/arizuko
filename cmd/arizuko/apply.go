package main

// Spec 5/8: `arizuko apply` + `arizuko export` orchestrator.
//
// Implementation choices:
//
//   - The config manifest spans TWO owner DBs (routd.db, onbod.db — the
//     only two with resreg resources per spec 5/16's owner-DB map), so
//     every verb here opens both stores and operates per SUBSYSTEM
//     (resreg.SubsystemRoutd / resreg.SubsystemOnbod). This is the step 4
//     fix for the long-standing inertness bug (BUGS.md Y1): these four
//     verbs used to call the frozen pre-split store.Open(messages.db) and
//     never reached a production instance.
//   - `apply <manifest_file>` reads ONE file (not a dir) in v1. The spec
//     talks about a manifest/ dir with merge semantics; we ship the
//     single-file path first because the directory path is more about
//     file ergonomics than engine correctness. Composition stays on the
//     spec until an operator hits it.
//   - `export` dumps each subsystem as its own `---`-separated YAML
//     document, concatenated into one file/stdout (spec 5/8 §"Surface":
//     "to a single path they concatenate as ---separated documents").
//     Document order is fixed (routd, then onbod) for determinism.
//   - The CLI dies on validation errors before touching the DB. The
//     content-hash CAS check + DELETE+INSERT happen in one tx per
//     subsystem via resreg.Apply.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources" // side-effect: register cold-tier resources
	"github.com/kronael/arizuko/store"
)

// subsystemOrder is the fixed, deterministic order export concatenates
// subsystem documents in and apply/plan report them in.
func subsystemOrder() []string {
	return []string{resreg.SubsystemRoutd, resreg.SubsystemOnbod}
}

// openSubsystemStores opens routd.db and onbod.db — the two owner DBs the
// config manifest spans. Strict: store.OpenRoutd/OpenOnbod error if the
// owning daemon has never booted to migrate its DB (no silent divergent
// schema), matching this CLI's existing "die loud" posture.
func openSubsystemStores(dataDir string) (map[string]*store.Store, error) {
	routd, err := store.OpenRoutd(dataDir + "/store")
	if err != nil {
		return nil, fmt.Errorf("open routd.db: %w", err)
	}
	onbod, err := store.OpenOnbod(dataDir + "/store")
	if err != nil {
		routd.Close()
		return nil, fmt.Errorf("open onbod.db: %w", err)
	}
	return map[string]*store.Store{
		resreg.SubsystemRoutd: routd,
		resreg.SubsystemOnbod: onbod,
	}, nil
}

func closeStores(stores map[string]*store.Store) {
	for _, s := range stores {
		s.Close()
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
	known, err := resreg.KnownFolders(owner.DB(), manifests...)
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

// committed records one subsystem whose transaction already went through, so
// a LATER subsystem's failure can put it back (spec 5/8 §"Cross-subsystem
// apply: per-subsystem transaction, pre-image rollback").
//
// pre is the subsystem's config projection as it stood BEFORE the forward
// apply — produced by the same Export/EmitYAML renderer the CAS hash uses, so
// there is no second backup format. postSum is the checksum the forward apply
// left behind, used as the rollback's own CAS token: if anything else moved
// the config in between, the rollback refuses instead of overwriting it.
type committed struct {
	subsystem string
	pre       map[string]any
	postSum   string
	scopes    []string
}

// applyDocs applies every document, one transaction per subsystem, and puts
// every already-committed subsystem back to its pre-apply projection if a
// later one fails. Returns one report line per applied subsystem.
//
// The pre-images are captured for ALL subsystems up front, before the first
// transaction opens: capturing lazily would read subsystem 2's state after
// subsystem 1 had already committed, which is still a valid pre-image for
// subsystem 2 but makes the ordering a silent correctness dependency instead
// of an obvious one.
//
// The rollback restores the manifest PROJECTION, not the database: a whole-DB
// swap would discard messages that arrived and memory a turn wrote during the
// apply window. Message history and filesystem trees are outside the
// projection and have their own recovery stories.
func applyDocs(ctx context.Context, stores map[string]*store.Store, docs []parsedDoc, force bool, actor, digest string) ([]string, error) {
	pre := map[string]map[string]any{}
	for _, d := range docs {
		st, ok := stores[d.subsystem]
		if !ok {
			return nil, fmt.Errorf("no store for subsystem %q", d.subsystem)
		}
		snapshot, _, _, err := resreg.ExportSnapshot(ctx, st.DB(), d.subsystem)
		if err != nil {
			return nil, fmt.Errorf("pre-image %s: %w", d.subsystem, err)
		}
		pre[d.subsystem] = snapshot
	}

	var report []string
	var done []committed
	for _, d := range docs {
		st := stores[d.subsystem]
		newSum, aerr := resreg.Apply(ctx, st.DB(), d.subsystem, d.checksum, force, d.manifest,
			&resreg.ApplyOpts{Actor: actor, ManifestDigest: digest})
		if aerr != nil {
			if rerr := rollback(ctx, stores, done, actor); rerr != nil {
				return report, fmt.Errorf("apply %s: %w; ROLLBACK ALSO FAILED, the instance is half-applied: %v",
					d.subsystem, aerr, rerr)
			}
			return report, fmt.Errorf("apply %s: %w", d.subsystem, aerr)
		}
		done = append(done, committed{
			subsystem: d.subsystem,
			pre:       pre[d.subsystem],
			postSum:   newSum,
			scopes:    resreg.ManifestScopes(d.subsystem, d.manifest),
		})
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
		if _, err := resreg.Apply(ctx, stores[c.subsystem].DB(), c.subsystem, c.postSum, false, c.pre,
			&resreg.ApplyOpts{Actor: actor + " (rollback)", PruneScopes: c.scopes}); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", c.subsystem, err))
		}
	}
	return errors.Join(errs...)
}

func cmdApply(args []string) {
	if len(args) < 2 {
		fmt.Println("usage: arizuko apply <instance> <manifest.yaml> [--force|-f]")
		os.Exit(1)
	}
	instance := args[0]
	file := args[1]
	force := false
	for _, a := range args[2:] {
		if a == "--force" || a == "-f" {
			force = true
		}
	}
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
	// The missing-group rule runs across ALL documents before the first tx
	// opens — a half-wired instance is worse than a refused restore. --force
	// does NOT override it: force means "the DB moved under my export", not
	// "write rows pointing at a group that does not exist".
	if bad := preflightFolders(stores, docs); len(bad) > 0 {
		for _, e := range bad {
			fmt.Fprintf(os.Stderr, "%v\n", e)
		}
		die("Failed: %s references folders that are not groups; add the groups rows or fix the names", file)
	}
	// Plan first (non-mutating) so the operator sees the delta the apply
	// commits — spec 5/8 §"Apply lifecycle" step 5 (print plan + ok).
	for _, d := range docs {
		if deltas, dperr := resreg.Plan(stores[d.subsystem].DB(), d.subsystem, d.manifest); dperr == nil {
			fmt.Printf("--- %s ---\n", d.subsystem)
			printPlan(deltas)
		}
	}
	digest := sha256.Sum256(data)
	// Apply writes its own single audit_log summary row in-tx (actor +
	// manifest digest + per-resource counts + final checksum), spec 5/8
	// §"CAS implementation" (3). No separate auditCLI — one row per apply.
	report, aerr := applyDocs(context.Background(), stores, docs, force,
		os.Getenv("USER"), hex.EncodeToString(digest[:]))
	for _, line := range report {
		fmt.Printf("applied %s — %s\n", file, line)
	}
	if aerr != nil {
		if errors.Is(aerr, resreg.ErrChecksumMismatch) {
			fmt.Fprintf(os.Stderr, "%v\nRe-export and re-apply, or use --force.\n", aerr)
			os.Exit(2)
		}
		die("Failed: %v", aerr)
	}
}

func cmdExport(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko export <instance> [output.yaml]")
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
		manifest, merr := resreg.Export(st.DB(), subsystem)
		if merr != nil {
			die("Failed: export %s: %v", subsystem, merr)
		}
		sum, cerr := resreg.Checksum(st.DB(), subsystem)
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
	os.Stdout.Write(out)
}

// cmdPlan: non-mutating diff of a manifest vs live DB (spec 5/8
// §"Apply lifecycle" step 3). Parses + validates, prints the per-resource
// add/update/unchanged/remove delta per subsystem document, never opens a
// write tx.
func cmdPlan(args []string) {
	if len(args) < 2 {
		fmt.Println("usage: arizuko plan <instance> <manifest.yaml>")
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
		dbSum, cerr := resreg.Checksum(st.DB(), subsystem)
		if cerr != nil {
			die("Failed: checksum %s: %v", subsystem, cerr)
		}
		deltas, derr := resreg.Plan(st.DB(), subsystem, manifest)
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

// printPlan renders the plan delta in catalog order. Changed resources
// list the add/update/remove PK strings. SkipApplyRebuild resources
// (secrets) never mutate via apply, so they print as informational
// "set/unset" — never actionable +/~/- deltas (spec 5/8 §"Secret
// safety": plan must agree with apply, which skips them).
func printPlan(deltas []resreg.ResourceDelta) {
	any := false
	for _, d := range deltas {
		if d.SkipApplyRebuild {
			if n := len(d.Add) + len(d.Update) + len(d.Unchanged); n > 0 {
				any = true
				fmt.Printf("%s: %d set (not applied — set via `arizuko secret set`)\n", d.Resource, n)
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
// §"arizuko get round-trip"). The fragment re-applies to a no-op — same
// shape `apply` accepts. Secret rows emit metadata only (the engine's
// SELECT omits the enc_value blob, which isn't in SecretsRow). Opens only
// the resource's OWN owner subsystem — resreg.Resource.DB says which.
func cmdGet(args []string) {
	if len(args) < 2 {
		fmt.Println("usage: arizuko get <instance> <resource>")
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
	frag, err := resreg.GetResource(st.DB(), resource)
	if err != nil {
		die("Failed: get %s: %v", resource, err)
	}
	out, err := resreg.EmitYAML(frag)
	if err != nil {
		die("Failed: emit yaml: %v", err)
	}
	os.Stdout.Write(out)
}
