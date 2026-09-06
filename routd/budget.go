package routd

import (
	"fmt"
	"log/slog"
)

// budgetGate is the pre-spawn cost-cap check. It returns a non-empty refusal
// message when today's spend is at or above the lower of the folder cap and
// (when known) the user cap, else "" (turn allowed). Cap == 0 means uncapped.
//
// remaining is what an allowed turn may still spend today, in cents: the lower
// of the two headrooms, and 0 when neither axis caps this turn. It is the
// budget the turn's gateway key carries (spec 6/23), read here because the
// caps and today's spend are the same rows the refusal decides on — asking a
// second time could answer differently.
func (l *Loop) budgetGate(folder, userSub string) (refusal string, remaining int) {
	if !l.costCapsEnabled {
		return "", 0
	}
	folderCap, err := l.db.FolderCap(folder)
	if err != nil {
		slog.Warn("budget: FolderCap failed", "folder", folder, "err", err)
		return "", 0
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
		return "", 0
	}

	if folderCap > 0 {
		spent, err := l.db.SpendTodayFolder(folder)
		if err != nil {
			slog.Warn("budget: SpendTodayFolder failed", "folder", folder, "err", err)
		} else if spent >= folderCap {
			slog.Info("budget: folder cap exhausted; refusing turn",
				"folder", folder, "spent_cents", spent, "cap_cents", folderCap)
			return budgetMsg("channel", spent, folderCap), 0
		} else {
			remaining = folderCap - spent
		}
	}
	if userCap > 0 {
		spent, err := l.db.SpendTodayUser(userSub)
		if err != nil {
			slog.Warn("budget: SpendTodayUser failed", "user_sub", userSub, "err", err)
		} else if spent >= userCap {
			slog.Info("budget: user cap exhausted; refusing turn",
				"user_sub", userSub, "spent_cents", spent, "cap_cents", userCap)
			return budgetMsg("you", spent, userCap), 0
		} else if headroom := userCap - spent; remaining == 0 || headroom < remaining {
			remaining = headroom
		}
	}
	return "", remaining
}

// budgetMsg renders the channel-visible refusal.
func budgetMsg(scope string, spent, cap int) string {
	return fmt.Sprintf(
		"Budget reached for today (%s spent %d of %d cents). Resumes at 00:00 UTC.",
		scope, spent, cap)
}
