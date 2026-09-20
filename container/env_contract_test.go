package container

// The spawn env is a contract between two languages: Go decides what reaches
// the container, TypeScript decides what it reads. Nothing joined the two, so a
// key ant read that buildArgs never emitted was not a build failure, not a test
// failure and not a log line — it was ant's own default, silently. Three keys
// have died that way (ARIZUKO_MODEL, RUNED_RUN_TIMEOUT, ARIZUKO_BACKEND), and
// the last one killed the second-backend slot for months.
//
// This test reads ant's ACTUAL source for the keys it takes from the container
// env, then asserts buildArgs emits each one. It fails when either side moves:
// ant adding a read, or Go dropping an emission.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

// notArizukos are keys ant reads that arizuko does not own, each with the owner
// named so the set cannot grow by shrug. Every one of them has an explicit
// fallback in ant, which is what makes the absence safe — a key arizuko owns
// has no sane default, and that is exactly why it must be emitted.
var notArizukos = map[string]string{
	"HOME":         "docker and the image; buildArgs also emits it when the uid is not 1000",
	"PATH":         "the image",
	"NODE_OPTIONS": "buildArgs when egress is on; node's own variable otherwise",
	"CODEX_HOME":   "codex; ant falls back to <home>/.codex (codex.ts codexHomeDir)",
}

// antEnvReads returns every environment key ant takes from the container env.
// It reads the .ts sources rather than a hand-kept list, so a new read is
// discovered instead of remembered. Test files are skipped: they set env to
// build fixtures, which is not a contract with the spawn.
func antEnvReads(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "ant", "src")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read ant/src: %v", err)
	}
	// Three shapes, because ant reads its env three ways. `process.env.KEY` and
	// `process.env["KEY"]` are the direct reads. `env.KEY` is a read off the
	// seam's env record, which index.ts builds from process.env — the harnesses
	// take it that way so a turn's secrets overlay cleanly. `const X_ENV = "KEY"`
	// is the name-as-a-constant indirection gateway.ts uses to stay import-free.
	direct := regexp.MustCompile(`(?:process\.)?env(?:\.([A-Z_][A-Z0-9_]{3,})|\[['"]([A-Z_][A-Z0-9_]{3,})['"]\])`)
	indirect := regexp.MustCompile(`(?m)^(?:export )?const [A-Z_]+_ENV = ['"]([A-Z_][A-Z0-9_]*)['"]`)
	seen := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".ts") || strings.HasSuffix(n, ".test.ts") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatalf("read ant/src/%s: %v", n, err)
		}
		for _, m := range direct.FindAllStringSubmatch(string(src), -1) {
			if m[1] != "" {
				seen[m[1]] = true
			} else {
				seen[m[2]] = true
			}
		}
		for _, m := range indirect.FindAllStringSubmatch(string(src), -1) {
			seen[m[1]] = true
		}
	}
	if len(seen) == 0 {
		t.Fatal("no `process.env.<KEY>` reads found in ant/src — update this extractor to how ant reads its env now")
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// A key ant reads must be a key buildArgs emits. cfg is filled so every
// conditional emission fires — an emission gated on an unset config value looks
// identical to no emission at all, which is the fault this pins.
func TestSpawnEnvCarriesEveryKeyAntReads(t *testing.T) {
	cfg := &core.Config{
		Timezone:          "UTC",
		Image:             "img:latest",
		AgentBackend:      "codex",
		ModelGatewayURL:   "http://litellm:4000",
	}
	args := strings.Join(buildArgs(cfg, nil, "c1", egressSpawn{}, "opus", "xhigh", 900000, false, false, "sk-virtual"), " ")

	for _, key := range antEnvReads(t) {
		if notArizukos[key] != "" {
			continue
		}
		if !strings.Contains(args, "-e "+key+"=") {
			t.Errorf("ant reads %s from the container env, and buildArgs never emits it — "+
				"the read silently falls to ant's own default (BUGS: ARIZUKO_BACKEND, "+
				"ARIZUKO_MODEL, RUNED_RUN_TIMEOUT all died this way). Emit it in buildArgs, "+
				"or stop reading it in ant/src.", key)
		}
	}
}

// The dual: an emission ant never reads is dead weight that reads as wiring.
// Keys consumed by something OTHER than ant in the container are listed, with
// who consumes each, so the list cannot grow by shrug.
func TestSpawnEnvEmitsNothingAntIgnores(t *testing.T) {
	consumedByOthers := map[string]string{
		"TZ":              "libc, for the agent's own clock",
		"HTTP_PROXY":      "the harness's HTTP client, via proxy-shim",
		"HTTPS_PROXY":     "the harness's HTTP client, via proxy-shim",
		"NO_PROXY":        "the harness's HTTP client, via proxy-shim",
		"NODE_OPTIONS":    "node itself, to load proxy-shim",
		"HOME":            "node, the harness CLIs, and the shell",
		"ARIZUKO_IS_ROOT": "ant/skills, read from the shell not from ant/src",
		"ARIZUKO_MODEL":   "ant/src via the seam's model field",
	}
	reads := map[string]bool{}
	for _, k := range antEnvReads(t) {
		reads[k] = true
	}
	cfg := &core.Config{
		Timezone: "UTC", Image: "img:latest", AgentBackend: "codex",
		ModelGatewayURL: "http://litellm:4000",
	}
	emitted := regexp.MustCompile(`^([A-Z_][A-Z0-9_]*)=`)
	args := buildArgs(cfg, nil, "c1", egressSpawn{}, "opus", "", 900000, false, false, "sk-virtual")
	for i, a := range args {
		if a != "-e" || i+1 >= len(args) {
			continue
		}
		m := emitted.FindStringSubmatch(args[i+1])
		if m == nil {
			continue
		}
		key := m[1]
		if reads[key] || consumedByOthers[key] != "" {
			continue
		}
		t.Errorf("buildArgs emits %s and nothing in ant/src reads it — either ant lost the "+
			"read (the key is now dead) or a non-ant consumer owns it, in which case name "+
			"that consumer in consumedByOthers.", key)
	}
}
