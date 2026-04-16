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
	if !strings.Contains(out, "env_file:\n      - .env") {
		t.Error("services missing env_file: [.env]")
	}
	if !strings.Contains(out, "'8095:8080'") {
		t.Error("proxyd external mapping should be WEB_PORT:8080")
	}
	// proxyd depends on webd
	if !strings.Contains(out, "depends_on: [gated, dashd, webd]") {
		t.Error("proxyd missing webd in depends_on")
	}
}

func TestInterpolate(t *testing.T) {
	env := map[string]string{"FOO": "bar", "BAZ": "qux"}
	got := interpolate("${FOO}-${BAZ}", env)
	if got != "bar-qux" {
		t.Errorf("got %q", got)
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
	if !strings.Contains(out, "env_file:\n      - .env") {
		t.Error("gated missing env_file: [.env]")
	}
	if !strings.Contains(out, "API_PORT: '8080'") {
		t.Error("gated missing API_PORT override pinning internal listen to 8080")
	}
}
