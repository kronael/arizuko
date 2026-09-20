package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/substrate"
)

func TestWorldOf(t *testing.T) {
	cases := []struct {
		folder   string
		elevated bool
		want     string
	}{
		{"", false, ""},
		{"atlas", false, "atlas"},
		{"atlas/support", false, "atlas"},
		{"atlas/eng/sre", false, "atlas"},
		{"anything", true, ""}, // elevated /root spans all worlds → ""
	}
	for _, c := range cases {
		if got := worldOf(c.folder, c.elevated); got != c.want {
			t.Errorf("worldOf(%q, %v) = %q, want %q", c.folder, c.elevated, got, c.want)
		}
	}
}

func TestPickIP_ValidSubnet(t *testing.T) {
	subnet := "10.42.7.0/24"
	for range 20 {
		ip, err := PickIP(subnet)
		if err != nil {
			t.Fatalf("PickIP error: %v", err)
		}
		if !strings.HasPrefix(ip, "10.42.7.") {
			t.Errorf("ip %q not in subnet %s", ip, subnet)
		}
		// Must not be .0, .1, .2, or .255
		oct := ip[len("10.42.7."):]
		for _, banned := range []string{"0", "1", "2", "255"} {
			if oct == banned {
				t.Errorf("reserved address returned: %s", ip)
			}
		}
	}
}

func TestPickIP_InvalidSubnet(t *testing.T) {
	if _, err := PickIP("not-a-cidr"); err == nil {
		t.Error("expected error for invalid CIDR")
	}
	// /31 too small
	if _, err := PickIP("10.0.0.0/31"); err == nil {
		t.Error("expected error for /31")
	}
}

func TestPickIP_IPv6Rejected(t *testing.T) {
	if _, err := PickIP("::1/128"); err == nil {
		t.Error("expected error for IPv6")
	}
}

// Effective is the one verdict on egress isolation: off by choice carries a
// reason and no error; an image with any other input missing is an error that
// names every missing input; a full config delivers the declaration.
func TestEffective(t *testing.T) {
	e := EgressConfig{}
	eff, err := e.Effective()
	if err != nil {
		t.Fatalf("isolation off by choice is not an error: %v", err)
	}
	if eff.Delivered.Egress != substrate.EgressNone || eff.Reason == "" {
		t.Errorf("zero config must deliver egress none with a reason, got %+v", eff)
	}
	if eff.Declared != Profile {
		t.Errorf("declared must be the substrate's ceiling, got %+v", eff.Declared)
	}

	e.Image = "arizuko-egress:latest"
	_, err = e.Effective()
	if err == nil {
		t.Fatal("an image with nothing else must be a misconfiguration error")
	}
	for _, want := range []string{"network prefix", "uplink network", "allowlist"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name the missing %q: %v", want, err)
		}
	}

	e.NetworkPrefix = "arizuko_krons"
	e.UplinkNetwork = "arizuko_krons_default"
	e.AllowlistFn = func(string) ([]string, error) { return nil, nil }
	eff, err = e.Effective()
	if err != nil {
		t.Fatalf("full config: %v", err)
	}
	if eff.Delivered != Profile || eff.Reason != "" {
		t.Errorf("full config must deliver the declaration with no reason, got %+v", eff)
	}
}

func TestBuildArgsWithEgress(t *testing.T) {
	cfg := &core.Config{Timezone: "UTC", Image: "img:latest"}
	eg := egressSpawn{
		Network:  "net-atlas",
		IP:       "10.99.5.42",
		ProxyURL: "http://10.99.5.7:3128",
	}
	args := buildArgs(cfg, nil, "c1", eg, "claude-fable-5", "", 1800000, false, false, "")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "--network net-atlas") {
		t.Errorf("missing --network: %s", joined)
	}
	if !strings.Contains(joined, "--ip 10.99.5.42") {
		t.Errorf("missing --ip: %s", joined)
	}
	if !strings.Contains(joined, "HTTP_PROXY=http://10.99.5.7:3128") {
		t.Errorf("missing HTTP_PROXY: %s", joined)
	}
	if !strings.Contains(joined, "NO_PROXY=localhost,127.0.0.1") {
		t.Errorf("missing NO_PROXY: %s", joined)
	}
	// model + query timeout MUST reach ant as real container env (-e), not just
	// settings.json — ant reads them from process.env.
	if !strings.Contains(joined, "ARIZUKO_MODEL=claude-fable-5") {
		t.Errorf("missing ARIZUKO_MODEL -e: %s", joined)
	}
	if !strings.Contains(joined, "ARIZUKO_QUERY_TIMEOUT_MS=1800000") {
		t.Errorf("missing ARIZUKO_QUERY_TIMEOUT_MS -e: %s", joined)
	}
}

// The backend selector must reach ant as real container env — the first
// ARIZUKO_BACKEND died precisely because buildArgs never emitted it
// (spec 6/7 § "Why this document exists").
func TestBuildArgsAgentBackend(t *testing.T) {
	cfg := &core.Config{Timezone: "UTC", Image: "img:latest", AgentBackend: "codex"}
	args := buildArgs(cfg, nil, "c4", egressSpawn{}, "", "", 0, false, false, "")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "ARIZUKO_BACKEND=codex") {
		t.Errorf("missing ARIZUKO_BACKEND -e: %s", joined)
	}
}

func TestBuildArgsNoBackendOmitsVar(t *testing.T) {
	cfg := &core.Config{Timezone: "UTC", Image: "img:latest"}
	args := buildArgs(cfg, nil, "c5", egressSpawn{}, "", "", 0, false, false, "")
	if strings.Contains(strings.Join(args, " "), "ARIZUKO_BACKEND") {
		t.Errorf("empty backend must omit -e ARIZUKO_BACKEND: %s", strings.Join(args, " "))
	}
}

func TestBuildArgsNoModelNoTimeout(t *testing.T) {
	cfg := &core.Config{Timezone: "UTC", Image: "img:latest"}
	args := buildArgs(cfg, nil, "c3", egressSpawn{}, "", "", 0, false, false, "")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "ARIZUKO_MODEL") {
		t.Errorf("empty model must omit -e ARIZUKO_MODEL: %s", joined)
	}
	if strings.Contains(joined, "ARIZUKO_QUERY_TIMEOUT_MS") {
		t.Errorf("zero timeout must omit -e ARIZUKO_QUERY_TIMEOUT_MS: %s", joined)
	}
}

// TestBuildArgsNoEgressIsUnchangedSpawn — the inert path. An instance with no
// egress configured gets no network, no proxy env and no --ip: exactly the
// spawn it had before egress existed. There is no default proxy URL to fall
// back to, because a spawn pointed at a proxy that was never started would
// fail every request while looking configured.
func TestBuildArgsNoEgressIsUnchangedSpawn(t *testing.T) {
	cfg := &core.Config{Timezone: "UTC", Image: "img:latest"}
	args := buildArgs(cfg, nil, "c2", egressSpawn{}, "", "", 0, false, false, "")
	joined := strings.Join(args, " ")
	for _, forbidden := range []string{"--network", "--ip", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("inert egress must not add %s: %s", forbidden, joined)
		}
	}
}

func TestHpPathEqualsProjectRoot(t *testing.T) {
	cfg := &core.Config{
		HostProjectRoot: "/host/inst",
		ProjectRoot:     "/srv/data/inst",
	}
	// local == ProjectRoot: rel="" → join gives HostProjectRoot
	got := hp(cfg, "/srv/data/inst")
	if got != "/host/inst" {
		t.Errorf("hp(local=ProjectRoot) = %q, want /host/inst", got)
	}
}

func TestSeedSettingsTier3NoWebPrefix(t *testing.T) {
	d := t.TempDir()
	cfg := &core.Config{Name: "Bot"}
	in := Input{Folder: "atlas/eng/sre"} // tier 3

	seedSettings(d, cfg, in, false)

	data, err := os.ReadFile(filepath.Join(d, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	env := s["env"].(map[string]any)
	if env["WEB_PREFIX"] != "" {
		t.Errorf("WEB_PREFIX (tier 3) = %v, want \"\"", env["WEB_PREFIX"])
	}
	if _, ok := env["ARIZUKO_TIER"]; ok {
		t.Error("ARIZUKO_TIER must be dropped (5/33 decision 10 — no tier rank)")
	}
}

// TestSeedSettingsPinsTranscriptRetention guards the fix for the stale
// chat-bound session bug: routd resumes a pinned transcript, so Claude Code's
// default 30-day retention must not prune it out from under the sessions table.
func TestSeedSettingsPinsTranscriptRetention(t *testing.T) {
	d := t.TempDir()
	cfg := &core.Config{Name: "Bot"}
	in := Input{Folder: "atlas"}

	seedSettings(d, cfg, in, false)

	data, err := os.ReadFile(filepath.Join(d, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	days, ok := s["cleanupPeriodDays"].(float64)
	if !ok {
		t.Fatalf("cleanupPeriodDays missing or not a number: %v", s["cleanupPeriodDays"])
	}
	if days < 3650 {
		t.Errorf("cleanupPeriodDays = %v, want >= 3650 (retention pinned)", days)
	}
}

func TestWriteTasksSnapshot_NonRootFilters(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir: filepath.Join(tmp, "groups"),
		IpcDir:    filepath.Join(tmp, "ipc"),
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	_ = cfg
	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}

	tasks := []core.Task{
		{ID: "t1", Owner: "atlas"},
		{ID: "t2", Owner: "atlas/support"},
		{ID: "t3", Owner: "other"},
	}
	WriteTasksSnapshot(folders, "atlas", false, tasks)

	ipcDir, _ := folders.IpcPath("atlas")
	data, err := os.ReadFile(filepath.Join(ipcDir, "current_tasks.json"))
	if err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	body := string(data)
	if strings.Contains(body, `"t3"`) {
		t.Error("non-owner task leaked into non-root snapshot")
	}
	if !strings.Contains(body, `"t1"`) {
		t.Error("owner task missing from non-root snapshot")
	}
}

func TestWriteGroupsSnapshot_NonRootClearsGroups(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir: filepath.Join(tmp, "groups"),
		IpcDir:    filepath.Join(tmp, "ipc"),
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	_ = cfg
	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}

	groups := []core.Group{{Folder: "alpha"}, {Folder: "beta"}}
	WriteGroupsSnapshot(folders, "atlas", false, groups)

	ipcDir, _ := folders.IpcPath("atlas")
	data, err := os.ReadFile(filepath.Join(ipcDir, "available_groups.json"))
	if err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	body := string(data)
	if strings.Contains(body, `"alpha"`) || strings.Contains(body, `"beta"`) {
		t.Error("group data leaked into non-root snapshot")
	}
}

func TestWriteGroupsSnapshot_RootKeepsGroups(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir: filepath.Join(tmp, "groups"),
		IpcDir:    filepath.Join(tmp, "ipc"),
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	_ = cfg
	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}

	groups := []core.Group{{Folder: "alpha"}, {Folder: "beta"}}
	WriteGroupsSnapshot(folders, "root", true, groups)

	ipcDir, _ := folders.IpcPath("root")
	data, err := os.ReadFile(filepath.Join(ipcDir, "available_groups.json"))
	if err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	if !strings.Contains(string(data), `"alpha"`) {
		t.Error("root snapshot missing groups")
	}
}

// BUGS L60: ARIZUKO_IS_ROOT lived only in settings.json's env block, which the
// SDK subprocess reads and ant does not — so ant's elevation check was false on
// every turn and the /var/lib/share skip never fired. It must be real container
// env, and present either way so the value is false rather than absent.
func TestBuildArgs_EmitsIsRootBothWays(t *testing.T) {
	cfg, _, _ := makeCfg(t)

	elevated := strings.Join(buildArgs(cfg, nil, "root1", egressSpawn{}, "", "", 0, true, false, ""), " ")
	if !strings.Contains(elevated, "ARIZUKO_IS_ROOT=1") {
		t.Errorf("elevated turn must carry ARIZUKO_IS_ROOT=1:\n%s", elevated)
	}

	plain := strings.Join(buildArgs(cfg, nil, "plain1", egressSpawn{}, "", "", 0, false, false, ""), " ")
	if !strings.Contains(plain, "ARIZUKO_IS_ROOT=") {
		t.Errorf("ordinary turn must still carry the var (empty):\n%s", plain)
	}
	if strings.Contains(plain, "ARIZUKO_IS_ROOT=1") {
		t.Errorf("ordinary turn must NOT be marked root:\n%s", plain)
	}
}

// The gateway pair reaches ant the same way as the backend selector: real
// container env, or the harness keeps its own credential and the gateway is
// silently bypassed. The URL is instance config; the key is the ONE routd
// minted for this turn, so it comes from the run and never from cfg — a
// process-wide key here would be the shared master key every spawn held.
func TestBuildArgsModelGateway(t *testing.T) {
	cfg := &core.Config{Timezone: "UTC", Image: "img:latest",
		ModelGatewayURL: "http://gw:4000", ModelGatewayKey: "master-key-abc123"}
	args := buildArgs(cfg, nil, "c6", egressSpawn{}, "", "", 0, false, false, "sk-this-turn")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-e MODEL_GATEWAY_URL=http://gw:4000", "-e MODEL_GATEWAY_KEY=sk-this-turn"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "master-key-abc123") {
		t.Errorf("the gateway MASTER key reached the container; it authorises the mint and stays in routd:\n%s", joined)
	}
}

// A spawn that arrives with no minted key on a gateway instance is refused
// before anything starts. spawnSecrets has already withheld the model
// credential from it, so running it produces a container that can reach no
// model and a failure that reads as the harness's.
func TestRunRefusesAGatewaySpawnWithNoKey(t *testing.T) {
	cfg := &core.Config{Timezone: "UTC", Image: "img:latest", ModelGatewayURL: "http://gw:4000"}
	out := Run(cfg, &groupfolder.Resolver{GroupsDir: t.TempDir()}, Input{Folder: "atlas"})
	if out.Status != "error" || !out.Terminal {
		t.Fatalf("Run = %+v; a gateway spawn with no virtual key must fail terminally", out)
	}
	if !strings.Contains(out.Error, "virtual key") {
		t.Errorf("Run error %q does not name the missing virtual key", out.Error)
	}
}

// Unset is today's argv: the two fields add nothing at all.
func TestBuildArgsNoGatewayOmitsVars(t *testing.T) {
	cfg := &core.Config{Timezone: "UTC", Image: "img:latest"}
	if got := strings.Join(buildArgs(cfg, nil, "c7", egressSpawn{}, "", "", 0, false, false, ""), " "); strings.Contains(got, "MODEL_GATEWAY") {
		t.Errorf("unset gateway must add no -e: %s", got)
	}
}
