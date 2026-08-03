package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// templateServices is the shipped fragment catalog — the files `arizuko package
// add` copies into <dataDir>/services, which docker includes verbatim.
const templateServices = "../template/services"

// fragment is the slice of a service fragment these tests assert on.
type fragment struct {
	Services map[string]struct {
		Volumes     []string       `yaml:"volumes"`
		Environment map[string]any `yaml:"environment"`
		EnvFile     []string       `yaml:"env_file"`
	} `yaml:"services"`
}

func readFragment(t *testing.T, name string) fragment {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(templateServices, name+".yml"))
	if err != nil {
		t.Fatalf("read fragment: %v", err)
	}
	var f fragment
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatalf("parse %s.yml: %v", name, err)
	}
	return f
}

// mountFor returns the host side of the volume bound at the container path
// target ("<host>:<container>[:mode]").
func mountFor(volumes []string, target string) (string, bool) {
	for _, v := range volumes {
		parts := strings.Split(v, ":")
		if len(parts) >= 2 && parts[1] == target {
			return parts[0], true
		}
	}
	return "", false
}

// Adapter state must live on a bind mount, and that mount must be the narrow
// subpath the daemon actually needs. teled/bskyd/reditd/linkd/emaid shipped with
// NO volumes at all (BUGS E1), so every container recreate wiped the Telegram
// offset, the Bluesky session, the Reddit cursors, the LinkedIn refresh token
// and emaid's whole SQLite DB — teled then re-answered Telegram's ~24h backlog.
func TestStatefulFragmentsMountTheirStateDir(t *testing.T) {
	for _, name := range []string{"bskyd", "emaid", "linkd", "reditd", "teled"} {
		svc := readFragment(t, name).Services[name]
		dataDir, _ := svc.Environment["DATA_DIR"].(string)
		if dataDir == "" {
			t.Errorf("%s: no DATA_DIR — state falls to the daemon's code default, which nothing mounts", name)
			continue
		}
		host, ok := mountFor(svc.Volumes, dataDir)
		if !ok {
			t.Errorf("%s: DATA_DIR %q is not a mount target (volumes %v) — state dies on recreate", name, dataDir, svc.Volumes)
			continue
		}
		if host == "${DATA_DIR}" {
			t.Errorf("%s: mounts the whole data dir; mount only the state subpath", name)
		}
	}
}

// seedCatalog writes a catalog dir and an instance services/ dir, returning both.
func seedCatalog(t *testing.T, catalog, installed map[string]string) (svcDir, tmplDir string) {
	t.Helper()
	dir := t.TempDir()
	tmplDir, svcDir = filepath.Join(dir, "catalog"), filepath.Join(dir, "services")
	for target, files := range map[string]map[string]string{tmplDir: catalog, svcDir: installed} {
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(target, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return svcDir, tmplDir
}

const teledV2 = "# teled\nservices:\n  teled:\n    image: arizuko:latest\n" +
	"    volumes:\n      - '${DATA_DIR}/store/teled:/srv/app/home/store/teled'\n"

// isSymlinkTo asserts path is a symlink pointing at want.
func isSymlinkTo(t *testing.T, path, want string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink", path)
	}
	got, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("readlink %s: %v", path, err)
	}
	if got != want {
		t.Fatalf("%s -> %q, want %q", path, got, want)
	}
}

// isRealFile asserts path is a regular file (never converted to a symlink).
func isRealFile(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("%s was converted to a symlink, want a real file", path)
	}
}

// R1: `services/*.yml` used to be COPIES of the catalog, so a shipped fragment
// fix (the E1 adapter state mounts) reached new installs only. RelinkCatalog
// replaces an identical copy with a symlink at the catalog — a
// `<kind>-<label>.yml` multi-account variant, a diverged hand edit, and an
// uncatalogued local fragment are all left as real files, never linked.
func TestRelinkCatalogClassifies(t *testing.T) {
	svcDir, tmplDir := seedCatalog(t,
		map[string]string{"teled.yml": teledV2},
		map[string]string{
			// byte-identical copy — safe to link
			"teled.yml": teledV2,
			// operator's own package, not ours
			"mine.yml": "services:\n  mine:\n    image: y\n",
		})
	hostDir := filepath.Join(t.TempDir(), "hostview", "template", "services")

	relinked, err := RelinkCatalog(svcDir, tmplDir, hostDir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(relinked, []string{"teled"}) {
		t.Fatalf("relinked = %v, want [teled]", relinked)
	}
	isSymlinkTo(t, filepath.Join(svcDir, "teled.yml"), filepath.Join(hostDir, "teled.yml"))
	isRealFile(t, filepath.Join(svcDir, "mine.yml"))
}

// A multi-account variant must never be linked: linking it would collide the
// container_name with the base service.
func TestRelinkCatalogSparesVariant(t *testing.T) {
	svcDir, tmplDir := seedCatalog(t,
		map[string]string{"teled.yml": teledV2},
		map[string]string{
			"teled.yml":       teledV2,
			"teled-rhias.yml": "services:\n  teled-rhias:\n    image: arizuko:latest\n    environment:\n      CHANNEL_SECRET: 'x'\n",
		})
	hostDir := filepath.Join(t.TempDir(), "hostview", "template", "services")

	if _, err := RelinkCatalog(svcDir, tmplDir, hostDir); err != nil {
		t.Fatal(err)
	}
	isRealFile(t, filepath.Join(svcDir, "teled-rhias.yml"))
	got, err := os.ReadFile(filepath.Join(svcDir, "teled-rhias.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "CHANNEL_SECRET") {
		t.Error("variant content was altered")
	}
}

// A plain `<kind>.yml` copy that has diverged from the catalog (a hand edit)
// must never be silently overwritten or replaced with a symlink — that would
// discard the operator's edit.
func TestRelinkCatalogSparesHandEdit(t *testing.T) {
	edited := "services:\n  teled:\n    image: arizuko:latest\n    ports: ['9999:9999']\n"
	svcDir, tmplDir := seedCatalog(t,
		map[string]string{"teled.yml": teledV2},
		map[string]string{"teled.yml": edited})
	hostDir := filepath.Join(t.TempDir(), "hostview", "template", "services")

	relinked, err := RelinkCatalog(svcDir, tmplDir, hostDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(relinked) != 0 {
		t.Fatalf("relinked = %v, want none (diverged copy)", relinked)
	}
	isRealFile(t, filepath.Join(svcDir, "teled.yml"))
	got, err := os.ReadFile(filepath.Join(svcDir, "teled.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != edited {
		t.Error("hand-edited fragment was altered")
	}
}

// A second relink must not even attempt to read through the fragment: on a
// real deploy, hostCatalogDir is a HOST path the ephemeral generate container
// cannot see (that's the whole reason it differs from readableCatalogDir), so
// re-reading an already-linked fragment there would fail loudly on every
// later generate. The already-linked case must be a pure no-op instead.
func TestRelinkCatalogIdempotent(t *testing.T) {
	svcDir, tmplDir := seedCatalog(t,
		map[string]string{"teled.yml": teledV2},
		map[string]string{"teled.yml": teledV2})
	// hostDir deliberately points nowhere readable — mirrors the ephemeral
	// generate container's view of a host-only catalog path.
	hostDir := filepath.Join(t.TempDir(), "unreadable-from-here", "template", "services")

	if _, err := RelinkCatalog(svcDir, tmplDir, hostDir); err != nil {
		t.Fatal(err)
	}
	relinked, err := RelinkCatalog(svcDir, tmplDir, hostDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(relinked) != 0 {
		t.Fatalf("second relink = %v, want none (already linked, nothing new)", relinked)
	}
	isSymlinkTo(t, filepath.Join(svcDir, "teled.yml"), filepath.Join(hostDir, "teled.yml"))
}

// A route-table sidecar carries the same drift risk as its base fragment and
// links alongside it when both sides match.
func TestRelinkCatalogLinksMatchingSidecar(t *testing.T) {
	routes := `[{"path":"/slack/","backend":"http://slakd:8080","auth":"public"}]`
	svcDir, tmplDir := seedCatalog(t,
		map[string]string{"slakd.yml": "services:\n  slakd:\n    image: arizuko:latest\n", "slakd-routes.json": routes},
		map[string]string{"slakd.yml": "services:\n  slakd:\n    image: arizuko:latest\n", "slakd-routes.json": routes})
	hostDir := filepath.Join(t.TempDir(), "hostview", "template", "services")

	if _, err := RelinkCatalog(svcDir, tmplDir, hostDir); err != nil {
		t.Fatal(err)
	}
	isSymlinkTo(t, filepath.Join(svcDir, "slakd-routes.json"), filepath.Join(hostDir, "slakd-routes.json"))
}

// HostCatalogDir must never fall back to this process's own directory — a
// symlink target is configured (.env HOST_APP_DIR) or absent, never derived.
func TestHostCatalogDirRequiresExplicitHostAppDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("ASSISTANT_NAME=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := HostCatalogDir(dir); ok {
		t.Fatal("HostCatalogDir returned ok with no HOST_APP_DIR set")
	}

	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("HOST_APP_DIR=/srv/checkout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := HostCatalogDir(dir)
	if !ok || got != "/srv/checkout/template/services" {
		t.Fatalf("HostCatalogDir = %q,%v, want /srv/checkout/template/services,true", got, ok)
	}
}

// E2: no fragment may read the shared `.env`. ttsd and kokoro did, and that file
// holds SECRETS_KEY, AUTH_SECRET, GITHUB_CLIENT_SECRET, CLAUDE_CODE_OAUTH_TOKEN
// and every bot token — kokoro being the third-party ghcr.io/remsky image.
func TestNoFragmentReadsTheSharedEnv(t *testing.T) {
	entries, err := os.ReadDir(templateServices)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		for svcName, svc := range readFragment(t, strings.TrimSuffix(e.Name(), ".yml")).Services {
			for _, f := range svc.EnvFile {
				if !strings.HasPrefix(f, "../env/") {
					t.Errorf("%s: %s reads %q; scope it to ../env/<service>.env", e.Name(), svcName, f)
				}
			}
		}
	}
}

// Every env_file a fragment names must actually be generated, or `docker compose
// up` fails on the missing file.
func TestFragmentEnvFilesAreGenerated(t *testing.T) {
	entries, err := os.ReadDir(templateServices)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		for svcName, svc := range readFragment(t, strings.TrimSuffix(e.Name(), ".yml")).Services {
			for _, f := range svc.EnvFile {
				daemon := strings.TrimSuffix(strings.TrimPrefix(f, "../env/"), ".env")
				if _, ok := daemonKeys[daemon]; !ok {
					t.Errorf("%s: %s reads env/%s.env, which writeEnvFiles never emits", e.Name(), svcName, daemon)
				}
			}
		}
	}
}

// A third-party image gets its declared keys only: commonKeys is what every
// arizuko daemon reads, and it includes OTEL_EXPORTER_OTLP_HEADERS (the
// collector's auth token) plus the host's filesystem layout.
func TestForeignImageEnvCarriesNoCommonKeys(t *testing.T) {
	dir := seed(t, "TZ=Europe/Prague\nOTEL_EXPORTER_OTLP_HEADERS=authorization=Bearer sk-collector\nHOST_DATA_DIR=/srv/data/x\n")
	gen(t, dir)

	got := read(t, dir, "env/kokoro.env")
	if !strings.Contains(got, "TZ=Europe/Prague") {
		t.Errorf("env/kokoro.env dropped its one declared key:\n%s", got)
	}
	for _, leaked := range []string{"OTEL_EXPORTER_OTLP_HEADERS", "HOST_DATA_DIR", "HOST_APP_DIR"} {
		if strings.Contains(got, leaked) {
			t.Errorf("env/kokoro.env leaks %s to the third-party image:\n%s", leaked, got)
		}
	}
}

// No fragment may mount the whole data dir. slakd did, while opening exactly one
// file in it (routd.db, for pane_sessions) — an adapter reachable from a public
// webhook does not need every daemon's database.
func TestNoFragmentMountsTheWholeDataDir(t *testing.T) {
	entries, err := os.ReadDir(templateServices)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yml")
		for svcName, svc := range readFragment(t, name).Services {
			for _, v := range svc.Volumes {
				if strings.HasPrefix(v, "${DATA_DIR}:") {
					t.Errorf("%s: %s mounts the whole data dir (%q)", e.Name(), svcName, v)
				}
			}
		}
	}
}
