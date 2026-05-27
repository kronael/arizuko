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
	"errors"
	"fmt"
	"os"

	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources" // side-effect: register cold-tier resources
	"github.com/kronael/arizuko/store"
)

func cmdApply(args []string) {
	if len(args) < 2 {
		fmt.Println("usage: arizuko apply <instance> <manifest.yaml> [--force]")
		os.Exit(1)
	}
	instance := args[0]
	file := args[1]
	force := false
	for _, a := range args[2:] {
		if a == "--force" {
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
	newVer, err := resreg.Apply(context.Background(), st.DB(), version, force, manifest)
	if err != nil {
		if errors.Is(err, resreg.ErrVersionMismatch) {
			fmt.Fprintf(os.Stderr, "config_version mismatch (manifest=%d db=%d). "+
				"Re-export and re-apply, or use --force.\n", version, newVer)
			os.Exit(2)
		}
		die("Failed: apply: %v", err)
	}
	fmt.Printf("applied %s; config_version: %d -> %d\n", file, version, newVer)
	auditCLI(st, "apply", []string{file})
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
