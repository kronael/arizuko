//go:build smoke

// Live-instance smoke checks — verify a DEPLOYED instance is actually up.
//
// ─── How to run ──────────────────────────────────────────────────────────────
//
// The default run is FREE: it only reads container health and hits /health
// endpoints. It spawns NO agent turns and spends NO model credits.
//
//	# everything free (container health + daemon /health + egress image):
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
//	-run TestSmoke_EgressImage       # touched egress / the per-spawn proxy
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
	"fmt"
	"os"
	"os/exec"
	"strconv"
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
	case "teled", "discd", "slakd", "emaid", "whapd":
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

// TestSmoke_EgressImage proves the instance can still start a spawn's egress
// proxy. There is no long-lived proxy container to probe any more — runed
// starts one per spawn and stops it with the turn (spec 6/24) — so the
// post-deploy question is whether the image it starts them FROM is present.
// A missing image fails every spawn, which is exactly the outage a redeploy
// that forgot `make images` would cause. Skips when the instance configures
// no egress (EGRESS_IMAGE unset in its .env). FREE.
func TestSmoke_EgressImage(t *testing.T) {
	inst := smokeInstance()
	env, err := os.ReadFile("/srv/data/arizuko_" + inst + "/.env")
	switch {
	case os.IsNotExist(err):
		t.Skipf("instance %q is not deployed on this host", inst)
	case err != nil:
		// The .env is root-owned. A Skip here would read as a pass and hide
		// the one check that catches a redeploy with no egress image.
		t.Fatalf("instance %q: read .env: %v — run this tier with sudo", inst, err)
	}
	image := envValue(string(env), "EGRESS_IMAGE")
	if image == "" {
		t.Skipf("instance %q has no egress configured", inst)
	}
	if _, err := docker(t, "image", "inspect", image); err != nil {
		t.Fatalf("egress image %q missing on this host — every spawn would fail: %v", image, err)
	}
}

// envValue reads KEY=value out of a .env body. Returns "" when unset.
func envValue(env, key string) string {
	for line := range strings.SplitSeq(env, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
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

	// ONE blocking POST: the chat-token JSON surface takes ?wait=<sec> and returns
	// once an assistant frame lands, so there is no client poll loop and the
	// server's own 120s cap bounds the spend. This exercises the delivery path
	// (webd → routd → container → reply), which is what the smoke tier is for;
	// the MCP door onto the same queue is covered by webd's unit tests.
	res := chatTokenPost(t, base, "smoke: reply with the single word ACK", 120)
	turnID := res["turn_id"]
	if turnID == nil {
		t.Fatal("chat POST returned no turn_id")
	}
	if res["assistant"] == nil {
		t.Fatalf("no assistant frame for turn %v within deadline — dispatch stalled", turnID)
	}
	t.Logf("e2e OK: turn %v produced an assistant frame", turnID)
}

// chatTokenPost makes ONE blocking POST /chat/<token>?wait=<sec> and returns the
// decoded JSON body ({user, turn_id, status, assistant?}). A single request whose
// client timeout outlives the server-side wait.
func chatTokenPost(t *testing.T, base, content string, wait int) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"content": content})
	url := fmt.Sprintf("%s?wait=%d", base, wait)
	// docker-free plain HTTP: the chat surface is public (behind proxyd).
	cmd := exec.Command("curl", "-sS", "--max-time", strconv.Itoa(wait+60),
		"-H", "content-type: application/json", "-d", string(body), url)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("chat POST: curl: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out.String()), &payload); err != nil {
		t.Fatalf("chat POST: decode %q: %v", out.String(), err)
	}
	return payload
}
