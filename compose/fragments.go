package compose

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

// Installed fragments are symlinks at the catalog, so a template edit reaches
// every instance on the next generate; a COPY refreshed by nothing reached
// NEW installs only (BUGS R1). Fragments are matched to the catalog by
// SERVICE KIND, not filename: a multi-account variant `teled-rhias.yml` maps
// to the `teled` template but is never linked — it carries its own
// container_name/env, and linking it would collide with the base.

// HostCatalogDir returns the HOST-resolvable catalog dir from .env's
// HOST_APP_DIR — the value a symlink target must use, because `docker
// compose` resolves `include:` on the bare host, never inside the ephemeral
// container `arizuko generate` may run in. False when HOST_APP_DIR is unset:
// a symlink target is never derived from where this process happens to run.
func HostCatalogDir(dataDir string) (string, bool) {
	env, _ := godotenv.Read(filepath.Join(dataDir, ".env"))
	host := env["HOST_APP_DIR"]
	if host == "" {
		return "", false
	}
	return filepath.Join(host, "template", "services"), true
}

// SymlinkFragment atomically points servicesDir/filename at
// hostCatalogDir/filename: symlink-then-rename, so a mid-failure never leaves
// the fragment missing.
func SymlinkFragment(servicesDir, hostCatalogDir, filename string) error {
	target := filepath.Join(hostCatalogDir, filename)
	dst := filepath.Join(servicesDir, filename)
	tmp := dst + ".symlink-tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return fmt.Errorf("symlink %s: %w", filename, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("point %s at the catalog: %w", filename, err)
	}
	return nil
}

// RelinkCatalog converts every installed fragment that is a byte-identical
// copy of its catalog template into a symlink at hostCatalogDir.
// readableCatalogDir is only READ for the comparison; hostCatalogDir is the
// string written into the link. Never linked: a multi-account variant, a
// fragment differing by even a byte (a rewritten comment is still the
// operator's edit), or one with no matching template. An already-linked
// fragment is never re-read: the host checkout may be unreadable from here.
// diverged names every fragment kept as a real file: indistinguishable from
// an operator edit, and also what an OLDER generator's output looks like, so
// the instance silently runs a frozen copy (BUGS L41); the caller must say so.
func RelinkCatalog(servicesDir, readableCatalogDir, hostCatalogDir string) (relinked []string, diverged []Diverged, err error) {
	names, err := readFragments(servicesDir)
	if err != nil {
		return nil, nil, err
	}
	for _, name := range names {
		if isSymlink(filepath.Join(servicesDir, name+".yml")) {
			// The sidecar is still checked: a package linked before its manifest
			// shipped has none.
			if err := relinkSidecar(servicesDir, readableCatalogDir, hostCatalogDir, name); err != nil {
				return relinked, diverged, err
			}
			continue
		}
		kind, tmpl, ok := catalogTemplate(readableCatalogDir, name)
		if !ok || name != kind {
			continue // no catalog match, or a variant — never touched
		}
		installed, rerr := os.ReadFile(filepath.Join(servicesDir, name+".yml"))
		if rerr != nil {
			return relinked, diverged, fmt.Errorf("read services/%s.yml: %w", name, rerr)
		}
		if !bytes.Equal(installed, tmpl) {
			// A hand edit or an older generator.
			diverged = append(diverged, Diverged{Name: name, Missing: missingLines(installed, tmpl)})
			continue
		}
		if err := SymlinkFragment(servicesDir, hostCatalogDir, name+".yml"); err != nil {
			return relinked, diverged, err
		}
		relinked = append(relinked, name)
		if err := relinkSidecar(servicesDir, readableCatalogDir, hostCatalogDir, name); err != nil {
			return relinked, diverged, err
		}
	}
	return relinked, diverged, nil
}

// Diverged is one fragment RelinkCatalog kept as a real file, with the catalog
// lines it does not carry — what turns the report into a work item (BUGS L41).
type Diverged struct {
	Name string

	// Missing holds the catalog's own lines, verbatim.
	Missing []string
}

// unquoteFragment strips YAML's interchangeable quote styles for comparison.
var unquoteFragment = strings.NewReplacer("'", "", `"`, "")

// missingLines returns the catalog's own lines that installed does not carry.
// The comparison is on fragmentKey, not the raw line: `KEY: "v"` against
// `KEY: 'v'` differs on nearly every line while withholding nothing.
func missingLines(installed, tmpl []byte) []string {
	have := make(map[string]bool)
	for _, line := range strings.Split(string(installed), "\n") {
		have[fragmentKey(line)] = true
	}
	var out []string
	for _, line := range strings.Split(string(tmpl), "\n") {
		key := fragmentKey(line)
		if key == "" || have[key] {
			continue
		}
		out = append(out, strings.TrimSpace(line))
	}
	return out
}

// fragmentKey normalizes a fragment line for comparison. It returns "" for a
// line that can withhold nothing: blank, or comment-only.
func fragmentKey(line string) string {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return ""
	}
	return strings.Join(strings.Fields(unquoteFragment.Replace(s)), " ")
}

// isSymlink reports whether path exists and is a symlink. False (not an
// error) when path is missing, a regular file, or unreadable.
func isSymlink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// relinkSidecar links <name>.yaml — the package's manifest — alongside a
// catalog-linked base fragment: the same drift risk applies to its rows and
// env needs. Linked when it matches the catalog, created when absent (the
// linked base fragment is the operator's acceptance of the catalog version);
// skipped when the catalog ships none, the installed one diverged, or it is
// already linked.
func relinkSidecar(servicesDir, readableCatalogDir, hostCatalogDir, name string) error {
	sidecar := name + ".yaml"
	if isSymlink(filepath.Join(servicesDir, sidecar)) {
		return nil
	}
	tmpl, terr := os.ReadFile(filepath.Join(readableCatalogDir, sidecar))
	if os.IsNotExist(terr) {
		return nil // catalog ships no manifest for this kind
	}
	if terr != nil {
		return fmt.Errorf("read catalog/%s: %w", sidecar, terr)
	}
	installed, ierr := os.ReadFile(filepath.Join(servicesDir, sidecar))
	if ierr != nil && !os.IsNotExist(ierr) {
		return fmt.Errorf("read services/%s: %w", sidecar, ierr)
	}
	if ierr == nil && !bytes.Equal(installed, tmpl) {
		return nil // hand-edited — keep it real
	}
	return SymlinkFragment(servicesDir, hostCatalogDir, sidecar)
}

// catalogTemplate resolves an installed fragment name to its catalog template;
// a multi-account variant falls back to the base kind, the same `<kind>-<label>`
// split envFileName uses.
func catalogTemplate(tmplDir, name string) (kind string, body []byte, ok bool) {
	for _, k := range candidateKinds(name) {
		b, err := os.ReadFile(filepath.Join(tmplDir, k+".yml"))
		if err == nil {
			return k, b, true
		}
	}
	return "", nil, false
}

func candidateKinds(name string) []string {
	if base, _, found := strings.Cut(name, "-"); found {
		return []string{name, base}
	}
	return []string{name}
}
