package compose

import (
	"bytes"
	"log/slog"
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
		dynstore.EnvCA+"=Zm9vCg==\n")
	gen(t, dir)
	for _, daemon := range []string{"routd", "runed", "authd", "onbod", "dashd", "webd"} {
		body := read(t, dir, filepath.Join("env", daemon+".env"))
		for _, want := range []string{
			dynstore.EnvURL + "=https://extenddb:18443",
			dynstore.EnvRegion + "=us-east-1",
			dynstore.EnvCA + "=Zm9vCg==",
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

// The host CLI's own file. `arizuko` runs outside the instance network, so the
// compose service name in DYNAMODB_URL reaches nothing from here; generate
// writes the loopback address of the port this instance publishes (BUGS L311).

// krons's file as it is on disk: comments the operator wrote, the store
// credential `extenddb init` minted, and a URL already addressed to the host.
// Byte-identical after a generate, because nothing here is the platform's to
// rewrite — and 0600 even though it arrived 0644.
func TestOperatorEnvKeepsAnOperatorAddressedFile(t *testing.T) {
	dir := seed(t, "APP=arizuko\nFLAVOR=krons\n"+dynstore.EnvURL+"=https://extenddb:18443\n")
	before := "# The operator's own store credential (spec 6/42).\n" +
		dynstore.EnvKeyID + "=AKIAOPERATOR\n" +
		dynstore.EnvSecret + "=operatorsecret\n" +
		"# The host's address for the same server.\n" +
		dynstore.EnvURL + "=https://127.0.0.1:18443\n"
	if err := os.MkdirAll(filepath.Join(dir, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, filepath.Join("env", "operator.env"), before)
	gen(t, dir)

	if after := read(t, dir, filepath.Join("env", "operator.env")); after != before {
		t.Errorf("operator.env was rewritten:\n--- before\n%s\n--- after\n%s", before, after)
	}
	mustBe0600(t, dir)
}

// The sloth failure itself: the file carried the in-network name, which the
// host cannot resolve. That one line is corrected and the credential the
// operator cannot regenerate survives beside it.
func TestOperatorEnvReplacesTheInNetworkName(t *testing.T) {
	dir := seed(t, "APP=arizuko\nFLAVOR=sloth\n"+dynstore.EnvURL+"=https://extenddb:18443\n")
	if err := os.MkdirAll(filepath.Join(dir, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, filepath.Join("env", "operator.env"),
		dynstore.EnvKeyID+"=AKIAOPERATOR\n"+
			dynstore.EnvSecret+"=operatorsecret\n"+
			dynstore.EnvURL+"=https://extenddb:18443\n"+
			dynstore.EnvCA+"=Zm9vCg==\n")
	gen(t, dir)

	body := read(t, dir, filepath.Join("env", "operator.env"))
	if !strings.Contains(body, dynstore.EnvURL+"=https://127.0.0.1:18443") {
		t.Errorf("operator.env still names the service:\n%s", body)
	}
	for _, keep := range []string{
		dynstore.EnvKeyID + "=AKIAOPERATOR",
		dynstore.EnvSecret + "=operatorsecret",
		dynstore.EnvCA + "=Zm9vCg==",
	} {
		if !strings.Contains(body, keep) {
			t.Errorf("operator.env lost %q:\n%s", keep, body)
		}
	}
	mustBe0600(t, dir)
}

// The second instance on a host publishes its own port, and the operator's
// address follows it — the whole point of STORE_HOST_PORT being in .env and
// not in a fragment an edit freezes.
func TestOperatorEnvFollowsThePublishedPort(t *testing.T) {
	dir := seed(t, "APP=arizuko\nFLAVOR=sloth\n"+
		dynstore.EnvURL+"=https://extenddb:18443\n"+
		storeHostPortKey+"=18444\n")
	gen(t, dir)

	body := read(t, dir, filepath.Join("env", "operator.env"))
	if !strings.Contains(body, dynstore.EnvURL+"=https://127.0.0.1:18444") {
		t.Errorf("operator.env does not address the published port:\n%s", body)
	}
	mustBe0600(t, dir)
}

// No store server, no file: an instance that opens no endpoint must not be
// handed an address for one.
func TestOperatorEnvAbsentWithoutAStoreServer(t *testing.T) {
	dir := seed(t, "APP=arizuko\nFLAVOR=marinade\n")
	gen(t, dir)
	if _, err := os.Stat(filepath.Join(dir, "env", "operator.env")); !os.IsNotExist(err) {
		t.Errorf("operator.env exists with no store endpoint: %v", err)
	}
}

// An endpoint the operator pointed somewhere else is already the host's view,
// and its port is not this instance's to guess.
func TestOperatorEnvLeavesAForeignEndpoint(t *testing.T) {
	dir := seed(t, "APP=arizuko\nFLAVOR=krons\n"+
		dynstore.EnvURL+"=https://dynamodb.us-east-1.amazonaws.com\n")
	gen(t, dir)
	if _, err := os.Stat(filepath.Join(dir, "env", "operator.env")); !os.IsNotExist(err) {
		t.Errorf("operator.env written for a foreign endpoint: %v", err)
	}
}

// The fragment's published host port and the manifest's declared default are
// one fact in two readers — docker's and generate's. Drift silently publishes
// one port and addresses another.
func TestStoreFragmentPublishesTheDeclaredPort(t *testing.T) {
	m, err := ReadManifest(filepath.Join(templateServices, storeService+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	need, ok := m.Env[storeHostPortKey]
	if !ok {
		t.Fatalf("%s.yaml declares no %s", storeService, storeHostPortKey)
	}
	fragment := readFile(t, filepath.Join(templateServices, storeService+".yml"))
	want := "- '127.0.0.1:${" + storeHostPortKey + ":-" + need.Default + "}:18443'"
	if !strings.Contains(fragment, want) {
		t.Errorf("%s.yml does not publish %q", storeService, want)
	}
}

func mustBe0600(t *testing.T, dir string) {
	t.Helper()
	st, err := os.Stat(filepath.Join(dir, "env", "operator.env"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("operator.env mode = %o, want 600 — it holds the store key", st.Mode().Perm())
	}
}

// A daemon with NEITHER store credential is normal — it has no identity yet.
// A daemon with exactly ONE is a half-written file, and skipping it silently
// turns a config fault into a runtime auth error on that daemon's first store
// call, far from the file that caused it.
func TestHalfWrittenStoreCredentialIsLoud(t *testing.T) {
	logs := captureComposeErrors(t)
	dir := t.TempDir()
	envDir := filepath.Join(dir, "env")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// routd gets an id and no secret; authd gets neither.
	if err := os.WriteFile(filepath.Join(envDir, "routd.env"),
		[]byte(dynstore.EnvKeyID+"=AKIAEXAMPLE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "authd.env"), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mergeStoreCredentials(dir, map[string]map[string]string{})

	got := logs.String()
	if !strings.Contains(got, "half written") || !strings.Contains(got, "routd") {
		t.Fatalf("a half-written credential logged no error naming the daemon: %q", got)
	}
	if strings.Contains(got, "authd") {
		t.Errorf("a daemon with NO credential was reported as a fault: %q", got)
	}
}

func captureComposeErrors(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}
