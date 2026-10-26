package runed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

// goldenCfg is one instance as runed sees it on a host: the release staged at
// app-src, an operator ~/.codex, a published web tree, one allowed mount root,
// and HOST_PROJECT_ROOT set, which is the deployment where runed itself runs in
// a container and every bind source must be rewritten to the host's path.
func goldenCfg(t *testing.T, root string) *core.Config {
	t.Helper()
	appSrc := filepath.Join(root, "app-src-image")
	write(t, filepath.Join(appSrc, "ant", "skills", "self", "MIGRATION_VERSION"), "9\n")
	write(t, filepath.Join(appSrc, "ant", "skills", "self", "SKILL.md"), "self\n")
	write(t, filepath.Join(appSrc, "ant", "output-styles", "slack.md"), "style\n")
	write(t, filepath.Join(appSrc, "ant", "skills", "migrate", "SKILL.md"), "migrate\n")
	write(t, filepath.Join(root, "codexhost", "auth.json"), "{}\n")
	write(t, filepath.Join(root, "codexhost", "config.toml"), "x=1\n")
	write(t, filepath.Join(root, "web", "pub", "arizuko", "index.html"), "docs\n")
	if err := os.MkdirAll(filepath.Join(root, "extra"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &core.Config{
		Name:              "krons",
		AgentBackend:      "claude",
		Timeout:           60 * time.Minute,
		IdleTimeout:       60 * time.Minute,
		Timezone:          "Europe/Prague",
		WebHost:           "krons.fiu.wtf",
		ProjectRoot:       root,
		HostProjectRoot:   "/srv/data/arizuko_krons",
		HostAppDir:        filepath.Join(root, "hostapp"),
		AppSrcDir:         appSrc,
		GroupsDir:         filepath.Join(root, "groups"),
		IpcDir:            filepath.Join(root, "ipc"),
		WebDir:            filepath.Join(root, "web"),
		HostCodexDir:      filepath.Join(root, "codexhost"),
		MountAllowedRoots: []string{filepath.Join(root, "extra")},
	}
	initializeRenderCore(t, cfg)
	return cfg
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// noHostSecrets empties every env-profile key on the host, so a golden is not
// at the mercy of the developer's own shell (readSecrets reads them all).
func noHostSecrets(t *testing.T) {
	t.Helper()
	for k := range store.EnvProfileKeys {
		t.Setenv(k, "")
	}
}

// mountLines renders the mount table the way a turn log does, so a golden reads
// as the table it pins.
func mountLines(mounts []core.Mount) []string {
	out := make([]string, 0, len(mounts))
	for _, m := range mounts {
		ro := ""
		if m.RO {
			ro = " ro"
		}
		out = append(out, m.Host+" -> "+m.Container+ro)
	}
	return out
}

// TestRenderTurnGolden is the behaviour-preservation gate: one representative
// wire request, and every byte the guest would receive named as a literal — the
// mount table in order, the process environment, the stdin frame and the egress
// allowlist. The expectations were read off the `container.Run` path before the
// renderer moved out of it, so a change here is a change the agent can see.
func TestRenderTurnGolden(t *testing.T) {
	noHostSecrets(t)
	root := t.TempDir()
	cfg := goldenCfg(t, root)
	folders := resolverFor(cfg)
	groupDir := filepath.Join(cfg.GroupsDir, "atlas/support")
	write(t, filepath.Join(groupDir, "PERSONA.md"), "You are Ada.\n")
	write(t, filepath.Join(groupDir, "SYSTEM.md"), "House rules.\n")
	write(t, filepath.Join(groupDir, "work.md"), "the work in flight\n")

	turn, err := renderTurn(cfg, folders, runedv1.RunRequest{
		Folder: "atlas/support", Topic: "t-42", ChatJID: "slack:T1/channel/C9",
		Channel: "slack", SessionID: "11111111-2222-3333-4444-555555555555",
		MessageBatch: "hello world", TriggerSender: "u1", CallerSub: "user:caller",
		TurnID: "turn-7", Model: "opus", Effort: "xhigh",
		ContainerConfig: map[string]any{
			"Timeout": int64(10 * time.Minute),
			"Mounts":  []any{map[string]any{"Host": filepath.Join(root, "extra"), "Container": "extra", "RO": false}},
		},
		ShareReadOnly: true, Egress: true, WebPublish: true, GHWrite: true,
		EgressAllowlist: []string{"github.com"},
		Secrets:         map[string]string{"ANTHROPIC_API_KEY": "sk-user"},
	}, "arizuko-krons-atlas-support-1700000000000", 20*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	if turn.Handle != "arizuko-krons-atlas-support-1700000000000" {
		t.Errorf("Handle=%q", turn.Handle)
	}
	if turn.Folder != "atlas/support" {
		t.Errorf("Folder=%q", turn.Folder)
	}

	// Every source is the path RUNED sees, and cfg carries HOST_DATA_DIR
	// (`/srv/data/arizuko_krons`) on purpose: the renderer must IGNORE it. It used
	// to translate every line through it, for the Docker substrate that handed the
	// path to the docker daemon on the host. virtiofsd is runed's own child, so a
	// translated path is a path that does not exist — which failed every turn on
	// krons (BUGS `L368`). A `/srv/data/` prefix in this list is that bug.
	wantMounts := []string{
		root + "/groups/atlas/support -> /home/node",
		selectedCore(t, cfg) + " -> /opt/arizuko/ant ro",
		selectedCore(t, cfg) + "/skills/base -> /home/node/.claude/skills/base ro",
		selectedCore(t, cfg) + "/skills/migrate -> /home/node/.claude/skills/migrate ro",
		selectedCore(t, cfg) + "/skills/self -> /home/node/.claude/skills/self ro",
		selectedCore(t, cfg) + "/CLAUDE.md -> /home/node/.claude/CLAUDE.md ro",
		selectedCore(t, cfg) + "/output-styles/brief.md -> /home/node/.claude/output-styles/brief.md ro",
		selectedCore(t, cfg) + "/output-styles/slack.md -> /home/node/.claude/output-styles/slack.md ro",
		root + "/groups/atlas/share -> /var/lib/share ro",
		root + "/ipc/atlas/support -> /run/ipc",
		root + "/groups/atlas/support/.codex -> /home/node/.codex",
		root + "/codexhost/auth.json -> /home/node/.codex/auth.json ro",
		root + "/codexhost/config.toml -> /home/node/.codex/config.toml ro",
		root + "/extra -> /mnt/extra ro",
		root + "/web/pub -> /var/lib/www ro",
		root + "/web/pub/atlas/support -> /home/node/public_html",
		root + "/web/priv/atlas/support -> /home/node/private_html",
	}
	for _, line := range wantMounts {
		if strings.HasPrefix(line, "/srv/data/") {
			t.Fatalf("this list asserts a translated host path (%q): the substrate cannot "+
				"reach it (BUGS L368)", line)
		}
	}
	if got := mountLines(turn.Mounts); strings.Join(got, "\n") != strings.Join(wantMounts, "\n") {
		t.Errorf("mount table\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantMounts, "\n"))
	}

	wantEnv := map[string]string{
		"TZ":                       "Europe/Prague",
		"ARIZUKO_MODEL":            "opus",
		"ARIZUKO_EFFORT":           "xhigh",
		"ARIZUKO_QUERY_TIMEOUT_MS": "1170000",
		"ARIZUKO_BACKEND":          "claude",
		"ARIZUKO_IS_ROOT":          "",
		"ARIZUKO_GH_WRITE":         "1",
	}
	if len(turn.Env) != len(wantEnv) {
		t.Errorf("env has %d keys, want %d: %v", len(turn.Env), len(wantEnv), turn.Env)
	}
	for k, want := range wantEnv {
		if turn.Env[k] != want {
			t.Errorf("env[%s]=%q want %q", k, turn.Env[k], want)
		}
	}

	wantStdin := `{"prompt":"[pending migration] Skills version 0 \u003c 9. Run /migrate (main group) to sync all groups.\nTopic session: t-42\n\u003cknowledge layer=\"work\"\u003e\nthe work in flight\n\u003c/knowledge\u003e\n\u003cuser id=\"u1\" /\u003e\n[solve] Invoke /solve now — classify, recall, match skills, then respond.\n\nhello world","sessionId":"11111111-2222-3333-4444-555555555555","chatJid":"slack:T1/channel/C9","groupFolder":"atlas/support","topic":"t-42","assistantName":"krons","secrets":{"ANTHROPIC_API_KEY":"sk-user"},"channelName":"slack","messageId":"turn-7","sender":"u1","persona":"You are Ada.","systemMd":"House rules."}`
	if string(turn.Stdin) != wantStdin {
		t.Errorf("stdin frame\n got: %s\nwant: %s", turn.Stdin, wantStdin)
	}

	if strings.Join(turn.Egress, ",") != "github.com,*" {
		t.Errorf("Egress=%v want [github.com *]", turn.Egress)
	}

	wantSettings := `{
  "cleanupPeriodDays": 3650000,
  "env": {
    "ARIZUKO_ASSISTANT_NAME": "krons",
    "ARIZUKO_GROUP_FOLDER": "atlas/support",
    "ARIZUKO_GROUP_NAME": "support",
    "ARIZUKO_GROUP_PARENT": "atlas",
    "ARIZUKO_IS_ROOT": "",
    "ARIZUKO_QUERY_TIMEOUT_MS": "1170000",
    "ARIZUKO_WORLD": "atlas",
    "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD": "1",
    "CLAUDE_CODE_DISABLE_AUTO_MEMORY": "0",
    "WEB_HOST": "krons.fiu.wtf",
    "WEB_PREFIX": "atlas"
  },
  "mcpServers": {
    "arizuko": {
      "args": [
        "STDIO",
        "UNIX-CONNECT:/run/ipc/gated.sock"
      ],
      "command": "socat"
    }
  },
  "outputStyle": "slack-thread",
  "permissions": {
    "defaultMode": "bypassPermissions"
  },
  "sandbox": {
    "enabled": false
  }
}
`
	got, err := os.ReadFile(filepath.Join(groupDir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("settings.json not rendered: %v", err)
	}
	if string(got) != wantSettings {
		t.Errorf("settings.json\n got:\n%s\nwant:\n%s", got, wantSettings)
	}
}

// TestRenderTurnGoldenElevated is the same gate for an operator /root turn on a
// gateway instance: every group's files mounted, the world shared instance-wide
// and writable, the docs subtree only (no web grant), the model credential
// withheld in favour of this turn's virtual key, and no ceiling.
func TestRenderTurnGoldenElevated(t *testing.T) {
	noHostSecrets(t)
	root := t.TempDir()
	cfg := goldenCfg(t, root)
	cfg.ModelGatewayURL = "http://litellm:4000"
	folders := resolverFor(cfg)

	turn, err := renderTurn(cfg, folders, runedv1.RunRequest{
		Folder: "main", ChatJID: "telegram:user/42", Channel: "telegram",
		MessageBatch: "do the thing", TurnID: "turn-9", Elevated: true,
		Secrets:         map[string]string{"ANTHROPIC_API_KEY": "sk-user", "GH_TOKEN": "ghp"},
		ModelGatewayKey: "sk-virtual-turn",
	}, "arizuko-krons-main-1700000000000", 0)
	if err != nil {
		t.Fatal(err)
	}

	wantMounts := []string{
		root + "/groups/main -> /home/node",
		selectedCore(t, cfg) + " -> /opt/arizuko/ant ro",
		selectedCore(t, cfg) + "/skills/base -> /home/node/.claude/skills/base ro",
		selectedCore(t, cfg) + "/skills/migrate -> /home/node/.claude/skills/migrate ro",
		selectedCore(t, cfg) + "/skills/self -> /home/node/.claude/skills/self ro",
		selectedCore(t, cfg) + "/CLAUDE.md -> /home/node/.claude/CLAUDE.md ro",
		selectedCore(t, cfg) + "/output-styles/brief.md -> /home/node/.claude/output-styles/brief.md ro",
		selectedCore(t, cfg) + "/output-styles/slack.md -> /home/node/.claude/output-styles/slack.md ro",
		root + "/groups/share -> /var/lib/share",
		root + "/ipc/main -> /run/ipc",
		root + "/groups/main/.codex -> /home/node/.codex",
		root + "/codexhost/auth.json -> /home/node/.codex/auth.json ro",
		root + "/codexhost/config.toml -> /home/node/.codex/config.toml ro",
		root + "/web/pub/arizuko -> /var/lib/www/arizuko ro",
		root + "/groups -> /var/lib/groups",
	}
	if got := mountLines(turn.Mounts); strings.Join(got, "\n") != strings.Join(wantMounts, "\n") {
		t.Errorf("mount table\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantMounts, "\n"))
	}

	wantEnv := map[string]string{
		"TZ":                "Europe/Prague",
		"ARIZUKO_BACKEND":   "claude",
		"MODEL_GATEWAY_URL": "http://litellm:4000",
		"MODEL_GATEWAY_KEY": "sk-virtual-turn",
		"ARIZUKO_IS_ROOT":   "1",
		"ARIZUKO_GH_WRITE":  "",
	}
	if len(turn.Env) != len(wantEnv) {
		t.Errorf("env has %d keys, want %d: %v", len(turn.Env), len(wantEnv), turn.Env)
	}
	for k, want := range wantEnv {
		if turn.Env[k] != want {
			t.Errorf("env[%s]=%q want %q", k, turn.Env[k], want)
		}
	}

	wantStdin := `{"prompt":"[pending migration] Skills version 0 \u003c 9. Run /migrate (main group) to sync all groups.\n[solve] Invoke /solve now — classify, recall, match skills, then respond.\n\ndo the thing","chatJid":"telegram:user/42","groupFolder":"main","assistantName":"krons","secrets":{"GH_TOKEN":"ghp"},"channelName":"telegram","messageId":"turn-9"}`
	if string(turn.Stdin) != wantStdin {
		t.Errorf("stdin frame\n got: %s\nwant: %s", turn.Stdin, wantStdin)
	}
	if turn.Egress != nil {
		t.Errorf("Egress=%v want nil (no allowlist, no grant)", turn.Egress)
	}
}

// TestPayloadCarriesOnlyTheAgentsKeys: the stdin frame is ant's ContainerInput
// and nothing else. A key the renderer adds here reaches every harness; a key it
// drops silently loses a context layer.
func TestPayloadCarriesOnlyTheAgentsKeys(t *testing.T) {
	noHostSecrets(t)
	root := t.TempDir()
	cfg := goldenCfg(t, root)
	turn, err := renderTurn(cfg, resolverFor(cfg), runedv1.RunRequest{
		Folder: "solo/inbox", ChatJID: "web:solo/inbox", Channel: "web",
		SessionID: "s1", MessageBatch: "hi", TriggerSender: "user:u1",
		TurnID: "m-1", Topic: "t", Secrets: map[string]string{"GH_TOKEN": "x"},
	}, "h", 0)
	if err != nil {
		t.Fatal(err)
	}

	var frame map[string]any
	if err := json.Unmarshal(turn.Stdin, &frame); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(frame))
	for k := range frame {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{
		"assistantName", "chatJid", "channelName", "groupFolder", "messageId",
		"prompt", "secrets", "sender", "sessionId", "topic",
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("frame keys\n got: %v\nwant: %v", got, want)
	}
}

// TestPreparePayloadPrependsTheContextLayers: the annotations the group dir
// carries land in front of the rendered batch, in the order the agent reads
// them, and the user's own prompt stays last.
func TestPreparePayloadPrependsTheContextLayers(t *testing.T) {
	root := t.TempDir()
	cfg := goldenCfg(t, root)
	groupDir := filepath.Join(cfg.GroupsDir, "atlas")
	write(t, filepath.Join(groupDir, "work.md"), "## Current task\nfixing bug")

	p := preparePayload(cfg, runedv1.RunRequest{
		Folder: "atlas", Topic: "inc-1", MessageBatch: "hello world",
	}, groupDir)

	for _, want := range []string{
		"[pending migration] Skills version 0 < 9.",
		"Topic session: inc-1",
		`<knowledge layer="work">`,
		"fixing bug",
		"[solve] Invoke /solve now",
	} {
		if !strings.Contains(p.Prompt, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p.Prompt)
		}
	}
	if !strings.HasSuffix(p.Prompt, "\n\nhello world") {
		t.Errorf("the rendered batch must stay last:\n%s", p.Prompt)
	}
	if strings.Index(p.Prompt, "Topic session") > strings.Index(p.Prompt, `layer="work"`) {
		t.Errorf("layer order changed:\n%s", p.Prompt)
	}
}

// A group with no extra layers gets the solve nudge and nothing else — an empty
// annotation must not appear as a blank layer.
func TestPreparePayloadWithoutLayers(t *testing.T) {
	root := t.TempDir()
	cfg := goldenCfg(t, root)
	groupDir := filepath.Join(cfg.GroupsDir, "atlas")
	write(t, filepath.Join(groupDir, ".claude", "skills", "self", "MIGRATION_VERSION"), "9\n")

	p := preparePayload(cfg, runedv1.RunRequest{Folder: "atlas", MessageBatch: "hi"}, groupDir)
	want := "[solve] Invoke /solve now — classify, recall, match skills, then respond.\n\nhi"
	if p.Prompt != want {
		t.Errorf("prompt=%q want %q", p.Prompt, want)
	}
	if p.Persona != "" || p.SystemMd != "" {
		t.Errorf("a group with no PERSONA.md/SYSTEM.md must send neither: %+v", p)
	}
}

// PERSONA.md and SYSTEM.md are read verbatim (trimmed) off the group dir, and
// the old SOUL.md name is migrated on read.
func TestPreparePayloadReadsThePersonaLayers(t *testing.T) {
	root := t.TempDir()
	cfg := goldenCfg(t, root)
	groupDir := filepath.Join(cfg.GroupsDir, "atlas")
	write(t, filepath.Join(groupDir, "SOUL.md"), "  warm and friendly\n")
	write(t, filepath.Join(groupDir, "SYSTEM.md"), "custom system prompt\n")

	p := preparePayload(cfg, runedv1.RunRequest{Folder: "atlas"}, groupDir)
	if p.Persona != "warm and friendly" {
		t.Errorf("persona=%q want the trimmed SOUL.md content", p.Persona)
	}
	if p.SystemMd != "custom system prompt" {
		t.Errorf("systemMd=%q", p.SystemMd)
	}
	if _, err := os.Stat(filepath.Join(groupDir, "PERSONA.md")); err != nil {
		t.Errorf("SOUL.md was not migrated to PERSONA.md: %v", err)
	}
}

func TestReadOptional(t *testing.T) {
	d := t.TempDir()
	if got := readOptional(filepath.Join(d, "nonexistent.md")); got != "" {
		t.Errorf("missing file returned %q, want empty", got)
	}
	p := filepath.Join(d, "PERSONA.md")
	write(t, p, "  be kind\n")
	if got := readOptional(p); got != "be kind" {
		t.Errorf("readOptional=%q want the trimmed content", got)
	}
}

// TestTurnEnvEmissions pins each conditional emission, in both directions: a key
// gated on an unset config value looks exactly like a key nothing emits, which
// is how ARIZUKO_BACKEND, ARIZUKO_MODEL and ARIZUKO_QUERY_TIMEOUT_MS each died
// silently (BUGS L60, spec 6/23).
func TestTurnEnvEmissions(t *testing.T) {
	full := &core.Config{
		Timezone: "UTC", AgentBackend: "codex", ModelGatewayURL: "http://gw:4000",
	}
	got := turnEnv(full, runedv1.RunRequest{
		Model: "opus", Effort: "xhigh", ModelGatewayKey: "sk-virtual",
		Elevated: true, GHWrite: true,
	}, 900000)
	want := map[string]string{
		"TZ":                       "UTC",
		"ARIZUKO_MODEL":            "opus",
		"ARIZUKO_EFFORT":           "xhigh",
		"ARIZUKO_QUERY_TIMEOUT_MS": "900000",
		"ARIZUKO_BACKEND":          "codex",
		"MODEL_GATEWAY_URL":        "http://gw:4000",
		"MODEL_GATEWAY_KEY":        "sk-virtual",
		"ARIZUKO_IS_ROOT":          "1",
		"ARIZUKO_GH_WRITE":         "1",
	}
	if len(got) != len(want) {
		t.Errorf("env has %d keys, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("env[%s]=%q want %q", k, got[k], v)
		}
	}

	bare := turnEnv(&core.Config{Timezone: "UTC"}, runedv1.RunRequest{}, 0)
	wantBare := map[string]string{
		"TZ": "UTC",
		// Always emitted, "1" or "", so each gate reads present-and-false.
		"ARIZUKO_IS_ROOT":  "",
		"ARIZUKO_GH_WRITE": "",
	}
	if len(bare) != len(wantBare) {
		t.Errorf("bare env has %d keys, want %d: %v", len(bare), len(wantBare), bare)
	}
	for k, v := range wantBare {
		if bare[k] != v {
			t.Errorf("bare env[%s]=%q want %q", k, bare[k], v)
		}
	}
}

// TestQueryTimeoutMs: the agent's query timeout is 30s below the ceiling,
// floored at 60s, and 0 when there is no ceiling — so the agent aborts and
// delivers a graceful summary before the substrate's hard kill.
func TestQueryTimeoutMs(t *testing.T) {
	cases := []struct {
		ttl  time.Duration
		want int64
	}{
		{0, 0},                                 // no ceiling → unset, agent default
		{20 * time.Minute, 19*60_000 + 30_000}, // 20m − 30s = 19m30s
		{70 * time.Second, 60_000},             // 70s − 30s = 40s, floored to 60s
		{30 * time.Second, 60_000},             // below the floor → 60s
	}
	for _, c := range cases {
		if got := queryTimeoutMs(c.ttl); got != c.want {
			t.Errorf("queryTimeoutMs(%s)=%d want %d", c.ttl, got, c.want)
		}
	}
}

// TestGroupConfigIgnoresUnknownKeys: live rows still carry keys from removed
// features, and a decode that failed on one would stop every turn in that
// folder — which is what let those removals ship with no data migration.
func TestGroupConfigIgnoresUnknownKeys(t *testing.T) {
	gc := groupConfig(map[string]any{
		"MaxChildren": 16,
		"Timeout":     int64(90 * time.Second),
		"Mounts":      []any{map[string]any{"Host": "/srv/x", "Container": "x", "RO": true}},
	})
	if gc.Timeout != 90*time.Second {
		t.Errorf("Timeout=%s want 90s", gc.Timeout)
	}
	if len(gc.Mounts) != 1 || gc.Mounts[0].Host != "/srv/x" || !gc.Mounts[0].RO {
		t.Errorf("Mounts=%+v want one RO /srv/x", gc.Mounts)
	}
	if got := groupConfig(nil); got.Timeout != 0 || got.Mounts != nil {
		t.Errorf("groupConfig(nil)=%+v want the zero value", got)
	}
}

// TestEgressAllowlistAppliesTheGrant: the `egress` grant appends "*", which the
// proxy translates to allow-all; without the grant the resolved allowlist rides
// through untouched and the request's own slice is never mutated.
func TestEgressAllowlistAppliesTheGrant(t *testing.T) {
	resolved := []string{"api.acme.com", "github.com"}
	req := runedv1.RunRequest{EgressAllowlist: resolved}
	if got := egressAllowlist(req); strings.Join(got, ",") != "api.acme.com,github.com" {
		t.Errorf("no grant: %v want the resolved list", got)
	}
	req.Egress = true
	if got := egressAllowlist(req); strings.Join(got, ",") != "api.acme.com,github.com,*" {
		t.Errorf("with the grant: %v want the list plus *", got)
	}
	if strings.Join(resolved, ",") != "api.acme.com,github.com" {
		t.Errorf("the request's own slice was mutated: %v", resolved)
	}
	if got := egressAllowlist(runedv1.RunRequest{Egress: true}); strings.Join(got, ",") != "*" {
		t.Errorf("grant with no allowlist: %v want [*]", got)
	}
}
