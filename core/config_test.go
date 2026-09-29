package core

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	// Clear env to test defaults
	for _, k := range []string{
		"ASSISTANT_NAME", "TELEGRAM_BOT_TOKEN",
		"MAX_CONCURRENT_CONTAINERS", "TZ",
	} {
		os.Unsetenv(k)
	}
	os.Setenv("ARIZUKO_DEV", "true")
	defer os.Unsetenv("ARIZUKO_DEV")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}

	// The shared default, not a literal — a second spelling here is the drift
	// this constant exists to prevent (BUGS L340).
	if cfg.Name != DefaultAssistantName {
		t.Fatalf("expected the shared default %q, got %q", DefaultAssistantName, cfg.Name)
	}
	if cfg.MaxContainers != 5 {
		t.Fatalf("expected 5 max containers, got %d", cfg.MaxContainers)
	}
	if cfg.Timezone != "UTC" {
		t.Fatalf("expected UTC timezone, got %q", cfg.Timezone)
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	os.Setenv("ASSISTANT_NAME", "TestBot")
	os.Setenv("TELEGRAM_BOT_TOKEN", "abc123")
	os.Setenv("MAX_CONCURRENT_CONTAINERS", "10")
	os.Setenv("ARIZUKO_DEV", "true")
	defer func() {
		os.Unsetenv("ASSISTANT_NAME")
		os.Unsetenv("TELEGRAM_BOT_TOKEN")
		os.Unsetenv("MAX_CONCURRENT_CONTAINERS")
		os.Unsetenv("ARIZUKO_DEV")
	}()

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Name != "TestBot" {
		t.Fatalf("expected TestBot, got %q", cfg.Name)
	}
	if cfg.TelegramToken != "abc123" {
		t.Fatalf("expected abc123, got %q", cfg.TelegramToken)
	}
	if cfg.MaxContainers != 10 {
		t.Fatalf("expected 10, got %d", cfg.MaxContainers)
	}
}

// TestLoadConfigFromDefaultsRootToDir pins the CLI path: LoadConfigFrom(dir)
// roots the config at dir when DATA_DIR is unset, so `arizuko create`/`group
// add` write groups/ under the instance dir, never the operator's cwd.
func TestLoadConfigFromDefaultsRootToDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", "")
	t.Setenv("ARIZUKO_DEV", "true")

	cfg, err := LoadConfigFrom(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "groups"); cfg.GroupsDir != want {
		t.Fatalf("GroupsDir = %q, want %q", cfg.GroupsDir, want)
	}
	if cfg.ProjectRoot != dir {
		t.Fatalf("ProjectRoot = %q, want %q", cfg.ProjectRoot, dir)
	}
}

// An explicit DATA_DIR still wins over the dir argument.
func TestLoadConfigFromRespectsDataDirEnv(t *testing.T) {
	dir := t.TempDir()
	override := t.TempDir()
	t.Setenv("DATA_DIR", override)
	t.Setenv("ARIZUKO_DEV", "true")

	cfg, err := LoadConfigFrom(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectRoot != override {
		t.Fatalf("ProjectRoot = %q, want DATA_DIR override %q", cfg.ProjectRoot, override)
	}
}

// An unset OR unparseable value must land on the documented default, never on
// the zero value: a poll interval of 0 is a busy loop, a container cap of 0 is
// a daemon that spawns nothing.
func TestEnvHelpers(t *testing.T) {
	const k = "ARIZUKO_CORE_TEST_ENV"
	t.Setenv(k, "")
	if got := EnvOr(k, "fallback"); got != "fallback" {
		t.Errorf("EnvOr empty: %q, want the default", got)
	}
	t.Setenv(k, "set")
	if got := EnvOr(k, "fallback"); got != "set" {
		t.Errorf("EnvOr set: %q", got)
	}

	ints := map[string]int{
		"":    7,
		"abc": 7,
		"1.5": 7,
		"3 ":  7, // strconv.Atoi rejects the trailing space
		"12":  12,
		"-4":  -4,
		"0":   0, // an explicit 0 IS the operator's choice and must win
	}
	for in, want := range ints {
		t.Setenv(k, in)
		if got := EnvInt(k, 7); got != want {
			t.Errorf("EnvInt(%q) = %d, want %d", in, got, want)
		}
	}

	durs := map[string]time.Duration{
		"":          time.Minute,
		"5":         time.Minute, // no unit
		"5 minutes": time.Minute,
		"forever":   time.Minute,
		"30s":       30 * time.Second,
		"2h":        2 * time.Hour,
		"0s":        0,
	}
	for in, want := range durs {
		t.Setenv(k, in)
		if got := DurOr(k, time.Minute); got != want {
			t.Errorf("DurOr(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestSecretKeyring(t *testing.T) {
	if got := SecretKeyring(""); got != nil {
		t.Errorf("empty: got %v, want nil", got)
	}
	if got := SecretKeyring("  ,  "); got != nil {
		t.Errorf("blank entries: got %v, want nil", got)
	}
	// First non-empty entry is the active seal key; rest are decrypt-only,
	// in order. Surrounding whitespace is trimmed, blank entries dropped.
	got := SecretKeyring(" k1 , ,k2,k3 ")
	want := []string{"k1", "k2", "k3"}
	if len(got) != len(want) {
		t.Fatalf("got %d keys, want %d (%q)", len(got), len(want), got)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Errorf("key %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// BUGS S2: routd calls RequireSecretKeyring at boot and exits when it errors,
// so an absent or blank SECRETS_KEY must never yield a usable keyring.
func TestRequireSecretKeyring(t *testing.T) {
	for _, raw := range []string{"", "   ", ",", " , , "} {
		got, err := RequireSecretKeyring(raw)
		if !errors.Is(err, ErrNoSecretsKey) {
			t.Errorf("RequireSecretKeyring(%q): err = %v, want ErrNoSecretsKey", raw, err)
		}
		if got != nil {
			t.Errorf("RequireSecretKeyring(%q): keyring = %v, want nil", raw, got)
		}
	}

	// A rotation value keeps every key, active first.
	got, err := RequireSecretKeyring("new,old")
	if err != nil {
		t.Fatalf("RequireSecretKeyring(rotation): %v", err)
	}
	if len(got) != 2 || string(got[0]) != "new" || string(got[1]) != "old" {
		t.Errorf("keyring = %q, want [new old]", got)
	}
}

// SEND_DISABLED_CHANNELS is matched against adapter names. An entry that keeps
// its surrounding whitespace matches nothing, so the operator believes a
// channel is disabled while it keeps sending.
func TestParseCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{",", nil},
		{"  ,  ", nil},
		{"teled", []string{"teled"}},
		{" teled ", []string{"teled"}},
		{"teled,slakd", []string{"teled", "slakd"}},
		{"teled, slakd ,, discd", []string{"teled", "slakd", "discd"}},
		{"\tteled\n", []string{"teled"}},
	}
	for _, c := range cases {
		if got := ParseCSV(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseCSV(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestSanitizeInstance(t *testing.T) {
	ok := []string{"alpha", "A1", "instance-1", "_under", "a", strings.Repeat("a", 32)}
	for _, s := range ok {
		if _, err := SanitizeInstance(s); err != nil {
			t.Errorf("SanitizeInstance(%q) unexpected err: %v", s, err)
		}
	}
	bad := []string{"", "../etc", "foo/bar", "-dashfirst", strings.Repeat("a", 33), "with space", "has\nnewline", "has:colon"}
	for _, s := range bad {
		if _, err := SanitizeInstance(s); err == nil {
			t.Errorf("SanitizeInstance(%q) expected err", s)
		}
	}
}

// PublicBaseURL is the ONE normaliser for WEB_HOST → public base URL. Every
// live instance sets WEB_HOST bare (krons.fiu.wtf) while AUTH_BASE_URL carries
// the scheme, so a raw concatenation emits a scheme-less link (BUGS F43).
func TestPublicBaseURL(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"krons.fiu.wtf", "https://krons.fiu.wtf"}, // the live shape
		{"sloth.quantblue.tech/", "https://sloth.quantblue.tech"},
		{" fab.krons.cx ", "https://fab.krons.cx"},
		{"https://krons.fiu.wtf", "https://krons.fiu.wtf"}, // already a URL: no double scheme
		{"http://krons.fiu.wtf/", "http://krons.fiu.wtf"},
		{"localhost:8080", "http://localhost:8080"}, // no local dev instance serves TLS
		{"127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"", ""}, // unset stays unset — callers gate on it
		{"   ", ""},
	} {
		if got := PublicBaseURL(c.in); got != c.want {
			t.Errorf("PublicBaseURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLoadConfigModelGateway(t *testing.T) {
	t.Setenv("MODEL_GATEWAY_URL", "http://gw:4000")
	t.Setenv("MODEL_GATEWAY_KEY", "virtual-key-abc123")
	t.Setenv("ARIZUKO_DEV", "true")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelGatewayURL != "http://gw:4000" || cfg.ModelGatewayKey != "virtual-key-abc123" {
		t.Fatalf("gateway pair not read: %q %q", cfg.ModelGatewayURL, cfg.ModelGatewayKey)
	}
}

// The operator's store credential lives in its own file, because .env reaches
// every service that has no scoped env file and this key reaches every owner.
func TestOperatorCredentialLoadsFromItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".env", "ASSISTANT_NAME=Test\nDYNAMODB_URL=https://extenddb:18443\n")
	write("env/operator.env", "AWS_ACCESS_KEY_ID=AKIAOPERATOR\nAWS_SECRET_ACCESS_KEY=shh\n")
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "DYNAMODB_URL", "DATA_DIR"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadConfigFrom(dir); err != nil {
		t.Fatalf("LoadConfigFrom: %v", err)
	}
	if got := os.Getenv("AWS_ACCESS_KEY_ID"); got != "AKIAOPERATOR" {
		t.Errorf("AWS_ACCESS_KEY_ID = %q, want the one in env/operator.env", got)
	}
	if got := os.Getenv("AWS_SECRET_ACCESS_KEY"); got != "shh" {
		t.Errorf("the secret did not load: %q", got)
	}
}

// A process inside the instance's network is handed the store's address and
// keeps it: the operator's file names the same server from the host, where the
// compose service name does not resolve.
func TestTheNetworkAddressSurvivesTheOperatorFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("DYNAMODB_URL=https://extenddb:18443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env", "operator.env"),
		[]byte("DYNAMODB_URL=https://127.0.0.1:18443\nAWS_ACCESS_KEY_ID=AKIAOP\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNAMODB_URL", "https://extenddb:18443")
	LoadInstanceEnv(dir)
	if got := os.Getenv("DYNAMODB_URL"); got != "https://extenddb:18443" {
		t.Errorf("DYNAMODB_URL = %q, want the address the process was given", got)
	}
	if got := os.Getenv("AWS_ACCESS_KEY_ID"); got != "AKIAOP" {
		t.Errorf("the operator's credential did not load: %q", got)
	}
}

// routd builds its LoopConfig from the environment directly rather than through
// LoadConfig, so a default declared in both places drifts silently — and the
// drift only shows on the instance that hits the fallback. APP_SRC_DIR is the
// pair that had already drifted: core fell to HOST_APP_DIR then the binary's
// directory, routd fell to HOST_APP_DIR then "" (BUGS L194).
func TestAppSrcDirFallsThroughToTheBinaryDir(t *testing.T) {
	t.Setenv("APP_SRC_DIR", "")
	t.Setenv("HOST_APP_DIR", "")
	if got := AppSrcDir(); got == "" {
		t.Error("AppSrcDir fell to the empty string; a bare daemon then resolves no app dir at all")
	}

	t.Setenv("HOST_APP_DIR", "/srv/app/from-host")
	if got := AppSrcDir(); got != "/srv/app/from-host" {
		t.Errorf("AppSrcDir = %q, want the HOST_APP_DIR value", got)
	}

	t.Setenv("APP_SRC_DIR", "/srv/app/explicit")
	if got := AppSrcDir(); got != "/srv/app/explicit" {
		t.Errorf("AppSrcDir = %q, want the explicit APP_SRC_DIR", got)
	}
}
