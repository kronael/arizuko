package store

// onboarding.go holds every read and write of `onboarding` and
// `onboarding_gates`: one item per unrouted chat waiting to be let in, and one
// item per admission gate. NOTHING ENFORCES THAT — `store` is not in
// db_utils/sql_layer_test.go's covered set, because that test walks a whole
// daemon directory and this package still holds statements for the tables that
// have not moved. Keeping the two out of the rest of the tree is the author's
// discipline until the package is empty enough to be named there.
//
// THE PARTITION IS store.OwnerOnbod — the literal "onbod" — AND IT IS NOT A
// CHOICE THIS FILE MAKES. Both tables are registered resreg resources
// (resreg/resources/onboarding.go, onboarding_gates.go), and an entity-backed
// resource's items live at its owner subsystem: resreg.SubsystemOnbod, which IS
// store.OwnerOnbod (resreg/resreg.go). A second spelling here would put this
// file's items in one partition and the engine's read in another, and the
// failure is silent — `arizuko export` comes back with an empty document and
// nothing to say why, because a query with a partition nobody wrote to is a
// legal query that selects nothing.
//
// WHAT THE CONSTANT COSTS is that every read below examines the whole
// collection. Measured on all three live snapshots 2026-09-07: 45 admission
// rows fleet-wide (krons 16, marinade 8, sloth 21) and ZERO gates. There is no
// drainage — DenyOnboarding is the only delete and it is an operator verb — so
// the bound is the number of distinct chats that ever messaged an instance
// unrouted. A jid is not the partition because eight of the ten reads select by
// something else (a status, a user_sub, a gate), and a query without a
// partition is refused by every driver.
//
// THE TIMESTAMPS ARE 20 BYTES AND THAT IS DECLARED, not re-rendered. Every
// `created` and `prompted_at` value in the fleet is exactly 20 bytes — 45 of 45
// — because one writer produces them all: time.RFC3339 of a .UTC() time, which
// truncates the fraction and ends in `Z`. `queued_at` and `admitted_at` hold no
// value at all anywhere. So the width is what the data already is, and widening
// it to core.TS would move every checksum and break the archive round-trip for
// nothing. The declaration is what makes the comparisons mean what they say:
// `prompted_at < now-PairingTTL` re-greets, `queued_at <` counts the queue
// ahead of you and `admitted_at >=` bounds today's quota, and all three are
// BYTE comparisons — a short value sorts into the wrong place instead of
// failing (BUGS L97).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore"
)

// onbodTSWidth is the width of every stored instant in these two entities and
// in invites: time.RFC3339 of a UTC time. A test pins it against the layout,
// because a constant that drifted from the format would refuse every write
// instead of guarding one.
const onbodTSWidth = 20

// onbodTS renders one instant at the declared width. It is the only place these
// entities format a time, so the width and the layout cannot disagree.
func onbodTS(at time.Time) string { return at.UTC().Format(time.RFC3339) }

// The attributes an admission item carries. `jid` is NOT among them: it IS the
// sort key, and storing the coordinate twice is two facts that can disagree.
// The five optional ones are ABSENT when they have no value — absent is the
// only null this model has — which is what the five COALESCE-on-read /
// nil-on-write hook pairs on the resource used to buy.
const (
	attrOnbStatus     = "status"
	attrOnbUserSub    = "user_sub"
	attrOnbGate       = "gate"
	attrOnbCreated    = "created"
	attrOnbPromptedAt = "prompted_at"
	attrOnbQueuedAt   = "queued_at"
	attrOnbAdmittedAt = "admitted_at"
)

// The attributes a gate item carries; `gate` is the sort key. `enabled` is an
// int rendered by strconv, which is what resreg's own entity path writes for an
// int field (resreg/entity.go itemFor) — the two readers of these items have to
// agree byte for byte.
const (
	attrGateLimitPerDay = "limit_per_day"
	attrGateEnabled     = "enabled"
)

// The statuses this package writes. onbod's knownStatuses is the reader's
// complete set; these are the four transitions the storage layer performs.
const (
	statusAwaitingMessage = "awaiting_message"
	statusQueued          = "queued"
	statusApproved        = "approved"
	statusRefused         = "refused"
)

// Every attribute a read COMPARES is declared, so every driver refuses a value
// of another width. The file header says why 20 and what the comparisons are.
func init() {
	objstore.OrderedAttr(Onboarding, attrOnbCreated, onbodTSWidth)
	objstore.OrderedAttr(Onboarding, attrOnbPromptedAt, onbodTSWidth)
	objstore.OrderedAttr(Onboarding, attrOnbQueuedAt, onbodTSWidth)
	objstore.OrderedAttr(Onboarding, attrOnbAdmittedAt, onbodTSWidth)
}

// onboardingKey is the coordinate of one admission. An empty jid is refused
// rather than written: every such row would address one item and the last write
// would win.
func onboardingKey(jid string) (objstore.Key, error) {
	if jid == "" {
		return objstore.Key{}, errors.New("onboarding row carries no jid, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerOnbod, Sort: jid}, nil
}

// gateKey is the coordinate of one gate.
func gateKey(gate string) (objstore.Key, error) {
	if gate == "" {
		return objstore.Key{}, errors.New("gate row carries no gate, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerOnbod, Sort: gate}, nil
}

// InsertOnboarding records that jid is unrouted and awaiting setup. It fires from
// routd's route-MISS branch, so it runs once per inbound message a route table did
// not claim.
//
// That makes it the re-greet trigger too (spec 5/31). `prompted_at` unset used to
// be a permanent lockout: the jid coordinate blocks a fresh item, so a user whose
// link died could never get another. Clearing prompted_at here arms the next tick,
// bounded two ways — the caller must have MISSED, and the last greeting must be
// older than PairingTTL. A plain time cooldown in the poll query has neither bound
// and would re-greet every stale row forever.
//
// TWO WRITES BECAUSE THE INTERFACE HAS TWO SHAPES. A Put carries the item's whole
// attribute set, so writing one unconditionally would erase the admission verdict
// on a row that already exists. The create-if-absent goes first and its REFUSAL —
// the chat is already known — is what selects the conditional reset. A refusal of
// that second write is equally normal: the row is not due a re-greet, which is
// every call but the one that is.
func (s *Store) InsertOnboarding(jid string) error {
	key, err := onboardingKey(jid)
	if err != nil {
		return err
	}
	ctx := context.Background()
	now := time.Now().UTC()
	created, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Put, Entity: Onboarding, Key: key,
		Attrs: map[string]string{
			attrOnbStatus:  statusAwaitingMessage,
			attrOnbCreated: onbodTS(now),
		},
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil {
		return fmt.Errorf("record %s as awaiting onboarding: %w", jid, err)
	}
	if created {
		return nil
	}
	// A comparison against an attribute the item does not carry is not TRUE on
	// either engine, so `prompted_at <` already excludes a row that was never
	// greeted; it needs no Present term of its own. A refusal is that guard
	// holding — the row is greeted and still inside its cooldown — which is the
	// answer this asks for.
	if _, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: Onboarding, Key: key,
		Remove: []string{attrOnbPromptedAt},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrOnbStatus, Op: objstore.Eq, Value: statusAwaitingMessage},
			{Attr: attrOnbPromptedAt, Op: objstore.Lt, Value: onbodTS(now.Add(-PairingTTL))},
		}},
	}); err != nil {
		return fmt.Errorf("re-arm the greeting for %s: %w", jid, err)
	}
	return nil
}

// CarryOnboardingLegacy moves rows from the onboarding_legacy TABLE the reshaping
// migration leaves behind into the onboarding entity, then drops it. Idempotent: a
// no-op once onboarding_legacy is gone, which is every boot on every live instance
// and every boot of a database created since the table became an entity. Every
// opener that runs the onboarding migrations also runs this.
//
// The read stays SQL because its subject IS a table — a legacy shape no entity has
// — and the write and the DROP ride the read's own transaction, so a crash between
// them leaves the source recoverable rather than half moved. db_utils.TableExists
// asks whichever engine this database is on whether the table is there.
//
// The token columns are deliberately NOT named here: onbod 0006 drops them, so
// naming them would fail outright against the post-0006 schema (BUGS F40).
func CarryOnboardingLegacy(db *sql.DB) error {
	exists, err := db_utils.TableExists(db, "onboarding_legacy")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // the commit below is the success path

	rows, err := tx.Query(`SELECT jid, status, prompted_at, created,
	                              user_sub, gate, queued_at, admitted_at
	                       FROM onboarding_legacy`)
	if err != nil {
		return fmt.Errorf("read onboarding_legacy: %w", err)
	}
	var legacy []OnboardingRow
	for rows.Next() {
		var (
			r OnboardingRow
			promptedAt, userSub,
			gate, queuedAt, admittedAt sql.NullString
		)
		if err := rows.Scan(&r.JID, &r.Status, &promptedAt, &r.Created,
			&userSub, &gate, &queuedAt, &admittedAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan onboarding_legacy row: %w", err)
		}
		r.PromptedAt, r.UserSub = promptedAt.String, userSub.String
		r.Gate, r.QueuedAt, r.AdmittedAt = gate.String, queuedAt.String, admittedAt.String
		legacy = append(legacy, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close() // must close before reusing tx for the writes below

	writes := make([]objstore.Write, 0, len(legacy))
	for _, r := range legacy {
		w, err := onboardingPut(r)
		if err != nil {
			return fmt.Errorf("carry onboarding row forward: %w", err)
		}
		writes = append(writes, w)
	}
	if _, err := applyChunks(context.Background(), "carry onboarding rows forward", writes,
		func(ctx context.Context, chunk []objstore.Write) (bool, error) {
			return New(db).TryWriteEntitiesInTx(ctx, tx, nil, chunk...)
		}); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE onboarding_legacy`); err != nil {
		return fmt.Errorf("drop onboarding_legacy: %w", err)
	}
	return tx.Commit()
}

type OnboardingGate struct {
	Gate        string
	LimitPerDay int
	Enabled     bool
}

// gateFrom renders one stored item as the gate a caller reads. The key comes
// back out of the coordinate, which is where it was written.
func gateFrom(it objstore.Item) (OnboardingGate, error) {
	limit, err := intAttr(OnboardingGates, it, attrGateLimitPerDay)
	if err != nil {
		return OnboardingGate{}, err
	}
	enabled, err := intAttr(OnboardingGates, it, attrGateEnabled)
	if err != nil {
		return OnboardingGate{}, err
	}
	return OnboardingGate{Gate: it.Key.Sort, LimitPerDay: limit, Enabled: enabled != 0}, nil
}

// intAttr reads a numeric attribute. A value that is not a number is an ERROR
// rather than a zero: a gate whose limit read as 0 would admit nobody and a
// max_uses that read as 0 would refuse every redemption, both silently.
func intAttr(entity string, it objstore.Item, attr string) (int, error) {
	text, ok := it.Attrs[attr]
	if !ok {
		return 0, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %s is %q, which is not a number: %w",
			entity, it.Key.Sort, attr, text, err)
	}
	return n, nil
}

// ListGates reads every gate, in key order — which is the driver's own order,
// so nothing sorts here.
func (s *Store) ListGates() ([]OnboardingGate, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: OnboardingGates, Partition: OwnerOnbod,
	})
	if err != nil {
		return nil, fmt.Errorf("read the gates: %w", err)
	}
	var out []OnboardingGate
	for _, it := range items {
		g, err := gateFrom(it)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

// EnabledGates reads the gates that admit — the set onbod's admission tick and
// its first-world verdict both ask for. The filter is the engine's, so a
// disabled gate never reaches this process.
func (s *Store) EnabledGates() ([]OnboardingGate, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: OnboardingGates, Partition: OwnerOnbod,
		Where: []objstore.Cmp{{Attr: attrGateEnabled, Op: objstore.Eq, Value: "1"}},
	})
	if err != nil {
		return nil, fmt.Errorf("read the enabled gates: %w", err)
	}
	var out []OnboardingGate
	for _, it := range items {
		g, err := gateFrom(it)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

// Gate reads one gate by key. (zero, false) when the instance has none by that
// name.
func (s *Store) Gate(gate string) (OnboardingGate, bool, error) {
	key, err := gateKey(gate)
	if err != nil {
		return OnboardingGate{}, false, err
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: OnboardingGates, Partition: key.Partition,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil {
		return OnboardingGate{}, false, fmt.Errorf("read gate %s: %w", gate, err)
	}
	if len(items) == 0 {
		return OnboardingGate{}, false, nil
	}
	g, err := gateFrom(items[0])
	if err != nil {
		return OnboardingGate{}, false, err
	}
	return g, true, nil
}

// PutGateTx sets a gate's daily admission limit on a transaction the CALLER
// owns, so the mutation and the audit row that records it commit together.
//
// TWO WRITES for the reason InsertOnboarding has two: a Put carries the whole
// attribute set, so an unconditional one would re-enable a gate the operator
// disabled. The create-if-absent lands enabled, and its refusal — the gate is
// already there — selects the update that touches only the limit.
func (s *Store) PutGateTx(ctx context.Context, tx *sql.Tx, gate string, limitPerDay int) error {
	key, err := gateKey(gate)
	if err != nil {
		return err
	}
	created, err := s.TryWriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Put, Entity: OnboardingGates, Key: key,
		Attrs: map[string]string{
			attrGateLimitPerDay: strconv.Itoa(limitPerDay),
			attrGateEnabled:     "1",
		},
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
	if err != nil {
		return fmt.Errorf("open gate %s: %w", gate, err)
	}
	if created {
		return nil
	}
	if err := s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Update, Entity: OnboardingGates, Key: key,
		Attrs: map[string]string{attrGateLimitPerDay: strconv.Itoa(limitPerDay)},
	}); err != nil {
		return fmt.Errorf("set gate %s's limit: %w", gate, err)
	}
	return nil
}

// EnableGateTx flips a gate's enablement on the caller's transaction. A gate
// that is not there is an ERROR: an Update requires the item to exist, and the
// refusal it reports is the ONLY signal that the flag did not move. Dropping it
// answered `PUT /v1/gates/<gate>` 200 {"ok":true} over zero rows, which onbod
// now answers 404 (BUGS L268).
func (s *Store) EnableGateTx(ctx context.Context, tx *sql.Tx, gate string, enabled bool) error {
	key, err := gateKey(gate)
	if err != nil {
		return err
	}
	if err := s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Update, Entity: OnboardingGates, Key: key,
		Attrs: map[string]string{attrGateEnabled: strconv.Itoa(btoi(enabled))},
	}); err != nil {
		return fmt.Errorf("set gate %s's enablement: %w", gate, err)
	}
	return nil
}

// DeleteGateTx removes a gate on the caller's transaction. Unconditional, which
// is what a delete means: asking twice is the same answer.
func (s *Store) DeleteGateTx(ctx context.Context, tx *sql.Tx, gate string) error {
	key, err := gateKey(gate)
	if err != nil {
		return err
	}
	if err := s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Delete, Entity: OnboardingGates, Key: key,
	}); err != nil {
		return fmt.Errorf("delete gate %s: %w", gate, err)
	}
	return nil
}

// PutGate is PutGateTx with a transaction and an audit row of its own, for the
// CLI and the chat command, which hold neither.
func (s *Store) PutGate(gate string, limitPerDay int) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		err := s.PutGateTx(context.Background(), tx, gate, limitPerDay)
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "gate.set",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "onboarding_gates/" + gate,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"gate":          gate,
				"limit_per_day": limitPerDay,
			},
		}, err
	})
}

// DeleteGate is DeleteGateTx with its own transaction and audit row.
func (s *Store) DeleteGate(gate string) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		err := s.DeleteGateTx(context.Background(), tx, gate)
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "gate.delete",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "onboarding_gates/" + gate,
			Outcome:  audit.OutcomeOK,
		}, err
	})
}

// EnableGate is EnableGateTx with its own transaction and audit row.
func (s *Store) EnableGate(gate string, enabled bool) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		err := s.EnableGateTx(context.Background(), tx, gate, enabled)
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "gate.set",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "onboarding_gates/" + gate,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"enabled": enabled,
			},
		}, err
	})
}

// OnboardingRow is one row from the onboarding admission table.
type OnboardingRow struct {
	JID        string `json:"jid"`
	Status     string `json:"status"`
	UserSub    string `json:"user_sub,omitempty"`
	Gate       string `json:"gate,omitempty"`
	Created    string `json:"created"`
	PromptedAt string `json:"prompted_at,omitempty"`
	QueuedAt   string `json:"queued_at,omitempty"`
	AdmittedAt string `json:"admitted_at,omitempty"`
}

// onboardingFrom renders one stored item as the row every read surface sees.
// The jid comes back out of the sort key, and an attribute the item does not
// carry leaves its field empty, which is what absent means.
func onboardingFrom(it objstore.Item) OnboardingRow {
	return OnboardingRow{
		JID:        it.Key.Sort,
		Status:     it.Attrs[attrOnbStatus],
		UserSub:    it.Attrs[attrOnbUserSub],
		Gate:       it.Attrs[attrOnbGate],
		Created:    it.Attrs[attrOnbCreated],
		PromptedAt: it.Attrs[attrOnbPromptedAt],
		QueuedAt:   it.Attrs[attrOnbQueuedAt],
		AdmittedAt: it.Attrs[attrOnbAdmittedAt],
	}
}

// onboardingPut renders a whole row as the item that stores it — the single
// renderer the legacy carry, the archive restore and the migration tool all
// write through, so three sources cannot disagree about the encoding.
//
// EVERY empty attribute is left OFF, `status` and `created` included, even
// though their columns were NOT NULL. Absent is the only null this model has,
// and this is byte for byte what resreg's own entity path writes for a string
// field (resreg/entity.go itemFor) — the two readers of these items have to
// agree, and "" is the one value they could have disagreed about.
func onboardingPut(r OnboardingRow) (objstore.Write, error) {
	key, err := onboardingKey(r.JID)
	if err != nil {
		return objstore.Write{}, err
	}
	attrs := map[string]string{}
	setAttr(attrs, attrOnbStatus, r.Status)
	setAttr(attrs, attrOnbCreated, r.Created)
	setAttr(attrs, attrOnbUserSub, r.UserSub)
	setAttr(attrs, attrOnbGate, r.Gate)
	setAttr(attrs, attrOnbPromptedAt, r.PromptedAt)
	setAttr(attrs, attrOnbQueuedAt, r.QueuedAt)
	setAttr(attrs, attrOnbAdmittedAt, r.AdmittedAt)
	return objstore.Write{
		Op: objstore.Put, Entity: Onboarding, Key: key, Attrs: attrs,
	}, nil
}

// gatePut renders a whole gate as the item that stores it, for the migration
// tool — the live writers move one attribute at a time and never replace the
// document (PutGateTx, EnableGateTx).
func gatePut(g OnboardingGate) (objstore.Write, error) {
	key, err := gateKey(g.Gate)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{
		Op: objstore.Put, Entity: OnboardingGates, Key: key,
		Attrs: map[string]string{
			attrGateLimitPerDay: strconv.Itoa(g.LimitPerDay),
			attrGateEnabled:     strconv.Itoa(btoi(g.Enabled)),
		},
	}, nil
}

// readOnboarding is the one read behind every admission query below, so "which
// admissions match this" cannot mean one thing to a page and another to a
// counter. The filter is the engine's; the ORDER is the coordinate's.
func (s *Store) readOnboarding(ctx context.Context, where ...objstore.Cmp) ([]OnboardingRow, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Onboarding, Partition: OwnerOnbod, Where: where,
	})
	if err != nil {
		return nil, fmt.Errorf("read the admissions: %w", err)
	}
	out := make([]OnboardingRow, 0, len(items))
	for _, it := range items {
		out = append(out, onboardingFrom(it))
	}
	return out, nil
}

// ListOnboarding returns onboarding rows, optionally filtered by status. Empty
// statusFilter returns all rows. Ordered by created DESC. The setup link is not
// in this projection and has no attribute left to be in: the bearer lives solely
// in the link the user was sent (Z3), and token_ref/token_expires were dropped
// by onbod 0006 once the 5/31 fold left nothing writing them (BUGS F40).
//
// The order is an ATTRIBUTE's, not the coordinate's, so it is applied here over
// a whole-collection read — the file header bounds what that costs. A stable
// sort over the driver's jid order makes two rows created in the same second
// come back in a fixed order, which the `ORDER BY created DESC` it replaces did
// not promise.
func (s *Store) ListOnboarding(statusFilter string) ([]OnboardingRow, error) {
	var where []objstore.Cmp
	if statusFilter != "" {
		where = append(where, objstore.Cmp{Attr: attrOnbStatus, Op: objstore.Eq, Value: statusFilter})
	}
	out, err := s.readOnboarding(context.Background(), where...)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out, nil
}

// UnpromptedJIDs names every chat that is waiting and has never been greeted —
// the poll tick's work list.
func (s *Store) UnpromptedJIDs() ([]string, error) {
	rows, err := s.readOnboarding(context.Background(),
		objstore.Cmp{Attr: attrOnbStatus, Op: objstore.Eq, Value: statusAwaitingMessage},
		objstore.Cmp{Attr: attrOnbPromptedAt, Op: objstore.Absent})
	if err != nil {
		return nil, err
	}
	return onboardingJIDs(rows), nil
}

// UnadmittedJIDs names every chat that is waiting and carries no admission
// verdict — the pairing observer's work list. `user_sub` absent is the dedup:
// a row is admitted once, and an agent-minted pairing is invisible here.
func (s *Store) UnadmittedJIDs() ([]string, error) {
	rows, err := s.readOnboarding(context.Background(),
		objstore.Cmp{Attr: attrOnbStatus, Op: objstore.Eq, Value: statusAwaitingMessage},
		objstore.Cmp{Attr: attrOnbUserSub, Op: objstore.Absent})
	if err != nil {
		return nil, err
	}
	return onboardingJIDs(rows), nil
}

// QueuedJIDs names the chats queued behind one gate, oldest first, at most
// limit of them — the batch a gate's remaining daily quota admits.
func (s *Store) QueuedJIDs(gate string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.readOnboarding(context.Background(),
		objstore.Cmp{Attr: attrOnbStatus, Op: objstore.Eq, Value: statusQueued},
		objstore.Cmp{Attr: attrOnbGate, Op: objstore.Eq, Value: gate})
	if err != nil {
		return nil, err
	}
	// Oldest first, by an attribute rather than the coordinate, so the order and
	// the limit are applied here — a Limit on the query would have cut by jid.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].QueuedAt < rows[j].QueuedAt })
	return onboardingJIDs(rows[:min(limit, len(rows))]), nil
}

// onboardingJIDs is the coordinate list of a row set.
func onboardingJIDs(rows []OnboardingRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.JID)
	}
	return out
}

// QueuedAdmission answers which gate is holding a user and since when.
// (zero, false) when the user is not queued.
func (s *Store) QueuedAdmission(userSub string) (OnboardingRow, bool, error) {
	rows, err := s.readOnboarding(context.Background(),
		objstore.Cmp{Attr: attrOnbStatus, Op: objstore.Eq, Value: statusQueued},
		objstore.Cmp{Attr: attrOnbUserSub, Op: objstore.Eq, Value: userSub})
	if err != nil || len(rows) == 0 {
		return OnboardingRow{}, false, err
	}
	return rows[0], true, nil
}

// ApprovedAdmissions counts a user's approved admissions — the second authority
// an admitted stranger holds to create a first world (spec 5/18 step 8).
func (s *Store) ApprovedAdmissions(userSub string) (int, error) {
	rows, err := s.readOnboarding(context.Background(),
		objstore.Cmp{Attr: attrOnbStatus, Op: objstore.Eq, Value: statusApproved},
		objstore.Cmp{Attr: attrOnbUserSub, Op: objstore.Eq, Value: userSub})
	return len(rows), err
}

// QueuePosition counts the chats queued behind one gate AHEAD of queuedAt — the
// number the queue page turns into a position and an ETA. The comparison is a
// byte comparison on a declared 20-byte attribute, which is what makes it a
// time comparison (file header).
func (s *Store) QueuePosition(gate, queuedAt string) (int, error) {
	rows, err := s.readOnboarding(context.Background(),
		objstore.Cmp{Attr: attrOnbStatus, Op: objstore.Eq, Value: statusQueued},
		objstore.Cmp{Attr: attrOnbGate, Op: objstore.Eq, Value: gate},
		objstore.Cmp{Attr: attrOnbQueuedAt, Op: objstore.Lt, Value: queuedAt})
	return len(rows), err
}

// AdmittedInWindow counts one gate's admissions inside a half-open instant
// range — today's quota, so a backlog queued on a prior day does not evade it.
func (s *Store) AdmittedInWindow(gate, from, to string) (int, error) {
	rows, err := s.readOnboarding(context.Background(),
		objstore.Cmp{Attr: attrOnbGate, Op: objstore.Eq, Value: gate},
		objstore.Cmp{Attr: attrOnbStatus, Op: objstore.Eq, Value: statusApproved},
		objstore.Cmp{Attr: attrOnbAdmittedAt, Op: objstore.Ge, Value: from},
		objstore.Cmp{Attr: attrOnbAdmittedAt, Op: objstore.Lt, Value: to})
	return len(rows), err
}

// ClaimOnboardingPrompt stamps the instant a chat was greeted, and only when it
// has not been. The condition IS the claim: two overlapping poll ticks cannot
// both win one jid, and only the tick this returns true for sends the link.
//
// The instant is rendered here rather than passed in, so the declared width and
// the layout that satisfies it stay in one place.
func (s *Store) ClaimOnboardingPrompt(jid string) (bool, error) {
	key, err := onboardingKey(jid)
	if err != nil {
		return false, err
	}
	claimed, err := s.TryWriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: Onboarding, Key: key,
		Attrs: map[string]string{attrOnbPromptedAt: onbodTS(time.Now())},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrOnbPromptedAt, Op: objstore.Absent},
		}},
	})
	if err != nil {
		return false, fmt.Errorf("claim the greeting for %s: %w", jid, err)
	}
	return claimed, nil
}

// QueueOnboarding records that a gate matched and the chat is waiting its turn.
func (s *Store) QueueOnboarding(jid, userSub, gate string) error {
	return s.setOnboarding(jid, "queue", map[string]string{
		attrOnbStatus:   statusQueued,
		attrOnbUserSub:  userSub,
		attrOnbGate:     gate,
		attrOnbQueuedAt: onbodTS(time.Now()),
	})
}

// RefuseOnboarding records that no gate matched. Terminal: user_sub is stamped
// so the row stops matching UnadmittedJIDs and the refusal is sent exactly once.
func (s *Store) RefuseOnboarding(jid, userSub string) error {
	return s.setOnboarding(jid, "refuse", map[string]string{
		attrOnbStatus:  statusRefused,
		attrOnbUserSub: userSub,
	})
}

// AdmitOnboarding records the admission of a paired chat on an instance with no
// gate configured, where every paired identity is let straight in.
//
// It is not ApproveOnboarding: that one is the OPERATOR's fast-path and must
// leave user_sub alone, because the operator approving a row says nothing about
// who the chat belongs to.
func (s *Store) AdmitOnboarding(jid, userSub string) error {
	return s.setOnboarding(jid, "admit", map[string]string{
		attrOnbStatus:     statusApproved,
		attrOnbUserSub:    userSub,
		attrOnbAdmittedAt: onbodTS(time.Now()),
	})
}

// setOnboarding merges one verdict into an admission row. A row that is not
// there is NOT an error — the UPDATEs these replace affected no rows and said
// nothing — because the observer reads the work list one tick before it writes,
// and an operator may deny a jid in between.
func (s *Store) setOnboarding(jid, what string, attrs map[string]string) error {
	key, err := onboardingKey(jid)
	if err != nil {
		return err
	}
	// A refusal is the jid no longer being there, which the doc above allows: the
	// observer reads the work list one tick before it writes.
	if _, err := s.TryWriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: Onboarding, Key: key, Attrs: attrs,
	}); err != nil {
		return fmt.Errorf("%s %s: %w", what, jid, err)
	}
	return nil
}

// AdmitQueued approves a gate's batch from the queue and answers how many
// landed. The caller records one audit event per admitted jid, so it needs the
// count rather than a bare error.
//
// The batch is CHUNKED, which is the one thing the transaction it replaces did
// differently: a whole gate's admissions rode one SQL transaction and rolled
// back together, where MaxBatchActions is the weakest engine's bound and a gate
// whose daily limit exceeds it now commits in pieces. That is safe because the
// pieces are independent and the next tick re-reads the queue — a partial run
// leaves work a re-run finishes, not a half-applied transaction.
func (s *Store) AdmitQueued(jids []string) (int, error) {
	now := onbodTS(time.Now())
	writes := make([]objstore.Write, 0, len(jids))
	for _, jid := range jids {
		key, err := onboardingKey(jid)
		if err != nil {
			return 0, err
		}
		writes = append(writes, objstore.Write{
			Op: objstore.Update, Entity: Onboarding, Key: key,
			Attrs: map[string]string{
				attrOnbStatus:     statusApproved,
				attrOnbAdmittedAt: now,
			},
		})
	}
	return s.applyInChunks(context.Background(), "admit the queued chats", writes)
}

// ApproveOnboarding sets a row to approved immediately (operator fast-path,
// bypassing the gate's daily limit). Returns an error if the row does not exist
// — the refusal an Update reports for a missing item IS that answer, and it is
// the same one the RowsAffected check it replaces gave.
func (s *Store) ApproveOnboarding(jid string) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		key, err := onboardingKey(jid)
		if err != nil {
			return audit.Event{}, err
		}
		approved, err := s.TryWriteEntitiesInTx(context.Background(), tx, nil, objstore.Write{
			Op: objstore.Update, Entity: Onboarding, Key: key,
			Attrs: map[string]string{
				attrOnbStatus:     statusApproved,
				attrOnbAdmittedAt: onbodTS(time.Now()),
			},
		})
		if err != nil {
			return audit.Event{}, err
		}
		if !approved {
			return audit.Event{}, fmt.Errorf("onboarding row not found: %s", jid)
		}
		return audit.Event{
			Category: audit.CategoryMutation, Action: "onboarding.approve",
			Actor: "operator", Surface: audit.SurfaceREST,
			Resource: "onboarding/" + jid, Outcome: audit.OutcomeOK,
		}, nil
	})
}

// DenyOnboarding deletes the onboarding row for jid (operator deny).
func (s *Store) DenyOnboarding(jid string) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		key, err := onboardingKey(jid)
		if err != nil {
			return audit.Event{}, err
		}
		err = s.WriteEntitiesInTx(context.Background(), tx, nil, objstore.Write{
			Op: objstore.Delete, Entity: Onboarding, Key: key,
		})
		return audit.Event{
			Category: audit.CategoryMutation, Action: "onboarding.deny",
			Actor: "operator", Surface: audit.SurfaceREST,
			Resource: "onboarding/" + jid, Outcome: audit.OutcomeOK,
		}, err
	})
}

// RepromptOnboarding resets an onboarding row to awaiting_message so the next poll
// tick greets it again. A FULL reset, admission verdict included: after the fold
// the observer only advances a row with no user_sub, so leaving the old verdict's
// user_sub behind would re-greet the chat and then never admit it again (BUGS O1).
// The pairing edge is untouched — it is the user's consent, not admission state.
//
// ONE WRITE, and that is the guarantee rather than a tidiness. Attrs and Remove
// are the two halves of a single document expression (objstore.Write), so the
// five attributes go together or none of them do; five writes could leave one
// behind, which is exactly the state O1 describes.
//
// It is the operator's cooldown BYPASS, for a chat that is not messaging again on
// its own.
func (s *Store) RepromptOnboarding(jid string) error {
	key, err := onboardingKey(jid)
	if err != nil {
		return err
	}
	if err := s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: Onboarding, Key: key,
		Attrs: map[string]string{attrOnbStatus: statusAwaitingMessage},
		Remove: []string{
			attrOnbPromptedAt, attrOnbUserSub, attrOnbGate,
			attrOnbQueuedAt, attrOnbAdmittedAt,
		},
	}); err != nil {
		return fmt.Errorf("reprompt %s: %w", jid, err)
	}
	return nil
}

// --- the archive lane (spec 5/8 "Message history", Z3) --------------------

// ArchiveOnboardingRow is one row of the archive's onboarding document — the
// same shape OnboardingRow (resreg/resources/onboarding.go) publishes. It used
// to carry token_ref/token_expires too, the hash-at-rest verifier and its
// expiry; onbod 0006 dropped both columns once the 5/31 fold left nothing
// writing them (BUGS F40), so there is no longer a credential in this document.
//
// Admissions still need this archive-only lane because they cannot ride the
// config lane and cannot be rederived. `onboarding` is SkipApplyRebuild —
// admissions are runtime state driven by chat traffic and the gate limiter,
// never declared in a manifest — so `arizuko apply` never writes the table at
// all. And rederiving is impossible: routeMiss advances agent_cursor past a
// route-missed message whether or not the admission insert succeeded (a
// deliberate fail-forward — a re-fed miss would replay forever), and import
// reproduces that by setting agent_cursor, so a pending admission not
// independently in the archive is gone for good: the person is neither in the
// queue nor going to message again.
//
// It keeps riding the same off-by-default gate as route_tokens and invites.
// The reason is no longer a verifier but the verdicts: importing admission
// decisions onto a live instance is a merge, not a restore.
type ArchiveOnboardingRow struct {
	JID        string `yaml:"jid"`
	Status     string `yaml:"status"`
	UserSub    string `yaml:"user_sub,omitempty"`
	Gate       string `yaml:"gate,omitempty"`
	Created    string `yaml:"created"`
	PromptedAt string `yaml:"prompted_at,omitempty"`
	QueuedAt   string `yaml:"queued_at,omitempty"`
	AdmittedAt string `yaml:"admitted_at,omitempty"`
}

// ExportOnboarding reads every admission row, ordered by jid for deterministic
// output — which is the coordinate's own order, so the driver supplies it.
func (s *Store) ExportOnboarding(ctx context.Context) ([]ArchiveOnboardingRow, error) {
	rows, err := s.readOnboarding(ctx)
	if err != nil {
		return nil, err
	}
	var out []ArchiveOnboardingRow
	for _, r := range rows {
		out = append(out, ArchiveOnboardingRow(r))
	}
	return out, nil
}

// ImportOnboarding writes by jid (the coordinate) and never replaces — matching
// ImportRouteTokens/ImportInvites. Ignore-if-present is the right shape: an
// admission's history (created, prompted_at, queued_at, admitted_at) is
// append-only in practice, and a jid already present on the target is a live
// admission this restore must not walk backwards.
//
// The already-present set is READ first, because a batch is all-or-nothing: a
// create-if-absent that lost would refuse every other row riding with it, where
// the INSERT OR IGNORE this replaces skipped one row and carried on. What stays
// conditional is the write itself, so a jid that appears between the read and
// the write is a loud refusal rather than a silent overwrite.
//
// Performs NO gating — the off-by-default / --force / proven-empty-target
// policy is `archive apply` orchestration in cmd/arizuko, not this
// primitive's job.
func (s *Store) ImportOnboarding(ctx context.Context, rows []ArchiveOnboardingRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	have, err := s.readOnboarding(ctx)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]bool, len(have)+len(rows))
	for _, r := range have {
		seen[r.JID] = true
	}
	writes := make([]objstore.Write, 0, len(rows))
	for _, r := range rows {
		if seen[r.JID] {
			continue
		}
		seen[r.JID] = true
		w, err := onboardingPut(OnboardingRow(r))
		if err != nil {
			return 0, fmt.Errorf("insert onboarding %s: %w", r.JID, err)
		}
		w.Cond = objstore.Cond{Item: objstore.ItemAbsent}
		writes = append(writes, w)
	}
	if _, err := s.applyInChunks(ctx, "import the admissions", writes); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// CountOnboarding counts every admission row — the proven-empty-target gate's
// third twin, alongside CountRouteTokens and CountInvites.
func (s *Store) CountOnboarding(ctx context.Context) (int, error) {
	rows, err := s.readOnboarding(ctx)
	return len(rows), err
}
