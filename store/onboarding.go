package store

// onboarding.go holds every read and write of `onboarding` and
// `onboarding_gates`: one item per unrouted chat waiting to be let in, and one
// item per admission gate.
//
// The partition is store.OwnerOnbod because both tables are registered resreg
// resources and resreg.SubsystemOnbod IS store.OwnerOnbod; a second spelling
// would empty `arizuko export` without any read failing. Every read examines
// the whole collection (45 admission rows fleet-wide, 2026-09-07; no drainage
// but DenyOnboarding), because eight of the ten reads select by an attribute.
//
// THE TIMESTAMPS ARE 20 BYTES AND THAT IS DECLARED, not re-rendered: every
// live value is time.RFC3339 of a UTC time, and widening them would move every
// checksum for nothing. The declaration is what makes `prompted_at <`,
// `queued_at <` and `admitted_at >=` time comparisons, because they are BYTE
// comparisons (BUGS L97).

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/objstore"
)

// onbodTSWidth is the width of every stored instant in these two entities and
// in invites: time.RFC3339 of a UTC time. A test pins it against the layout.
const onbodTSWidth = 20

// onbodTS renders one instant at the declared width: the only place these
// entities format a time.
func onbodTS(at time.Time) string { return at.UTC().Format(time.RFC3339) }

// The attributes an admission item carries. `jid` IS the sort key. The five
// optional ones are ABSENT when they have no value.
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
// int rendered by strconv, what resreg's own entity path writes for an int
// field (resreg/entity.go itemFor).
const (
	attrGateLimitPerDay = "limit_per_day"
	attrGateEnabled     = "enabled"
)

// The statuses this package writes. onbod's knownStatuses is the reader's
// complete set.
const (
	statusAwaitingMessage = "awaiting_message"
	statusQueued          = "queued"
	statusApproved        = "approved"
	statusRefused         = "refused"
)

// Every attribute a read COMPARES is declared, so every driver refuses a value
// of another width.
func init() {
	objstore.OrderedAttr(Onboarding, attrOnbCreated, onbodTSWidth)
	objstore.OrderedAttr(Onboarding, attrOnbPromptedAt, onbodTSWidth)
	objstore.OrderedAttr(Onboarding, attrOnbQueuedAt, onbodTSWidth)
	objstore.OrderedAttr(Onboarding, attrOnbAdmittedAt, onbodTSWidth)
}

// onboardingKey is the coordinate of one admission. An empty jid is refused:
// every such row would address one item.
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

// InsertOnboarding records that jid is unrouted and awaiting setup. It fires
// from routd's route-MISS branch, once per inbound message no route claimed,
// which makes it the re-greet trigger too (spec 5/31-identity-pairing): an unset `prompted_at`
// was a permanent lockout, because the jid coordinate blocks a fresh item.
// Clearing it here arms the next tick, bounded two ways: the caller MISSED,
// and the last greeting is older than PairingTTL.
//
// Two writes: a Put would erase the admission verdict on an existing row, so
// the create-if-absent goes first and its REFUSAL selects the conditional
// reset, whose own refusal is normal (the row is not due a re-greet).
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
	// `prompted_at <` already excludes a row that was never greeted: a
	// comparison against an absent attribute is not TRUE on either engine. A
	// refusal is that guard holding.
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

type OnboardingGate struct {
	Gate        string
	LimitPerDay int
	Enabled     bool
}

// gateFrom renders one stored item as the gate a caller reads.
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
// rather than a zero: a gate whose limit read as 0 would admit nobody, and a
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

// ListGates reads every gate, in key order.
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

// EnabledGates reads the gates that admit: the set onbod's admission tick and
// its first-world verdict both ask for.
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

// SetGateInto buffers ONE gate change on the caller's write set: a batch may
// name each item once, and both halves address the same gate. limitPerDay 0
// leaves the limit alone and enabled nil leaves enablement alone, the
// tri-state `PUT /v1/gates/<gate>` carries.
//
// A call that sets the limit CREATES the gate, so it is a Merge and refuses
// nothing; `enabled` is an Init there, because a caller that came to set the
// limit must not re-enable a gate the operator disabled. A call that only
// flips the flag must FIND the gate, so it is an Update, and refused is the
// "no such gate": dropping it answered 200 over zero rows (BUGS L268).
func (s *Store) SetGateInto(ws *WriteSet, gate string, limitPerDay int, enabled *bool,
	refused error) error {
	if limitPerDay <= 0 {
		if enabled == nil {
			return nil
		}
		w, err := gateEnableWrite(gate, *enabled)
		if err != nil {
			return err
		}
		return ws.AddGuarded(refused, w)
	}
	w, err := gateUpsert(gate, limitPerDay)
	if err != nil {
		return err
	}
	if enabled != nil {
		// An attribute has ONE writer per write (objstore.validateMerge), so
		// the flag the caller named takes the place of the born-with one.
		flip, ferr := gateEnableWrite(gate, *enabled)
		if ferr != nil {
			return ferr
		}
		maps.Copy(w.Attrs, flip.Attrs)
		delete(w.Init, attrGateEnabled)
	}
	ws.Add(w)
	return nil
}

// gateUpsert renders "this gate exists and its limit is that" as ONE write.
// `enabled` is an Init because it is the flag a gate is BORN with; a Put would
// re-enable a gate the operator disabled.
func gateUpsert(gate string, limitPerDay int) (objstore.Write, error) {
	key, err := gateKey(gate)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{
		Op: objstore.Merge, Entity: OnboardingGates, Key: key,
		Attrs: map[string]string{attrGateLimitPerDay: strconv.Itoa(limitPerDay)},
		Init:  map[string]string{attrGateEnabled: "1"},
	}, nil
}

// gateEnableWrite renders one flip of the flag. The Update is the must-exist
// half: a gate that is not there refuses, and that refusal is the 404 (BUGS
// L268).
func gateEnableWrite(gate string, enabled bool) (objstore.Write, error) {
	key, err := gateKey(gate)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{
		Op: objstore.Update, Entity: OnboardingGates, Key: key,
		Attrs: map[string]string{attrGateEnabled: strconv.Itoa(btoi(enabled))},
	}, nil
}

// DeleteGateInto buffers one gate's removal on the caller's write set.
// Unconditional: asking twice is the same answer.
func (s *Store) DeleteGateInto(ws *WriteSet, gate string) error {
	w, err := gateDeleteWrite(gate)
	if err != nil {
		return err
	}
	ws.Add(w)
	return nil
}

// gateDeleteWrite renders one gate's removal.
func gateDeleteWrite(gate string) (objstore.Write, error) {
	key, err := gateKey(gate)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{Op: objstore.Delete, Entity: OnboardingGates, Key: key}, nil
}

// PutGate is gateUpsert with an audit row of its own, for the CLI and the chat
// command.
func (s *Store) PutGate(gate string, limitPerDay int) error {
	return s.writeAudited(func() ([]objstore.Write, audit.Event, error) {
		w, err := gateUpsert(gate, limitPerDay)
		if err != nil {
			return nil, audit.Event{}, err
		}
		return []objstore.Write{w}, audit.Event{
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
		}, nil
	})
}

// DeleteGate is gateDeleteWrite with an audit row of its own.
func (s *Store) DeleteGate(gate string) error {
	return s.writeAudited(func() ([]objstore.Write, audit.Event, error) {
		w, err := gateDeleteWrite(gate)
		if err != nil {
			return nil, audit.Event{}, err
		}
		return []objstore.Write{w}, audit.Event{
			Category: audit.CategoryMutation,
			Action:   "gate.delete",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "onboarding_gates/" + gate,
			Outcome:  audit.OutcomeOK,
		}, nil
	})
}

// EnableGate is gateEnableWrite with an audit row of its own.
func (s *Store) EnableGate(gate string, enabled bool) error {
	return s.writeAudited(func() ([]objstore.Write, audit.Event, error) {
		w, err := gateEnableWrite(gate, enabled)
		if err != nil {
			return nil, audit.Event{}, err
		}
		return []objstore.Write{w}, audit.Event{
			Category: audit.CategoryMutation,
			Action:   "gate.set",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "onboarding_gates/" + gate,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"enabled": enabled,
			},
		}, nil
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

// onboardingFrom renders one stored item as the row every read surface sees;
// an absent attribute leaves its field empty.
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

// onboardingPut renders a whole row as the item that stores it: the single
// renderer EVERY writer goes through. EVERY empty attribute is left OFF, `status` and `created` included:
// byte for byte what resreg's own entity path writes for a string field
// (resreg/entity.go itemFor).
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
// tool; the live writers move one attribute at a time.
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

// readOnboarding is the one read behind every admission query below, so
// "which admissions match this" cannot mean one thing to a page and another to
// a counter.
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

// ListOnboarding returns onboarding rows, optionally filtered by status,
// ordered by created DESC (an attribute's order, applied here). The setup
// link is not in this projection: the bearer lives solely in the link the
// user was sent (Z3, BUGS F40). A stable sort makes two rows created in one
// second come back in a fixed order.
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

// UnpromptedJIDs names every chat that is waiting and has never been greeted:
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
// verdict: the pairing observer's work list. `user_sub` absent is the dedup:
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
// limit of them: the batch a gate's remaining daily quota admits.
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
	// Oldest first by an attribute, so the order and the limit are applied
	// here: a Limit on the query would have cut by jid.
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

// ApprovedAdmissions counts a user's approved admissions: the second authority
// an admitted stranger holds to create a first world (spec 5/18 step 8).
func (s *Store) ApprovedAdmissions(userSub string) (int, error) {
	rows, err := s.readOnboarding(context.Background(),
		objstore.Cmp{Attr: attrOnbStatus, Op: objstore.Eq, Value: statusApproved},
		objstore.Cmp{Attr: attrOnbUserSub, Op: objstore.Eq, Value: userSub})
	return len(rows), err
}

// QueuePosition counts the chats queued behind one gate AHEAD of queuedAt: the
// number the queue page turns into a position and an ETA. A byte comparison
// on a declared 20-byte attribute (file header).
func (s *Store) QueuePosition(gate, queuedAt string) (int, error) {
	rows, err := s.readOnboarding(context.Background(),
		objstore.Cmp{Attr: attrOnbStatus, Op: objstore.Eq, Value: statusQueued},
		objstore.Cmp{Attr: attrOnbGate, Op: objstore.Eq, Value: gate},
		objstore.Cmp{Attr: attrOnbQueuedAt, Op: objstore.Lt, Value: queuedAt})
	return len(rows), err
}

// AdmittedInWindow counts one gate's admissions inside a half-open instant
// range: today's quota, so a backlog queued on a prior day does not evade it.
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
// both win one jid. The instant is rendered here rather than passed in, so the
// declared width and the layout stay in one place.
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
// gate configured. It is not ApproveOnboarding: that one is the OPERATOR's
// fast-path and must leave user_sub alone, because the operator approving a
// row says nothing about who the chat belongs to.
func (s *Store) AdmitOnboarding(jid, userSub string) error {
	return s.setOnboarding(jid, "admit", map[string]string{
		attrOnbStatus:     statusApproved,
		attrOnbUserSub:    userSub,
		attrOnbAdmittedAt: onbodTS(time.Now()),
	})
}

// setOnboarding merges one verdict into an admission row. A row that is not
// there is NOT an error: the observer reads the work list one tick before it
// writes, and an operator may deny a jid in between.
func (s *Store) setOnboarding(jid, what string, attrs map[string]string) error {
	key, err := onboardingKey(jid)
	if err != nil {
		return err
	}
	// A refusal is the jid no longer being there.
	if _, err := s.TryWriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: Onboarding, Key: key, Attrs: attrs,
	}); err != nil {
		return fmt.Errorf("%s %s: %w", what, jid, err)
	}
	return nil
}

// AdmitQueued approves a gate's batch from the queue and answers how many
// landed, because the caller records one audit event per admitted jid. It is
// CHUNKED: a gate whose daily limit exceeds MaxBatchActions commits in pieces,
// which is safe because the pieces are independent and the next tick re-reads
// the queue.
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

// ApproveOnboarding sets a row to approved immediately: the operator fast-path,
// bypassing the gate's daily limit. An error when the row does not exist: the
// refusal an Update reports for a missing item IS that answer.
func (s *Store) ApproveOnboarding(jid string) error {
	approved, err := s.tryWriteAudited(func() ([]objstore.Write, audit.Event, error) {
		key, err := onboardingKey(jid)
		if err != nil {
			return nil, audit.Event{}, err
		}
		w := objstore.Write{
			Op: objstore.Update, Entity: Onboarding, Key: key,
			Attrs: map[string]string{
				attrOnbStatus:     statusApproved,
				attrOnbAdmittedAt: onbodTS(time.Now()),
			},
		}
		return []objstore.Write{w}, audit.Event{
			Category: audit.CategoryMutation, Action: "onboarding.approve",
			Actor: "operator", Surface: audit.SurfaceREST,
			Resource: "onboarding/" + jid, Outcome: audit.OutcomeOK,
		}, nil
	})
	if err != nil {
		return err
	}
	if !approved {
		return fmt.Errorf("onboarding row not found: %s", jid)
	}
	return nil
}

// DenyOnboarding deletes the onboarding row for jid (operator deny).
func (s *Store) DenyOnboarding(jid string) error {
	return s.writeAudited(func() ([]objstore.Write, audit.Event, error) {
		key, err := onboardingKey(jid)
		if err != nil {
			return nil, audit.Event{}, err
		}
		w := objstore.Write{Op: objstore.Delete, Entity: Onboarding, Key: key}
		return []objstore.Write{w}, audit.Event{
			Category: audit.CategoryMutation, Action: "onboarding.deny",
			Actor: "operator", Surface: audit.SurfaceREST,
			Resource: "onboarding/" + jid, Outcome: audit.OutcomeOK,
		}, nil
	})
}

// RepromptOnboarding resets an onboarding row to awaiting_message so the next
// poll tick greets it again: the operator's cooldown BYPASS. A FULL reset,
// admission verdict included: the observer only advances a row with no
// user_sub, so a leftover verdict would re-greet the chat and never admit it
// (BUGS O1). ONE write: Attrs and Remove are one document expression, so the
// five attributes go together; five writes could leave one behind, which is
// O1. The pairing edge is untouched: it is the user's consent, not admission
// state.
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

// ArchiveOnboardingRow is one row of the archive's onboarding document, the
// same shape OnboardingRow publishes; it carries no credential (BUGS F40).
//
// Admissions need this archive-only lane: `onboarding` is SkipApplyRebuild
// (runtime state, never declared in a manifest), and rederiving is
// impossible, because routeMiss advances agent_cursor past a route-missed
// message whether or not the admission insert succeeded (a deliberate
// fail-forward). It rides the same off-by-default gate as route_tokens and
// invites: importing admission verdicts onto a live instance is a merge, not a
// restore.
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

// ExportOnboarding reads every admission row, in jid order (the coordinate's).
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

// ImportOnboarding writes by jid and never replaces: an admission's history is
// append-only, and a jid already present on the target is a live admission
// this restore must not walk backwards. The present set is READ first,
// because a batch is all-or-nothing and one lost create-if-absent would refuse
// every other row; the write itself stays conditional, so a jid that appears
// in between is a loud refusal. NO gating: that is `archive apply`
// orchestration in cmd/arizuko.
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

// CountOnboarding counts every admission row: the proven-empty-target gate's
// third twin, beside CountRouteTokens and CountInvites.
func (s *Store) CountOnboarding(ctx context.Context) (int, error) {
	rows, err := s.readOnboarding(ctx)
	return len(rows), err
}
