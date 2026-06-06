package routd

import (
	"fmt"
	"log/slog"
	"strings"
)

// authSubPrefixes are the schemes authd writes as the canonical user sub
// (google: / github: / local:). Adapter senders (telegram:user/..., slack:user/...,
// bluesky:user/...) are per-platform IDs, not bound to an auth_users row, so they
// carry no per-user cap today. Verbatim from gateway.budget.go authSubPrefixes.
var authSubPrefixes = []string{"google:", "github:", "local:"}

// callerSubOfMsg returns the user_sub for the per-user budget cap. Empty string
// disables the user-cap branch (folder cap still binds). Only authd's JWT
// schemes are recognised; anon: and adapter-prefixed senders return "".
// Verbatim from gateway.callerSubOfMsg.
func callerSubOfMsg(sender string) string {
	for _, p := range authSubPrefixes {
		if strings.HasPrefix(sender, p) {
			return sender
		}
	}
	return ""
}

// budgetGate is the pre-spawn cost-cap check (spec 5/34), ported from
// gateway.budgetGate. It returns a non-empty refusal message when today's spend
// is at or above the LOWER of the folder cap and (when known) the user cap, else
// "" (turn allowed).
//
// Cap == 0 means uncapped (the default). Per-user cap composes with per-folder:
// the binding cap is the lower of the two non-zero values. userSub is empty for
// adapter/anon/system turns (callerSubOfMsg) — then only the folder cap binds.
func (l *Loop) budgetGate(folder, userSub string) string {
	if !l.costCapsEnabled {
		return ""
	}
	folderCap, err := l.db.FolderCap(folder)
	if err != nil {
		slog.Warn("budget: FolderCap failed", "folder", folder, "err", err)
		return ""
	}
	userCap := 0
	if userSub != "" {
		userCap, err = l.db.UserCap(userSub)
		if err != nil {
			slog.Warn("budget: UserCap failed", "user_sub", userSub, "err", err)
			userCap = 0
		}
	}
	if folderCap == 0 && userCap == 0 {
		return ""
	}

	if folderCap > 0 {
		spent, err := l.db.SpendTodayFolder(folder)
		if err != nil {
			slog.Warn("budget: SpendTodayFolder failed", "folder", folder, "err", err)
		} else if spent >= folderCap {
			slog.Info("budget: folder cap exhausted; refusing turn",
				"folder", folder, "spent_cents", spent, "cap_cents", folderCap)
			return budgetMsg("channel", spent, folderCap)
		}
	}
	if userCap > 0 {
		spent, err := l.db.SpendTodayUser(userSub)
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

// budgetMsg renders the channel-visible refusal (verbatim from
// gateway.budgetMsg so the capped-turn output matches gated exactly).
func budgetMsg(scope string, spent, cap int) string {
	return fmt.Sprintf(
		"Budget reached for today (%s spent %d of %d cents). Resumes at 00:00 UTC.",
		scope, spent, cap)
}
