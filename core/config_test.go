package core

import (
	"os"
	"strings"
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
	os.Setenv("ARIZUKO_DEV", "true")
	defer os.Unsetenv("ARIZUKO_DEV")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Name != "Andy" {
		t.Fatalf("expected default name 'Andy', got %q", cfg.Name)
	}
	if cfg.Image != "arizuko-ant:latest" {
		t.Fatalf("expected default image, got %q", cfg.Image)
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
	os.Setenv("CONTAINER_IMAGE", "custom:v1")
	os.Setenv("MAX_CONCURRENT_CONTAINERS", "10")
	os.Setenv("ARIZUKO_DEV", "true")
	defer func() {
		os.Unsetenv("ASSISTANT_NAME")
		os.Unsetenv("TELEGRAM_BOT_TOKEN")
		os.Unsetenv("CONTAINER_IMAGE")
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
	if cfg.Image != "custom:v1" {
		t.Fatalf("expected custom:v1, got %q", cfg.Image)
	}
	if cfg.MaxContainers != 10 {
		t.Fatalf("expected 10, got %d", cfg.MaxContainers)
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
