package compose

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// Manifest is the generate-time view of a package's arizuko.yml: the parts
// that must exist before any daemon or DB does. Routes go to proxyd_routes;
// Env is what the package's process needs from the instance .env.
type Manifest struct {
	Routes []ProxydRoute
	Env    map[string]resreg.EnvNeed
}

// ReadManifest uses the same strict declaration boundary as package install.
func ReadManifest(path string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(path)
	if err != nil {
		return m, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	declaration, err := resreg.ParseDeclaration(b)
	if err != nil {
		return m, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	m.Env = declaration.Env
	for _, doc := range declaration.Documents {
		routes, ok := doc.Rows[resources.ProxydRoutesName].([]resources.ProxydRoutesRow)
		if !ok {
			continue
		}
		for _, r := range routes {
			m.Routes = append(m.Routes, ProxydRoute{
				Path:            r.Path,
				Backend:         r.Backend,
				Auth:            r.Auth,
				GatedBy:         r.GatedBy,
				PreserveHeaders: r.PreserveHeaders,
				StripPrefix:     r.StripPrefix,
				RedirectTo:      r.RedirectTo,
			})
		}
	}
	return m, nil
}

// ManifestRoutes returns the proxyd_routes block of one package manifest.
func ManifestRoutes(path string) ([]ProxydRoute, error) {
	m, err := ReadManifest(path)
	return m.Routes, err
}

// CatalogDir returns the package catalog this process can READ: the checkout
// named by HOST_APP_DIR when visible from here, else the copy baked into the
// image. Never the process's own directory.
func CatalogDir(hostAppDir string) string {
	if hostAppDir != "" {
		if dir := filepath.Join(hostAppDir, "template", "services"); isDir(dir) {
			return dir
		}
	}
	return filepath.Join(bakedAppSrc, "template", "services")
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func manifestPath(servicesDir, catalogDir, name string) string {
	local := filepath.Join(filepath.Dir(servicesDir), "packages", name, "arizuko.yml")
	if isSymlink(filepath.Join(servicesDir, name+".yml")) {
		return filepath.Join(catalogDir, name, "arizuko.yml")
	}
	return local
}

// ProvisionPackageEnv resolves one package's `env` declarations against the
// instance .env. A key the operator set wins; `generate` mints a token once
// and appends it to .env, where it is the operator's from then on; `default`
// fills env for this run only; `required` refuses, naming the key and the
// file. env is updated in place so every later reader of the run sees the
// resolved values. Returns the keys minted this run.
func ProvisionPackageEnv(dataDir, manifestPath, name string, env map[string]string) ([]string, error) {
	m, err := ReadManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	minted := map[string]string{}
	for _, key := range slices.Sorted(maps.Keys(m.Env)) {
		if env[key] != "" {
			continue
		}
		need := m.Env[key]
		switch {
		case need.Required:
			return nil, fmt.Errorf("%s needs %s — set it in %s, then re-run",
				name, key, filepath.Join(dataDir, ".env"))
		case need.Generate:
			minted[key] = core.GenHexToken()
			env[key] = minted[key]
		default:
			env[key] = need.Default
		}
	}
	if len(minted) == 0 {
		return nil, nil
	}
	if err := appendEnvKeys(dataDir, minted); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return slices.Sorted(maps.Keys(minted)), nil
}

// appendEnvKeys adds KEY=VALUE lines above the compose-managed block of .env.
// A key already present — even empty — is refused rather than rewritten: an
// operator line is never edited by the platform.
func appendEnvKeys(dataDir string, kv map[string]string) error {
	path := filepath.Join(dataDir, ".env")
	prev, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(prev), "\n"), "\n")
	if len(prev) == 0 {
		lines = nil
	}
	for _, line := range lines {
		k, _, ok := strings.Cut(line, "=")
		if ok {
			if _, mine := kv[strings.TrimSpace(k)]; mine {
				return fmt.Errorf("%s is present but empty in %s — set it or delete the line", k, path)
			}
		}
	}
	var added []string
	for _, k := range slices.Sorted(maps.Keys(kv)) {
		added = append(added, k+"="+kv[k])
	}
	at := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, managedBegin) })
	if at == -1 {
		at = len(lines)
	}
	lines = slices.Insert(lines, at, added...)
	return writeEnvAtomic(dataDir, path, strings.Join(lines, "\n")+"\n")
}

// writeEnvAtomic writes one of the instance's env files via temp file plus
// rename, 0600: both hold secrets.
func writeEnvAtomic(dataDir, path, body string) error {
	tmp, err := os.CreateTemp(dataDir, ".env.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// bundledName returns name when the release's own catalog ships a manifest for
// it, else "" — the test ReservedRouteClaim's `/dash/` exception rests on:
// nothing a package installs can write into the catalog.
func bundledName(catalogDir, name string) string {
	if catalogDir == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Join(catalogDir, name, "arizuko.yml")); err != nil {
		return ""
	}
	return name
}
