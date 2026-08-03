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

// R1: `services/*.yml` are COPIES of the catalog, so a shipped fragment fix (the
// E1 adapter state mounts) reaches new installs only. Classification is what
// makes the drift visible: a plain `<kind>.yml` copy is replaceable, a
// `<kind>-<label>.yml` multi-account variant is the operator's and never is.
func TestPlanFragmentSyncClassifies(t *testing.T) {
	svcDir, tmplDir := seedCatalog(t,
		map[string]string{"teled.yml": teledV2},
		map[string]string{
			// byte-identical copy — nothing to do
			"teled.yml": teledV2,
			// pre-fix copy: no state mount, still emitting the retired CHANNEL_SECRET
			"teled-rhias.yml": "services:\n  teled-rhias:\n    image: arizuko:latest\n" +
				"    environment:\n      CHANNEL_SECRET: 'x'\n",
			// operator's own package, not ours
			"mine.yml": "services:\n  mine:\n    image: y\n",
		})

	plan, err := PlanFragmentSync(svcDir, tmplDir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]FragmentDrift{}
	for _, d := range plan {
		got[d.File] = d
	}
	if s := got["teled.yml"].State; s != FragmentCurrent {
		t.Errorf("identical copy = %s, want current", s)
	}
	if s := got["mine.yml"].State; s != FragmentLocal {
		t.Errorf("uncatalogued fragment = %s, want local", s)
	}
	v := got["teled-rhias.yml"]
	if v.State != FragmentVariant || v.Kind != "teled" {
		t.Fatalf("variant = %s/%q, want variant/teled (match by kind, not filename)", v.State, v.Kind)
	}
	// The safety property: replacing a variant with the base template would give
	// two services the same container_name. Sync must never rewrite it.
	if v.Stale() {
		t.Error("variant marked stale — sync would clobber the operator's account fragment")
	}
	if !slices.ContainsFunc(v.Missing, func(l string) bool { return strings.Contains(l, "store/teled") }) {
		t.Errorf("variant missing-lines %v omit the catalog's state mount", v.Missing)
	}
	if !slices.ContainsFunc(v.Extra, func(l string) bool { return strings.Contains(l, "CHANNEL_SECRET") }) {
		t.Errorf("variant extra-lines %v omit the retired CHANNEL_SECRET", v.Extra)
	}
}

// A plain `<kind>.yml` copy that has fallen behind is the case sync exists for.
func TestPlanFragmentSyncFlagsStaleCopy(t *testing.T) {
	svcDir, tmplDir := seedCatalog(t,
		map[string]string{"teled.yml": teledV2},
		map[string]string{"teled.yml": "services:\n  teled:\n    image: arizuko:latest\n"})

	plan, err := PlanFragmentSync(svcDir, tmplDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || !plan[0].Stale() {
		t.Fatalf("plan = %+v, want one stale fragment", plan)
	}
	report := strings.Join(Report(plan), "\n")
	if !strings.Contains(report, "teled.yml is behind") || !strings.Contains(report, "store/teled") {
		t.Fatalf("report does not show what would change:\n%s", report)
	}
}

// A value-only change (an image tag bump) has no field-level delta to print but
// must still be classified stale — otherwise the catalog's new value never
// reaches the instance, which is the whole failure this path exists to end.
func TestPlanFragmentSyncFlagsValueOnlyDrift(t *testing.T) {
	svcDir, tmplDir := seedCatalog(t,
		map[string]string{"kokoro.yml": "services:\n  kokoro:\n    image: ghcr.io/x:v2\n"},
		map[string]string{"kokoro.yml": "services:\n  kokoro:\n    image: ghcr.io/x:v1\n"})

	plan, err := PlanFragmentSync(svcDir, tmplDir)
	if err != nil {
		t.Fatal(err)
	}
	if !plan[0].Stale() {
		t.Fatalf("value-only drift = %s, want stale", plan[0].State)
	}
	if report := strings.Join(Report(plan), "\n"); !strings.Contains(report, "values differ") {
		t.Fatalf("report hides value-only drift:\n%s", report)
	}
}

// Comments are not behaviour: a reworded header must not nag the operator on
// every generate.
func TestPlanFragmentSyncIgnoresComments(t *testing.T) {
	svcDir, tmplDir := seedCatalog(t,
		map[string]string{"teled.yml": "# rewritten header\n" + teledV2},
		map[string]string{"teled.yml": "# old header\n\n" + teledV2})

	plan, err := PlanFragmentSync(svcDir, tmplDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(Report(plan)) != 0 {
		t.Fatalf("comment-only drift reported: %v", Report(plan))
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
