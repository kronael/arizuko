package runed

import (
	"maps"
	"os"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// readSecrets is the operator platform fallback: every env-profile model
// key set in the host .env is passed through to the guest. Iterating
// store.EnvProfileKeys (the canonical set) instead of a local literal keeps
// it from drifting — the list previously carried only 2 of the 4 keys, so a
// platform OPENAI_API_KEY / CODEX_API_KEY in .env never reached the agent.
func readSecrets() map[string]string {
	var s map[string]string
	for k := range store.EnvProfileKeys {
		if v := os.Getenv(k); v != "" {
			if s == nil {
				s = make(map[string]string, len(store.EnvProfileKeys))
			}
			s[k] = v
		}
	}
	return s
}

// spawnSecrets is the one secret map a guest receives: the operator anchors
// under the trigger user's own env-profile rows. A configured model gateway
// holds the real credential and the turn presents MODEL_GATEWAY_KEY instead
// (turnEnv), so the model keys are withheld from BOTH sources — a filter on
// readSecrets alone would still hand the user's own row to the guest
// (spec 6/23 Fault 1). Every other key, a `shell:env:<KEY>` grant, passes
// through unchanged; with no gateway the map is exactly the merge.
func spawnSecrets(cfg *core.Config, override map[string]string) map[string]string {
	s := mergeSecrets(readSecrets(), override)
	if cfg.ModelGatewayURL == "" {
		return s
	}
	for k := range store.EnvProfileKeys {
		delete(s, k)
	}
	return s
}

// SpawnHoldsModelCredential reports whether every spawn from this
// configuration carries the operator model credential in the guest's env: no
// gateway to withhold it (spawnSecrets) and an anchor set. runed logs it once
// at start. The scrub in ant/src/guards.ts is not a boundary: every process in
// the guest shares one uid, nothing sets hidepid, and /proc/<pid>/environ
// is readable across siblings, so a shell reads the harness's env whatever the
// harness strips from its child (BUGS L131).
func SpawnHoldsModelCredential(cfg *core.Config) bool {
	return cfg.ModelGatewayURL == "" && readSecrets() != nil
}

// mergeSecrets overlays override onto base (override wins per key) and returns
// the combined env map. base is the operator anchors (floor); override is the
// trigger user's own env-profile keys (BYOA) — so a user's own ANTHROPIC_API_KEY
// shadows the operator anchor, while a user with no BYOA key keeps the anchor.
// Both sides hold store.EnvProfileKeys only; capability credentials are brokered.
func mergeSecrets(base, override map[string]string) map[string]string {
	if base == nil {
		base = make(map[string]string, len(override))
	}
	maps.Copy(base, override)
	return base
}
