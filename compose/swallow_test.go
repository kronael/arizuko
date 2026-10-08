package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore/dynstore"
)

// Two generate-time reads used to answer "no store server" and "no manifest"
// for EVERY failure, including the ones that mean the opposite. Both now test
// the condition they meant instead of testing err.

// A store URL the operator set and got wrong is a refusal, not a skip: leaving
// operator.env unwritten points the host CLI at nothing (BUGS L311).
func TestWriteOperatorEnvRefusesAnUnparseableStoreURL(t *testing.T) {
	dir := t.TempDir()
	err := writeOperatorEnv(dir, map[string]string{dynstore.EnvURL: "http://[::1"})
	if err == nil {
		t.Fatal("an unparseable store URL was accepted")
	}
	if !strings.Contains(err.Error(), dynstore.EnvURL) {
		t.Errorf("error does not name the key: %v", err)
	}
	if _, serr := os.Stat(operatorEnvPath(dir)); !os.IsNotExist(serr) {
		t.Errorf("operator.env written for an unparseable URL: %v", serr)
	}
}

// An absent key is not a bad one: url.Parse takes "" cleanly, so the empty
// case still means "this instance runs no store server" and writes nothing.
func TestWriteOperatorEnvSkipsWhenNoStoreServer(t *testing.T) {
	dir := t.TempDir()
	if err := writeOperatorEnv(dir, map[string]string{}); err != nil {
		t.Fatalf("no store server must be a skip, got %v", err)
	}
	if _, err := os.Stat(operatorEnvPath(dir)); !os.IsNotExist(err) {
		t.Errorf("operator.env written with no store server: %v", err)
	}
}

// A catalog manifest that is present but unreadable is not a catalog that
// ships none: the second reads as "nothing to link" and leaves the package's
// rows and env needs silently unlinked.
func TestRelinkSidecarRefusesAnUnreadableManifest(t *testing.T) {
	services, catalog := t.TempDir(), t.TempDir()
	// A directory where the manifest belongs: ReadFile fails with something
	// other than "does not exist", on any uid, root included.
	if err := os.Mkdir(filepath.Join(catalog, "teled.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := relinkSidecar(services, catalog, catalog, "teled")
	if err == nil {
		t.Fatal("an unreadable catalog manifest was treated as an absent one")
	}
	if !strings.Contains(err.Error(), "teled.yaml") {
		t.Errorf("error does not name the manifest: %v", err)
	}
}

// The absent case stays a skip — most kinds ship no manifest at all.
func TestRelinkSidecarSkipsWhenCatalogShipsNoManifest(t *testing.T) {
	services, catalog := t.TempDir(), t.TempDir()
	if err := relinkSidecar(services, catalog, catalog, "teled"); err != nil {
		t.Fatalf("a catalog with no manifest must be a skip, got %v", err)
	}
}

// TestNoServiceReadsTheSharedEnv pins BUGS `L200`'s one deletable line. `envFileName`
// fell back to the instance's shared `.env` for any service with no `daemonKeys` entry.
// That file holds every secret the instance has — `SECRETS_KEY`, every channel token,
// the model credential — so adding a stanza without adding its key list handed that
// service the lot, and the compose file rendered and the container started.
//
// The fallback returns "" now, and Generate refuses the rendered file if any service
// still points at `.env`.
func TestNoServiceReadsTheSharedEnv(t *testing.T) {
	// The real generated file, not a fixture: this is the artifact that ships.
	out := gen(t, seed(t, ""))
	if strings.Contains(out, "- .env\n") {
		t.Errorf("a service reads the shared .env:\n%s", out)
	}

	// And the refusal fires on the shape, so the guard cannot pass by never running.
	bad := "services:\n  newd:\n    image: x\n    env_file:\n      - .env\n"
	err := checkNoServiceReadsTheSharedEnv(bad)
	if err == nil {
		t.Fatal("a service reading the shared .env must refuse generation")
	}
	for _, want := range []string{"newd", "every secret", "daemonKeys"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must say %q: %v", want, err)
		}
	}

	// A service with its own file is fine, and one with NO env_file is fine too:
	// vited serves a read-only volume and reads nothing.
	ok := "services:\n  routd:\n    env_file:\n      - env/routd.env\n  vited:\n    image: y\n"
	if err := checkNoServiceReadsTheSharedEnv(ok); err != nil {
		t.Errorf("a scoped file and an absent one are both legal: %v", err)
	}
}
