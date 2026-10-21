package runed

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// seedSettings renders the group's .claude/settings.json for this turn and
// refreshes the platform-owned files beside it. The guest reads the file off
// its home mount, so this runs before the guest starts.
func seedSettings(
	claudeDir string, cfg *core.Config,
	req runedv1.RunRequest, queryTimeout int64,
) {
	container.SeedOutputStyles(cfg, claudeDir)
	container.SeedMigrateSkill(cfg, claudeDir)
	folder := string(req.Folder)
	elevated := req.Elevated
	fp := filepath.Join(claudeDir, "settings.json")
	var settings map[string]any
	if data, err := os.ReadFile(fp); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			// The merge below starts from an empty map, so a file that will not parse
			// is REPLACED rather than merged and the operator's keys are gone.
			slog.Warn("settings.json will not parse; it is rewritten from defaults, not merged",
				"path", fp, "folder", folder, "err", err)
		}
	}
	if settings == nil {
		settings = map[string]any{}
	}

	env, _ := settings["env"].(map[string]any)
	if env == nil {
		env = map[string]any{
			"CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD": "1",
			"CLAUDE_CODE_DISABLE_AUTO_MEMORY":              "0",
		}
	}

	env["WEB_HOST"] = cfg.WebHost
	env["ARIZUKO_ASSISTANT_NAME"] = cfg.Name
	env["ARIZUKO_IS_ROOT"] = ""
	// WEB_PREFIX tells the agent its publishing surface: "pub" for an elevated /root
	// turn (served at /pub/<folder>/), the world subdomain when the folder holds
	// web:publish (its world's vhost — a top-level world IS its own world), "" with no
	// web grant (no mount, no surface). Spec: specs/5/V-web-vhosts.md.
	switch {
	case elevated:
		env["ARIZUKO_IS_ROOT"] = "1"
		env["WEB_PREFIX"] = "pub"
	case req.WebPublish:
		env["WEB_PREFIX"] = worldOf(folder, elevated)
	default:
		env["WEB_PREFIX"] = ""
	}
	env["ARIZUKO_GROUP_FOLDER"] = folder
	env["ARIZUKO_GROUP_NAME"] = groupfolder.NameOf(folder)
	env["ARIZUKO_GROUP_PARENT"] = groupfolder.ParentOf(folder)
	env["ARIZUKO_WORLD"] = worldOf(folder, elevated)
	// ARIZUKO_TIER (the depth rank the agent used to see) is dropped — 5/33-paths-roles decision 10:
	// identity is world + path + granted capabilities, never a rank. worldOf survives
	// ONLY above for WEB_PREFIX (the vhost coordinate, not authz).
	if queryTimeout > 0 {
		env["ARIZUKO_QUERY_TIMEOUT_MS"] = strconv.FormatInt(queryTimeout, 10)
	}
	if req.Channel != "" {
		// nil pane lookup: no caller resolves an open Slack assistant pane, so
		// the slack-pane row of 5/Y is reachable only through this argument.
		if name := pickOutputStyle(req.Channel, req.ChatJID, req.Topic, nil); name != "" {
			settings["outputStyle"] = name
		}
	}
	settings["env"] = env

	// Claude Code's permission + sandbox layer is asserted by the platform, not the
	// agent: arizuko isolates via the egress proxy, the substrate and the routd MCP
	// socket. Write both authoritatively every spawn, so a stray agent-added
	// permissions block cannot accumulate and a future SDK default cannot silently
	// re-enable the sandbox.
	settings["permissions"] = map[string]any{"defaultMode": "bypassPermissions"}
	settings["sandbox"] = map[string]any{"enabled": false}

	// routd's `sessions` table pins one transcript per chat and resumes it, and Claude
	// Code's default 30-day retention would prune that transcript out from under it →
	// "No conversation found" → silent context loss. There is NO disable value (0
	// fails validation, min is 1 day), so a ~10000-year number is the only way never
	// to prune.
	settings["cleanupPeriodDays"] = 3650000

	servers, _ := settings["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["arizuko"] = map[string]any{
		"command": "socat",
		"args":    []string{"STDIO", "UNIX-CONNECT:/run/ipc/gated.sock"},
	}
	settings["mcpServers"] = servers

	data, _ := json.MarshalIndent(settings, "", "  ")
	tmp := fp + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		// settings.json carries the mcpServers socket + outputStyle; without it
		// the agent spawns with no reply/send tools and silently returns 0
		// messages ("typing but no reply"). A permission-denied here means the
		// group home isn't owned by the run uid — a misconfigured (hand-mkdir'd,
		// root-owned) home. Fail LOUD with the folder so it's not invisible.
		slog.Error("group home not writable — agent will run config-less and not reply; chown the home to the run uid",
			"folder", folder, "path", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, fp); err != nil {
		slog.Warn("failed to rename settings", "from", tmp, "to", fp, "err", err)
	}
	slog.Debug("settings seeded", "path", fp)
}

// pickOutputStyle resolves the agent's outputStyle setting for one turn: derives
// <platform>-<surface> from the JID/topic/pane signal, falling back to <platform>
// (spec 5/Y-output-styles-per-surface). The agent image ships every per-surface file, and per-group operator
// overrides at the same path take precedence automatically. No host-side existence
// check — name selection and file loading are separate concerns.
func pickOutputStyle(
	channel, chatJID, topic string,
	paneLookup func(channelID string) bool,
) string {
	if channel == "" {
		return ""
	}
	if surface := deriveSurface(channel, chatJID, topic, paneLookup); surface != "" {
		return channel + "-" + surface
	}
	return channel
}

// deriveSurface maps (channel, chatJID, topic, pane) to a surface
// suffix. Returns "" when the platform has no per-surface split.
// Mirrors the table in specs/5/Y-output-styles-per-surface.md.
func deriveSurface(
	channel, chatJID, topic string,
	paneLookup func(channelID string) bool,
) string {
	switch channel {
	case "slack":
		p, ok := chanlib.ParseSlackJID(chatJID)
		if !ok {
			return ""
		}
		switch p.Kind {
		case "dm":
			return "dm"
		case "group":
			return "channel"
		case "channel":
			if paneLookup != nil && paneLookup(p.ID) {
				return "pane"
			}
			if topic != "" {
				return "thread"
			}
			return "channel"
		}
	case "telegram":
		rest := strings.TrimPrefix(chatJID, "telegram:")
		if rest == chatJID {
			return ""
		}
		kind, _, ok := strings.Cut(rest, "/")
		if !ok {
			return ""
		}
		switch kind {
		case "user":
			return "dm"
		case "group":
			return "group"
		}
	case "discord":
		rest := strings.TrimPrefix(chatJID, "discord:")
		if rest == chatJID {
			return ""
		}
		kind, _, ok := strings.Cut(rest, "/")
		if !ok {
			return ""
		}
		if kind == "dm" {
			return "dm"
		}
		return "channel"
	}
	return ""
}
