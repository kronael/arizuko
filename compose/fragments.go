package compose

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

// Installed fragments used to be COPIES of `template/services/*.yml`, refreshed by
// nothing, so a template edit reached NEW installs only — which is how the E1
// adapter-state mounts stayed absent on every running instance (BUGS R1). RelinkCatalog
// replaces the copy with a symlink at the catalog, so an edit reaches every instance on
// the next generate.
//
// Fragments are matched to the catalog by SERVICE KIND, not filename: a multi-account
// variant `teled-rhias.yml` maps to the `teled` template but is never linked — it
// carries its own container_name/env, and linking it would collide with the base.

// HostCatalogDir returns the HOST-resolvable catalog dir from
// <dataDir>/.env's HOST_APP_DIR — the value a symlink target must use,
// because `docker compose` always resolves `include:` on the bare host,
// never inside the ephemeral container `arizuko generate` may itself run in
// (the deploy systemd unit's ExecStartPre runs `docker run --rm
// arizuko:latest arizuko generate <inst>`, which mounts only the instance
// data dir — not the checkout). False when HOST_APP_DIR is not set in .env:
// unlike packageTemplates (which may fall back to this process's own
// directory purely to read catalog bytes for comparison), a symlink target
// is never derived from where this process happens to run (CLAUDE.md
// "identity is configured, never derived").
func HostCatalogDir(dataDir string) (string, bool) {
	env, _ := godotenv.Read(filepath.Join(dataDir, ".env"))
	host := env["HOST_APP_DIR"]
	if host == "" {
		return "", false
	}
	return filepath.Join(host, "template", "services"), true
}

// SymlinkFragment atomically points servicesDir/filename at
// hostCatalogDir/filename, replacing any existing file or symlink there.
// Symlink-then-rename, so a mid-failure never leaves the fragment missing
// (the previous file/symlink stays until the rename succeeds).
func SymlinkFragment(servicesDir, hostCatalogDir, filename string) error {
	target := filepath.Join(hostCatalogDir, filename)
	dst := filepath.Join(servicesDir, filename)
	tmp := dst + ".symlink-tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return fmt.Errorf("symlink %s: %w", filename, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("point %s at the catalog: %w", filename, err)
	}
	return nil
}

// RelinkCatalog converts every installed fragment in servicesDir that is a
// byte-identical copy of its catalog template into a symlink at
// hostCatalogDir, idempotently. readableCatalogDir is used only to READ
// template bytes for the comparison — packageTemplates' resolution, which
// works from inside the ephemeral generate container too, via the image's
// baked-in /opt/arizuko/template/services. hostCatalogDir is the string
// written into the symlink (see HostCatalogDir). A fragment is left
// untouched — never linked — when:
//   - its filename encodes a multi-account variant (<kind>-<label>.yml):
//     linking would discard the operator's own container_name/env;
//   - its content differs from the catalog template by even a byte (a
//     rewritten comment counts — it is still the operator's edit): kept as a
//     real file permanently, never auto-touched again;
//   - no catalog template matches its kind: an operator-local fragment.
//
// Byte-exact, not a semantic diff: `packages add` writes the catalog fragment
// verbatim, so anything else on disk means the operator (or a stale catalog
// version) put it there, and converting is destructive either way.
//
// Idempotent WITHOUT reading through an already-linked fragment: hostCatalogDir
// need not be readable from here at all (the ephemeral container `arizuko
// generate` may run in cannot see the host checkout it just linked to — see
// HostCatalogDir), so a fragment that is already a symlink is left exactly as
// is on every later run, never re-read, never re-compared.
// diverged names every fragment kept as a real file because its bytes differ
// from the catalog, and carries the catalog lines that never reached it. That
// is indistinguishable here from an operator edit, and it is also what a
// fragment written by an OLDER generator looks like — so the instance silently
// keeps running a frozen copy. On krons that hid three live faults at once:
// whapd's dedup mount (added with BUGS L32) never arrived and the adapter
// crash-looped, teled's state mount (F53) and typing timeout (T1) were absent,
// and ttsd/kokoro still loaded the whole instance .env instead of their scoped
// env files. The caller must say so; converting automatically would overwrite
// real operator edits and needs sign-off (BUGS L41).
func RelinkCatalog(servicesDir, readableCatalogDir, hostCatalogDir string) (relinked []string, diverged []Diverged, err error) {
	names, err := readFragments(servicesDir)
	if err != nil {
		return nil, nil, err
	}
	for _, name := range names {
		if isSymlink(filepath.Join(servicesDir, name+".yml")) {
			// Linked by this or an earlier run. The sidecar is still checked:
			// a package linked before its manifest shipped has none, and the
			// catalog's routes and env needs must reach it too.
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

// Diverged is an installed fragment kept as a real file because its bytes
// differ from the catalog, with the catalog lines it does not carry.
//
// Missing is what turns the report into a work item: on krons the divergence
// was three specific absent lines — whapd's dedup mount, teled's state mount
// and ttsd's scoped env file — and each one was a live fault (BUGS L41).
type Diverged struct {
	Name string

	// Missing holds the catalog's own lines, verbatim, that the installed copy
	// does not carry. Comments and blank lines are left out, and quoting and
	// indentation are normalized before comparing, so a fragment that differs
	// from the catalog only in YAML style reports nothing.
	Missing []string
}

// unquoteFragment strips YAML's interchangeable quote styles for comparison.
var unquoteFragment = strings.NewReplacer("'", "", `"`, "")

// missingLines returns the catalog's own lines, verbatim, that installed does
// not carry — the mounts and env vars a frozen fragment withholds.
//
// The comparison is on a normalized key, quotes stripped and whitespace
// collapsed: a generator that writes `KEY: "v"` where the catalog writes
// `KEY: 'v'` differs on nearly every line of a fragment while withholding
// nothing, and sloth's frozen teled.yml is exactly that plus three real
// absences. Comments and blank lines never carry a mount, so they are left out.
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
// catalog-linked base fragment: the same drift risk applies to a package's
// declared rows and env needs as to its compose fragment. The instance's copy
// is linked when it matches the catalog and created when it is absent (the
// base fragment being linked is the operator's acceptance of the catalog
// version of this package). Silently skipped (not an error) when the catalog
// ships none, the installed one has diverged, or it is already linked.
func relinkSidecar(servicesDir, readableCatalogDir, hostCatalogDir, name string) error {
	sidecar := name + ".yaml"
	if isSymlink(filepath.Join(servicesDir, sidecar)) {
		return nil
	}
	tmpl, terr := os.ReadFile(filepath.Join(readableCatalogDir, sidecar))
	if terr != nil {
		return nil // catalog ships no manifest for this kind
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

// catalogTemplate resolves an installed fragment name to its catalog
// template. `teled` matches `teled.yml`; the multi-account variant
// `teled-rhias` falls back to the base kind's template, the same
// `<kind>-<label>` split envFileName uses to share one env file across
// accounts.
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
