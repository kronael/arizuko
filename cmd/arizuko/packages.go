package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kronael/arizuko/core"
)

// packageTemplates returns the bundled catalog dir: the repo's
// template/services when HOST_APP_DIR points at a checkout, else the copy
// baked into arizuko:latest by the Dockerfile.
func packageTemplates(dataDir string) string {
	cfg, err := core.LoadConfigFrom(dataDir)
	if err == nil && cfg.HostAppDir != "" {
		if dir := filepath.Join(cfg.HostAppDir, "template", "services"); dirExists(dir) {
			return dir
		}
	}
	return "/opt/arizuko/template/services"
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// listFragments returns the `<name>.yml` service fragments in dir.
func listFragments(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yml") {
			names = append(names, strings.TrimSuffix(e.Name(), ".yml"))
		}
	}
	return names
}

// cmdPackages manages which adapter packages an instance runs: each is a
// compose fragment (plus optional `<name>-routes.json`) copied from the
// bundled catalog into <dataDir>/services/. `arizuko generate` includes every
// fragment it finds there.
func cmdPackages(args []string) {
	usage := "arizuko packages <instance> list | add <name> | remove <name>"
	need(args, 2, usage)
	dataDir := mustInstanceDir(args[0])
	tmplDir := packageTemplates(dataDir)
	svcDir := filepath.Join(dataDir, "services")

	switch args[1] {
	case "list":
		enabled := map[string]bool{}
		for _, n := range listFragments(svcDir) {
			enabled[n] = true
		}
		for _, n := range listFragments(tmplDir) {
			state := "available"
			if enabled[n] {
				state = "enabled"
			}
			fmt.Printf("%-12s %s\n", n, state)
			delete(enabled, n)
		}
		for n := range enabled {
			fmt.Printf("%-12s enabled (local)\n", n)
		}
	case "add":
		need(args, 3, usage)
		name := args[2]
		if err := os.MkdirAll(svcDir, 0o755); err != nil {
			die("Failed: mkdir services: %v", err)
		}
		if err := copyFile(filepath.Join(tmplDir, name+".yml"), filepath.Join(svcDir, name+".yml")); err != nil {
			die("Failed: add %s: %v (catalog: %s)", name, err, tmplDir)
		}
		// A package's proxyd routes travel with it — proxyd's route table is
		// assembled from these at generate time.
		if err := copyFile(filepath.Join(tmplDir, name+"-routes.json"), filepath.Join(svcDir, name+"-routes.json")); err != nil && !os.IsNotExist(err) {
			die("Failed: add %s routes: %v", name, err)
		}
		fmt.Printf("added %s — run `arizuko generate %s` to apply\n", name, args[0])
	case "remove":
		need(args, 3, usage)
		name := args[2]
		if err := os.Remove(filepath.Join(svcDir, name+".yml")); err != nil {
			die("Failed: remove %s: %v", name, err)
		}
		if err := os.Remove(filepath.Join(svcDir, name+"-routes.json")); err != nil && !os.IsNotExist(err) {
			die("Failed: remove %s routes: %v", name, err)
		}
		fmt.Printf("removed %s — run `arizuko generate %s` to apply\n", name, args[0])
	default:
		die("usage: " + usage)
	}
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
