package store

// groups.go holds every read and write of `groups`: one item per group,
// addressed by its folder.
//
// THE PARTITION IS OwnerRoutd AND IT IS NOT A CHOICE THIS FILE MAKES. `groups`
// is a registered resreg resource, and an entity-backed resource's items live
// at its owner subsystem (resreg.SubsystemRoutd, which IS store.OwnerRoutd).
//
// THE FOLDER IS THE COORDINATE, which is what makes a subtree a key predicate:
// SiblingFolders reads `parent` and everything under `parent/` as one
// begins_with, exactly the containment `groupfolder.SQLSubtree` renders in SQL
// and for the same reason it is `substr` and never `LIKE` — `_` and `%` are
// LIKE wildcards on both engines, and sqlite folds ASCII case on top (BUGS
// L122, L252).
//
// TWO JSON DOCUMENTS LIVE IN ATTRIBUTES, and the names are not what they look
// like. `container_config` carries core.Group.Config — the spawn shape, mounts
// and timeout. `config` carries the per-group SETTINGS an operator flips:
// model, open, the two observe windows, thread_replies. Nothing but the
// settings' own writers touches the second, which is what makes an upsert of
// the first safe.
//
// AN UPSERT IS TWO WRITES, because a Put carries the item's WHOLE attribute
// set: one unconditional Put would erase the settings document and the cost cap
// beside it. The create-if-absent lands the whole row, and its refusal — the
// group is already there — selects the Update that merges only the three fields
// an upsert owns. That is the shape store/onboarding.go already uses.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/router"
)

// The attributes a group item carries. `folder` is NOT among them: it is the
// coordinate.
const (
	attrGrpAddedAt         = "added_at"
	attrGrpContainerConfig = "container_config"
	attrGrpProduct         = "product"
	attrGrpConfig          = "config"
	attrGrpUpdatedAt       = "updated_at"
	attrGrpCostCap         = "cost_cap_cents_per_day"
)

// The keys inside the settings document. They are the operator-facing names
// `arizuko group set` and the dashboard write, and every reader below reads
// them through these constants so a typo cannot make one writer's key
// invisible to its reader.
const (
	cfgModel          = "model"
	cfgOpen           = "open"
	cfgObserveWinMsgs = "observe_window_messages"
	cfgObserveWinChar = "observe_window_chars"
	cfgThreadReplies  = "thread_replies"
)

// groupKey is the coordinate of one group.
func groupKey(folder string) (objstore.Key, error) {
	if folder == "" {
		return objstore.Key{}, errors.New("group row carries no folder, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: folder}, nil
}

// settingsOf parses one item's settings document. An absent or unparseable one
// is the EMPTY document, which is what `json_extract` over a NULL column
// answered: every reader below has its own default for a key that is not there.
func settingsOf(it objstore.Item) map[string]any {
	out := map[string]any{}
	if raw := it.Attrs[attrGrpConfig]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return map[string]any{}
		}
	}
	return out
}

// settingNum reads one numeric setting. JSON numbers decode as float64, and a
// key holding JSON null is the same answer as a key that is not there — which
// is what `json_extract` gave, and what SetGroupObserveWindow writes to clear.
func settingNum(cfg map[string]any, key string) (int, bool) {
	v, ok := cfg[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// groupFrom builds one group from one item.
func groupFrom(it objstore.Item) core.Group {
	g := core.Group{Folder: it.Key.Sort, Product: it.Attrs[attrGrpProduct]}
	g.AddedAt, _ = time.Parse(core.TSLayout, it.Attrs[attrGrpAddedAt])
	if raw := it.Attrs[attrGrpContainerConfig]; raw != "" {
		json.Unmarshal([]byte(raw), &g.Config)
	}
	cfg := settingsOf(it)
	if model, ok := cfg[cfgModel].(string); ok {
		g.Model = model
	}
	// Defaults match IsGroupOpen and GroupObserveWindow exactly: absent `open`
	// means OPEN, and an unset window is -1 (no per-group cap) rather than 0,
	// which would read as a cap of nothing.
	open, ok := settingNum(cfg, cfgOpen)
	g.Open = !ok || open != 0
	g.ObserveWindowMessages, g.ObserveWindowChars = -1, -1
	if n, ok := settingNum(cfg, cfgObserveWinMsgs); ok {
		g.ObserveWindowMessages = n
	}
	if n, ok := settingNum(cfg, cfgObserveWinChar); ok {
		g.ObserveWindowChars = n
	}
	return g
}

// groupItem reads one group's item, or reports that there is none.
func (s *Store) groupItem(ctx context.Context, folder string) (objstore.Item, bool, error) {
	return s.groupItemVia(ctx, folder)
}

// groupItemVia is groupItem through a transaction the caller owns, for the
// upsert that reads the settings document it is about to merge into
// (scopes.go reads).
func (s *Store) groupItemVia(ctx context.Context, folder string) (objstore.Item, bool, error) {
	key, err := groupKey(folder)
	if err != nil {
		return objstore.Item{}, false, err
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Groups, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil {
		return objstore.Item{}, false, fmt.Errorf("read group %q: %w", folder, err)
	}
	if len(items) == 0 {
		return objstore.Item{}, false, nil
	}
	return items[0], true, nil
}

// groupAttrs renders what an upsert STATES about a group: the three fields it
// owns, and the settings document when the caller named a model.
//
// THE MODEL IS THE ONE SETTING AN UPSERT STATES: register_group carries it, and
// dispatchRun reads it back to tell runed which model to spawn. It is folded
// INTO `settings` rather than replacing them, because `config` holds ONE
// document and the open flag and the two observe windows live in the same one —
// so the caller hands over the settings the group has today, and a folder with
// none hands over nothing.
//
// An EMPTY model states nothing and clears nothing. SetGroupModel("") is the
// explicit clear, and a caller that names a group without naming a model — the
// CLI's `group add`, register_group's two arguments — is not asking for the
// override to go.
func groupAttrs(g core.Group, settings map[string]any) (map[string]string, error) {
	cfgJSON, err := json.Marshal(g.Config)
	if err != nil {
		return nil, fmt.Errorf("group %q container config: %w", g.Folder, err)
	}
	product := g.Product
	if product == "" {
		product = core.DefaultProduct
	}
	attrs := map[string]string{
		attrGrpContainerConfig: string(cfgJSON),
		attrGrpProduct:         product,
		attrGrpUpdatedAt:       core.NowTS(),
	}
	if g.Model == "" {
		return attrs, nil
	}
	if settings == nil {
		settings = map[string]any{}
	}
	settings[cfgModel] = g.Model
	raw, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("group %q settings: %w", g.Folder, err)
	}
	attrs[attrGrpConfig] = string(raw)
	return attrs, nil
}

// groupCreate renders the create-if-absent a folder CREATION issues, whose
// refusal is the "that folder is already someone's" answer its callers give.
func groupCreate(g core.Group) (objstore.Write, error) {
	key, err := groupKey(g.Folder)
	if err != nil {
		return objstore.Write{}, err
	}
	attrs, err := groupAttrs(g, nil)
	if err != nil {
		return objstore.Write{}, err
	}
	attrs[attrGrpAddedAt] = core.TS(g.AddedAt)
	return objstore.Write{
		Op: objstore.Put, Entity: Groups, Key: key, Attrs: attrs,
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}, nil
}

// groupUpsert renders the ONE write an upsert is: merge the fields it owns into
// the item, create the item when the folder is new, and stamp `added_at` only on
// that create.
//
// `added_at` is Init because it is the instant the folder APPEARED. A merge that
// restated it would overwrite that fact with the instant of the newest upsert,
// and every caller but the CLI's `group add` passes time.Now().
func groupUpsert(g core.Group, settings map[string]any) (objstore.Write, error) {
	key, err := groupKey(g.Folder)
	if err != nil {
		return objstore.Write{}, err
	}
	attrs, err := groupAttrs(g, settings)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{
		Op: objstore.Merge, Entity: Groups, Key: key, Attrs: attrs,
		Init: map[string]string{attrGrpAddedAt: core.TS(g.AddedAt)},
	}, nil
}

// putGroup writes the upsert, on a transaction the caller owns when it has one.
func (s *Store) putGroup(ctx context.Context, g core.Group) error {
	w, err := s.groupUpsertWrite(ctx, g)
	if err != nil {
		return err
	}
	return s.WriteEntities(ctx, nil, w)
}

// groupUpsertWrite reads what the upsert needs and renders it, for both sinks
// that write one: the caller's transaction and the audited batch.
//
// ONE WRITE, and the read ahead of it happens only when the caller named a
// model: the settings document is what a model override is folded into, and
// nothing else here needs the item. The pair this replaces decided which of two
// writes landed by the first one's REFUSAL, which is what made the upsert two
// actions on one item and therefore a transaction (spec 6/40 §"Merge").
func (s *Store) groupUpsertWrite(ctx context.Context, g core.Group) (objstore.Write, error) {
	var settings map[string]any
	if g.Model != "" {
		it, ok, err := s.groupItemVia(ctx, g.Folder)
		if err != nil {
			return objstore.Write{}, err
		}
		if ok {
			settings = settingsOf(it)
		}
	}
	return groupUpsert(g, settings)
}

func (s *Store) PutGroup(g core.Group) error {
	product := g.Product
	if product == "" {
		product = core.DefaultProduct
	}
	return s.writeAudited(func() ([]objstore.Write, audit.Event, error) {
		w, err := s.groupUpsertWrite(context.Background(), g)
		if err != nil {
			return nil, audit.Event{}, err
		}
		return []objstore.Write{w}, audit.Event{
			Category: audit.CategoryMutation,
			Action:   "group.upsert",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "groups/" + g.Folder,
			Folder:   g.Folder,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"product": product,
			},
		}, nil
	})
}

// PutGroupRow upserts a group identity row WITHOUT emitting an audit_log row —
// the audit-free twin of PutGroup, for the CLI `group add`, which records the
// act itself with auditCLI; a second row would double-count one creation.
//
// A groups row implies the member edge, in the SAME batch as the row (BUGS
// L119): a folder with no edge expands to nothing, matches zero allow rows and
// comes up with no messaging tools at all. The batch carries the cycle walk's
// marker compare-and-swap as well, which the transaction this replaces gave up
// (membership.go AddEdgeWith).
func (s *Store) PutGroupRow(g core.Group) error {
	ctx := context.Background()
	var settings map[string]any
	if g.Model != "" {
		it, ok, err := s.groupItem(ctx, g.Folder)
		if err != nil {
			return err
		}
		if ok {
			settings = settingsOf(it)
		}
	}
	row, err := groupUpsert(g, settings)
	if err != nil {
		return err
	}
	// The upsert is a Merge and the edge is create-if-absent, so nothing in this
	// batch can refuse and the verdict says only that it ran.
	if _, err := SeedMemberRole(ctx, s, g.Folder, row); err != nil {
		return err
	}
	return nil
}

// DeleteGroup removes the folder's subtree and every row that confers on it or
// routes into it, and returns how many groups rows went. It is the ONE delete:
// the operator's `arizuko group rm`, dashd's delete page and routd's
// registration rollback all land here. What it removes is declared once in
// folderScopePurge.
//
// The purge is the whole point, and it has to be total. A bare delete of the
// groups item leaves routes aimed at a folder that no longer resolves,
// engagement state pinned to it, and scheduled_tasks that timed keeps firing
// forever — sloth ran `Coach-mem-0..4` nightly from 2026-05-05 without one of
// them ever executing, and each firing reached onbod, which read the unroutable
// jid as a new account to onboard (BUGS L66, L67). Worse, an acl row scoped
// under the folder makes whoever administered it administer the next tenant of
// the same path (BUGS L198).
//
// The subtree, not the row: a child folder left behind is an orphan holding
// grants of its own, and a child's acl rows sit under the parent's path.
//
// A folder with no groups row in its subtree returns 0 and writes nothing,
// audit row included — there is no delete to record.
func (s *Store) DeleteGroup(folder string) (int, error) {
	ctx := context.Background()
	// The purge removes more rows than one batch holds, so it runs as a sweep.
	// `groups` goes first, which is what makes the folder unreachable: a failure
	// part-way leaves rows nothing can find, and the next run collects them.
	removed, err := s.purgeFolder(ctx, folder)
	if errors.Is(err, errNoSuchGroup) || removed == 0 {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	actor, actorSub, surface := s.auditIdentity()
	_, rec := audit.WriteFor(audit.Event{
		Category:      audit.CategoryMutation,
		Action:        "group.delete",
		Actor:         actor,
		ActorSub:      actorSub,
		Surface:       surface,
		Resource:      "groups/" + folder,
		Folder:        folder,
		Outcome:       audit.OutcomeOK,
		ParamsSummary: map[string]any{"groups_removed": removed},
	})
	if err := s.WriteEntities(ctx, nil, rec); err != nil {
		return removed, err
	}
	return removed, nil
}

// GroupFolders lists every group's folder in path order, capped at limit — 0 is
// uncapped. The folder IS the coordinate, so the read arrives in that order with
// no sort of its own, which is what `ORDER BY folder` gave.
//
// It answers with an ERROR where AllGroups answers with a nil map, because its
// three callers count on the difference: dashd's portal dot renders "at least
// this many" and must not print 0 for a store it could not reach, and onbod's
// world picker must not offer a caller an empty list of worlds it merely failed
// to read.
func (s *Store) GroupFolders(limit int) ([]string, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Groups, Partition: OwnerRoutd, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("read group folders: %w", err)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Key.Sort)
	}
	return out, nil
}

// GroupCreateWrite hands the groups CREATE to the two daemons that assemble a
// folder creation's whole batch themselves: dashd's operator page and onbod's
// world creation each land the row, the member edge and an admin grant together
// — a group row with no edge expands to nothing (BUGS L119) and a group row with
// no grant is a folder nobody can open.
//
// It hands over the WRITE and not the shape, so this package stays the one place
// a groups item is rendered.
//
// It is create-if-absent and it is the ONLY guarded write in either batch, so a
// refused batch is the folder ALREADY BEING THERE — the answer both callers
// refuse on, rather than taking over a world they did not make. PutGroup is an
// upsert and would hand it over silently.
func GroupCreateWrite(g core.Group) (objstore.Write, error) { return groupCreate(g) }

func (s *Store) AllGroups() map[string]core.Group {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Groups, Partition: OwnerRoutd,
	})
	if err != nil {
		slog.Error("AllGroups read failed; failing closed", "err", err)
		return nil
	}
	out := make(map[string]core.Group, len(items))
	for _, it := range items {
		g := groupFrom(it)
		out[g.Folder] = g
	}
	return out
}

func (s *Store) DefaultFolderForJID(jid string) string {
	msg := core.Message{ChatJID: jid, Verb: "message"}
	t := router.ResolveRoute(msg, s.AllRoutes())
	return core.ParseRouteTarget(t).Folder
}

func (s *Store) GroupByFolder(folder string) (core.Group, bool) {
	it, ok, err := s.groupItem(context.Background(), folder)
	if err != nil || !ok {
		return core.Group{}, false
	}
	return groupFrom(it), true
}

// IsGroupOpen reports the folder's `open` flag. A missing group defaults to
// true so a freshly-created one stays visible to its siblings until an operator
// explicitly closes it. Spec 6/F.
func (s *Store) IsGroupOpen(folder string) bool {
	it, ok, err := s.groupItem(context.Background(), folder)
	if err != nil || !ok {
		return true
	}
	open, set := settingNum(settingsOf(it), cfgOpen)
	return !set || open != 0
}

// setSetting rewrites one key of the settings document, leaving the rest of it
// and every other attribute alone. A nil value writes JSON null, which is what
// clearing a setting means to every reader; remove takes the key out entirely.
//
// The read and the write are two calls, and the group is the unit of both: a
// second setting written in between would be overwritten. That is what
// `json_set` on one statement gave and this does not, and it is the cost of a
// document attribute. Two operators editing one group's settings in the same
// instant is the collision; the last writer wins.
func (s *Store) setSetting(folder, key string, value any, remove bool) error {
	ctx := context.Background()
	it, ok, err := s.groupItem(ctx, folder)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("group %q has no row to set %s on", folder, key)
	}
	cfg := settingsOf(it)
	if remove {
		delete(cfg, key)
	} else {
		cfg[key] = value
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("group %q settings: %w", folder, err)
	}
	err = s.WriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: Groups, Key: it.Key,
		Attrs: map[string]string{attrGrpConfig: string(raw)},
	})
	return err
}

// SetGroupOpen flips the visibility bit. The group must exist (groups are
// created via SetupGroup/PutGroup before any caller can flip the flag).
func (s *Store) SetGroupOpen(folder string, open bool) error {
	return s.setSetting(folder, cfgOpen, btoi(open), false)
}

// GroupObserveWindow returns the per-group observe-window caps. (-1,-1)
// means "not set; fall back to env defaults". Per-route caps still win
// over per-group; per-group still wins over env. Spec 6/F.
func (s *Store) GroupObserveWindow(folder string) (msgs, chars int) {
	msgs, chars = -1, -1
	it, ok, err := s.groupItem(context.Background(), folder)
	if err != nil || !ok {
		return msgs, chars
	}
	cfg := settingsOf(it)
	if n, set := settingNum(cfg, cfgObserveWinMsgs); set {
		msgs = n
	}
	if n, set := settingNum(cfg, cfgObserveWinChar); set {
		chars = n
	}
	return msgs, chars
}

// SetGroupObserveWindow writes per-group caps; pass -1 to clear (the gateway
// then falls back to the env default for that field). A cleared field is set to
// JSON null, which every reader answers the same way it answers a key that is
// not there.
func (s *Store) SetGroupObserveWindow(folder string, msgs, chars int) error {
	ctx := context.Background()
	it, ok, err := s.groupItem(ctx, folder)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("group %q has no row to set an observe window on", folder)
	}
	cfg := settingsOf(it)
	cfg[cfgObserveWinMsgs] = nil
	if msgs >= 0 {
		cfg[cfgObserveWinMsgs] = msgs
	}
	cfg[cfgObserveWinChar] = nil
	if chars >= 0 {
		cfg[cfgObserveWinChar] = chars
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("group %q settings: %w", folder, err)
	}
	err = s.WriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: Groups, Key: it.Key,
		Attrs: map[string]string{attrGrpConfig: string(raw)},
	})
	return err
}

// SetThreadReplies sets the per-group thread_replies preference. nil = reset to
// the default (group chats thread, DMs do not). true = always thread. false =
// never thread (replies land inline regardless of chat kind).
func (s *Store) SetThreadReplies(folder string, on *bool) error {
	if on == nil {
		return s.setSetting(folder, cfgThreadReplies, nil, true)
	}
	return s.setSetting(folder, cfgThreadReplies, btoi(*on), false)
}

// SetGroupModel sets the per-group model override. Empty string clears it
// (gateway then falls back to instance default).
func (s *Store) SetGroupModel(folder, model string) error {
	if model == "" {
		return s.setSetting(folder, cfgModel, nil, true)
	}
	return s.setSetting(folder, cfgModel, model, false)
}

// SiblingFolders returns folders that share folder's immediate parent,
// excluding folder itself and any closed sibling. Root folders (no parent) have
// no siblings — returns nil. Spec 6/F ambient join.
func (s *Store) SiblingFolders(folder string) []string {
	parent := groupfolder.ParentOf(folder)
	if parent == "" {
		return nil
	}
	// begins_with on the coordinate, with "/" as the subtree separator: it
	// matches `parent` itself and everything under `parent/`, and nothing else.
	// That exactness is why the SQL half is substr and never LIKE — `_` and `%`
	// are legal in a folder name and are LIKE wildcards, and sqlite folds ASCII
	// case on top, so LIKE lists a stranger's folder as a sibling (BUGS L252).
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Groups, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.PrefixKey, Value: parent, Sep: "/"},
	})
	if err != nil {
		return nil
	}
	var out []string
	for _, it := range items {
		f := it.Key.Sort
		if f == folder || f == parent {
			continue
		}
		// Direct children only — exclude grandchildren like "parent/x/y".
		if groupfolder.ParentOf(f) != parent {
			continue
		}
		if open, set := settingNum(settingsOf(it), cfgOpen); set && open == 0 {
			continue
		}
		out = append(out, f)
	}
	slices.Sort(out)
	return out
}

// costCapOf reads the per-group daily spend cap, in cents. Absent is no cap.
func costCapOf(it objstore.Item) int {
	n, err := strconv.Atoi(it.Attrs[attrGrpCostCap])
	if err != nil {
		return 0
	}
	return n
}

// GroupConfig answers the two fields a spawn forwards to runed: the per-group
// model override, and `container_config` as the opaque document it is on the
// wire. It stays a map rather than core.GroupConfig because runed round-trips it
// verbatim, and a struct would silently drop a key it does not declare.
//
// An unknown group answers ("", nil), which the caller reads as "use the
// instance defaults".
func (s *Store) GroupConfig(folder string) (model string, cfg map[string]any) {
	it, ok, err := s.groupItem(context.Background(), folder)
	if err != nil || !ok {
		return "", nil
	}
	if m, isStr := settingsOf(it)[cfgModel].(string); isStr {
		model = m
	}
	if raw := it.Attrs[attrGrpContainerConfig]; raw != "" && raw != "{}" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return model, nil
		}
	}
	return model, cfg
}

// ThreadReplies reads the group's thread_replies preference — the read twin of
// SetThreadReplies. A key that is not there (and an unknown group) yields dflt,
// which the caller sets from the chat's own kind: group chats thread by default,
// DMs do not.
func (s *Store) ThreadReplies(folder string, dflt bool) bool {
	it, ok, err := s.groupItem(context.Background(), folder)
	if err != nil || !ok {
		return dflt
	}
	on, set := settingNum(settingsOf(it), cfgThreadReplies)
	if !set {
		return dflt
	}
	return on == 1
}

// FolderCap returns the folder's per-day spend cap in cents. Zero means
// uncapped, which is also what an unknown group answers — the budget gate treats
// both as "no cap", and inventing one for a group that does not exist would stop
// turns nobody capped.
func (s *Store) FolderCap(folder string) (int, error) {
	it, ok, err := s.groupItem(context.Background(), folder)
	if err != nil || !ok {
		return 0, err
	}
	return costCapOf(it), nil
}

// SetFolderCap writes the folder's per-day spend cap. Zero is uncapped and it is
// WRITTEN as "0" rather than taken away.
//
// That is the exception to "absent is the only null", and it is not this file's
// choice: a Go int has no unset, so resreg's own writer of these items renders
// every int field always (resreg/entity.go itemFor), and the two writers of one
// item have to agree byte for byte. An `arizuko apply` of a manifest naming no
// cap would otherwise store "0" where this writer stored nothing, and a later
// diff of the two would report a change neither made.
//
// An Update writes nothing when the group is missing, and that refusal is an
// ERROR here: `arizuko budget set` names a folder, and a folder nobody created
// must not print success over a cap that was never stored.
func (s *Store) SetFolderCap(folder string, cents int) error {
	key, err := groupKey(folder)
	if err != nil {
		return err
	}
	err = s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: Groups, Key: key,
		Attrs: map[string]string{attrGrpCostCap: strconv.Itoa(cents)},
	})
	return err
}
