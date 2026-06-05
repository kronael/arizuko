package routd

import (
	"fmt"
	"log/slog"
)

// budgetGate is the pre-spawn cost-cap check (spec 5/34), ported from
// gateway.budgetGate. It returns a non-empty refusal message when today's
// folder spend is at or above the folder's daily cap, else "" (turn allowed).
//
// Cap == 0 means uncapped (the default), so a zero-cap folder always passes.
//
// FEDERATION NOTE: gated also composed a per-user cap (store.UserCap /
// SpendTodayUser, keyed on the JWT-derived user_sub). routd's cost_log has no
// user_sub column and routd holds no auth_users table (authd owns identity in
// the split topology), so the per-user branch can't be wired in-process — it
// awaits a federated cost/identity surface. Folder-cap enforcement (the
// channel-scoped binding cap) is fully ported.
func (l *Loop) budgetGate(folder string) string {
	if !l.costCapsEnabled {
		return ""
	}
	folderCap, err := l.db.FolderCap(folder)
	if err != nil {
		slog.Warn("budget: FolderCap failed", "folder", folder, "err", err)
		return ""
	}
	if folderCap == 0 {
		return ""
	}
	spent, err := l.db.SpendTodayFolder(folder)
	if err != nil {
		slog.Warn("budget: SpendTodayFolder failed", "folder", folder, "err", err)
		return ""
	}
	if spent >= folderCap {
		slog.Info("budget: folder cap exhausted; refusing turn",
			"folder", folder, "spent_cents", spent, "cap_cents", folderCap)
		return budgetMsg("channel", spent, folderCap)
	}
	return ""
}

// budgetMsg renders the channel-visible refusal (verbatim from
// gateway.budgetMsg so the capped-turn output matches gated exactly).
func budgetMsg(scope string, spent, cap int) string {
	return fmt.Sprintf(
		"Budget reached for today (%s spent %d of %d cents). Resumes at 00:00 UTC.",
		scope, spent, cap)
}
