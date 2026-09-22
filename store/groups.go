package store

// groups.go holds every read and write of `groups`: one item per group,
// addressed by its folder.
//
// The partition is OwnerRoutd because resreg's entity path writes these items
// too (resreg.SubsystemRoutd IS store.OwnerRoutd). The folder is the
// coordinate, so a subtree is one begins_with; the containment is `substr` and
// never `LIKE`, because `_` and `%` are LIKE wildcards and sqlite folds ASCII
// case on top (BUGS L122, L252).
//
// Two JSON documents live in attributes: `container_config` is
// core.Group.Config (the spawn shape), and `config` is the operator SETTINGS
// (model, open, the two observe windows, thread_replies). Only the settings'
// own writers touch the second, which is what makes an upsert of the first
// safe.

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

// The keys inside the settings document: the operator-facing names
// `arizuko group set` and the dashboard write.
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
// is the EMPTY document; every reader has its own default for a missing key.
func settingsOf(it objstore.Item) map[string]any {
	out := map[string]any{}
	if raw := it.Attrs[attrGrpConfig]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return map[string]any{}
		}
	}
	return out
}

// settingNum reads one numeric setting. A key holding JSON null is the same
// answer as a missing key, which is what SetGroupObserveWindow writes to clear.
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
	// means OPEN, and an unset window is -1, not 0 (a cap of nothing).
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

// groupItemVia reads one group's item by its folder.
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
// owns, and the settings document when the caller named a model. The model is
// folded INTO the settings, because `config` is ONE document. An EMPTY model
// states nothing and clears nothing: SetGroupModel("") is the explicit clear.
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

// groupUpsert renders the ONE write an upsert is. `added_at` is Init because it
// is the instant the folder APPEARED, and every caller but the CLI's
// `group add` passes time.Now().
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

// groupUpsertWrite reads what the upsert needs and renders it. The read happens
// only when the caller named a model: the settings document is what the
// override is folded into.
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

// PutGroupRow is the audit-free twin of PutGroup, for the CLI `group add`,
// which records the act itself. A groups row implies the member edge, in the
// SAME batch (BUGS L119): a folder with no edge expands to nothing and comes
// up with no messaging tools at all.
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
	// The upsert is a Merge and the edge is create-if-absent, so nothing in
	// this batch can refuse and the verdict says only that it ran.
	if _, err := SeedMemberRole(ctx, s, g.Folder, row); err != nil {
		return err
	}
	return nil
}

// DeleteGroup removes the folder's subtree and every row that confers on it or
// routes into it: the ONE delete behind `arizuko group rm`, dashd's delete page
// and routd's registration rollback, with the scope declared once in
// folder_scope.go.
//
// The purge has to be total. A bare delete of the groups item left tasks timed
// fired forever, each firing read by onbod as a new account to onboard (BUGS
// L66, L67), and an acl row under the path administering the next tenant
// (BUGS L198). The subtree, not the row: a child left behind is an orphan
// holding grants of its own.
//
// It returns the `groups` rows deleted AND the orphan rows swept with them;
// counting only the first made a sweep read as "no group at X" (BUGS L323). A
// subtree holding nothing records no audit row.
func (s *Store) DeleteGroup(folder string) (int, int, error) {
	ctx := context.Background()
	// `groups` goes first, which makes the folder unreachable: a failure
	// part-way leaves rows nothing can find, and the next run collects them.
	removed, swept, err := s.purgeFolder(ctx, folder)
	// The error comes FIRST: testing the count first reported a failed query
	// as an empty folder.
	if err != nil {
		return 0, 0, err
	}
	if removed == 0 && swept == 0 {
		return 0, 0, nil
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
		ParamsSummary: map[string]any{"groups_removed": removed, "orphans_swept": swept},
	})
	if err := s.WriteEntities(ctx, nil, rec); err != nil {
		return removed, swept, err
	}
	return removed, swept, nil
}

// GroupFolders lists every group's folder in path order, capped at limit (0 is
// uncapped). It answers with an ERROR where AllGroups answers with a nil map:
// dashd's portal dot must not print 0 for a store it could not reach, and
// onbod's world picker must not offer an empty list it merely failed to read.
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

// GroupCreateWrite hands the groups CREATE to the daemons that assemble a
// folder creation's whole batch themselves (dashd, onbod): the row, the member
// edge and an admin grant land together (BUGS L119). It is create-if-absent
// and the ONLY guarded write in either batch, so a refused batch is the folder
// ALREADY BEING THERE; PutGroup is an upsert and would hand it over silently.
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
// explicitly closes it. Spec 5/F.
func (s *Store) IsGroupOpen(folder string) bool {
	it, ok, err := s.groupItem(context.Background(), folder)
	if err != nil || !ok {
		return true
	}
	open, set := settingNum(settingsOf(it), cfgOpen)
	return !set || open != 0
}

// setSetting rewrites one key of the settings document, leaving the rest
// alone. A nil value writes JSON null (a clear); remove takes the key out. The
// read and the write are two calls, so two operators editing one group in the
// same instant collide: the last writer wins.
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

// SetGroupOpen flips the visibility bit. The group must exist.
func (s *Store) SetGroupOpen(folder string, open bool) error {
	return s.setSetting(folder, cfgOpen, btoi(open), false)
}

// GroupObserveWindow returns the per-group observe-window caps. (-1,-1) means
// "not set; fall back to env defaults". Per-route caps win over per-group,
// per-group over env. Spec 5/F.
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

// SetGroupObserveWindow writes per-group caps; pass -1 to clear a field, which
// is stored as JSON null.
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

// SetThreadReplies sets the per-group thread_replies preference. nil resets to
// the default (group chats thread, DMs do not); true always threads; false
// never threads.
func (s *Store) SetThreadReplies(folder string, on *bool) error {
	if on == nil {
		return s.setSetting(folder, cfgThreadReplies, nil, true)
	}
	return s.setSetting(folder, cfgThreadReplies, btoi(*on), false)
}

// SetGroupModel sets the per-group model override. Empty string clears it.
func (s *Store) SetGroupModel(folder, model string) error {
	if model == "" {
		return s.setSetting(folder, cfgModel, nil, true)
	}
	return s.setSetting(folder, cfgModel, model, false)
}

// SiblingFolders returns folders that share folder's immediate parent,
// excluding folder itself and any closed sibling. Root folders have no
// siblings. Spec 5/F ambient join.
func (s *Store) SiblingFolders(folder string) []string {
	parent := groupfolder.ParentOf(folder)
	if parent == "" {
		return nil
	}
	// begins_with on the coordinate with "/" as the subtree separator: it
	// matches `parent` and everything under `parent/`, nothing else. That
	// exactness is why the SQL half is substr and never LIKE (BUGS L252).
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
		// Direct children only.
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
// wire. A map rather than core.GroupConfig, because runed round-trips it
// verbatim and a struct would silently drop a key it does not declare. An
// unknown group answers ("", nil): the instance defaults.
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

// ThreadReplies reads the group's thread_replies preference. A missing key (and
// an unknown group) yields dflt, which the caller sets from the chat's kind.
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
// uncapped, also for an unknown group: inventing a cap would stop turns nobody
// capped.
func (s *Store) FolderCap(folder string) (int, error) {
	it, ok, err := s.groupItem(context.Background(), folder)
	if err != nil || !ok {
		return 0, err
	}
	return costCapOf(it), nil
}

// SetFolderCap writes the folder's per-day spend cap. Zero is WRITTEN as "0"
// rather than taken away: the exception to "absent is the only null", because
// resreg's writer of these items renders every int field always
// (resreg/entity.go itemFor) and the two writers must agree byte for byte. A
// missing group is an ERROR: `arizuko budget set` must not print success over
// a cap that was never stored.
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
