package core

import (
	"os"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	// Clear env to test defaults
	for _, k := range []string{
		"ASSISTANT_NAME", "TELEGRAM_BOT_TOKEN",
		"CONTAINER_IMAGE", "MAX_CONCURRENT_CONTAINERS", "TZ",
	} {
		os.Unsetenv(k)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Name != "Andy" {
		t.Fatalf("expected default name 'Andy', got %q", cfg.Name)
	}
	if cfg.Image != "arizuko-agent:latest" {
		t.Fatalf("expected default image, got %q", cfg.Image)
	}
	if cfg.MaxContainers != 5 {
		t.Fatalf("expected 5 max containers, got %d", cfg.MaxContainers)
	}
	if cfg.Timezone != "UTC" {
		t.Fatalf("expected UTC timezone, got %q", cfg.Timezone)
	}
	if cfg.TriggerRE == nil {
		t.Fatal("expected TriggerRE to be compiled")
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	os.Setenv("ASSISTANT_NAME", "TestBot")
	os.Setenv("TELEGRAM_BOT_TOKEN", "abc123")
	os.Setenv("CONTAINER_IMAGE", "custom:v1")
	os.Setenv("MAX_CONCURRENT_CONTAINERS", "10")
	defer func() {
		os.Unsetenv("ASSISTANT_NAME")
		os.Unsetenv("TELEGRAM_BOT_TOKEN")
		os.Unsetenv("CONTAINER_IMAGE")
		os.Unsetenv("MAX_CONCURRENT_CONTAINERS")
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
	if cfg.Image != "custom:v1" {
		t.Fatalf("expected custom:v1, got %q", cfg.Image)
	}
	if cfg.MaxContainers != 10 {
		t.Fatalf("expected 10, got %d", cfg.MaxContainers)
	}
}

func TestLoadConfigTriggerRE(t *testing.T) {
	os.Setenv("ASSISTANT_NAME", "REDACTED")
	defer os.Unsetenv("ASSISTANT_NAME")

	cfg, _ := LoadConfig()

	if !cfg.TriggerRE.MatchString("@REDACTED hello") {
		t.Fatal("should match @REDACTED")
	}
	if !cfg.TriggerRE.MatchString("@REDACTED hello") {
		t.Fatal("should match case-insensitive")
	}
	if cfg.TriggerRE.MatchString("hello @REDACTED") {
		t.Fatal("should only match at start")
	}
	if cfg.TriggerRE.MatchString("@REDACTEDful") {
		t.Fatal("should require word boundary")
	}
}

func TestIsRoot(t *testing.T) {
	cfg := &Config{}

	if !cfg.IsRoot("main") {
		t.Fatal("main should be root")
	}
	if cfg.IsRoot("main/code") {
		t.Fatal("main/code should not be root")
	}
	if cfg.IsRoot("main/sub/deep") {
		t.Fatal("nested should not be root")
	}
}

func TestLoadConfigEmailSMTPFallback(t *testing.T) {
	os.Setenv("EMAIL_IMAP_HOST", "imap.example.com")
	os.Unsetenv("EMAIL_SMTP_HOST")
	defer os.Unsetenv("EMAIL_IMAP_HOST")

	cfg, _ := LoadConfig()
	if cfg.EmailSMTP != "smtp.example.com" {
		t.Fatalf("expected smtp fallback, got %q", cfg.EmailSMTP)
	}
}

func TestLoadConfigVitePortDerivation(t *testing.T) {
	os.Setenv("WEB_PORT", "8080")
	os.Unsetenv("VITE_PORT_INTERNAL")
	defer os.Unsetenv("WEB_PORT")

	cfg, _ := LoadConfig()
	if cfg.VitePort != 8081 {
		t.Fatalf("expected vite port 8081, got %d", cfg.VitePort)
	}
}

func TestEnvHelpers(t *testing.T) {
	os.Unsetenv("TEST_VAR")

	if got := envOr("TEST_VAR", "default"); got != "default" {
		t.Fatalf("expected default, got %q", got)
	}

	os.Setenv("TEST_VAR", "set")
	defer os.Unsetenv("TEST_VAR")
	if got := envOr("TEST_VAR", "default"); got != "set" {
		t.Fatalf("expected set, got %q", got)
	}

	os.Setenv("TEST_INT", "42")
	defer os.Unsetenv("TEST_INT")
	if got := envInt("TEST_INT", 0); got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}

	os.Setenv("TEST_INT", "bad")
	if got := envInt("TEST_INT", 99); got != 99 {
		t.Fatalf("expected fallback 99, got %d", got)
	}
}
