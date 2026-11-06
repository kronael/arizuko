package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestParseToken(t *testing.T) {
	cases := []struct {
		in       string
		tok      string
		userMode bool
		bad      bool
	}{
		{"Bot abc", "abc", false, false},
		{"abc", "abc", true, false},
		{"Bot ", "", false, true},
		{"", "", false, true},
		{"Botabc", "Botabc", true, false},
	}
	for _, c := range cases {
		tok, user, err := parseToken(c.in)
		if (err != nil) != c.bad || tok != c.tok || user != c.userMode {
			t.Errorf("parseToken(%q) = %q, %v, %v; want %q, %v, bad=%v", c.in, tok, user, err, c.tok, c.userMode, c.bad)
		}
	}
}

func TestLoadConfigModeFromPrefix(t *testing.T) {
	t.Setenv("ROUTER_URL", "http://router:7000")
	t.Setenv("DISCORD_TOKEN", "Bot tok")
	if c := loadConfig(); c.DiscordToken != "tok" || c.UserMode {
		t.Errorf("bot: %+v", c)
	}
	t.Setenv("DISCORD_TOKEN", "utok")
	if c := loadConfig(); c.DiscordToken != "utok" || !c.UserMode {
		t.Errorf("user: %+v", c)
	}
}

func TestLoadConfigRefusesEmptyToken(t *testing.T) {
	if os.Getenv("DISCD_REFUSE_CHILD") == "1" {
		loadConfig()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestLoadConfigRefusesEmptyToken")
	cmd.Env = append(os.Environ(), "DISCD_REFUSE_CHILD=1", "DISCORD_TOKEN=", "ROUTER_URL=http://r:1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("empty DISCORD_TOKEN started; output:\n%s", out)
	}
	if !strings.Contains(string(out), "DISCORD_TOKEN") {
		t.Errorf("refusal does not name DISCORD_TOKEN:\n%s", out)
	}
}
