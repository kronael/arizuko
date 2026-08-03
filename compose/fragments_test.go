package compose

import (
	"os"
	"path/filepath"
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
