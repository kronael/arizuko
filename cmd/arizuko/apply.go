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
	docs, err := resreg.SplitDocuments(data)
	if err != nil {
		die("Failed: split %s: %v", file, err)
	}
	digest := sha256.Sum256(data)
	digestHex := hex.EncodeToString(digest[:])
	for _, doc := range docs {
		manifest, checksum, perr := resreg.ParseYAML(doc)
		if perr != nil {
			die("Failed: parse %s: %v", file, perr)
		}
		if len(manifest) == 0 {
			continue // an empty document (e.g. a trailing ---) carries nothing to apply
		}
		subsystem, serr := resreg.SubsystemOf(manifest)
		if serr != nil {
			die("Failed: %v", serr)
		}
		st := stores[subsystem]
		// Plan first (non-mutating) so the operator sees the delta the apply
		// commits — spec 5/8 §"Apply lifecycle" step 5 (print plan + ok).
		if deltas, perr := resreg.Plan(st.DB(), subsystem, manifest); perr == nil {
			fmt.Printf("--- %s ---\n", subsystem)
			printPlan(deltas)
		}
		// Apply writes its own single audit_log summary row in-tx (actor +
		// manifest digest + per-resource counts + final checksum), spec 5/8
		// §"CAS implementation" (3). No separate auditCLI — one row per apply.
		opts := &resreg.ApplyOpts{Actor: os.Getenv("USER"), ManifestDigest: digestHex}
		newSum, aerr := resreg.Apply(context.Background(), st.DB(), subsystem, checksum, force, manifest, opts)
		if aerr != nil {
			if errors.Is(aerr, resreg.ErrChecksumMismatch) {
				fmt.Fprintf(os.Stderr, "%s: checksum mismatch (manifest=%s db=%s). "+
					"Re-export and re-apply, or use --force.\n", subsystem, checksum, newSum)
				os.Exit(2)
			}
			die("Failed: apply %s: %v", subsystem, aerr)
		}
		fmt.Printf("applied %s (%s); checksum: %s -> %s\n", file, subsystem, checksum, newSum)
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
	docs, err := resreg.SplitDocuments(data)
	if err != nil {
		die("Failed: split %s: %v", file, err)
	}
	for _, doc := range docs {
		manifest, checksum, perr := resreg.ParseYAML(doc)
		if perr != nil {
			die("Failed: parse %s: %v", file, perr)
		}
		if len(manifest) == 0 {
			continue
		}
		subsystem, serr := resreg.SubsystemOf(manifest)
		if serr != nil {
			die("Failed: %v", serr)
		}
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
