package main

import (
	"os"
	"testing"
)

// Strict env: missing token = exit. We can't test os.Exit directly without
// subprocess gymnastics; instead exercise the happy path with both required
// vars present and assert defaults flow through.
func TestLoadConfig_Defaults(t *testing.T) {
	os.Setenv("SLACK_BOT_TOKEN", "xoxb-test")
	os.Setenv("SLACK_SIGNING_SECRET", "shh")
	os.Setenv("ROUTER_URL", "http://gated:8080")
	os.Setenv("CHANNEL_NAME", "")
	os.Setenv("LISTEN_ADDR", "")
	os.Setenv("LISTEN_URL", "")
	os.Setenv("SLAKD_USERS_CACHE_TTL", "")
	defer func() {
		for _, k := range []string{"SLACK_BOT_TOKEN", "SLACK_SIGNING_SECRET", "ROUTER_URL"} {
			os.Unsetenv(k)
		}
	}()
	cfg := loadConfig()
	if cfg.Name != "slack" {
		t.Errorf("name = %q", cfg.Name)
	}
	if cfg.BotToken != "xoxb-test" {
		t.Errorf("token = %q", cfg.BotToken)
	}
	if cfg.SigningSecret != "shh" {
		t.Errorf("signing secret = %q", cfg.SigningSecret)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("addr = %q", cfg.ListenAddr)
	}
	if cfg.CacheTTL.Seconds() != 900 {
		t.Errorf("cache ttl = %v", cfg.CacheTTL)
	}
}

func TestLoadConfig_SlakdChannelSecretOverridesGeneric(t *testing.T) {
	os.Setenv("SLACK_BOT_TOKEN", "xoxb-test")
	os.Setenv("SLACK_SIGNING_SECRET", "shh")
	os.Setenv("ROUTER_URL", "http://gated:8080")
	os.Setenv("CHANNEL_SECRET", "generic")
	os.Setenv("SLAKD_CHANNEL_SECRET", "slack-specific")
	defer func() {
		for _, k := range []string{"SLACK_BOT_TOKEN", "SLACK_SIGNING_SECRET", "ROUTER_URL", "CHANNEL_SECRET", "SLAKD_CHANNEL_SECRET"} {
			os.Unsetenv(k)
		}
	}()
	cfg := loadConfig()
	if cfg.ChannelSecret != "slack-specific" {
		t.Errorf("expected SLAKD_CHANNEL_SECRET to win, got %q", cfg.ChannelSecret)
	}
}
