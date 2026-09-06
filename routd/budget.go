package routd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// budgetGate is the pre-spawn cost check, and the point where arizuko's cap
// policy reaches the thing that enforces it (spec 6/39).
//
// One GET /team/info per turn does three jobs at once: it creates the folder's
// team when the gateway has none — which every model call needs, not just a
// capped one — it moves the team's budget onto the cap the operator now holds,
// and it reads the spend this refusal decides on. That is why the reconcile
// lives here rather than beside SetFolderCap: routd is the only holder of the
// master key, and the gate is the serialization point the cap already crosses.
//
// It returns a non-empty refusal message when the gateway's meter is at or over
// a cap, else "". A non-nil error is a gateway that did not answer: the caller
// must NOT spawn, because a turn dispatched against a blind meter is exactly
// the second ledger this spec removes.
func (l *Loop) budgetGate(ctx context.Context, folder, userSub string) (string, error) {
	folderCap, userCap := l.caps(folder, userSub)
	if l.gateway == nil {
		// No meter. A cap the operator set is enforced by nothing, and a cap that
		// silently does nothing is worse than no cap, so it is named here rather
		// than left to be discovered on an invoice.
		if folderCap > 0 || userCap > 0 {
			slog.Error("cost cap set but this instance has no model gateway; nothing enforces it",
				"folder", folder, "folder_cap_cents", folderCap, "user_cap_cents", userCap)
		}
		return "", nil
	}

	refusal, err := l.folderMeter(ctx, folder, folderCap)
	if err != nil || refusal != "" {
		return refusal, err
	}
	return l.userMeter(ctx, userSub, userCap)
}

// caps reads the two ceilings the operator set. COST_CAPS_ENABLED off reports
// both as uncapped, which is what clears them at the gateway too — the escape
// hatch has to reach the thing doing the enforcing, or turning it off would
// leave the gateway refusing turns arizuko no longer caps.
func (l *Loop) caps(folder, userSub string) (folderCap, userCap int) {
	if !l.costCapsEnabled {
		return 0, 0
	}
	folderCap, err := l.db.FolderCap(folder)
	if err != nil {
		slog.Warn("budget: FolderCap failed", "folder", folder, "err", err)
		folderCap = 0
	}
	if userSub != "" {
		userCap, err = l.db.UserCap(userSub)
		if err != nil {
			slog.Warn("budget: UserCap failed", "user_sub", userSub, "err", err)
			userCap = 0
		}
	}
	return folderCap, userCap
}

// folderMeter reconciles the folder's team with the operator's cap and reads
// what the gateway has charged it today.
func (l *Loop) folderMeter(ctx context.Context, folder string, capCents int) (string, error) {
	b, err := l.gateway.teamBudget(ctx, folder)
	if errors.Is(err, errGatewayNotFound) {
		// First turn this folder has ever run against this gateway. It starts at
		// zero spend, so it is created and allowed in the same breath.
		if err := l.gateway.createTeam(ctx, folder, capCents); err != nil {
			return "", fmt.Errorf("model gateway create team %s: %w", folder, err)
		}
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("model gateway read team %s: %w", folder, err)
	}
	if b.CapCents != capCents {
		if err := l.gateway.updateTeam(ctx, folder, capCents); err != nil {
			return "", fmt.Errorf("model gateway update team %s: %w", folder, err)
		}
	}
	if capCents > 0 && b.SpentCents >= capCents {
		slog.Info("budget: folder cap exhausted; refusing turn",
			"folder", folder, "spent_cents", b.SpentCents, "cap_cents", capCents)
		return budgetMsg("channel", b.SpentCents, capCents), nil
	}
	return "", nil
}

// userMeter reconciles one person's cap and reads what the gateway has charged
// them today. An uncapped caller costs no call at all: the gateway meters them
// through the key's user_id whether or not it holds a row for them, and a row
// exists only to carry a budget.
func (l *Loop) userMeter(ctx context.Context, userSub string, capCents int) (string, error) {
	if userSub == "" || capCents == 0 {
		return "", nil
	}
	b, err := l.gateway.userBudget(ctx, userSub)
	if errors.Is(err, errGatewayNotFound) {
		if err := l.gateway.createUser(ctx, userSub, capCents); err != nil {
			return "", fmt.Errorf("model gateway create user %s: %w", userSub, err)
		}
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("model gateway read user %s: %w", userSub, err)
	}
	if b.CapCents != capCents {
		if err := l.gateway.updateUser(ctx, userSub, capCents); err != nil {
			return "", fmt.Errorf("model gateway update user %s: %w", userSub, err)
		}
	}
	if b.SpentCents >= capCents {
		slog.Info("budget: user cap exhausted; refusing turn",
			"user_sub", userSub, "spent_cents", b.SpentCents, "cap_cents", capCents)
		return budgetMsg("you", b.SpentCents, capCents), nil
	}
	return "", nil
}

// budgetMsg renders the channel-visible refusal. The numbers are the gateway's,
// and 00:00 UTC is when the "1d" budget the gate set actually resets.
func budgetMsg(scope string, spent, cap int) string {
	return fmt.Sprintf(
		"Budget reached for today (%s spent %d of %d cents). Resumes at 00:00 UTC.",
		scope, spent, cap)
}
