package store

// import.go is the destination half of the data move (spec 6/37 step 7): the
// source halves, cmd/arizuko's `migrate-store` and `migrate-onbod`, read the
// old tables and hand plain values here, and this file owns the item shape.
//
// `pane_sessions.opened_at` is REWRITTEN through core.TS: 8 of marinade's 81
// rows are 29 bytes against the other 73's 30, so byte order and time order
// already disagree there (BUGS L97). onbod's four instants are COPIED: every
// value in the fleet is exactly 20 bytes (measured 2026-09-07), and widening
// them would move every checksum for nothing. A wrong width is REFUSED by the
// driver's own validator before any write lands.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// PaneRow is one legacy `pane_sessions` row as the source reads it. A NULL
// column arrives as the empty string, which is an absent attribute on the
// other side.
type PaneRow struct {
	TeamID     string
	UserID     string
	ThreadTS   string
	ChannelID  string
	ContextJID string
	OpenedAt   string
}

// UseRow is one legacy `secret_use_log` row, carrying the rowid its new id is
// built from: the table declared no primary key, so the rowid is the only thing
// that separated two uses recorded in one instant.
type UseRow struct {
	RowID     int64
	TS        string
	SpawnID   string
	CallerSub string
	Folder    string
	Tool      string
	Key       string
	Scope     string
	Status    string
	LatencyMS int64
}

// Snapshot is the legacy tables this package has moved, as plain values.
type Snapshot struct {
	Panes []PaneRow
	Uses  []UseRow
}

// Counts is what an import wrote, per entity, for the operator to check against
// the source.
type Counts struct {
	Panes int
	Uses  int
}

// Import writes snap into the entities and answers what landed. The writes are
// UNCONDITIONAL: a migration is re-runnable by design and every key is a
// function of the source row, so a re-import writes the same item; the
// create-if-absent that protects a RUNNING system stays on the live writers.
func (s *Store) Import(ctx context.Context, snap Snapshot) (Counts, error) {
	writes, counts, err := importWrites(snap)
	if err != nil {
		return Counts{}, err
	}
	if _, err := s.applyInChunks(ctx, "import the moved tables", writes); err != nil {
		return Counts{}, err
	}
	return counts, nil
}

// importWrites turns a snapshot into the writes that represent it, or names why
// it cannot be represented. Every refusal is collected and reported at once.
func importWrites(snap Snapshot) ([]objstore.Write, Counts, error) {
	var (
		writes  []objstore.Write
		counts  Counts
		refused []string
	)
	claimed := map[string]bool{}
	for _, r := range snap.Panes {
		key, err := paneKey(r.TeamID, r.UserID, r.ThreadTS)
		if err != nil {
			refused = append(refused, err.Error())
			continue
		}
		if claimed[key.Sort] {
			refused = append(refused, fmt.Sprintf("two panes address %s", key.Sort))
			continue
		}
		opened, err := normalizeTS("pane "+key.Sort+" opened_at", r.OpenedAt)
		if err != nil {
			refused = append(refused, err.Error())
			continue
		}
		if opened == "" {
			// The channel read picks the newest by it, so a pane with no
			// opened_at could never be the pane its channel is showing.
			refused = append(refused, "pane "+key.Sort+" has no opened_at, and the channel "+
				"read picks the newest by it")
			continue
		}
		claimed[key.Sort] = true
		attrs := map[string]string{
			attrPaneChannel:  r.ChannelID,
			attrPaneOpenedAt: opened,
		}
		setAttr(attrs, attrPaneContext, r.ContextJID)
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: PaneSessions, Key: key, Attrs: attrs,
		})
		counts.Panes++
	}

	ids := map[string]int64{}
	for _, r := range snap.Uses {
		id, err := LegacyUseID(r)
		if err != nil {
			refused = append(refused, err.Error())
			continue
		}
		if was, dup := ids[id]; dup {
			refused = append(refused, fmt.Sprintf("rows %d and %d both address %s",
				was, r.RowID, id))
			continue
		}
		ids[id] = r.RowID
		w := secretUseWrite(id, SecretUseRow{
			SpawnID:   r.SpawnID,
			CallerSub: r.CallerSub,
			Folder:    r.Folder,
			Tool:      r.Tool,
			Key:       r.Key,
			Scope:     r.Scope,
			Status:    r.Status,
			LatencyMS: r.LatencyMS,
		})
		w.Cond = objstore.Cond{}
		writes = append(writes, w)
		counts.Uses++
	}
	if len(refused) > 0 {
		return nil, Counts{}, fmt.Errorf("%d rows the entities cannot address:\n  %s",
			len(refused), strings.Join(refused, "\n  "))
	}
	return writes, counts, nil
}

// LegacyUseID is the id one legacy `secret_use_log` row becomes: the instant it
// recorded, at core.TS's width, and the row's own rowid as the suffix. The
// rowid rather than randomness makes the import RE-RUNNABLE and carries the
// order of two uses recorded in one instant.
func LegacyUseID(r UseRow) (string, error) {
	at, err := time.Parse(time.RFC3339Nano, r.TS)
	if err != nil {
		return "", fmt.Errorf("use %d records %q, which is not a time: %w", r.RowID, r.TS, err)
	}
	if r.RowID < 0 {
		return "", fmt.Errorf("use %d has no rowid to carry", r.RowID)
	}
	return core.TS(at) + "-" + fmt.Sprintf("%0*x", secretUseSuffixHex, r.RowID), nil
}

// normalizeTS re-renders a stored instant at core.TS's width. An empty value
// stays empty: absent is the only null, and the caller decides whether it may
// be.
func normalizeTS(what, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("%s is %q, which is not a time: %w", what, value, err)
	}
	return core.TS(at), nil
}

// --- onbod's three tables ------------------------------------------------

// OnbodSnapshot is the legacy `onboarding`, `onboarding_gates` and `invites`
// tables as the source reads them, as this package's own plain values.
type OnbodSnapshot struct {
	Admissions  []OnboardingRow
	Gates       []OnboardingGate
	Invites     []ArchiveInviteRow
	Redemptions []RedemptionRow
}

// RedemptionRow is one legacy `invite_redemptions` row, carrying the rowid its
// position's suffix is built from.
type RedemptionRow struct {
	RowID      int64
	UserSub    string
	TargetGlob string
	RedeemedAt string
}

// OnbodCounts is what an import wrote, per entity, for the operator to check
// against the source. A ZERO IS NOT AN ALL-CLEAR: `onboarding_gates` is empty
// on all three live instances (2026-09-07), so no gates is correct there and
// the FIRST symptom of a tool pointed at the wrong database anywhere else.
type OnbodCounts struct {
	Admissions  int
	Gates       int
	Invites     int
	Redemptions int
}

// CheckOnbodSnapshot reports whether the entities can represent snap, writing
// nothing: a row the store cannot address is named before the run that would
// stop half way through.
func CheckOnbodSnapshot(snap OnbodSnapshot) (OnbodCounts, error) {
	_, counts, err := onbodWrites(snap)
	return counts, err
}

// ImportOnbod writes snap into the entities and answers what landed. The
// writes are UNCONDITIONAL, for Import's reason. ImportOnboarding and
// ImportInvites, the archive lane, deliberately do NOT overwrite: a jid the
// target already holds is a live admission a restore must not walk backwards.
func (s *Store) ImportOnbod(ctx context.Context, snap OnbodSnapshot) (OnbodCounts, error) {
	writes, counts, err := onbodWrites(snap)
	if err != nil {
		return OnbodCounts{}, err
	}
	if _, err := s.applyInChunks(ctx, "import onbod's tables", writes); err != nil {
		return OnbodCounts{}, err
	}
	return counts, nil
}

// onbodWrites turns a snapshot into the writes that represent it, or names why
// it cannot be represented. Every refusal is collected and reported at once.
func onbodWrites(snap OnbodSnapshot) ([]objstore.Write, OnbodCounts, error) {
	var (
		writes  []objstore.Write
		counts  OnbodCounts
		refused []string
	)
	// One claimed-coordinate set per entity: the three share a partition.
	claimed := map[string]map[string]bool{
		Onboarding: {}, OnboardingGates: {}, Invites: {}, InviteRedemptions: {},
	}
	take := func(entity, sort, what string) bool {
		if claimed[entity][sort] {
			refused = append(refused, fmt.Sprintf("two %s rows carry %s", what, sort))
			return false
		}
		claimed[entity][sort] = true
		return true
	}
	add := func(w objstore.Write, err error) bool {
		if err != nil {
			refused = append(refused, err.Error())
			return false
		}
		// The width rule is the DRIVER's, so it is checked with the driver's
		// own validator rather than a second copy of the widths (BUGS L97).
		if err := objstore.ValidateBatch(nil, []objstore.Write{w}); err != nil {
			refused = append(refused, err.Error())
			return false
		}
		writes = append(writes, w)
		return true
	}
	for _, r := range snap.Admissions {
		if r.JID != "" && !take(Onboarding, r.JID, "onboarding") {
			continue
		}
		if add(onboardingPut(r)) {
			counts.Admissions++
		}
	}
	for _, g := range snap.Gates {
		if g.Gate != "" && !take(OnboardingGates, g.Gate, "gate") {
			continue
		}
		if add(gatePut(g)) {
			counts.Gates++
		}
	}
	for _, inv := range snap.Invites {
		if inv.Ref != "" && !take(Invites, inv.Ref, "invite") {
			continue
		}
		if add(archiveInvitePut(inv)) {
			counts.Invites++
		}
	}
	for _, r := range snap.Redemptions {
		w, err := redemptionRowPut(r)
		if err != nil {
			refused = append(refused, err.Error())
			continue
		}
		// The person is the PARTITION, so only a repeat inside one partition
		// is a collision.
		if !take(InviteRedemptions, w.Key.Partition+KeySep+w.Key.Sort, "redemption") {
			continue
		}
		if add(w, nil) {
			counts.Redemptions++
		}
	}
	if len(refused) > 0 {
		return nil, OnbodCounts{}, fmt.Errorf("%d rows the entities cannot address:\n  %s",
			len(refused), strings.Join(refused, "\n  "))
	}
	return writes, counts, nil
}
