// Package components gates the orthogonality rule the shippable siblings are
// built on: egress/, substrate/ and mcpfw/ live in arizuko's single go.mod but
// are meant to be usable outside it, so the boundary is enforced by the import
// graph rather than by module separation (root CLAUDE.md, "Canonical paths").
//
// The invariant breaks silently without a test: one convenient
// `import "arizuko/core"` for a config struct, and the component stops
// shipping alone without a single failure.
package components

import (
	"os/exec"
	"strings"
	"testing"
)

const (
	modulePath = "github.com/kronael/arizuko/"
	// go list resolves ./... against its working directory, not the test binary's.
	root = "../.."
)

// shippable are the components that must stand alone. They may import each
// other — a component reads substrate's declaration and egress's allowlist
// matcher — but nothing else inside arizuko. sandbox/ is not in the set: the
// KVM substrate is runed's own unexported code, and qemubox is the standalone
// tool (spec 6/41-qemu-substrate).
var shippable = []string{"egress", "substrate", "mcpfw"}

func deps(t *testing.T, pattern string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", pattern)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pattern, err)
	}
	var internal []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(line, modulePath) {
			internal = append(internal, strings.TrimPrefix(line, modulePath))
		}
	}
	return internal
}

func TestShippableComponentsImportNoArizukoInternals(t *testing.T) {
	allowed := map[string]bool{}
	for _, c := range shippable {
		allowed[c] = true
	}
	for _, c := range shippable {
		for _, dep := range deps(t, "./"+c+"/...") {
			root := strings.SplitN(dep, "/", 2)[0]
			if !allowed[root] {
				t.Errorf("%s imports arizuko-internal %q — a shippable component may depend only on stdlib, third parties and %v",
					c, dep, shippable)
			}
		}
	}
}

// TestSubstrateIsALeaf: the capability declaration imports nothing of
// arizuko's, siblings included. Mechanism reads the declaration — container
// and sandbox import substrate — never the reverse: a declaration that
// imported egress or core could name an allowlist or a folder, and then a
// substrate could carry policy (spec 6/16, the component contract).
func TestSubstrateIsALeaf(t *testing.T) {
	for _, dep := range deps(t, "./substrate") {
		if dep != "substrate" {
			t.Errorf("substrate imports arizuko-internal %q — the declaration must be a leaf", dep)
		}
	}
}

// TestContainerImportsNoIPC: the spawn substrate does not link the agent tool
// surface. routd hosts the MCP socket and hands runed only the ipc dir to
// mount; a container package that imports ipc pulls every tool, store callback
// and connector into a process whose job is `docker run`.
func TestContainerImportsNoIPC(t *testing.T) {
	for _, dep := range deps(t, "./container") {
		if dep == "ipc" || strings.HasPrefix(dep, "ipc/") {
			t.Errorf("container imports arizuko-internal %q — the spawn substrate must not link the tool surface", dep)
		}
	}
}
