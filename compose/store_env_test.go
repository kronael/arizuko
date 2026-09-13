package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore/dynstore"
)

// The endpoint and its trust bundle reach every daemon, because every daemon
// reads its owner's tables from the same server (spec 6/42 §7).
func TestStoreEndpointReachesEveryDaemon(t *testing.T) {
	dir := seed(t, "APP=arizuko\nFLAVOR=test\n"+
		dynstore.EnvURL+"=https://extenddb:18443\n"+
		dynstore.EnvRegion+"=us-east-1\n"+
		dynstore.EnvCABundle+"=/srv/app/home/store-ca.pem\n")
	gen(t, dir)
	for _, daemon := range []string{"routd", "runed", "authd", "onbod", "dashd", "webd"} {
		body := read(t, dir, filepath.Join("env", daemon+".env"))
		for _, want := range []string{
			dynstore.EnvURL + "=https://extenddb:18443",
			dynstore.EnvRegion + "=us-east-1",
			dynstore.EnvCABundle + "=/srv/app/home/store-ca.pem",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("env/%s.env carries no %q", daemon, want)
			}
		}
	}
}

// A daemon's access key is ITS OWN. It stays out of the shared .env, and a
// regenerate carries forward what the server minted for that daemon — a key
// that changed under a daemon signs every request wrong.
func TestEachDaemonKeepsItsOwnStoreKey(t *testing.T) {
	dir := seed(t, "APP=arizuko\nFLAVOR=test\n"+dynstore.EnvURL+"=https://extenddb:18443\n")
	if err := os.MkdirAll(filepath.Join(dir, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, filepath.Join("env", "routd.env"),
		dynstore.EnvKeyID+"=AKIAROUTD\n"+dynstore.EnvSecret+"=routdsecret\n")
	write(t, dir, filepath.Join("env", "authd.env"),
		dynstore.EnvKeyID+"=AKIAAUTHD\n"+dynstore.EnvSecret+"=authdsecret\n")
	gen(t, dir)

	routd := read(t, dir, filepath.Join("env", "routd.env"))
	if !strings.Contains(routd, dynstore.EnvKeyID+"=AKIAROUTD") {
		t.Errorf("routd lost its own key:\n%s", routd)
	}
	if strings.Contains(routd, "AKIAAUTHD") {
		t.Errorf("routd reads authd's key:\n%s", routd)
	}
	authd := read(t, dir, filepath.Join("env", "authd.env"))
	if !strings.Contains(authd, dynstore.EnvKeyID+"=AKIAAUTHD") {
		t.Errorf("authd lost its own key:\n%s", authd)
	}
	if body := read(t, dir, ".env"); strings.Contains(body, dynstore.EnvKeyID) {
		t.Errorf(".env carries an access key:\n%s", body)
	}
}
