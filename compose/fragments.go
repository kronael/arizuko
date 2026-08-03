package compose

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

// Installed fragments used to be COPIES of `template/services/*.yml` —
// `packages add` copied once and nothing ever refreshed them, so a template
// edit reached NEW installs only. That is how the E1 adapter-state mounts
// stayed absent on every running instance while the template already carried
// them (BUGS R1). RelinkCatalog replaces the copy with a symlink at the
// catalog, so an edit reaches every instance on the next generate — there is
// no copy left to fall behind.
//
// Fragments are matched to the catalog by SERVICE KIND, not filename: a
// multi-account variant `teled-rhias.yml` maps to the `teled` template
// (envFileName uses the same `<kind>-<label>` split) but is never linked — it
// carries its own container_name/env, and linking it would collide with the
// base service.

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
func RelinkCatalog(servicesDir, readableCatalogDir, hostCatalogDir string) (relinked []string, err error) {
	names, err := readFragments(servicesDir)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if isSymlink(filepath.Join(servicesDir, name+".yml")) {
			continue // already linked, by this or an earlier run
		}
		kind, tmpl, ok := catalogTemplate(readableCatalogDir, name)
		if !ok || name != kind {
			continue // no catalog match, or a variant — never touched
		}
		installed, rerr := os.ReadFile(filepath.Join(servicesDir, name+".yml"))
		if rerr != nil {
			return relinked, fmt.Errorf("read services/%s.yml: %w", name, rerr)
		}
		if !bytes.Equal(installed, tmpl) {
			continue // diverged from the catalog — a hand edit, keep it real
		}
		if err := SymlinkFragment(servicesDir, hostCatalogDir, name+".yml"); err != nil {
			return relinked, err
		}
		relinked = append(relinked, name)
		if err := relinkSidecarIfIdentical(servicesDir, readableCatalogDir, hostCatalogDir, name); err != nil {
			return relinked, err
		}
	}
	return relinked, nil
}

// isSymlink reports whether path exists and is a symlink. False (not an
// error) when path is missing, a regular file, or unreadable.
func isSymlink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// relinkSidecarIfIdentical links <name>-routes.json alongside its base
// fragment when both the catalog and the instance carry one and they match —
// the same drift risk applies to a package's route table as to its compose
// fragment. Silently skipped (not an error) when either side has no sidecar,
// the installed one has diverged, or it is already linked.
func relinkSidecarIfIdentical(servicesDir, readableCatalogDir, hostCatalogDir, name string) error {
	sidecar := name + "-routes.json"
	if isSymlink(filepath.Join(servicesDir, sidecar)) {
		return nil
	}
	tmpl, terr := os.ReadFile(filepath.Join(readableCatalogDir, sidecar))
	if terr != nil {
		return nil // catalog ships no routes sidecar for this kind
	}
	installed, ierr := os.ReadFile(filepath.Join(servicesDir, sidecar))
	if ierr != nil {
		return nil // instance never installed the sidecar
	}
	if !bytes.Equal(installed, tmpl) {
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
