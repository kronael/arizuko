package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateMinimal(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("ASSISTANT_NAME=test\nAPI_PORT=8080\n"), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "services:") {
		t.Error("missing services header")
	}
	if !strings.Contains(out, "gated:") {
		t.Error("missing gated service")
	}
	if !strings.Contains(out, "arizuko:latest") {
		t.Error("missing gated image")
	}
	if !strings.Contains(out, "host.docker.internal:host-gateway") {
		t.Error("gated missing extra_hosts for host.docker.internal — host-side services unreachable")
	}
	if !strings.Contains(out, "user: '1000:1000'") {
		t.Error("gated missing user:1000 — will create root-owned files in shared data dir")
	}
	if !strings.Contains(out, "group_add:") {
		t.Error("gated missing group_add — docker.sock inaccessible as uid 1000")
	}
}

func TestGenerateWithChannel(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"ASSISTANT_NAME=test\nAPI_PORT=8080\nCHANNEL_SECRET=s3cr3t\nTELEGRAM_BOT_TOKEN=tok123\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "services/telegram.toml"), []byte(`
image = "arizuko-telegram:latest"

[environment]
ROUTER_URL = "http://gated:8080"
TELEGRAM_TOKEN = "${TELEGRAM_BOT_TOKEN}"
CHANNEL_SECRET = "${CHANNEL_SECRET}"
`), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "telegram:") {
		t.Error("missing telegram service")
	}
	if !strings.Contains(out, "arizuko-telegram:latest") {
		t.Error("missing telegram image")
	}
	if !strings.Contains(out, "tok123") {
		t.Error("TELEGRAM_BOT_TOKEN not interpolated")
	}
	if !strings.Contains(out, "s3cr3t") {
		t.Error("CHANNEL_SECRET not interpolated")
	}
	if !strings.Contains(out, "depends_on: [gated]") {
		t.Error("missing depends_on gated")
	}
}

func TestGenerateMultipleServices(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("API_PORT=9090\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "services/discord.toml"), []byte(`
image = "arizuko-discord:latest"
[environment]
ROUTER_URL = "http://gated:9090"
`), 0o644)
	os.WriteFile(filepath.Join(dir, "services/telegram.toml"), []byte(`
image = "arizuko-telegram:latest"
[environment]
ROUTER_URL = "http://gated:9090"
`), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	di := strings.Index(out, "discord:")
	ti := strings.Index(out, "telegram:")
	if di < 0 || ti < 0 {
		t.Fatal("missing services")
	}
	if di > ti {
		t.Error("services not sorted alphabetically")
	}
}

func TestGenerateNoServicesDir(t *testing.T) {
	dir := t.TempDir()
	_, err := Generate(dir)
	if err == nil {
		t.Error("expected error for missing services dir")
	}
}

func TestGenerateCustomDependsOn(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(dir, "services/whisper.toml"), []byte(`
image = "whisper:latest"
depends_on = ["gated", "telegram"]
`), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "depends_on: [gated, telegram]") {
		t.Error("custom depends_on not rendered")
	}
}

func TestGenerateWebServices(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"WEB_PORT=8095\nCHANNEL_SECRET=sec\nAUTH_SECRET=jwt\nASSISTANT_NAME=bot\n"), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "webd:") {
		t.Error("missing webd service")
	}
	// Shared env flows via env_file; peer URLs default in code to
	// http://<svc>:8080.
	if !strings.Contains(out, "env_file:\n      - env/gated.env") {
		t.Error("gated missing scoped env_file: env/gated.env")
	}
	if !strings.Contains(out, "'8095:8080'") {
		t.Error("proxyd external mapping should be WEB_PORT:8080")
	}
	// proxyd depends on webd
	if !strings.Contains(out, "depends_on: [gated, dashd, webd]") {
		t.Error("proxyd missing webd in depends_on")
	}
}

func TestGenerateWithWebDAV(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"WEBDAV_ENABLED=true\nWEB_PORT=443\nAPI_PORT=8080\n"), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "davd:") {
		t.Error("missing davd service")
	}
	if !strings.Contains(out, "arizuko-davd") {
		t.Error("davd should use arizuko-davd image (sigoden/dufs wrapped with healthcheck)")
	}
	if strings.Contains(out, "/data:ro") {
		t.Error("davd /data mount should be read-write; proxyd davAllow is the write-block enforcement")
	}
	if !strings.Contains(out, ":/data\n") {
		t.Error("davd should mount /data (read-write)")
	}
	if !strings.Contains(out, "depends_on") {
		t.Error("davd missing depends_on")
	}
}

func TestGenerateWebDAVDefaultOn(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"WEB_PORT=443\nAPI_PORT=8080\n"), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "davd:") {
		t.Error("davd service should be present by default (WEBDAV_ENABLED defaults to true)")
	}
	if !strings.Contains(out, `\"path\":\"/dav/\"`) {
		t.Error("proxyd should receive /dav/ route in PROXYD_ROUTES_JSON by default")
	}
}

func TestGenerateWebDAVDisabled(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"WEBDAV_ENABLED=false\nWEB_PORT=443\nAPI_PORT=8080\n"), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "davd:") {
		t.Error("davd service should be absent when WEBDAV_ENABLED=false")
	}
	if strings.Contains(out, `\"path\":\"/dav/\"`) {
		t.Error("proxyd should not receive /dav/ route when WEBDAV_ENABLED=false")
	}
}

// TestGenerateProfiles pins the PROFILE → built-in services matrix from
// specs/4/Y-minimal-setup.md. WEB_PORT is set so the web bundle (webd,
// proxyd, vited) is eligible; the profile gates non-web built-ins.
func TestGenerateProfiles(t *testing.T) {
	cases := []struct {
		profile string
		wantIn  []string
		wantOut []string
	}{
		{
			profile: "minimal",
			wantIn:  []string{"  gated:"},
			wantOut: []string{"  webd:", "  proxyd:", "  vited:", "  timed:", "  dashd:"},
		},
		{
			profile: "web",
			wantIn:  []string{"  gated:", "  webd:", "  proxyd:", "  vited:"},
			wantOut: []string{"  timed:", "  dashd:", "  davd:", "  onbod:"},
		},
		{
			profile: "standard",
			wantIn:  []string{"  gated:", "  timed:", "  webd:", "  proxyd:", "  vited:"},
			wantOut: []string{"  dashd:", "  davd:", "  onbod:"},
		},
		{
			profile: "full",
			wantIn:  []string{"  gated:", "  timed:", "  webd:", "  proxyd:", "  vited:", "  dashd:", "  davd:"},
			wantOut: []string{"  onbod:"}, // ONBOARDING_ENABLED unset
		},
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			dir := t.TempDir()
			os.MkdirAll(filepath.Join(dir, "services"), 0o755)
			os.WriteFile(filepath.Join(dir, ".env"), []byte(
				"PROFILE="+tc.profile+"\nWEB_PORT=8095\nAPI_PORT=8080\n"), 0o644)
			out, err := Generate(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(out, want) {
					t.Errorf("PROFILE=%s: missing %q", tc.profile, want)
				}
			}
			for _, dont := range tc.wantOut {
				if strings.Contains(out, dont) {
					t.Errorf("PROFILE=%s: unexpected %q", tc.profile, dont)
				}
			}
		})
	}
}

// TestGenerateMultiAccountAdapter verifies that <adapter>-<label>.toml
// services share the base adapter's env_file (specs/5/R-multi-account.md).
func TestGenerateMultiAccountAdapter(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"CHANNEL_SECRET=s\nTELEGRAM_BOT_TOKEN=tok\nAPI_PORT=8080\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "services/teled-work.toml"), []byte(`
image = "arizuko:latest"
entrypoint = ["teled"]
[environment]
ROUTER_URL = "http://gated:8080"
LISTEN_URL = "http://teled-work:8080"
`), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "  teled-work:\n") {
		t.Fatal("missing teled-work service")
	}
	// teled-work must reuse env/teled.env so per-daemon secret scoping holds.
	idx := strings.Index(out, "  teled-work:\n")
	tail := out[idx:]
	if !strings.Contains(tail[:200], "env/teled.env") {
		t.Errorf("teled-work should reuse env/teled.env, got:\n%s", tail[:200])
	}
}

func TestInterpolate(t *testing.T) {
	env := map[string]string{"FOO": "bar", "BAZ": "qux"}
	got := interpolate("${FOO}-${BAZ}", env)
	if got != "bar-qux" {
		t.Errorf("got %q", got)
	}
}

// TestProxydRoutes_AllAdaptersDeclared: every service with a [[proxyd_route]]
// shows up in the generated PROXYD_ROUTES_JSON env var on proxyd.
func TestProxydRoutes_AllAdaptersDeclared(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"WEB_PORT=8095\nAPI_PORT=8080\nCHANNEL_SECRET=s\nSLACK_BOT_TOKEN=tok\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "services/slakd.toml"), []byte(`
image = "arizuko:latest"
entrypoint = ["slakd"]
[environment]
SLACK_BOT_TOKEN = "${SLACK_BOT_TOKEN}"

[[proxyd_route]]
path = "/slack/"
backend = "http://slakd:8080"
auth = "public"
gated_by = "SLACK_BOT_TOKEN"
preserve_headers = ["X-Slack-Signature", "X-Slack-Request-Timestamp"]
`), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "PROXYD_ROUTES_JSON") {
		t.Fatal("proxyd missing PROXYD_ROUTES_JSON env injection")
	}
	if !strings.Contains(out, `\"path\":\"/slack/\"`) {
		t.Errorf("slack route not serialized into PROXYD_ROUTES_JSON; got:\n%s", out)
	}
	if !strings.Contains(out, `\"backend\":\"http://slakd:8080\"`) {
		t.Errorf("slack backend missing in PROXYD_ROUTES_JSON")
	}
}

func TestProxydRoutes_GatedBy_Skipped_When_Env_Unset(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"WEB_PORT=8095\nAPI_PORT=8080\nCHANNEL_SECRET=s\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "services/slakd.toml"), []byte(`
image = "arizuko:latest"
entrypoint = ["slakd"]
[environment]
SLACK_BOT_TOKEN = "${SLACK_BOT_TOKEN}"

[[proxyd_route]]
path = "/slack/"
backend = "http://slakd:8080"
auth = "public"
gated_by = "SLACK_BOT_TOKEN"
`), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `\"path\":\"/slack/\"`) {
		t.Errorf("slack route should be skipped when SLACK_BOT_TOKEN unset; got:\n%s", out)
	}
}

func TestProxydRoutes_GatedBy_Included_When_Env_Set(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"WEB_PORT=8095\nAPI_PORT=8080\nCHANNEL_SECRET=s\nSLACK_BOT_TOKEN=tok\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "services/slakd.toml"), []byte(`
image = "arizuko:latest"
entrypoint = ["slakd"]
[environment]
SLACK_BOT_TOKEN = "${SLACK_BOT_TOKEN}"

[[proxyd_route]]
path = "/slack/"
backend = "http://slakd:8080"
auth = "public"
gated_by = "SLACK_BOT_TOKEN"
`), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `\"path\":\"/slack/\"`) {
		t.Errorf("slack route should be present when SLACK_BOT_TOKEN set; got:\n%s", out)
	}
}

func TestGenerateEgressIsolation(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"EGRESS_ISOLATION=true\nCRACKBOX_ADMIN_API=http://crackbox:3129\nAPI_PORT=8080\n"), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "  crackbox:\n") {
		t.Error("crackbox service missing")
	}
	// No `agents` shared network — folder networks are runtime-managed by gated.
	if strings.Contains(out, "\nnetworks:\n") {
		t.Error("compose still declares networks block — folder networks should be runtime-managed, not compose")
	}
	if strings.Contains(out, "networks: [agents") || strings.Contains(out, "networks: [agents, default]") {
		t.Error("crackbox should not attach to a static `agents` network")
	}
}

func TestRouterEnvPassthrough(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(
		"ASSISTANT_NAME=bot\nCONTAINER_IMAGE=agent:v2\nAPI_PORT=8080\n"), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Shared config flows via env_file — compose doesn't duplicate these
	// keys in per-service environment blocks anymore. Asserting env_file
	// is enough: docker-compose reads .env at container start.
	if !strings.Contains(out, "env_file:\n      - env/gated.env") {
		t.Error("gated missing scoped env_file: env/gated.env")
	}
	if !strings.Contains(out, `API_PORT: "8080"`) {
		t.Error("gated missing API_PORT override pinning internal listen to 8080")
	}
}
