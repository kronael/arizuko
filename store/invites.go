package store

// invites.go holds every read and write of `invites`: one item per live invite,
// minted once and consumed up to max_uses times.
//
// The partition is store.OwnerOnbod: `invites` is a registered resreg resource
// whose owner subsystem IS this string, so a different spelling would hide
// every invite from `arizuko export` without any read failing. The sort key is
// `ref`, the hash-at-rest handle a redemption resolves a raw bearer to (I1).
// Its width is deliberately NOT declared: nothing compares refs by order, and
// OrderedSort would be a length check wearing an ordering mechanism's clothes.
// The list and the export read the whole collection (4 invites fleet-wide).
//
// The redemption is a compare-and-swap whose condition is THE VALUE THE READ
// SAW; losing it is ErrInviteUnavailable. The redemption item, the counter and
// the audit row commit in ONE batch, so a redemption cannot exist for an invite
// whose counter did not move.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
)

// Invite is one invite row. It carries no raw-token field: the bearer is
// generated, hashed, and returned exactly once by CreateInvite. Every other
// accessor works from Ref, the hash-at-rest key.
type Invite struct {
	Ref         string
	TargetGlob  string
	IssuedBySub string
	IssuedAt    time.Time
	ExpiresAt   *time.Time
	MaxUses     int
	UsedCount   int
}

// ErrInviteUnavailable is returned by ConsumeInvite when the token is
// missing, exhausted, or expired.
var ErrInviteUnavailable = errors.New("invite unavailable")

// ErrInviteRefUnknown is returned when a ref matches no live invite.
var ErrInviteRefUnknown = errors.New("invite ref not found")

// The attributes an invite item carries. `ref` IS the sort key. `expires_at`
// is ABSENT on an invite minted with no --expires: absent is the only null
// this model has.
const (
	attrInvTargetGlob  = "target_glob"
	attrInvIssuedBySub = "issued_by_sub"
	attrInvIssuedAt    = "issued_at"
	attrInvExpiresAt   = "expires_at"
	attrInvMaxUses     = "max_uses"
	attrInvUsedCount   = "used_count"
)

// Both instants are declared: the list orders by issued_at and the redemption
// compares expires_at, and both are byte comparisons (onboarding.go says why
// 20).
func init() {
	objstore.OrderedAttr(Invites, attrInvIssuedAt, onbodTSWidth)
	objstore.OrderedAttr(Invites, attrInvExpiresAt, onbodTSWidth)
}

// inviteKey is the coordinate of one invite.
func inviteKey(ref string) (objstore.Key, error) {
	if ref == "" {
		return objstore.Key{}, errors.New("invite carries no ref, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerOnbod, Sort: ref}, nil
}

// inviteFrom renders one stored item as the row a caller reads. The two COUNTS
// are read strictly: a max_uses read as 0 would refuse every redemption and a
// used_count read as 0 would allow an unlimited number. The two INSTANTS are
// read leniently: an unparseable expires_at reads as already expired, which
// fails closed.
func inviteFrom(it objstore.Item) (*Invite, error) {
	maxUses, err := intAttr(Invites, it, attrInvMaxUses)
	if err != nil {
		return nil, err
	}
	usedCount, err := intAttr(Invites, it, attrInvUsedCount)
	if err != nil {
		return nil, err
	}
	inv := &Invite{
		Ref:         it.Key.Sort,
		TargetGlob:  it.Attrs[attrInvTargetGlob],
		IssuedBySub: it.Attrs[attrInvIssuedBySub],
		MaxUses:     maxUses,
		UsedCount:   usedCount,
	}
	inv.IssuedAt, _ = time.Parse(time.RFC3339, it.Attrs[attrInvIssuedAt])
	if expires, ok := it.Attrs[attrInvExpiresAt]; ok {
		t, _ := time.Parse(time.RFC3339, expires)
		inv.ExpiresAt = &t
	}
	return inv, nil
}

// invitePut renders a whole invite as the item that stores it: the single
// renderer the mint, the archive restore and the migration tool write through.
// An int is always written, byte for byte what resreg's own entity path does
// (resreg/entity.go itemFor).
func invitePut(inv Invite) (objstore.Write, error) {
	key, err := inviteKey(inv.Ref)
	if err != nil {
		return objstore.Write{}, err
	}
	attrs := map[string]string{
		attrInvMaxUses:   strconv.Itoa(inv.MaxUses),
		attrInvUsedCount: strconv.Itoa(inv.UsedCount),
	}
	setAttr(attrs, attrInvTargetGlob, inv.TargetGlob)
	setAttr(attrs, attrInvIssuedBySub, inv.IssuedBySub)
	setAttr(attrs, attrInvIssuedAt, onbodTS(inv.IssuedAt))
	if inv.ExpiresAt != nil {
		attrs[attrInvExpiresAt] = onbodTS(*inv.ExpiresAt)
	}
	return objstore.Write{
		Op: objstore.Put, Entity: Invites, Key: key, Attrs: attrs,
	}, nil
}

// MintInvite validates an invite's fields and generates its bearer, returning
// the row to store and the raw token to hand back exactly once. It is separate
// from the write so onbod's REST handler shares the validity rule.
func MintInvite(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (Invite, string, error) {
	if targetGlob == "" {
		return Invite{}, "", errors.New("target_glob required")
	}
	// Strip the trailing slash (subworld-create mode) before validating.
	check := strings.TrimSuffix(targetGlob, "/")
	if check == "" {
		check = "/"
	}
	if check != "/" && !groupfolder.IsValidFolder(check) {
		return Invite{}, "", fmt.Errorf("invalid target_glob %q", targetGlob)
	}
	if issuedBySub == "" {
		return Invite{}, "", errors.New("issued_by_sub required")
	}
	if maxUses < 1 {
		maxUses = 1
	}
	token := core.GenHexToken()
	return Invite{
		Ref:         TokenRef(token),
		TargetGlob:  targetGlob,
		IssuedBySub: issuedBySub,
		IssuedAt:    time.Now().UTC(),
		ExpiresAt:   expiresAt,
		MaxUses:     maxUses,
	}, token, nil
}

// InsertInviteInto buffers a minted invite on the caller's write set. The write
// is create-if-absent and its refusal is an ERROR: a ref is 256 bits of hash,
// so a collision means two callers minted the same bearer, and overwriting
// would revoke the first one's live invite.
func (s *Store) InsertInviteInto(ws *WriteSet, inv Invite, refused error) error {
	w, err := inviteCreateWrite(inv)
	if err != nil {
		return err
	}
	return ws.AddGuarded(refused, w)
}

// inviteCreateWrite renders a minted invite as the write that stores it, for
// both sinks that store one.
func inviteCreateWrite(inv Invite) (objstore.Write, error) {
	w, err := invitePut(inv)
	if err != nil {
		return objstore.Write{}, err
	}
	w.Cond = objstore.Cond{Item: objstore.ItemAbsent}
	return w, nil
}

func errInviteRefTaken(ref string) error {
	return fmt.Errorf("an invite already exists at ref %s", ref)
}

// CreateInvite mints an invite and returns the row plus the raw bearer, shown
// to the caller exactly once and never stored.
func (s *Store) CreateInvite(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (*Invite, string, error) {
	inv, token, err := MintInvite(targetGlob, issuedBySub, maxUses, expiresAt)
	if err != nil {
		return nil, "", err
	}
	stored, err := s.tryWriteAudited(func() ([]objstore.Write, audit.Event, error) {
		w, err := inviteCreateWrite(inv)
		if err != nil {
			return nil, audit.Event{}, err
		}
		return []objstore.Write{w}, audit.Event{
			Category: audit.CategoryAuthN,
			Action:   "invite.create",
			Actor:    "user:" + issuedBySub,
			ActorSub: issuedBySub,
			Surface:  audit.SurfaceGateway,
			Resource: "invites/" + inv.Ref[:8],
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"target_glob": targetGlob,
				"max_uses":    inv.MaxUses,
			},
		}, nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("store the invite: %w", err)
	}
	if !stored {
		return nil, "", errInviteRefTaken(inv.Ref)
	}
	return &inv, token, nil
}

// readInvite is the point lookup behind every ref-addressed operation, so
// "which invite is this" cannot mean one thing to the reader and another to
// the redemption. A ref that matches nothing is ErrInviteRefUnknown.
func (s *Store) readInvite(ctx context.Context, ref string) (*Invite, error) {
	key, err := inviteKey(ref)
	if err != nil {
		return nil, err
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Invites, Partition: key.Partition,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil {
		return nil, fmt.Errorf("read invite %s: %w", ref, err)
	}
	if len(items) == 0 {
		return nil, ErrInviteRefUnknown
	}
	return inviteFrom(items[0])
}

// GetInvite resolves the raw bearer token presented at redemption to its row.
func (s *Store) GetInvite(token string) (*Invite, error) {
	return s.readInvite(context.Background(), TokenRef(token))
}

// ListInvites reads every invite, newest first, optionally narrowed to one
// issuer. The order is an ATTRIBUTE's, so it is applied here over a
// whole-collection read.
func (s *Store) ListInvites(forIssuer string) ([]Invite, error) {
	var where []objstore.Cmp
	if forIssuer != "" {
		where = append(where, objstore.Cmp{
			Attr: attrInvIssuedBySub, Op: objstore.Eq, Value: forIssuer})
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Invites, Partition: OwnerOnbod, Where: where,
	})
	if err != nil {
		return nil, fmt.Errorf("read the invites: %w", err)
	}
	out := make([]Invite, 0, len(items))
	for _, it := range items {
		inv, err := inviteFrom(it)
		if err != nil {
			return nil, err
		}
		out = append(out, *inv)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].IssuedAt.After(out[j].IssuedAt) })
	return out, nil
}

// RevokeInviteByRefInto buffers the deletion of the invite identified by ref
// (TokenRef) on the caller's write set: ref IS the coordinate (I1), so this is
// a direct delete. It is conditioned on the invite being there, so a caller
// cannot mistake a no-op for a revocation; refused is the "no such ref" the
// surface answers.
func (s *Store) RevokeInviteByRefInto(ws *WriteSet, ref string, refused error) error {
	w, err := inviteRevokeWrite(ref)
	if err != nil {
		return err
	}
	return ws.AddGuarded(refused, w)
}

// inviteRevokeWrite renders the revocation of one ref. The condition is what
// makes an unknown ref answerable.
func inviteRevokeWrite(ref string) (objstore.Write, error) {
	key, err := inviteKey(ref)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{
		Op: objstore.Delete, Entity: Invites, Key: key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	}, nil
}

// RevokeInviteByRef is RevokeInviteByRefInto with an audit row of its own, for
// the CLI and the chat command.
func (s *Store) RevokeInviteByRef(ref string) error {
	revoked, err := s.tryWriteAudited(func() ([]objstore.Write, audit.Event, error) {
		w, err := inviteRevokeWrite(ref)
		if err != nil {
			return nil, audit.Event{}, err
		}
		return []objstore.Write{w}, audit.Event{
			Category: audit.CategoryAuthN,
			Action:   "invite.revoke",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "invites/" + ref[:min(len(ref), 8)],
			Outcome:  audit.OutcomeOK,
		}, nil
	})
	if err != nil {
		return fmt.Errorf("revoke invite %s: %w", ref, err)
	}
	if !revoked {
		return ErrInviteRefUnknown
	}
	return nil
}

// ConsumeInvite claims one use (guarding max_uses and expiry) and inserts an
// acl admin row in one batch; only the first max_uses callers succeed. It
// requires invites + acl in ONE DB; the split topology (acl in routd.db,
// invites in onbod.db) uses ConsumeInviteNoGrant plus a routd-side acl write.
func (s *Store) ConsumeInvite(token, userSub string) (*Invite, error) {
	return s.consumeInvite(token, userSub, true)
}

// ConsumeInviteNoGrant is the split twin: same claim + audit, but it does NOT
// write the acl grant. acl is routd-OWNED, so the caller writes that row to
// routd.db separately (AddACLRow).
func (s *Store) ConsumeInviteNoGrant(token, userSub string) (*Invite, error) {
	return s.consumeInvite(token, userSub, false)
}

// usedCountWrite is the compare-and-swap that moves an invite's counter, and
// the single renderer of it: a redemption and its reversal have to agree about
// what a lost race looks like. The condition is the value the caller's own
// read saw (objstore.Increment says why a string attribute has no in-place
// increment).
func usedCountWrite(key objstore.Key, was, next int) objstore.Write {
	return objstore.Write{
		Op: objstore.Update, Entity: Invites, Key: key,
		Attrs: map[string]string{attrInvUsedCount: strconv.Itoa(next)},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrInvUsedCount, Op: objstore.Eq, Value: strconv.Itoa(was)},
		}},
	}
}

// RestoreInvite reverses a ConsumeInviteNoGrant, releasing one use, when the
// caller's downstream grant write to routd.db fails: without it the invite is
// burned with no grant and the user is locked out. It goes through the counter
// PROTOCOL so a lost race cannot become a lost decrement. `!= 0` because the
// counter is never negative.
func (s *Store) RestoreInvite(token string) error {
	key, err := inviteKey(TokenRef(token))
	if err != nil {
		return err
	}
	// A refusal is NOT an error: the counter is already zero.
	if _, _, err := s.incrementEntity(context.Background(), Invites, key,
		attrInvUsedCount, -1,
		[]objstore.Cmp{{Attr: attrInvUsedCount, Op: objstore.Ne, Value: "0"}}, nil); err != nil {
		return fmt.Errorf("release a use of invite %s: %w", key.Sort, err)
	}
	return nil
}

func (s *Store) consumeInvite(token, userSub string, grantACL bool) (*Invite, error) {
	if userSub == "" {
		return nil, errors.New("user_sub required")
	}
	ctx := context.Background()
	ref := TokenRef(token)
	key, err := inviteKey(ref)
	if err != nil {
		return nil, err
	}
	// These checks decide whether this caller has anything to race for; the
	// counter's condition below decides the race.
	inv, err := s.readInvite(ctx, ref)
	if err != nil {
		if errors.Is(err, ErrInviteRefUnknown) {
			return nil, ErrInviteUnavailable
		}
		return nil, err
	}
	now := time.Now().UTC()
	if inv.UsedCount >= inv.MaxUses {
		return nil, ErrInviteUnavailable
	}
	if inv.ExpiresAt != nil && !inv.ExpiresAt.After(now) {
		return nil, ErrInviteUnavailable
	}

	// The claim is the one write that may refuse; winning it is what gives
	// this caller the right to everything below.
	ws := &WriteSet{}
	if err := ws.AddGuarded(ErrInviteUnavailable,
		usedCountWrite(key, inv.UsedCount, inv.UsedCount+1)); err != nil {
		return nil, err
	}

	// The row the caller gets back is the row AFTER the claim.
	inv.UsedCount++

	// Subgroup-create invites (trailing slash) grant no folder access: the
	// folder does not exist until the username picker names it. What they DO
	// record is the parent subtree, the authority handleCreateWorld derives
	// the parent folder from; a `pending_target` cookie was forgeable (BUGS
	// F50). It goes in regardless of grantACL: invite_redemptions is
	// onbod-owned.
	if strings.HasSuffix(inv.TargetGlob, "/") {
		at, err := redemptionNowStamp()
		if err != nil {
			return nil, err
		}
		w, err := redemptionWrite(userSub, at, inv.TargetGlob)
		if err != nil {
			return nil, err
		}
		ws.Add(w)
	} else if grantACL {
		// Rendered by `acl`'s own writer, create-if-absent: redeeming a second
		// invite onto a glob the caller already holds must not restamp it. It
		// rides THIS batch; the split topology cannot, so onbod calls
		// ConsumeInviteNoGrant and writes the row through AddACLRow.
		w, err := aclPut(aclDefaults(core.ACLRow{
			Principal: userSub,
			Action:    "admin",
			Scope:     inv.TargetGlob,
			Effect:    "allow",
			GrantedBy: "invite",
		}))
		if err != nil {
			return nil, err
		}
		ws.Add(w)
	}
	_, rec := audit.WriteFor(audit.Event{
		Category: audit.CategoryAuthN,
		Action:   "invite.consume",
		Actor:    "user:" + userSub,
		ActorSub: userSub,
		Surface:  audit.SurfaceGateway,
		Resource: "invites/" + ref[:8],
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"target_glob": inv.TargetGlob,
			"used_count":  inv.UsedCount,
		},
	})
	if err := s.Apply(ctx, ws, rec); err != nil {
		return nil, err
	}
	return inv, nil
}

// --- the archive lane (spec 5/8 "Secret and token values") ----------------

// ArchiveInviteRow is one row of the archive's invites document: InvitesRow's
// shape (resreg/resources/invites.go) with Ref emitted. The config manifest
// hides Ref; the archive's UPSERT lane needs it as the conflict key.
type ArchiveInviteRow struct {
	Ref         string `yaml:"ref"`
	TargetGlob  string `yaml:"target_glob"`
	IssuedBySub string `yaml:"issued_by_sub"`
	IssuedAt    string `yaml:"issued_at"`
	ExpiresAt   string `yaml:"expires_at,omitempty"`
	MaxUses     int    `yaml:"max_uses"`
	UsedCount   int    `yaml:"used_count"`
}

// archiveInvitePut renders one archive row as the item that stores it, through
// the raw attributes rather than Invite: the archive holds the instants as the
// TEXT they are stored as, and a parse round trip would silently rewrite them.
func archiveInvitePut(r ArchiveInviteRow) (objstore.Write, error) {
	key, err := inviteKey(r.Ref)
	if err != nil {
		return objstore.Write{}, err
	}
	attrs := map[string]string{
		attrInvMaxUses:   strconv.Itoa(r.MaxUses),
		attrInvUsedCount: strconv.Itoa(r.UsedCount),
	}
	setAttr(attrs, attrInvTargetGlob, r.TargetGlob)
	setAttr(attrs, attrInvIssuedBySub, r.IssuedBySub)
	setAttr(attrs, attrInvIssuedAt, r.IssuedAt)
	setAttr(attrs, attrInvExpiresAt, r.ExpiresAt)
	return objstore.Write{
		Op: objstore.Put, Entity: Invites, Key: key, Attrs: attrs,
	}, nil
}

// readArchiveInvites is the whole collection as the archive spells it, in ref
// order.
func (s *Store) readArchiveInvites(ctx context.Context) ([]ArchiveInviteRow, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: Invites, Partition: OwnerOnbod})
	if err != nil {
		return nil, fmt.Errorf("query invites: %w", err)
	}
	out := make([]ArchiveInviteRow, 0, len(items))
	for _, it := range items {
		maxUses, err := intAttr(Invites, it, attrInvMaxUses)
		if err != nil {
			return nil, err
		}
		usedCount, err := intAttr(Invites, it, attrInvUsedCount)
		if err != nil {
			return nil, err
		}
		out = append(out, ArchiveInviteRow{
			Ref:         it.Key.Sort,
			TargetGlob:  it.Attrs[attrInvTargetGlob],
			IssuedBySub: it.Attrs[attrInvIssuedBySub],
			IssuedAt:    it.Attrs[attrInvIssuedAt],
			ExpiresAt:   it.Attrs[attrInvExpiresAt],
			MaxUses:     maxUses,
			UsedCount:   usedCount,
		})
	}
	return out, nil
}

// ExportInvites reads every invite with its ref hash-at-rest coordinate.
func (s *Store) ExportInvites(ctx context.Context) ([]ArchiveInviteRow, error) {
	rows, err := s.readArchiveInvites(ctx)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows, nil
}

// ImportInvites writes by ref and never replaces (ImportRouteTokens' reason):
// an invite's content never changes once issued, and used_count moves via
// redemption, not restore. It performs NO gating. The present set is read
// first (ImportOnboarding's reason): a batch is all-or-nothing, so one
// create-if-absent that lost would refuse every row riding with it.
func (s *Store) ImportInvites(ctx context.Context, rows []ArchiveInviteRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	have, err := s.readArchiveInvites(ctx)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]bool, len(have)+len(rows))
	for _, r := range have {
		seen[r.Ref] = true
	}
	writes := make([]objstore.Write, 0, len(rows))
	for _, r := range rows {
		if seen[r.Ref] {
			continue
		}
		seen[r.Ref] = true
		w, err := archiveInvitePut(r)
		if err != nil {
			return 0, fmt.Errorf("insert invite %s: %w", r.Ref, err)
		}
		w.Cond = objstore.Cond{Item: objstore.ItemAbsent}
		writes = append(writes, w)
	}
	if _, err := s.applyInChunks(ctx, "import the invites", writes); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// CountInvites counts every invite: the proven-empty-target gate's onbod-side
// twin of CountRouteTokens.
func (s *Store) CountInvites(ctx context.Context) (int, error) {
	rows, err := s.readArchiveInvites(ctx)
	return len(rows), err
}
