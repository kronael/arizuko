package routd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// reconcileCaps moves the gateway onto arizuko's cap policy before the turn's
// key is minted (spec 6/39). The gateway enforces: LiteLLM refuses a model call
// over a team or user budget, so routd compares no spend and refuses nothing.
// It keeps the gateway's rows in step with the operator's caps, and creates the
// folder's team when the gateway has none (every model call needs it). A non-nil
// error is a gateway that did not answer: the caller must NOT spawn.
func (l *Loop) reconcileCaps(ctx context.Context, folder, userSub string) error {
	folderCap, userCap, err := l.caps(folder, userSub)
	if err != nil {
		return err
	}
	if l.gateway == nil {
		// No meter: a cap that silently does nothing is worse than no cap, so
		// name it here rather than on an invoice.
		if folderCap > 0 || userCap > 0 {
			slog.Error("cost cap set but this instance has no model gateway; nothing enforces it",
				"folder", folder, "folder_cap_cents", folderCap, "user_cap_cents", userCap)
		}
		return nil
	}

	if err := l.reconcileTeam(ctx, folder, folderCap); err != nil {
		return err
	}
	return l.reconcileUser(ctx, userSub, userCap)
}

// caps reads the two ceilings the operator set. COST_CAPS_ENABLED off reports
// both as uncapped, which clears them at the gateway too — or turning it off
// would leave the gateway refusing turns arizuko does not cap.
func (l *Loop) caps(folder, userSub string) (folderCap, userCap int, err error) {
	if !l.costCapsEnabled {
		return 0, 0, nil
	}
	folderCap, err = l.db.FolderCap(folder)
	if err != nil {
		return 0, 0, fmt.Errorf("read folder cap %s: %w", folder, err)
	}
	if userSub != "" {
		userCap, err = l.db.UserCap(userSub)
		if err != nil {
			return 0, 0, fmt.Errorf("read user cap %s: %w", userSub, err)
		}
	}
	return folderCap, userCap, nil
}

// reconcileTeam moves the folder's team onto the operator's cap.
func (l *Loop) reconcileTeam(ctx context.Context, folder string, capCents int) error {
	b, err := l.gateway.teamBudget(ctx, folder)
	if errors.Is(err, errGatewayNotFound) {
		if err := l.gateway.createTeam(ctx, folder, capCents); err != nil {
			return fmt.Errorf("model gateway create team %s: %w", folder, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("model gateway read team %s: %w", folder, err)
	}
	if b.CapCents != capCents {
		if err := l.gateway.updateTeam(ctx, folder, capCents); err != nil {
			return fmt.Errorf("model gateway update team %s: %w", folder, err)
		}
	}
	return nil
}

func (l *Loop) reconcileUser(ctx context.Context, userSub string, capCents int) error {
	if userSub == "" {
		return nil
	}
	b, err := l.gateway.userBudget(ctx, userSub)
	if errors.Is(err, errGatewayNotFound) {
		// No row and no cap is the common path: a row exists only to carry a budget,
		// so there is nothing to create and the gateway still meters through user_id.
		if capCents == 0 {
			return nil
		}
		if err := l.gateway.createUser(ctx, userSub, capCents); err != nil {
			return fmt.Errorf("model gateway create user %s: %w", userSub, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("model gateway read user %s: %w", userSub, err)
	}
	if b.CapCents != capCents {
		if err := l.gateway.updateUser(ctx, userSub, capCents); err != nil {
			return fmt.Errorf("model gateway update user %s: %w", userSub, err)
		}
	}
	return nil
}
