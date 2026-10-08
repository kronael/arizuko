package routd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// budgetGate is the pre-spawn cost check, where arizuko's cap policy reaches
// the thing that enforces it (spec 6/39). One GET /team/info per turn creates
// the folder's team when the gateway has none (every model call needs it),
// moves the team's budget onto the operator's cap, and reads the spend the
// refusal decides on; the gate is the serialization point the cap already
// crosses. Non-empty refusal when a cap is spent, else "". A non-nil error is
// a gateway that did not answer: the caller must NOT spawn — a turn against a
// blind meter is the second ledger this spec removes.
func (l *Loop) budgetGate(ctx context.Context, folder, userSub string) (string, error) {
	folderCap, userCap := l.caps(folder, userSub)
	if l.gateway == nil {
		// No meter: a cap that silently does nothing is worse than no cap, so
		// name it here rather than on an invoice.
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
// both as uncapped, which clears them at the gateway too — or turning it off
// would leave the gateway refusing turns arizuko does not cap.
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
		// First turn against this gateway: zero spend, so created and allowed
		// in one breath.
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

// userMeter reconciles one person's cap and reads today's charge, the same way
// folderMeter reconciles the folder's.
//
// It READS even when the operator's cap is zero, and that is the whole of BUGS
// `L401`'s sibling `L402`: returning early on `capCents == 0` was right for someone
// who never had a cap and wrong for someone whose cap was REMOVED. The gateway row
// already existed, still carried the old `max_budget`, and nothing ever cleared it —
// so taking a person's cap away left the gateway refusing them, with no cap anywhere
// in arizuko to explain why. `folderMeter` has always pushed the zero
// (`b.CapCents != capCents` is true for 0 too); only the user half returned first.
//
// The cost is one gateway read per turn for a caller who holds no cap, which is what
// the early return bought. It is a local call on the compose network, the folder half
// already pays it on every turn, and the alternative is a second piece of state
// recording "this person once had a cap" — a worse trade than one read.
func (l *Loop) userMeter(ctx context.Context, userSub string, capCents int) (string, error) {
	if userSub == "" {
		return "", nil
	}
	b, err := l.gateway.userBudget(ctx, userSub)
	if errors.Is(err, errGatewayNotFound) {
		// No row and no cap is the common path: a row exists only to carry a budget,
		// so there is nothing to create and the gateway still meters through user_id.
		if capCents == 0 {
			return "", nil
		}
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
	// An uncapped person is never refused, whatever the row says about yesterday.
	if capCents > 0 && b.SpentCents >= capCents {
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
