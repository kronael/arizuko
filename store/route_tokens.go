package store

// route_tokens.go holds every read and write of `route_tokens`: one item per
// ingress credential, addressed by hex(sha256(token)).
//
// The partition is OwnerRoutd because resreg's entity path writes these items
// too (resreg.SubsystemRoutd IS store.OwnerRoutd). The digest is the only
// field that can be the coordinate: krons carries 43 rows over 27 distinct
// jids, so a key on the jid would collide 16 live credentials onto one item
// (BUGS L260). Every read that is not a resolution scans the partition (43
// items); the two hot-path reads, LookupRouteToken and PeekPairing, are exact
// key reads.
//
// `kind` IS ALWAYS WRITTEN: it is the resource's own row filter (`kind =
// 'route'`), and an ABSENT attribute answers no comparison, so a row that left
// it off would be invisible to export and untouched by apply.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a token item carries. `token_hash` is the coordinate.
const (
	attrTokJID          = "jid"
	attrTokOwnerFolder  = "owner_folder"
	attrTokTargetFolder = "target_folder"
	attrTokCreatedAt    = "created_at"
	attrTokContext      = "context"
	attrTokKind         = "kind"
)

// RouteToken maps a bearer token to a single inbound JID + admin folder (spec
// 5/W). JID kind is encoded in the prefix: `web:<folder>[/<suffix>]` for
// anonymous web chat, `hook:<folder>/<source>[...]` for labeled webhook ingest.
// owner_folder bounds revocation and may diverge from the folder embedded in
// the JID.
type RouteToken struct {
	JID         string
	OwnerFolder string
	CreatedAt   time.Time
	// Context is the optional issuer-authored per-link processing
	// instructions (spec 5/W § link context).
	Context string
	// TargetFolder is the folder the token DELIVERS to, recorded by the mint.
	// Empty for a token minted before the mint recorded it; Folder falls back
	// to the JID grammar for those.
	TargetFolder string
}

// Folder returns the folder the token delivers to. The mint records the target
// (spec 5/W); the JID grammar answers only for a token whose mint did not,
// because a suffixed JID is ambiguous: `web:t/sfx` reads as folder `t/sfx`,
// and `hook:a/b/c` reads as folder `a/b`.
func (t RouteToken) Folder() string {
	if t.TargetFolder != "" {
		return t.TargetFolder
	}
	return groupfolder.JidFolder(t.JID)
}

// ErrRouteTokenTargetTaken reports a mint whose JID already delivers to a
// different folder. A JID names ONE chat, so two folders must never mint the
// same JID: resolution reads target_folder back by JID and could not choose.
var ErrRouteTokenTargetTaken = errors.New("jid already delivers to a different folder")

// The JID-derived kind of a route token: which inbound surface its JID names.
// Distinct from the stored `kind` attribute below.
const (
	RouteTokenJIDKindChat = "chat" // jid prefix "web:"
	RouteTokenJIDKindHook = "hook" // jid prefix "hook:"
)

// The stored `kind` attribute (spec 5/31-identity-pairing). Route delivery resolves only
// KindRoute rows and pairing redemption only KindPair rows, so neither
// credential can be redeemed as the other.
const (
	RouteTokenKindRoute = "route"
	RouteTokenKindPair  = "pair"
)

// routeTokenKey is the coordinate of one token: the digest of the raw bearer.
func routeTokenKey(rawToken string) (objstore.Key, error) {
	if rawToken == "" {
		return objstore.Key{}, errors.New("route token is empty, so it has no coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: TokenRef(rawToken)}, nil
}

// routeTokenFrom reads one item back.
func routeTokenFrom(it objstore.Item) RouteToken {
	t := RouteToken{
		JID:          it.Attrs[attrTokJID],
		OwnerFolder:  it.Attrs[attrTokOwnerFolder],
		TargetFolder: it.Attrs[attrTokTargetFolder],
		Context:      it.Attrs[attrTokContext],
	}
	t.CreatedAt, _ = time.Parse(core.TSLayout, it.Attrs[attrTokCreatedAt])
	return t
}

// routeTokensOfKind reads every token of one kind: the ONE scan the attribute
// reads share, so they cannot disagree about which rows count.
func routeTokensOfKind(ctx context.Context, q EntityQuerier, kind string) ([]objstore.Item, error) {
	items, err := q.Query(ctx, objstore.Query{
		Entity: RouteTokens, Partition: OwnerRoutd,
		Where: []objstore.Cmp{{Attr: attrTokKind, Op: objstore.Eq, Value: kind}},
	})
	if err != nil {
		return nil, fmt.Errorf("read route tokens: %w", err)
	}
	return items, nil
}

// GenRouteToken returns a 256-bit base64url-encoded random token. The raw token
// is returned to the caller exactly once; only the sha256 hash is persisted.
func GenRouteToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// RouteTokenJIDKind returns "chat" for web: jids, "hook" for hook: jids, ""
// otherwise.
func RouteTokenJIDKind(jid string) string {
	switch {
	case strings.HasPrefix(jid, "web:"):
		return RouteTokenJIDKindChat
	case strings.HasPrefix(jid, "hook:"):
		return RouteTokenJIDKindHook
	default:
		return ""
	}
}

// CheckRouteTokenTarget rejects a mint that would bind an existing JID to a
// second delivery folder. A row whose mint recorded no target never conflicts.
// Both mint paths call it, so one function holds the rule.
func CheckRouteTokenTarget(ctx context.Context, s objstore.Store, jid, target string) error {
	items, err := routeTokensOfKind(ctx, s, RouteTokenKindRoute)
	if err != nil {
		return err
	}
	for _, it := range items {
		other := it.Attrs[attrTokTargetFolder]
		if it.Attrs[attrTokJID] == jid && other != "" && other != target {
			return fmt.Errorf("%w: %s delivers to %s", ErrRouteTokenTargetTaken, jid, other)
		}
	}
	return nil
}

// RouteTokenTargetByJID returns the folder a web:/hook: JID delivers to, or ""
// when no token records one; callers fall back to groupfolder.JidFolder. It is
// the single reader every folder-from-JID path goes through, and
// CheckRouteTokenTarget keeps a JID bound to ONE folder, so the first answer
// is the whole answer.
func RouteTokenTargetByJID(ctx context.Context, s objstore.Store, jid string) (string, error) {
	items, err := routeTokensOfKind(ctx, s, RouteTokenKindRoute)
	if err != nil {
		return "", err
	}
	for _, it := range items {
		if it.Attrs[attrTokJID] == jid && it.Attrs[attrTokTargetFolder] != "" {
			return it.Attrs[attrTokTargetFolder], nil
		}
	}
	return "", nil
}

// routeTokenPut renders the write that stores one token.
func routeTokenPut(rawToken string, t RouteToken, kind string) (objstore.Write, error) {
	key, err := routeTokenKey(rawToken)
	if err != nil {
		return objstore.Write{}, err
	}
	ts := t.CreatedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	attrs := map[string]string{
		attrTokJID:       t.JID,
		attrTokCreatedAt: core.TS(ts),
		attrTokKind:      kind,
	}
	setAttr(attrs, attrTokOwnerFolder, t.OwnerFolder)
	setAttr(attrs, attrTokTargetFolder, t.TargetFolder)
	setAttr(attrs, attrTokContext, t.Context)
	return objstore.Write{Op: objstore.Put, Entity: RouteTokens, Key: key, Attrs: attrs}, nil
}

// InsertRouteToken persists a new token. The caller chooses the raw token (via
// GenRouteToken) and returns it to the user; the store retains only
// sha256(raw). Every mint MUST set TargetFolder: only the mint knows it, and a
// token carrying none reads back through the ambiguous JID grammar (spec 5/W
// § Resolution).
func (s *Store) InsertRouteToken(rawToken string, t RouteToken) error {
	if rawToken == "" {
		return fmt.Errorf("empty token")
	}
	if t.JID == "" || t.OwnerFolder == "" {
		return fmt.Errorf("jid and owner_folder required")
	}
	if RouteTokenJIDKind(t.JID) == "" {
		return fmt.Errorf(`jid must start with "web:" or "hook:"`)
	}
	ctx := context.Background()
	if err := CheckRouteTokenTarget(ctx, s.Obj(), t.JID, t.TargetFolder); err != nil {
		return err
	}
	w, err := routeTokenPut(rawToken, t, RouteTokenKindRoute)
	if err != nil {
		return err
	}
	actor, actorSub, surface := s.auditIdentity()
	return s.writeAudited(func() ([]objstore.Write, audit.Event, error) {
		return []objstore.Write{w}, audit.Event{
			Category: audit.CategoryChannel,
			Action:   "route_token.mint",
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
			Resource: "route_tokens/" + t.JID,
			Folder:   t.OwnerFolder,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"owner_folder": t.OwnerFolder,
				"jid_kind":     RouteTokenJIDKind(t.JID),
			},
		}, nil
	})
}

// routeTokenItem reads the item one raw token names, kind-scoped so a pairing
// credential never resolves as a delivery bearer and the reverse.
func routeTokenItem(ctx context.Context, s objstore.Store, rawToken, kind string) (objstore.Item, bool, error) {
	key, err := routeTokenKey(rawToken)
	if err != nil {
		return objstore.Item{}, false, err
	}
	items, err := s.Query(ctx, objstore.Query{
		Entity: RouteTokens, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil {
		return objstore.Item{}, false, fmt.Errorf("resolve route token: %w", err)
	}
	if len(items) == 0 || items[0].Attrs[attrTokKind] != kind {
		return objstore.Item{}, false, nil
	}
	return items[0], true, nil
}

// LookupRouteToken resolves a raw DELIVERY bearer token to its row. (zero,
// false) when the token is unknown or is not a route-kind row: a pairing token
// (spec 5/31-identity-pairing) never resolves here.
func (s *Store) LookupRouteToken(rawToken string) (RouteToken, bool) {
	it, ok, err := routeTokenItem(context.Background(), s.Obj(), rawToken, RouteTokenKindRoute)
	if err != nil || !ok {
		return RouteToken{}, false
	}
	return routeTokenFrom(it), true
}

// ListRouteTokens returns the DELIVERY tokens owned by ownerFolder, newest
// first. Pairing rows are excluded: enumerating ten-minute bearer attempts is
// surface nobody needs (spec 5/31-identity-pairing § Unpair).
func (s *Store) ListRouteTokens(ownerFolder string) []RouteToken {
	items, err := routeTokensOfKind(context.Background(), s.Obj(), RouteTokenKindRoute)
	if err != nil {
		return nil
	}
	var out []RouteToken
	for _, it := range items {
		if it.Attrs[attrTokOwnerFolder] != ownerFolder {
			continue
		}
		out = append(out, routeTokenFrom(it))
	}
	slices.SortFunc(out, func(a, b RouteToken) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out
}

// RouteTokenOwner returns the owner_folder of the DELIVERY token with this jid.
// The owner is an ATTRIBUTE of the row, never parsed back out of the JID:
// `web:<folder>/<suffix>` parses to `<folder>/<suffix>`, which is not a folder
// and matches no row (BUGS L52).
func (s *Store) RouteTokenOwner(jid string) (string, bool) {
	items, err := routeTokensOfKind(context.Background(), s.Obj(), RouteTokenKindRoute)
	if err != nil {
		return "", false
	}
	for _, it := range items {
		if it.Attrs[attrTokJID] == jid {
			return it.Attrs[attrTokOwnerFolder], true
		}
	}
	return "", false
}

// RevokeRouteToken deletes DELIVERY tokens with the given jid owned by
// ownerFolder. Returns (true, nil) if at least one went, (false, nil) when no
// matching row existed. ACL scope: the caller's `owner_folder` MUST match;
// agents in folder A cannot revoke folder B's tokens.
func (s *Store) RevokeRouteToken(jid, ownerFolder string) (bool, error) {
	ctx := context.Background()
	items, err := routeTokensOfKind(ctx, s.Obj(), RouteTokenKindRoute)
	if err != nil {
		return false, err
	}
	var writes []objstore.Write
	for _, it := range items {
		if it.Attrs[attrTokJID] != jid || it.Attrs[attrTokOwnerFolder] != ownerFolder {
			continue
		}
		writes = append(writes, objstore.Write{
			Op: objstore.Delete, Entity: RouteTokens, Key: it.Key,
		})
	}
	if len(writes) == 0 {
		return false, nil
	}
	// Every mint of this jid adds a row, so the revoke is a sweep: a row that
	// survives a part-way failure is still revocable by the next run.
	if _, err := applyChunks(ctx, "revoke "+jid, writes,
		func(ctx context.Context, chunk []objstore.Write) (bool, error) {
			return s.TryWriteEntities(ctx, nil, chunk...)
		}); err != nil {
		return false, err
	}
	actor, actorSub, surface := s.auditIdentity()
	_, rec := audit.WriteFor(audit.Event{
		Category: audit.CategoryChannel,
		Action:   "route_token.revoke",
		Actor:    actor,
		ActorSub: actorSub,
		Surface:  surface,
		Resource: "route_tokens/" + jid,
		Folder:   ownerFolder,
		Outcome:  audit.OutcomeOK,
	})
	if err := s.WriteEntities(ctx, nil, rec); err != nil {
		return true, err
	}
	return true, nil
}

// --- the archive lane (spec 5/8 "Secret and token values") ----------------
//
// The archive carries a route token's VALUE, which the config manifest never
// does, so the functions below are this table's second reader, beside its
// first.

// ArchiveRouteTokenRow is one row of the archive's route_tokens document:
// RouteTokensRow's shape (resreg/resources/route_tokens.go) plus TokenHash, the
// verifier the config manifest never carries. Restricted to kind='route' at
// read time: a pairing token has no archival value, and reviving one is
// actively harmful.
type ArchiveRouteTokenRow struct {
	TokenHash    string `yaml:"token_hash"`
	JID          string `yaml:"jid"`
	OwnerFolder  string `yaml:"owner_folder"`
	TargetFolder string `yaml:"target_folder,omitempty"`
	CreatedAt    string `yaml:"created_at"`
	Context      string `yaml:"context,omitempty"`
}

// ExportRouteTokens reads every kind='route' row with its token_hash verifier,
// which IS the item's coordinate, already in the form YAML carries.
func (s *Store) ExportRouteTokens(ctx context.Context) ([]ArchiveRouteTokenRow, error) {
	items, err := routeTokensOfKind(ctx, s.Obj(), RouteTokenKindRoute)
	if err != nil {
		return nil, err
	}
	out := make([]ArchiveRouteTokenRow, 0, len(items))
	for _, it := range items {
		t := routeTokenFrom(it)
		out = append(out, ArchiveRouteTokenRow{
			TokenHash:    it.Key.Sort,
			JID:          t.JID,
			OwnerFolder:  t.OwnerFolder,
			TargetFolder: t.TargetFolder,
			CreatedAt:    it.Attrs[attrTokCreatedAt],
			Context:      t.Context,
		})
	}
	slices.SortFunc(out, func(a, b ArchiveRouteTokenRow) int { return strings.Compare(a.JID, b.JID) })
	return out, nil
}

// ImportRouteTokens writes each row create-if-absent by token_hash: a route
// token's content never legitimately changes once minted. Every imported row's
// kind is forced to 'route'; this function never writes a pairing token. It
// performs NO gating (spec 5/8 "Secret and token values" (2)): that is
// `archive apply` orchestration in cmd/arizuko. The rows already there are read
// FIRST and left out of the batch: a batch is all-or-nothing, so one
// create-if-absent whose item exists would refuse every other write.
func (s *Store) ImportRouteTokens(ctx context.Context, rows []ArchiveRouteTokenRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	have, err := s.Obj().Query(ctx, objstore.Query{Entity: RouteTokens, Partition: OwnerRoutd})
	if err != nil {
		return 0, fmt.Errorf("read the route tokens already there: %w", err)
	}
	seen := make(map[string]bool, len(have)+len(rows))
	for _, it := range have {
		seen[it.Key.Sort] = true
	}
	writes := make([]objstore.Write, 0, len(rows))
	for _, r := range rows {
		if r.TokenHash == "" {
			return 0, fmt.Errorf("route_token %s: no token_hash, which is its coordinate", r.JID)
		}
		if seen[r.TokenHash] {
			continue
		}
		seen[r.TokenHash] = true
		attrs := map[string]string{
			attrTokJID:       r.JID,
			attrTokCreatedAt: r.CreatedAt,
			attrTokKind:      RouteTokenKindRoute,
		}
		setAttr(attrs, attrTokOwnerFolder, r.OwnerFolder)
		setAttr(attrs, attrTokTargetFolder, r.TargetFolder)
		setAttr(attrs, attrTokContext, r.Context)
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: RouteTokens,
			Key:   objstore.Key{Partition: OwnerRoutd, Sort: r.TokenHash},
			Attrs: attrs,
			Cond:  objstore.Cond{Item: objstore.ItemAbsent},
		})
	}
	if _, err := s.applyInChunks(ctx, "import route tokens", writes); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// CountRouteTokens counts EVERY item, kind='route' and kind='pair' alike: the
// proven-empty-target gate (spec 5/8 "Secret and token values" (2)) wants a
// genuinely fresh table, not merely zero route-kind rows.
func (s *Store) CountRouteTokens(ctx context.Context) (int, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: RouteTokens, Partition: OwnerRoutd})
	if err != nil {
		return 0, err
	}
	return len(items), nil
}

// MintRouteTokenInto mints a DELIVERY token for jid, buffers the write on the
// caller's write set and returns the raw bearer ONCE; only its digest is
// stored. It is routd's `/v1/route_tokens` minter, where InsertRouteToken
// would record the mint twice. The bearer is generated here and handed back
// before anything is written. It is IssuePairingLink's delivery twin; both
// render through routeTokenPut. target is the folder the token DELIVERS to:
// the JID grammar cannot recover it once a suffix is present (spec 5/W), so
// the mint is the only place it is known.
func (s *Store) MintRouteTokenInto(ctx context.Context, ws *WriteSet, jid, ownerFolder,
	targetFolder, linkContext string) (string, error) {
	if jid == "" {
		return "", errors.New("route token: jid required")
	}
	if err := CheckRouteTokenTarget(ctx, s.Obj(), jid, targetFolder); err != nil {
		return "", err
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("route token: generate token for %s: %w", jid, err)
	}
	raw := hex.EncodeToString(b[:])
	w, err := routeTokenPut(raw, RouteToken{
		JID: jid, OwnerFolder: ownerFolder, TargetFolder: targetFolder, Context: linkContext,
	}, RouteTokenKindRoute)
	if err != nil {
		return "", fmt.Errorf("route token: mint for %s: %w", jid, err)
	}
	ws.Add(w)
	return raw, nil
}

// RevokeRouteTokenInto buffers the deletion of every DELIVERY token for jid
// owned by ownerFolder and reports whether any was there. The owner_folder
// match IS the containment: a cross-folder revoke answers false rather than
// removing somebody else's bearer. The answer comes from the READ, before one
// write is sent.
//
// ONE JID MAY HOLD SEVERAL TOKENS, and the set goes in WHOLE rather than in
// chunks: a revoke that stopped part-way would leave live bearers behind under
// an audit row saying the jid was revoked. Past objstore.MaxBatchActions the
// batch is refused naming the count, a loud answer; the largest live instance
// holds 43 route tokens over 27 jids (2026-09-07).
func (s *Store) RevokeRouteTokenInto(ctx context.Context, ws *WriteSet,
	jid, ownerFolder string) (bool, error) {
	items, err := routeTokensOfKind(ctx, s.Obj(), RouteTokenKindRoute)
	if err != nil {
		return false, err
	}
	var writes []objstore.Write
	for _, it := range items {
		if it.Attrs[attrTokJID] != jid || it.Attrs[attrTokOwnerFolder] != ownerFolder {
			continue
		}
		writes = append(writes, objstore.Write{
			Op: objstore.Delete, Entity: RouteTokens, Key: it.Key,
		})
	}
	if len(writes) == 0 {
		return false, nil
	}
	ws.Add(writes...)
	return true, nil
}
