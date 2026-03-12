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
	if !strings.Contains(out, "router:") {
		t.Error("missing router service")
	}
	if !strings.Contains(out, "arizuko:latest") {
		t.Error("missing router image")
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
ROUTER_URL = "http://router:8080"
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
	if !strings.Contains(out, "depends_on: [router]") {
		t.Error("missing depends_on router")
	}
}

func TestGenerateMultipleServices(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("API_PORT=9090\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "services/discord.toml"), []byte(`
image = "arizuko-discord:latest"
[environment]
ROUTER_URL = "http://router:9090"
`), 0o644)
	os.WriteFile(filepath.Join(dir, "services/telegram.toml"), []byte(`
image = "arizuko-telegram:latest"
[environment]
ROUTER_URL = "http://router:9090"
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
depends_on = ["router", "telegram"]
`), 0o644)

	out, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "depends_on: [router, telegram]") {
		t.Error("custom depends_on not rendered")
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
	if !strings.Contains(out, "ASSISTANT_NAME: 'bot'") {
		t.Error("ASSISTANT_NAME not passed to router")
	}
	if !strings.Contains(out, "CONTAINER_IMAGE: 'agent:v2'") {
		t.Error("CONTAINER_IMAGE not passed to router")
	}
}
