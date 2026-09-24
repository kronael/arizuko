package runed

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/diary"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/router"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// payload is the agent's stdin frame — ant/src/index.ts ContainerInput. The
// field ORDER is the wire order, so a reordering here reorders the JSON the
// agent parses; keep it beside the tags it names.
type payload struct {
	Prompt    string            `json:"prompt"`
	SessionID string            `json:"sessionId,omitempty"`
	ChatJID   string            `json:"chatJid"`
	Folder    string            `json:"groupFolder"`
	Topic     string            `json:"topic,omitempty"`
	AsstName  string            `json:"assistantName,omitempty"`
	Secrets   map[string]string `json:"secrets,omitempty"`
	Channel   string            `json:"channelName,omitempty"`
	MessageID string            `json:"messageId,omitempty"`
	Sender    string            `json:"sender,omitempty"`
	Persona   string            `json:"persona,omitempty"`
	SystemMd  string            `json:"systemMd,omitempty"`
}

// renderTurn is the ONE renderer: the wire request in, one Turn out. Every fact
// a guest receives is decided here — the mount table, the process environment,
// the stdin frame and the egress allowlist — so a substrate delivers bytes and
// names and reads no grant, persona or session id (spec 6/41 §"The renderer").
// ttl is the run ceiling the caller armed ctx with; 0 means no ceiling.
//
// It also lands the per-spawn files the mounts point at: the group dir, the
// gateway caps, .claude/settings.json and the refreshed platform skills.
func renderTurn(
	cfg *core.Config, folders *groupfolder.Resolver,
	req runedv1.RunRequest, handle string, ttl time.Duration,
) Turn {
	folder := string(req.Folder)
	gc := groupConfig(req.ContainerConfig)
	groupDir, _ := folders.GroupPath(folder)
	os.MkdirAll(groupDir, 0o755)
	container.WriteGatewayCaps(groupDir, cfg)

	queryTimeout := queryTimeoutMs(ttl)
	mounts := buildMounts(cfg, folders, req, gc, groupDir)
	seedSettings(filepath.Join(groupDir, ".claude"), cfg, req, queryTimeout)

	p := preparePayload(cfg, req, groupDir)
	// The payload carries MODEL CREDENTIALS ONLY: the operator anchors from the
	// host .env (store.EnvProfileKeys) as the base, with the trigger user's own
	// env-profile keys overlaid, so a web-chat user's BYOA key shadows the anchor
	// for that spawn. Anchors stay as the floor so a user without one still
	// reaches the LLM.
	//
	// A capability credential arrives here only when a `shell:env:<KEY>` grant
	// names it — routd resolves that per key and adds nothing without a row.
	// Every other secret is broker-resolved per tool call inside
	// ipc.CallConnectorTool, so it lives in the connector subprocess's env on the
	// host (spec 5/13 §Trust model).
	p.Secrets = spawnSecrets(cfg, req.Secrets)
	p.AsstName = cfg.Name
	stdin, _ := json.Marshal(p)

	return Turn{
		Handle: handle,
		Folder: folder,
		Mounts: mounts,
		Env:    turnEnv(cfg, req, queryTimeout),
		Stdin:  stdin,
		Egress: egressAllowlist(req),
	}
}

// turnEnv is the agent's process environment.
//
// ant reads every one of these from process.env, so a key this map omits is
// silently dead in the guest: settings.json's env block reaches the SDK
// subprocess, not ant itself. Cost: sloth's fable override and every
// RUNED_RUN_TIMEOUT bump were dead until 2026-06-09 (ARIZUKO_MODEL,
// ARIZUKO_QUERY_TIMEOUT_MS), the first ARIZUKO_BACKEND died the same way, and
// ARIZUKO_IS_ROOT read undefined on EVERY turn while it was written only into
// settings.json (BUGS L60, spec 6/23).
func turnEnv(cfg *core.Config, req runedv1.RunRequest, queryTimeout int64) map[string]string {
	env := map[string]string{"TZ": cfg.Timezone}
	if req.Model != "" {
		env["ARIZUKO_MODEL"] = req.Model
	}
	if req.Effort != "" {
		env["ARIZUKO_EFFORT"] = req.Effort
	}
	if queryTimeout > 0 {
		env["ARIZUKO_QUERY_TIMEOUT_MS"] = strconv.FormatInt(queryTimeout, 10)
	}
	if cfg.AgentBackend != "" {
		env["ARIZUKO_BACKEND"] = cfg.AgentBackend
	}
	// The URL is instance config; the key is this ONE turn's, minted by routd
	// and dead when the turn ends (spec 6/23), so it arrives with the request
	// and is never read from cfg.
	if cfg.ModelGatewayURL != "" {
		env["MODEL_GATEWAY_URL"] = cfg.ModelGatewayURL
		env["MODEL_GATEWAY_KEY"] = req.ModelGatewayKey
	}
	// Always emitted, "1" or "", so each gate reads present-and-false rather
	// than absent-and-ambiguous.
	env["ARIZUKO_IS_ROOT"] = flag(req.Elevated)
	env["ARIZUKO_GH_WRITE"] = flag(req.GHWrite)
	return env
}

// flag renders a capability decision as the "1"-or-empty pair the guest reads.
func flag(on bool) string {
	if on {
		return "1"
	}
	return ""
}

// egressAllowlist is the proxy's allowlist with the grants applied: the
// `egress` grant (5/33-paths-roles, resolved by routd from /root or an operator
// delegation) appends "*" so the turn's own proxy allows every host. No grant →
// the inherited allowlist only. Cloned, so the rendered list never aliases the
// request's.
func egressAllowlist(req runedv1.RunRequest) []string {
	if !req.Egress {
		return req.EgressAllowlist
	}
	return append(slices.Clone(req.EgressAllowlist), "*")
}

// groupConfig decodes the opaque container_config the request forwards. Keys
// core.GroupConfig does not name are ignored rather than fatal — live rows
// still carry leftovers from removed features, which is what let those
// removals ship without a data migration.
func groupConfig(raw map[string]any) core.GroupConfig {
	var gc core.GroupConfig
	if len(raw) == 0 {
		return gc
	}
	b, _ := json.Marshal(raw)
	_ = json.Unmarshal(b, &gc)
	return gc
}

// queryTimeoutMs derives the agent's in-guest query timeout from the run
// ceiling: 30s below it so the agent delivers a graceful summary BEFORE the
// hard kill, floored at 60s. No ceiling means 0: the agent's own default.
func queryTimeoutMs(ttl time.Duration) int64 {
	if ttl <= 0 {
		return 0
	}
	q := max(ttl-30*time.Second, 60*time.Second)
	return q.Milliseconds()
}

// preparePayload builds the stdin frame: the context layers this turn reads off
// the group dir, prefixed onto the rendered prompt.
func preparePayload(cfg *core.Config, req runedv1.RunRequest, groupDir string) payload {
	p := payload{
		Prompt:    req.MessageBatch,
		SessionID: req.SessionID,
		ChatJID:   req.ChatJID,
		Folder:    string(req.Folder),
		Topic:     req.Topic,
		Channel:   req.Channel,
		MessageID: req.TurnID,
		Sender:    req.TriggerSender,
	}
	var ann []string
	latest := container.MigrationVersion(
		filepath.Join(cfg.EffectiveAppSrcDir(), "ant", "skills", "self", "MIGRATION_VERSION"))
	agent := container.MigrationVersion(
		filepath.Join(groupDir, ".claude", "skills", "self", "MIGRATION_VERSION"))
	if agent < latest {
		ann = append(ann, fmt.Sprintf(
			"[pending migration] Skills version %d < %d. "+
				"Run /migrate (main group) to sync all groups.",
			agent, latest))
	}
	if p.Topic != "" {
		ann = append(ann, "Topic session: "+p.Topic)
	}
	if ep := container.ReadRecentEpisodes(groupDir); ep != "" {
		ann = append(ann, ep)
	}
	if d := diary.Read(groupDir, 14); d != "" {
		ann = append(ann, d)
	}
	if wk := readOptional(filepath.Join(groupDir, "work.md")); wk != "" {
		ann = append(ann,
			"<knowledge layer=\"work\">\n"+wk+"\n</knowledge>")
	}
	if uc := router.UserContextXml(p.Sender, groupDir); uc != "" {
		ann = append(ann, uc)
	}

	// Auto-migrate the old SOUL.md name to PERSONA.md on read.
	personaPath := filepath.Join(groupDir, "PERSONA.md")
	soulPath := filepath.Join(groupDir, "SOUL.md")
	if _, err := os.Stat(personaPath); err != nil {
		if _, err := os.Stat(soulPath); err == nil {
			_ = os.Rename(soulPath, personaPath)
		}
	}
	p.Persona = readOptional(personaPath)
	p.SystemMd = readOptional(filepath.Join(groupDir, "SYSTEM.md"))

	ann = append(ann,
		"[solve] Invoke /solve now — classify, recall, "+
			"match skills, then respond.")

	if len(ann) > 0 {
		p.Prompt = strings.Join(ann, "\n") + "\n\n" + p.Prompt
	}
	return p
}

func readOptional(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
