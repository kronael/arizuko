package main

// Spec 5/36: `arizuko apply` + `arizuko export` orchestrator.
//
// Implementation choices:
//
//   - `apply <manifest_file>` reads ONE file (not a dir) in v1. The
//     spec talks about a manifest/ dir with merge semantics; we ship
//     the single-file path first because the directory path is more
//     about file ergonomics than engine correctness. Composition stays
//     on the spec until an operator hits it.
//   - `export` dumps `arizuko_<instance>` store as one YAML doc to
//     stdout. The deterministic-key-order acceptance criterion is met
//     by resreg.EmitYAML's canonical sort.
//   - The CLI dies on validation errors before touching the DB. CAS
//     check + DELETE+INSERT happen in one tx via resreg.Apply.

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
	st, err := store.Open(dataDir + "/store")
	if err != nil {
		die("Failed: open store: %v", err)
	}
	defer st.Close()
	data, err := os.ReadFile(file)
	if err != nil {
		die("Failed: read %s: %v", file, err)
	}
	manifest, version, err := resreg.ParseYAML(data)
	if err != nil {
		die("Failed: parse %s: %v", file, err)
	}
	// Pre-apply DB version is the true "from"; the manifest version is not
	// (with --force against a drifted DB they differ, and printing manifest
	// would misreport the prior state).
	fromVer, err := resreg.ConfigVersion(st.DB())
	if err != nil {
		die("Failed: read config_version: %v", err)
	}
	// Plan first (non-mutating) so the operator sees the delta the apply
	// commits — spec 5/36 §"Apply lifecycle" step 5 (print plan + ok).
	if deltas, perr := resreg.Plan(st.DB(), manifest); perr == nil {
		printPlan(deltas)
	}
	// Apply writes its own single audit_log summary row in-tx (actor +
	// manifest digest + per-resource counts + final version), spec 5/36
	// §"CAS implementation" (3). No separate auditCLI — one row per apply.
	digest := sha256.Sum256(data)
	opts := &resreg.ApplyOpts{Actor: os.Getenv("USER"), ManifestDigest: hex.EncodeToString(digest[:])}
	newVer, err := resreg.Apply(context.Background(), st.DB(), version, force, manifest, opts)
	if err != nil {
		if errors.Is(err, resreg.ErrVersionMismatch) {
			fmt.Fprintf(os.Stderr, "config_version mismatch (manifest=%d db=%d). "+
				"Re-export and re-apply, or use --force.\n", version, newVer)
			os.Exit(2)
		}
		die("Failed: apply: %v", err)
	}
	fmt.Printf("applied %s; config_version: %d -> %d\n", file, fromVer, newVer)
}

func cmdExport(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: arizuko export <instance> [output.yaml]")
		os.Exit(1)
	}
	instance := args[0]
	dataDir := mustInstanceDir(instance)
	st, err := store.Open(dataDir + "/store")
	if err != nil {
		die("Failed: open store: %v", err)
	}
	defer st.Close()
	manifest, err := resreg.Export(st.DB())
	if err != nil {
		die("Failed: export: %v", err)
	}
	out, err := resreg.EmitYAML(manifest)
	if err != nil {
		die("Failed: emit yaml: %v", err)
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

// cmdPlan: non-mutating diff of a manifest vs live DB (spec 5/36
// §"Apply lifecycle" step 3). Parses + validates, prints the per-resource
// add/update/unchanged/remove delta, never opens a write tx.
func cmdPlan(args []string) {
	if len(args) < 2 {
		fmt.Println("usage: arizuko plan <instance> <manifest.yaml>")
		os.Exit(1)
	}
	instance := args[0]
	file := args[1]
	dataDir := mustInstanceDir(instance)
	st, err := store.Open(dataDir + "/store")
	if err != nil {
		die("Failed: open store: %v", err)
	}
	defer st.Close()
	data, err := os.ReadFile(file)
	if err != nil {
		die("Failed: read %s: %v", file, err)
	}
	manifest, version, err := resreg.ParseYAML(data)
	if err != nil {
		die("Failed: parse %s: %v", file, err)
	}
	dbVer, err := resreg.ConfigVersion(st.DB())
	if err != nil {
		die("Failed: read config_version: %v", err)
	}
	deltas, err := resreg.Plan(st.DB(), manifest)
	if err != nil {
		die("Failed: plan: %v", err)
	}
	printPlan(deltas)
	if version != dbVer {
		fmt.Printf("\nconfig_version: manifest=%d db=%d — apply would reject without --force\n", version, dbVer)
	} else {
		fmt.Printf("\nconfig_version: %d (match)\n", dbVer)
	}
}

// printPlan renders the plan delta in catalog order. Changed resources
// list the add/update/remove PK strings. SkipApplyRebuild resources
// (secrets) never mutate via apply, so they print as informational
// "set/unset" — never actionable +/~/- deltas (spec 5/36 §"Secret
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

// cmdGet: emit a live-DB manifest fragment for one resource (spec 5/36
// §"arizuko get round-trip"). The fragment re-applies to a no-op — same
// shape `apply` accepts. Secret rows emit metadata only (the engine's
// SELECT omits the enc_value blob, which isn't in SecretsRow).
func cmdGet(args []string) {
	if len(args) < 2 {
		fmt.Println("usage: arizuko get <instance> <resource>")
		os.Exit(1)
	}
	instance := args[0]
	resource := args[1]
	dataDir := mustInstanceDir(instance)
	st, err := store.Open(dataDir + "/store")
	if err != nil {
		die("Failed: open store: %v", err)
	}
	defer st.Close()
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
