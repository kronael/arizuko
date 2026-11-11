package compose

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// templateServices is the release's own service packages — the fragments `arizuko
// package` writes into <dataDir>/services, which docker includes verbatim.
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
	b, err := os.ReadFile(filepath.Join(templateServices, name, name+".yml"))
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

// An adapter that writes a FILE must mount a bind for it, and that mount must be
// the narrow subpath the daemon actually needs. teled shipped
// with NO volumes at all (BUGS E1), so every container recreate wiped the
// Telegram offset — teled then re-answered Telegram's ~24h backlog.
//
// An adapter that writes no file is NOT here: its tables are on the store server
// and it mounts no part of the instance tree.
func TestStatefulFragmentsMountTheirStateDir(t *testing.T) {
	for _, name := range []string{"teled"} {
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
				if _, ok := daemonKeys[daemon]; !ok && envFileName(svcName) != "env/"+daemon+".env" {
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

// litellm has no env file: docker interpolates its env straight from .env, so
// the set of `${KEY}` references in the fragment IS the set of instance values
// the third-party image can read. Pin it — a new reference is a new key handed
// to a foreign image, and MODEL_GATEWAY_KEY must stay required (`:?`), or a
// missing key starts the gateway open to every container on the network.
func TestLitellmFragmentReadsOnlyItsDeclaredKeys(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateServices, "litellm", "litellm.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var f fragment
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse litellm.yml: %v", err)
	}
	for name, svc := range f.Services {
		if len(svc.EnvFile) != 0 {
			t.Errorf("litellm.yml gives %s an env_file; its env is the interpolated keys below, nothing more", name)
		}
	}
	want := []string{
		"ANTHROPIC_API_KEY", "APP", "CLAUDE_CODE_OAUTH_TOKEN", "FLAVOR",
		"MODEL_GATEWAY_DB_PASSWORD", "MODEL_GATEWAY_KEY", "OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_PROTOCOL", "TZ",
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`\$\{([A-Z_]+)`).FindAllStringSubmatch(string(raw), -1) {
		seen[m[1]] = true
	}
	var got []string
	for k := range seen {
		got = append(got, k)
	}
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("litellm.yml interpolates %v, want exactly %v", got, want)
	}
	if !strings.Contains(string(raw), "${MODEL_GATEWAY_KEY:?") {
		t.Error("MODEL_GATEWAY_KEY must be a required interpolation (`:?`) so the gateway never starts without a key")
	}
	if !strings.Contains(string(raw), "${MODEL_GATEWAY_DB_PASSWORD:?") {
		t.Error("MODEL_GATEWAY_DB_PASSWORD must be a required interpolation (`:?`); an empty password starts a gateway database open on the compose network")
	}
}

// The gateway's key management, its team budgets and its spend log are all
// Postgres-only, so a litellm without a reachable database mints no per-turn
// key, holds no cap and records no usage: routd's mint fails and every turn
// fails with it. Pin the wiring — the database is named, it is healthy before
// the gateway starts, its data survives a recreate, and it holds the gateway's
// state rather than the instance's (spec 6/23, 6/39).
func TestLitellmFragmentWiresItsDatabase(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(templateServices, "litellm", "litellm.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
			Volumes     []string          `yaml:"volumes"`
			DependsOn   map[string]struct {
				Condition string `yaml:"condition"`
			} `yaml:"depends_on"`
			Healthcheck map[string]any `yaml:"healthcheck"`
		} `yaml:"services"`
		Volumes map[string]any `yaml:"volumes"`
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse litellm.yml: %v", err)
	}
	gateway, ok := f.Services["litellm"]
	if !ok {
		t.Fatal("litellm.yml declares no litellm service")
	}
	db, ok := f.Services["litellm_db"]
	if !ok {
		t.Fatal("litellm.yml declares no litellm_db; without it /key/generate and /spend/logs both fail")
	}
	if url := gateway.Environment["DATABASE_URL"]; !strings.Contains(url, "@litellm_db:5432/") {
		t.Errorf("litellm DATABASE_URL is %q; it must name the litellm_db service", url)
	}
	if c := gateway.DependsOn["litellm_db"].Condition; c != "service_healthy" {
		t.Errorf("litellm depends on litellm_db with condition %q, want service_healthy — "+
			"litellm runs its schema migration at boot and exits when the database is not up yet", c)
	}
	if len(db.Healthcheck) == 0 {
		t.Error("litellm_db has no healthcheck, so service_healthy above can never be satisfied")
	}
	data, mounted := mountFor(db.Volumes, "/var/lib/postgresql/data")
	if !mounted {
		t.Fatalf("litellm_db does not mount its data dir (volumes %v); every recreate loses every key and every spend row", db.Volumes)
	}
	if strings.HasPrefix(data, "${DATA_DIR}") || strings.HasPrefix(data, "/") {
		t.Errorf("litellm_db keeps its data at %q; this is the gateway's bookkeeping, not instance state, "+
			"so it belongs in a named volume rather than the dir backup and restore own", data)
	}
	if _, declared := f.Volumes[data]; !declared {
		t.Errorf("litellm_db mounts volume %q that the fragment never declares in its top-level volumes:", data)
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
