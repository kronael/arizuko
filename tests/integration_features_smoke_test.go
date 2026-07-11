//go:build smoke

// Live-instance smoke checks — verify a DEPLOYED instance is actually up.
//
// ─── How to run ──────────────────────────────────────────────────────────────
//
// The default run is FREE: it only reads container health and hits /health
// endpoints. It spawns NO agent turns and spends NO model credits.
//
//	# everything free (container health + daemon /health + crackbox egress):
//	go test ./tests/ -tags smoke -run TestSmoke -v
//
//	# pick a different instance (default krons):
//	SMOKE_INSTANCE=marinade go test ./tests/ -tags smoke -run TestSmoke -v
//
//	# this host needs sudo for docker:
//	SMOKE_DOCKER='sudo docker' go test ./tests/ -tags smoke -run TestSmoke -v
//
// RUN ONLY THE PART THAT COVERS WHAT YOU CHANGED — don't run the whole file
// out of habit. Each tier is an independent top-level test:
//
//	-run TestSmoke_ContainerHealth   # touched compose / a Dockerfile / a daemon boot / a daemon's /health
//	-run TestSmoke_CrackboxEgress    # touched crackbox / egress rules
//	-run TestSmoke_EndToEnd          # touched routing / dispatch / the agent (SEE BELOW)
//
// ─── The credit-spending tier ────────────────────────────────────────────────
//
// TestSmoke_EndToEnd sends ONE real message through a chat token and waits for
// ONE agent reply. That spawns a container and spends model credits. It is
// GATED: it Skips unless the operator explicitly opts in with
//
//	SMOKE_E2E=1 SMOKE_CHAT_URL=https://<host>/chat/<token> \
//	  go test ./tests/ -tags smoke -run TestSmoke_EndToEnd -v
//
// It sends exactly one message and makes exactly one server-blocking wait call
// (no client-side poll loop, hard deadline) so it cannot loop or drain the
// account. Only a human should ever trigger a full run that includes this tier.
//
// ─── Port model ──────────────────────────────────────────────────────────────
//
// Daemons listen on :8080 INSIDE their container and most publish no host port
// (per-container :8080 is collision-free on the docker network). So these
// checks go through `docker` — enumerate `arizuko_*_<instance>` containers and
// read their state — NOT a fixed host-port map, which drifts the moment a host
// runs more than one instance. Each daemon's docker healthcheck already IS a
// `wget http://127.0.0.1:8080/health` probe, so a `healthy` container proves
// its /health answers 200. Set SMOKE_DOCKER to override the docker binary
// (e.g. 'sudo docker').

package tests

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// smokeInstance returns the instance flavor under test (default krons).
func smokeInstance() string {
	if i := os.Getenv("SMOKE_INSTANCE"); i != "" {
		return i
	}
	return "krons"
}

// dockerArgs splits SMOKE_DOCKER (default "docker") so a host that needs
// `sudo docker` works without a wrapper script.
func dockerArgs() []string {
	d := os.Getenv("SMOKE_DOCKER")
	if d == "" {
		d = "docker"
	}
	return strings.Fields(d)
}

// docker runs a docker subcommand and returns trimmed stdout. A non-zero exit
// is returned as err with stderr folded into the message.
func docker(t *testing.T, args ...string) (string, error) {
	t.Helper()
	base := dockerArgs()
	cmd := exec.Command(base[0], append(base[1:], args...)...)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		return "", &dockerErr{args: args, stderr: strings.TrimSpace(errb.String()), err: err}
	}
	return strings.TrimSpace(out.String()), nil
}

type dockerErr struct {
	args   []string
	stderr string
	err    error
}

func (e *dockerErr) Error() string {
	return "docker " + strings.Join(e.args, " ") + ": " + e.err.Error() + ": " + e.stderr
}

// instanceContainers lists every container for the instance, most-specific
// first. A missing docker binary or a stopped instance skips the whole suite —
// there is nothing to smoke.
func instanceContainers(t *testing.T) []string {
	t.Helper()
	inst := smokeInstance()
	out, err := docker(t, "ps", "--filter", "name=arizuko_.*_"+inst, "--format", "{{.Names}}")
	if err != nil {
		t.Skipf("cannot list containers for %q (%v) — is the instance deployed and docker reachable?", inst, err)
	}
	if out == "" {
		t.Skipf("no running containers for instance %q — deploy it first", inst)
	}
	return strings.Split(out, "\n")
}

// isAdapter reports whether a container is a channel adapter (name ends in `d`
// and matches a known adapter). Adapters legitimately report unhealthy when the
// remote platform link is down (whapd unpaired, emaid IMAP unreachable), which
// is an OPERATOR credential issue, not a code regression — so the health tier
// warns on an unhealthy adapter instead of failing.
func isAdapter(container string) bool {
	// container names are arizuko_<daemon>_<flavor>; the daemon is field 1.
	parts := strings.SplitN(container, "_", 3)
	if len(parts) < 2 {
		return false
	}
	// adapter variants carry a `-suffix` (e.g. teled-rhias is a 2nd Telegram
	// bot) — match on the daemon prefix.
	daemon := parts[1]
	if i := strings.IndexByte(daemon, '-'); i >= 0 {
		daemon = daemon[:i]
	}
	switch daemon {
	case "teled", "discd", "slakd", "mastd", "bskyd", "reditd", "emaid", "linkd", "whapd", "twitd":
		return true
	}
	return false
}

// health returns the container's healthcheck status ("healthy"/"unhealthy"/
// "starting"), or "" when the image declares no healthcheck (sidecars like
// ttsd/kokoro/whisper) — those are liveness-only, checked as Running.
func health(t *testing.T, container string) string {
	t.Helper()
	out, err := docker(t, "inspect", "-f", "{{if .State.Health}}{{.State.Health.Status}}{{end}}", container)
	if err != nil {
		t.Errorf("inspect %s: %v", container, err)
		return ""
	}
	return out
}

// running reports whether the container's State.Running is true.
func running(t *testing.T, container string) bool {
	t.Helper()
	out, err := docker(t, "inspect", "-f", "{{.State.Running}}", container)
	if err != nil {
		t.Errorf("inspect %s: %v", container, err)
		return false
	}
	return out == "true"
}

// TestSmoke_ContainerHealth asserts every container for the instance is up.
// Core daemons must be `healthy`; a healthcheck-less sidecar must be Running;
// an unhealthy ADAPTER is a warning (platform link down = operator issue), not
// a failure. FREE — no model credits.
func TestSmoke_ContainerHealth(t *testing.T) {
	for _, c := range instanceContainers(t) {
		c := c
		t.Run(c, func(t *testing.T) {
			if !running(t, c) {
				t.Errorf("%s is not running", c)
				return
			}
			switch st := health(t, c); st {
			case "", "healthy":
				// no healthcheck (sidecar) → Running is enough; or healthy.
			case "starting":
				t.Logf("%s health=starting (still booting)", c)
			default: // unhealthy
				if isAdapter(c) {
					t.Logf("WARN %s health=%s — platform link likely down (operator: re-auth/re-pair)", c, st)
				} else {
					t.Errorf("%s health=%s, want healthy", c, st)
				}
			}
		})
	}
}

// TestSmoke_CrackboxEgress proves the egress proxy answers, exec'd from runed
// (the only daemon on crackbox's network). Skips when the instance runs no
// crackbox (CRACKBOX_ADMIN_API unset in its .env). FREE.
func TestSmoke_CrackboxEgress(t *testing.T) {
	inst := smokeInstance()
	env, err := os.ReadFile("/srv/data/arizuko_" + inst + "/.env")
	if err != nil || !strings.Contains(string(env), "CRACKBOX_ADMIN_API=") {
		t.Skipf("instance %q has no crackbox configured", inst)
	}
	runed := "arizuko_runed_" + inst
	out, err := docker(t, "exec", runed, "wget", "-qO-", "--timeout=3", "http://crackbox:3129/health")
	if err != nil {
		t.Fatalf("crackbox /health via %s: %v", runed, err)
	}
	if !strings.Contains(out, `"status":"ok"`) {
		t.Errorf("crackbox /health = %q, want ok", out)
	}
}

// TestSmoke_EndToEnd sends ONE real message through a chat token and waits for
// ONE agent reply. GATED behind SMOKE_E2E=1 — it spawns a container and spends
// model credits. One send + one server-blocking wait (no client poll loop, hard
// deadline) so it cannot loop or drain the account. Opt-in only.
func TestSmoke_EndToEnd(t *testing.T) {
	if os.Getenv("SMOKE_E2E") != "1" {
		t.Skip("credit-spending tier — set SMOKE_E2E=1 (and SMOKE_CHAT_URL) to run")
	}
	base := os.Getenv("SMOKE_CHAT_URL") // e.g. https://krons.fiu.wtf/chat/<token>
	if base == "" {
		t.Fatal("SMOKE_E2E=1 requires SMOKE_CHAT_URL=https://<host>/chat/<token>")
	}
	base = strings.TrimRight(base, "/")

	// 1) send_message → turn_id (one MCP call over the chat token).
	turnID := chatMCPCall(t, base, "send_message", map[string]any{
		"text": "smoke: reply with the single word ACK",
	})["turn_id"]
	if turnID == nil {
		t.Fatal("send_message returned no turn_id")
	}

	// 2) get_round wait=true — ONE blocking call, server-side ~5min deadline,
	// no client loop. Returns once an assistant frame lands.
	res := chatMCPCall(t, base, "get_round", map[string]any{
		"turn_id": turnID,
		"wait":    true,
	})
	frames, _ := res["frames"].([]any)
	if len(frames) == 0 {
		t.Fatalf("no assistant frames for turn %v within deadline — dispatch stalled", turnID)
	}
	t.Logf("e2e OK: turn %v produced %d assistant frame(s)", turnID, len(frames))
}

// chatMCPCall makes one JSON-RPC tools/call against a /chat/<token>/mcp
// surface and returns the tool result's structured content. A single request
// with a 6-minute client timeout that bounds the server's ~5min wait.
func chatMCPCall(t *testing.T, base, tool string, args map[string]any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	})
	// use docker-free plain HTTP: the chat surface is public (behind proxyd).
	req := exec.Command("curl", "-sS", "--max-time", "360",
		"-H", "content-type: application/json", "-d", string(body), base+"/mcp")
	var out, errb strings.Builder
	req.Stdout = &out
	req.Stderr = &errb
	if err := req.Run(); err != nil {
		t.Fatalf("%s: curl: %v: %s", tool, err, strings.TrimSpace(errb.String()))
	}
	var env struct {
		Result struct {
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(out.String()), &env); err != nil {
		t.Fatalf("%s: decode %q: %v", tool, out.String(), err)
	}
	if len(env.Error) > 0 {
		t.Fatalf("%s: rpc error: %s", tool, env.Error)
	}
	return env.Result.StructuredContent
}
