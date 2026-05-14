package gateway

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

// authSubPrefixes are the schemes that auth/oauth.go writes into
// auth_users.sub (see auth/oauth.go google: / github: / local:).
// Adapter senders (telegram:user/..., slack:user/..., bluesky:user/...)
// are per-platform user IDs and are NOT bound to auth_users rows, so
// they can't carry a per-user cap today. Interim until spec 6/5
// Caller shape lands and unifies the identifier.
var authSubPrefixes = []string{"google:", "github:", "local:"}

// callerSubOfMsg returns the user_sub to use for the per-user budget
// cap. Empty string disables the user-cap branch in budgetGate (folder
// cap still binds). Recognised schemes are only those that auth/oauth
// writes; anon: and adapter-prefixed senders return "".
func callerSubOfMsg(sender string) string {
	for _, p := range authSubPrefixes {
		if strings.HasPrefix(sender, p) {
			return sender
		}
	}
	return ""
}

// budgetGate is the pre-spawn check from spec 5/34. Returns a non-empty
// refusal message when today's spend is at or above the lower of the
// folder cap and (when known) the user cap. Returns "" when the turn is
// allowed to proceed.
//
// Cap == 0 means uncapped (the default), so a zero-cap folder always
// passes. Per-user cap composes with per-folder; the binding cap is the
// lower of the two non-zero values.
func (g *Gateway) budgetGate(folder, userSub string) string {
	if !g.cfg.CostCapsEnabled {
		return ""
	}
	folderCap, err := g.store.FolderCap(folder)
	if err != nil {
		slog.Warn("budget: FolderCap failed", "folder", folder, "err", err)
		return ""
	}
	userCap := 0
	if userSub != "" {
		userCap, err = g.store.UserCap(userSub)
		if err != nil {
			slog.Warn("budget: UserCap failed", "user_sub", userSub, "err", err)
			userCap = 0
		}
	}
	if folderCap == 0 && userCap == 0 {
		return ""
	}

	if folderCap > 0 {
		spent, err := g.store.SpendTodayFolder(folder)
		if err != nil {
			slog.Warn("budget: SpendTodayFolder failed", "folder", folder, "err", err)
		} else if spent >= folderCap {
			slog.Info("budget: folder cap exhausted; refusing turn",
				"folder", folder, "spent_cents", spent, "cap_cents", folderCap)
			return budgetMsg("channel", spent, folderCap)
		}
	}
	if userCap > 0 {
		spent, err := g.store.SpendTodayUser(userSub)
		if err != nil {
			slog.Warn("budget: SpendTodayUser failed", "user_sub", userSub, "err", err)
		} else if spent >= userCap {
			slog.Info("budget: user cap exhausted; refusing turn",
				"user_sub", userSub, "spent_cents", spent, "cap_cents", userCap)
			return budgetMsg("you", spent, userCap)
		}
	}
	return ""
}

func budgetMsg(scope string, spent, cap int) string {
	return fmt.Sprintf(
		"Budget reached for today (%s spent %d of %d cents). Resumes at 00:00 UTC.",
		scope, spent, cap)
}

// logCost is the single writer into cost_log. Both the Anthropic
// post-turn path and the external-LLM MCP hook funnel through here so
// the CostRow shape lives in one place.
func (g *Gateway) logCost(folder, userSub, model string, u ipc.ModelUsage) error {
	return g.store.LogCost(store.CostRow{
		Folder:     folder,
		UserSub:    userSub,
		Model:      model,
		InputTok:   u.Input,
		CacheRead:  u.CacheRead,
		CacheWrite: u.CacheWrite,
		OutputTok:  u.Output,
		Cents:      u.CostCents,
	})
}

// recordTurnCost writes one cost_log row per model when usage was reported.
// Empty Models is a no-op (ant versions before the cost-caps cutover).
// Costs are SDK-provided; we treat them as authoritative for v1 and
// re-aggregate at read time.
func (g *Gateway) recordTurnCost(folder, callerSub string, models map[string]ipc.ModelUsage) {
	for model, u := range models {
		if err := g.logCost(folder, callerSub, model, u); err != nil {
			slog.Warn("budget: LogCost failed",
				"folder", folder, "model", model, "err", err)
		}
	}
}
