package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/routd"
)

func TestManifestFilenameChild(t *testing.T) {
	packed := os.Getenv("ARIZUKO_PKG_ARGS")
	if packed == "" {
		t.Skip("subprocess only")
	}
	args := strings.Split(packed, "\n")
	switch args[0] {
	case "create":
		cmdCreate(args[1:])
	case "group":
		cmdGroup(args[1:])
	case "products":
		cmdProducts(args[1:])
	case "packages":
		cmdPackages(args[1:])
	default:
		t.Fatalf("unknown command %q", args[0])
	}
	os.Exit(0)
}

func TestManifestGroupConsumerStates(t *testing.T) {
	for _, state := range []string{"missing", "metadata", "empty", "malformed", "empty-source", "legacy", "mixed"} {
		t.Run(state, func(t *testing.T) {
			dataDir, _ := mixInstance(t, nil, nil)
			group := filepath.Join(dataDir, "groups", "main")
			if err := os.Remove(filepath.Join(group, "arizuko.toml")); err != nil {
				t.Fatal(err)
			}
			const metadata = "# keep these bytes\nname = \"unrelated-identity\"\ncustom = \"mine\"\n"
			body := metadata
			switch state {
			case "empty":
				body += "product = []\n"
			case "malformed":
				body = "name = [\n"
			case "empty-source":
				body += "[[product]]\n"
			case "legacy", "mixed":
				mustWrite(t, filepath.Join(group, "products.toml"), "[[product]]\nsource = \"old\"\n")
			}
			if state != "missing" && state != "legacy" {
				mustWrite(t, filepath.Join(group, "arizuko.toml"), body)
			}
			before := treeHashes(t, group)
			out, err := runManifestCLI(t, []string{"products", "t", "apply", "main"})
			if state == "metadata" || state == "empty" {
				if err != nil || !strings.Contains(out, "no products selected") {
					t.Fatalf("empty selection must be a no-op: %v, %s", err, out)
				}
			} else if err == nil || !strings.Contains(out, "arizuko.toml") {
				t.Fatalf("invalid declaration accepted: %v, %s", err, out)
			}
			if state == "legacy" || state == "mixed" {
				for _, want := range []string{"products.toml", "merge", "remove"} {
					if !strings.Contains(out, want) {
						t.Errorf("legacy refusal omitted %q: %s", want, out)
					}
				}
			}
			after := treeHashes(t, group)
			if len(after) != len(before) {
				t.Errorf("apply changed file set: %v -> %v", before, after)
			}
			for rel, hash := range before {
				if after[rel] != hash {
					t.Errorf("apply changed %s", rel)
				}
			}
			db, err := routd.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			records, err := db.InstalledPackages()
			if err != nil || len(records) != 0 {
				t.Errorf("no-op/refusal created records: %+v, %v", records, err)
			}
		})
	}
}

func runManifestCLI(t *testing.T, args []string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestManifestFilenameChild$")
	cmd.Env = append(os.Environ(), "ARIZUKO_PKG_ARGS="+strings.Join(args, "\n"))
	out, err := cmd.CombinedOutput()
	t.Logf("%s: %s", strings.Join(args, " "), out)
	return string(out), err
}

func TestManifestApplyAndSyncPreserveOwnership(t *testing.T) {
	dataDir, base := mixInstance(t, map[string]map[string]string{
		"one": {"arizuko.toml": "name = \"one\"\n", "PERSONA.md": "first",
			"skills/first/SKILL.md": "first skill"},
		"two": {"arizuko.toml": "name = \"two\"\n", "PERSONA.md": "second",
			"skills/shared/SKILL.md": "second skill"},
	}, []string{"one", "two"})
	path := filepath.Join(dataDir, "groups", "main", "arizuko.toml")
	selections, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := "# operator metadata\nname = \"caller\"\nbrand = \"Mine\"\n" +
		string(selections) + "\n[extension]\nowner = \"keep\"\n"
	mustWrite(t, path, body)
	db, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutInstalledPackage(routd.InstalledPackage{
		Folder: "main", Name: "one", Manifest: map[string][]string{"group_seed": {"arizuko.toml"}},
		AssetHashes: map[string]string{"file:arizuko.toml": sha256hex([]byte(body))},
	}); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"apply", "sync"} {
		args := []string{"products", "t", "apply", "main"}
		wantSkill := "second skill"
		if verb == "sync" {
			wantSkill = "updated skill"
			mustWrite(t, filepath.Join(base, "src", "two", "skills", "shared", "SKILL.md"), wantSkill)
			args = []string{"packages", "t", "sync"}
		}
		if out, err := runManifestCLI(t, args); err != nil {
			t.Fatalf("%s: %v, %s", verb, err, out)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != body {
			t.Errorf("%s changed the caller manifest: %q, %v", verb, got, err)
		}
		if got := readGroupFile(t, dataDir, "main", "PERSONA.md"); got != "first" {
			t.Errorf("persona precedence = %q", got)
		}
		if got := selectedSkill(t, dataDir, "main", "two", "shared"); got != wantSkill {
			t.Errorf("skill update = %q, want %q", got, wantSkill)
		}
		for _, name := range []string{"one", "two"} {
			rec, ok, err := db.InstalledPackage("main", name)
			if err != nil || !ok || rec.Source != filepath.Join(base, "src", name) {
				t.Fatalf("source identity changed: %+v, %v, %v", rec, ok, err)
			}
			if _, owns := rec.AssetHashes["file:arizuko.toml"]; owns {
				t.Error("record retains caller manifest hash")
			}
			for _, rel := range rec.Manifest["group_seed"] {
				if rel == "arizuko.toml" {
					t.Error("record retains caller manifest ownership")
				}
			}
		}
	}
}

func TestManifestInstalledGroupRequiresSelections(t *testing.T) {
	for _, verb := range []string{"apply", "sync"} {
		for _, state := range []string{"missing", "metadata", "empty"} {
			t.Run(verb+"/"+state, func(t *testing.T) {
				dataDir, _ := mixInstance(t, nil, nil)
				group := filepath.Join(dataDir, "groups", "main")
				path := filepath.Join(group, "arizuko.toml")
				if state == "missing" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				} else {
					body := "name = \"caller\"\n"
					if state == "empty" {
						body += "product = []\n"
					}
					mustWrite(t, path, body)
				}
				mustWrite(t, filepath.Join(group, "PERSONA.md"), "operator persona")
				db, err := routd.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if err := db.PutInstalledPackage(routd.InstalledPackage{Folder: "main", Name: "installed"}); err != nil {
					t.Fatal(err)
				}
				before := treeHashes(t, group)
				args := []string{"products", "t", "apply", "main"}
				if verb == "sync" {
					args = []string{"packages", "t", "sync"}
				}
				out, err := runManifestCLI(t, args)
				if err == nil || !strings.Contains(out, "main has installed products") ||
					!strings.Contains(out, "arizuko.toml") {
					t.Fatalf("installed selections silently lost: %v, %s", err, out)
				}
				after := treeHashes(t, group)
				if len(after) != len(before) {
					t.Error("refusal changed file set")
				}
				for rel, hash := range before {
					if after[rel] != hash {
						t.Errorf("refusal changed %s", rel)
					}
				}
				if _, ok, err := db.InstalledPackage("main", "installed"); !ok || err != nil {
					t.Errorf("refusal lost installed record: %v, %v", ok, err)
				}
			})
		}
	}
}

func TestManifestSeedPreservesTarget(t *testing.T) {
	for _, action := range []string{"create", "group"} {
		for _, state := range []string{"matching", "metadata", "malformed", "different", "multiple", "legacy", "mixed", "no-product", "no-product-malformed"} {
			t.Run(action+"/"+state, func(t *testing.T) {
				dataDir, base := mixInstance(t, nil, nil)
				t.Setenv("HOST_APP_DIR", base)
				source := filepath.Join(base, "ant", "examples", "sample")
				mustWrite(t, filepath.Join(source, "arizuko.toml"), "name = \"sample\"\n")
				mustWrite(t, filepath.Join(source, "PERSONA.md"), "source persona")
				folder := "main"
				args := []string{"create", "t", "--product", "sample"}
				if action == "group" {
					folder = "sample"
					args = []string{"group", "t", "add", "web:sample", folder, "--product", "sample"}
				}
				group := filepath.Join(dataDir, "groups", folder)
				path := filepath.Join(group, "arizuko.toml")
				body := "# caller-owned\nname = \"caller\"\n[[product]]\nsource = \"ant/examples/sample\"\n"
				switch state {
				case "metadata", "no-product":
					body = "# metadata only\nname = \"caller\"\n"
				case "malformed", "no-product-malformed":
					body = "name = [\n"
				case "different":
					body = "[[product]]\nsource = \"ant/examples/other\"\n"
				case "multiple":
					body += "[[product]]\nsource = \"ant/examples/other\"\n"
				case "legacy", "mixed":
					mustWrite(t, filepath.Join(group, "products.toml"), "[[product]]\nsource = \"old\"\n")
				}
				mustWrite(t, path, body)
				if state == "legacy" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				mustWrite(t, filepath.Join(group, "PERSONA.md"), "operator persona")
				before := treeHashes(t, group)
				if strings.HasPrefix(state, "no-product") {
					args = args[:len(args)-2]
				}
				out, err := runManifestCLI(t, args)
				if state == "matching" || strings.HasPrefix(state, "no-product") {
					if err != nil {
						t.Fatalf("matching/no-product seed refused: %v, %s", err, out)
					}
				} else {
					if err == nil || !strings.Contains(out, "arizuko.toml") {
						t.Fatalf("conflict accepted: %v, %s", err, out)
					}
					after := treeHashes(t, group)
					if len(after) != len(before) {
						t.Error("conflict wrote payload")
					}
					for rel, hash := range before {
						if after[rel] != hash {
							t.Errorf("conflict changed %s", rel)
						}
					}
				}
				got, err := os.ReadFile(path)
				if state == "legacy" {
					if !os.IsNotExist(err) {
						t.Errorf("legacy refusal invented a manifest: %q, %v", got, err)
					}
				} else if err != nil || string(got) != body {
					t.Errorf("caller manifest changed: %q, %v", got, err)
				}
			})
		}
	}
}

func TestManifestNoProductLeavesAbsent(t *testing.T) {
	for _, action := range []string{"create", "group"} {
		t.Run(action, func(t *testing.T) {
			dataDir, _ := mixInstance(t, nil, nil)
			folder := "main"
			args := []string{"create", "t"}
			if action == "group" {
				folder = "empty"
				args = []string{"group", "t", "add", "web:empty", folder}
			} else if err := os.Remove(filepath.Join(dataDir, "groups", folder, "arizuko.toml")); err != nil {
				t.Fatal(err)
			}
			if out, err := runManifestCLI(t, args); err != nil {
				t.Fatalf("setup without a product: %v, %s", err, out)
			}
			if _, err := os.Stat(filepath.Join(dataDir, "groups", folder, "arizuko.toml")); !os.IsNotExist(err) {
				t.Errorf("setup invented a manifest: %v", err)
			}
		})
	}
}

func TestManifestFreshSeedLifecycle(t *testing.T) {
	for _, action := range []string{"create", "group"} {
		t.Run(action, func(t *testing.T) {
			dataDir, base := mixInstance(t, nil, nil)
			t.Setenv("HOST_APP_DIR", base)
			source := filepath.Join(base, "ant", "examples", "sample")
			mustWrite(t, filepath.Join(source, "arizuko.toml"), "name = \"sample\"\n")
			mustWrite(t, filepath.Join(source, "PERSONA.md"), "source persona")
			mustWrite(t, filepath.Join(source, "skills", "shared", "SKILL.md"), "v1")
			instance, folder := "t", "sample"
			args := []string{"group", instance, "add", "web:sample", folder, "--product", "sample"}
			if action == "create" {
				instance, folder = "fresh", "main"
				dataDir = filepath.Join(base, "arizuko_fresh")
				unsealPackageFixtures(t, dataDir)
				t.Setenv("DATA_DIR", dataDir)
				args = []string{"create", instance, "--product", "sample"}
			}
			if out, err := runManifestCLI(t, args); err != nil {
				t.Fatalf("seed: %v, %s", err, out)
			}
			path := filepath.Join(dataDir, "groups", folder, "arizuko.toml")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			db, err := routd.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			wantRecords := 0
			if action == "group" {
				wantRecords = 1
			}
			records, err := db.InstalledPackages()
			if err != nil || len(records) != wantRecords {
				t.Fatalf("fresh seed receipts: %+v, %v", records, err)
			}
			if action == "group" && (records[0].Name != "core" || records[0].Folder != "") {
				t.Fatalf("fresh group recorded optional products: %+v", records)
			}
			mustWrite(t, filepath.Join(source, "skills", "shared", "SKILL.md"), "v2")
			if out, err := runManifestCLI(t, []string{"packages", instance, "sync"}); err != nil {
				t.Fatalf("sync without records: %v, %s", err, out)
			}
			if got := readGroupFile(t, dataDir, folder, ".claude/skills/shared/SKILL.md"); got != "v1" {
				t.Errorf("sync discovered an unrecorded group: %q", got)
			}
			if out, err := runManifestCLI(t, []string{"products", instance, "apply", folder}); err == nil || !strings.Contains(out, "conflicts") {
				t.Fatalf("copied skill accepted without adoption: %v, %s", err, out)
			}
			copyDir := filepath.Join(dataDir, "groups", folder, ".claude", "skills", "shared")
			if err := os.Remove(filepath.Join(copyDir, "SKILL.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(copyDir); err != nil {
				t.Fatal(err)
			}
			if out, err := runManifestCLI(t, []string{"products", instance, "apply", folder}); err != nil {
				t.Fatalf("initial explicit apply: %v, %s", err, out)
			}
			mustWrite(t, filepath.Join(source, "skills", "shared", "SKILL.md"), "v3")
			if out, err := runManifestCLI(t, []string{"packages", instance, "sync"}); err != nil {
				t.Fatalf("record-driven sync: %v, %s", err, out)
			}
			if got := selectedSkill(t, dataDir, folder, "sample", "shared"); got != "v3" {
				t.Errorf("sync did not update recorded group: %q", got)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != string(body) {
				t.Errorf("lifecycle changed group manifest: %q, %v", got, err)
			}
		})
	}
}

func TestManifestFilename(t *testing.T) {
	manifest := "name = \"sample\"\nbrand = \"Sample\"\ntagline = \"Support desk.\"\n" +
		"skills = [\"triage\"]\n[[env]]\nkey = \"SAMPLE_TOKEN\"\n" +
		"required = true\nhint = \"Token for the sample adapter\"\n" +
		"[[env]]\nkey = \"SAMPLE_LABEL\"\nrequired = false\nhint = \"Optional label\"\n"
	for _, action := range []string{"create", "group", "list", "apply"} {
		t.Run(action, func(t *testing.T) {
			for _, state := range []string{"new", "both", "old-only", "missing", "malformed", "nameless", "nested"} {
				t.Run(state, func(t *testing.T) {
					dataDir, base := mixInstance(t, nil, nil)
					t.Setenv("HOST_APP_DIR", base)
					source := filepath.Join(base, "ant", "examples", "sample")
					if err := os.MkdirAll(source, 0o755); err != nil {
						t.Fatal(err)
					}
					files := map[string]string{"PERSONA.md": "Sample persona.\n"}
					switch state {
					case "new":
						files["arizuko.toml"] = manifest
					case "both":
						files["arizuko.toml"] = manifest
						files["PRODUCT.md"] = "name = \"legacy\"\n"
					case "old-only":
						files["PRODUCT.md"] = manifest
					case "malformed":
						files["arizuko.toml"] = "name = [\n"
						files["PRODUCT.md"] = manifest
					case "nameless":
						files["arizuko.toml"] = "brand = \"Sample\"\n"
					case "nested":
						files["arizuko.toml"] = manifest + "\n[[product]]\nsource = \"nested\"\n"
					}
					for name, body := range files {
						if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0o644); err != nil {
							t.Fatal(err)
						}
					}
					mix := "[[product]]\nsource = \"ant/examples/sample\"\n"
					mixPath := filepath.Join(dataDir, "groups", "main", "arizuko.toml")
					if err := os.WriteFile(mixPath, []byte(mix), 0o644); err != nil {
						t.Fatal(err)
					}
					args := []string{"products", "t", action, "main"}
					groupDir := filepath.Join(dataDir, "groups", "main")
					switch action {
					case "create":
						args = []string{"create", "fresh", "--product", "sample"}
						unsealPackageFixtures(t, filepath.Join(base, "arizuko_fresh"))
						t.Setenv("DATA_DIR", filepath.Join(base, "arizuko_fresh"))
						groupDir = filepath.Join(base, "arizuko_fresh", "groups", "main")
					case "group":
						args = []string{"group", "t", "add", "web:sample", "sample", "--product", "sample"}
						groupDir = filepath.Join(dataDir, "groups", "sample")
					}
					out, err := runManifestCLI(t, args)
					success := state == "new" || state == "both" ||
						(action == "create" || action == "group") && state == "nameless"
					if action == "list" && state != "nameless" {
						if err != nil {
							t.Fatalf("list: %v", err)
						}
						if success {
							for _, want := range []string{"sample", "Sample", "Support desk."} {
								if !strings.Contains(out, want) {
									t.Errorf("catalog missing %q: %s", want, out)
								}
							}
						} else if !strings.Contains(out, "sample") || !strings.Contains(out, "no readable arizuko.toml") {
							t.Fatalf("catalog did not report the manifest: %s", out)
						}
						return
					}
					if !success {
						if err == nil || !strings.Contains(out, "Failed:") {
							t.Fatalf("invalid manifest accepted: %s", out)
						}
						if action != "create" || state == "malformed" {
							if !strings.Contains(out, "arizuko.toml") {
								t.Errorf("refusal omitted the manifest filename: %s", out)
							}
						}
						if _, err := os.Stat(filepath.Join(groupDir, "PERSONA.md")); !os.IsNotExist(err) {
							t.Errorf("refusal seeded the persona: %v", err)
						}
						return
					}
					if err != nil {
						t.Fatalf("valid manifest refused: %v", err)
					}
					got, err := os.ReadFile(filepath.Join(groupDir, "arizuko.toml"))
					if err != nil {
						t.Fatal(err)
					}
					if action == "apply" {
						if string(got) != mix {
							t.Errorf("group manifest changed: %q", got)
						}
					} else if !strings.Contains(string(got), "source = \"ant/examples/sample\"") ||
						strings.Contains(string(got), "name =") || strings.Contains(string(got), "SAMPLE_TOKEN") {
						t.Errorf("seed did not declare only the selected source: %q", got)
					}
					if action == "create" && state != "nameless" {
						for _, want := range []string{"SAMPLE_TOKEN", "(required)", "Token for the sample adapter",
							"SAMPLE_LABEL", "(optional)", "Optional label", "skills: triage"} {
							if !strings.Contains(out, want) {
								t.Errorf("create omitted %q: %s", want, out)
							}
						}
					}
					if action == "group" || action == "apply" {
						db, err := routd.Open()
						if err != nil {
							t.Fatal(err)
						}
						defer db.Close()
						if action == "group" {
							if _, ok := db.AllGroups()["sample"]; !ok {
								t.Error("group row missing")
							}
						} else {
							rec, ok, err := db.InstalledPackage("main", "sample")
							if err != nil || !ok || rec.Source != "ant/examples/sample" {
								t.Errorf("installed record = %+v, ok=%v, err=%v", rec, ok, err)
							}
						}
					}
					if got, err := os.ReadFile(mixPath); err != nil || string(got) != mix {
						t.Errorf("arizuko.toml changed: %q, err=%v", got, err)
					}
				})
			}
		})
	}
}
