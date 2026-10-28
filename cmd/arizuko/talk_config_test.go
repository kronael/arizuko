package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The config file is the whole credential store for `arizuko talk`. Every one
// of these is a way an operator loses a turn to a silent wrong answer.

func writeTalkConfig(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "talk.toml")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// WriteFile is umask-masked; the mode is the point of half these tests.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return path
}

const twoChatConfig = `
url   = "https://one.example"
token = "top-token"

[work]
url   = "https://two.example"
token = "work-token"
group = "corp/eng"
`

func noEnv(string) string { return "" }

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// A file another local account can read is not a secret. psql refuses a
// group-readable .pgpass for this reason and so does talk; reading it anyway
// would teach the operator that a leaked bearer is fine.
func TestLoadTalkConfigRefusesAGroupReadableFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660} {
		path := writeTalkConfig(t, twoChatConfig, mode)
		_, _, _, err := loadTalkConfig(path)
		if err == nil {
			t.Fatalf("mode %04o was accepted; a bearer readable by another account must be refused", mode)
		}
		if !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("mode %04o error = %q, want the chmod fix named", mode, err)
		}
	}
}

func TestLoadTalkConfigAcceptsOwnerOnlyModes(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400} {
		path := writeTalkConfig(t, twoChatConfig, mode)
		top, named, found, err := loadTalkConfig(path)
		if err != nil || !found {
			t.Fatalf("mode %04o: err=%v found=%v, want it read", mode, err, found)
		}
		if top.Token != "top-token" || named["work"].Token != "work-token" {
			t.Errorf("mode %04o parsed wrong: top=%+v work=%+v", mode, top, named["work"])
		}
	}
}

// No file is not an error — the env layer or a flag can still name a chat.
func TestLoadTalkConfigTreatsAMissingFileAsNotFound(t *testing.T) {
	_, _, found, err := loadTalkConfig(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("missing file returned err %v, want none", err)
	}
	if found {
		t.Error("found = true for a file that does not exist")
	}
}

// A typo'd key must not read as "nothing configured" — that sends the operator
// to the wrong end of the problem.
func TestParseTalkTOMLRejectsAnUnknownKey(t *testing.T) {
	if _, _, err := parseTalkTOML([]byte(`tokne = "x"`)); err == nil {
		t.Fatal("a misspelled top-level key was accepted")
	}
	_, _, err := parseTalkTOML([]byte("[work]\ntokne = \"x\"\n"))
	if err == nil {
		t.Fatal("a misspelled key inside a section was accepted")
	}
	if !strings.Contains(err.Error(), "[work]") {
		t.Errorf("error = %q, want the section named", err)
	}
}

// Precedence: flag > @name > top-level > env > error.
func TestResolveTalkEntryPrecedence(t *testing.T) {
	top, named, _ := mustParse(t, twoChatConfig)
	env := envMap(map[string]string{
		"ARIZUKO_CHAT_URL":   "https://env.example",
		"ARIZUKO_CHAT_TOKEN": "env-token",
	})

	cases := []struct {
		name             string
		flags            talkFlags
		getenv           func(string) string
		wantURL, wantTok string
	}{
		{"top-level is the one chat", talkFlags{}, noEnv, "https://one.example", "top-token"},
		{"@name selects a section", talkFlags{Section: "work"}, noEnv, "https://two.example", "work-token"},
		{"@name beats top-level even with env set", talkFlags{Section: "work"}, env, "https://two.example", "work-token"},
		{"top-level beats env", talkFlags{}, env, "https://one.example", "top-token"},
		{"a flag beats @name", talkFlags{Section: "work", Token: "flag-token"}, noEnv, "https://two.example", "flag-token"},
		{"a flag beats top-level", talkFlags{URL: "https://flag.example"}, noEnv, "https://flag.example", "top-token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveTalkEntry(c.flags, top, named, true, c.getenv, "/cfg/talk.toml")
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got.URL != c.wantURL || got.Token != c.wantTok {
				t.Errorf("got url=%q token=%q, want url=%q token=%q",
					got.URL, got.Token, c.wantURL, c.wantTok)
			}
		})
	}
}

// Env is the layer below the file, and only reached when the file names nothing.
func TestResolveTalkEntryFallsBackToEnvWithNoConfig(t *testing.T) {
	got, err := resolveTalkEntry(talkFlags{}, talkEntry{}, nil, false, envMap(map[string]string{
		"ARIZUKO_CHAT_URL":   "https://env.example",
		"ARIZUKO_CHAT_TOKEN": "env-token",
	}), "/cfg/talk.toml")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.URL != "https://env.example" || got.Token != "env-token" {
		t.Errorf("got %+v, want the env pair", got)
	}
}

// The bottom of the precedence chain is a USABLE error: it must print the file
// to write and the commands that fill it, not "config not found".
func TestResolveTalkEntryErrorNamesTheFileAndTheCommands(t *testing.T) {
	_, err := resolveTalkEntry(talkFlags{}, talkEntry{}, nil, false, noEnv, "/cfg/talk.toml")
	if err == nil {
		t.Fatal("no chat configured was accepted")
	}
	for _, want := range []string{
		"/cfg/talk.toml", // which file
		`url   = "`,      // what to put in it
		`token = "`,
		"chmod 600", // and with which mode
		"arizuko token [-i instance] issue bearer", // where the token comes from
		"arizuko group [-i instance] grant",        // and what actually decides reach
		"ARIZUKO_CHAT_TOKEN",                       // the other way in
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q; full text:\n%s", want, err)
		}
	}
}

// A section is a COMPLETE chat, never a partial one inheriting the top-level
// url — the same no-parent-inheritance rule the platform applies elsewhere.
func TestResolveTalkEntryDoesNotInheritIntoASection(t *testing.T) {
	top, named, _ := mustParse(t, "url = \"https://one.example\"\ntoken = \"t\"\n\n[bare]\ntoken = \"b\"\n")
	_, err := resolveTalkEntry(talkFlags{Section: "bare"}, top, named, true, noEnv, "/cfg/talk.toml")
	if err == nil {
		t.Fatal("[bare] borrowed the top-level url; a section must stand alone")
	}
}

func TestResolveTalkEntryReportsAnUnknownSection(t *testing.T) {
	top, named, _ := mustParse(t, twoChatConfig)
	_, err := resolveTalkEntry(talkFlags{Section: "home"}, top, named, true, noEnv, "/cfg/talk.toml")
	if err == nil {
		t.Fatal("@home resolved against a config that has no [home]")
	}
	if !strings.Contains(err.Error(), "@work") {
		t.Errorf("error = %q, want the sections that DO exist listed", err)
	}
}

// The group travels with the chat: @work carries corp/eng so no -g is needed.
func TestResolveTalkEntryCarriesTheSectionGroup(t *testing.T) {
	top, named, _ := mustParse(t, twoChatConfig)
	got, err := resolveTalkEntry(talkFlags{Section: "work"}, top, named, true, noEnv, "/cfg/talk.toml")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Group != "corp/eng" {
		t.Errorf("group = %q, want corp/eng from the section", got.Group)
	}
	withFlag, err := resolveTalkEntry(
		talkFlags{Section: "work", Group: "other/f"}, top, named, true, noEnv, "/cfg/talk.toml")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if withFlag.Group != "other/f" {
		t.Errorf("group = %q, want -g to win over the section", withFlag.Group)
	}
}

// A trailing slash in the configured url would build https://h//mcp.
func TestResolveTalkEntryTrimsTheURL(t *testing.T) {
	got, err := resolveTalkEntry(talkFlags{}, talkEntry{URL: " https://h/ ", Token: "t"},
		nil, true, noEnv, "/cfg/talk.toml")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.URL != "https://h" {
		t.Errorf("url = %q, want the trailing slash and spaces gone", got.URL)
	}
}

func mustParse(t *testing.T, body string) (talkEntry, map[string]talkEntry, error) {
	t.Helper()
	top, named, err := parseTalkTOML([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return top, named, nil
}
