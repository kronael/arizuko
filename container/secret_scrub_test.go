package container

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

// The Bash secret-scrub list lives in TypeScript (ant/src/guards.ts) while the
// set it must partition lives in Go (store.EnvProfileKeys), so neither language
// can import the other's declaration. This reads the source instead — the same
// shape as routd's DefaultModel guard.
//
// readSecrets passes every EnvProfileKeys value set in the host .env into the
// container, so each one either reaches the agent's shell or does not, and the
// choice must be deliberate. BUGS L131: the list held 2 of 5 and nothing said
// whether the other 3 were an omission or a decision.
const guardsTS = "../ant/src/guards.ts"

// agentDrivenCLIKeys are the EnvProfileKeys the agent legitimately spends from
// its own shell: /oracle shells out to `codex` and probes exactly these two
// (ant/skills/oracle/SKILL.md). Scrubbing them would break a shipped skill.
var agentDrivenCLIKeys = []string{"OPENAI_API_KEY", "CODEX_API_KEY"}

func scrubbedKeys(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile(guardsTS)
	if err != nil {
		t.Fatalf("read %s: %v", guardsTS, err)
	}
	m := regexp.MustCompile(`(?s)SECRET_ENV_VARS\s*=\s*\[(.*?)\]`).FindSubmatch(src)
	if m == nil {
		t.Fatalf("%s: no SECRET_ENV_VARS array found — did the declaration move?", guardsTS)
	}
	var out []string
	for _, q := range regexp.MustCompile(`'([A-Z0-9_]+)'`).FindAllStringSubmatch(string(m[1]), -1) {
		out = append(out, q[1])
	}
	if len(out) == 0 {
		t.Fatalf("%s: SECRET_ENV_VARS parsed to nothing", guardsTS)
	}
	return out
}

// Every model credential is either scrubbed from the agent's shell or named as
// one the agent drives a CLI with. A key added to EnvProfileKeys lands in
// neither set and fails here, so the omission cannot repeat silently.
func TestEveryEnvProfileKeyIsScrubbedOrExplicitlyAgentDriven(t *testing.T) {
	scrubbed := scrubbedKeys(t)
	for key := range store.EnvProfileKeys {
		if slices.Contains(scrubbed, key) || slices.Contains(agentDrivenCLIKeys, key) {
			continue
		}
		t.Errorf("%s is in store.EnvProfileKeys but neither scrubbed by SECRET_ENV_VARS "+
			"nor listed in agentDrivenCLIKeys — readSecrets ships it into the container, "+
			"so it is readable from the agent's shell by omission (BUGS L131)", key)
	}
}

// The scrub list may not name a key that is not a model credential: unsetting
// something the agent needs breaks its shell with no signal but a failing
// command.
func TestScrubListNamesOnlyEnvProfileKeys(t *testing.T) {
	for _, key := range scrubbedKeys(t) {
		if _, ok := store.EnvProfileKeys[key]; !ok {
			t.Errorf("SECRET_ENV_VARS scrubs %q, which is not in store.EnvProfileKeys", key)
		}
	}
}

// The two halves are disjoint by construction — a key cannot be both scrubbed
// and spent by the agent.
func TestScrubbedAndAgentDrivenDoNotOverlap(t *testing.T) {
	for _, key := range scrubbedKeys(t) {
		if slices.Contains(agentDrivenCLIKeys, key) {
			t.Errorf("%s is both scrubbed and listed as agent-driven; /oracle would fail", key)
		}
	}
}

// The Anthropic credentials specifically must never survive into the shell:
// nothing under ant/skills reads them, so there is no legitimate shell use.
func TestAnthropicCredentialsAreAllScrubbed(t *testing.T) {
	scrubbed := scrubbedKeys(t)
	for key := range store.EnvProfileKeys {
		if !strings.Contains(key, "ANTHROPIC") && !strings.Contains(key, "CLAUDE") {
			continue
		}
		if !slices.Contains(scrubbed, key) {
			t.Errorf("%s is an Anthropic model credential and is not scrubbed", key)
		}
	}
}
