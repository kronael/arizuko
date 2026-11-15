package routd

// pending_actions.go — a tool call suspended until a human approves it (spec
// 5/19); the hold RULE lives in `acl` (auth.CheckHold), this file is only the
// suspended CALL's state. A held call BLOCKS the agent's turn, and the waiter
// that suspended it runs it on approval, inline, as the ORIGINAL agent, so the
// agent reports the real result: returning "held" and letting the agent carry
// on let it report an action it had not taken. The coordinate and attribute
// names are store's; the writes are routd's, through *store.Store, never the
// driver (BUGS L264). THE ID IS THE COORDINATE; every other selection reads
// the partition and filters. The three timestamps are RFC3339.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/expand"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// PendingAction is one suspended call.
type PendingAction struct {
	ID           string `json:"id"`
	GroupFolder  string `json:"group_folder"`
	CallerAgent  string `json:"caller_agent"`
	Tool         string `json:"tool"`
	Args         string `json:"args"`
	Status       string `json:"status"`
	ChatJID      string `json:"chat_jid"`
	CreatedAt    string `json:"created_at"`
	ReviewedBy   string `json:"reviewed_by"`
	ReviewedAt   string `json:"reviewed_at"`
	ReviewerNote string `json:"reviewer_note"`
	ExpiresAt    string `json:"expires_at"`
	// A reaction names the message it targets, so NoticeID is what turns a ✅
	// into this call's verdict.
	NoticeID string `json:"notice_id"`

	// DERIVED from Args, never stored: the ask line, what an `always` verdict
	// would write and that rule in words, computed by the one renderer
	// (withMatcher) so the notice, the acknowledgement, the REST row, the
	// approvals page and the writer cannot disagree (specs/6/49-approval-ask.md).
	Ask      string `json:"ask"`
	Matcher  string `json:"matcher"`
	RuleText string `json:"rule_text"`
	// Non-empty means `always` is refused rather than approximated.
	MatcherError string `json:"matcher_error,omitempty"`
}

// The states a held call can be in. There is no `released`: the waiter runs an
// approved call inline and the row's last word is the verdict itself.
const (
	PendingHeld     = "held"
	PendingApproved = "approved"
	PendingRejected = "rejected"
	PendingExpired  = "expired"
)

// PutPendingAction records a held call. An unconditional Put: the id is
// minted per call, so a second Put on one id is a caller mistake, not a race.
func (d *DB) PutPendingAction(p PendingAction) error {
	if p.ID == "" || p.GroupFolder == "" || p.Tool == "" {
		return fmt.Errorf("pending action needs id, folder and tool")
	}
	key, err := store.PendingActionKey(p.ID)
	if err != nil {
		return err
	}
	if p.Status == "" {
		p.Status = PendingHeld
	}
	if p.CreatedAt == "" {
		p.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	err = d.store().WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Put, Entity: store.PendingActions, Key: key, Attrs: pendingAttrs(p),
	})
	return err
}

func pendingAttrs(p PendingAction) map[string]string {
	attrs := map[string]string{}
	for name, v := range map[string]string{
		store.AttrPendGroupFolder:  p.GroupFolder,
		store.AttrPendCallerAgent:  p.CallerAgent,
		store.AttrPendTool:         p.Tool,
		store.AttrPendArgs:         p.Args,
		store.AttrPendStatus:       p.Status,
		store.AttrPendChatJID:      p.ChatJID,
		store.AttrPendCreatedAt:    p.CreatedAt,
		store.AttrPendReviewedBy:   p.ReviewedBy,
		store.AttrPendReviewedAt:   p.ReviewedAt,
		store.AttrPendReviewerNote: p.ReviewerNote,
		store.AttrPendExpiresAt:    p.ExpiresAt,
		store.AttrPendNoticeID:     p.NoticeID,
	} {
		if v != "" {
			attrs[name] = v
		}
	}
	return attrs
}

func pendingFrom(it objstore.Item) PendingAction {
	p := PendingAction{
		ID:           it.Key.Sort,
		GroupFolder:  it.Attrs[store.AttrPendGroupFolder],
		CallerAgent:  it.Attrs[store.AttrPendCallerAgent],
		Tool:         it.Attrs[store.AttrPendTool],
		Args:         it.Attrs[store.AttrPendArgs],
		Status:       it.Attrs[store.AttrPendStatus],
		ChatJID:      it.Attrs[store.AttrPendChatJID],
		CreatedAt:    it.Attrs[store.AttrPendCreatedAt],
		ReviewedBy:   it.Attrs[store.AttrPendReviewedBy],
		ReviewedAt:   it.Attrs[store.AttrPendReviewedAt],
		ReviewerNote: it.Attrs[store.AttrPendReviewerNote],
		ExpiresAt:    it.Attrs[store.AttrPendExpiresAt],
		NoticeID:     it.Attrs[store.AttrPendNoticeID],
	}
	return withMatcher(applyExpiry(p))
}

func (d *DB) pendingItems(where ...objstore.Cmp) ([]objstore.Item, error) {
	return d.Obj().Query(context.Background(), objstore.Query{
		Entity: store.PendingActions, Partition: store.OwnerRoutd, Where: where,
	})
}

func (d *DB) pendingAction(id string) (PendingAction, bool) {
	p, ok, _ := d.readPendingAction(id)
	return p, ok
}
func (d *DB) readPendingAction(id string) (PendingAction, bool, error) {
	key, err := store.PendingActionKey(id)
	if err != nil {
		return PendingAction{}, false, err
	}
	var items []objstore.Item
	_, err = objstore.ReadConsistent(context.Background(), d.Obj(), store.OwnerScope(store.OwnerRoutd), func(ctx context.Context) error {
		var readErr error
		items, readErr = d.Obj().Query(ctx, objstore.Query{Entity: store.PendingActions, Partition: store.OwnerRoutd, Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort}})
		return readErr
	})
	if err != nil || len(items) == 0 {
		return PendingAction{}, false, err
	}
	return pendingFrom(items[0]), true, nil
}

// GetPendingAction reads one row by id, for the blocking waiter.
func (d *DB) GetPendingAction(id string) (PendingAction, bool) { return d.pendingAction(id) }

// SetPendingNotice records which chat message announced this row, so a
// reaction on that message can be read as its verdict.
func (d *DB) SetPendingNotice(id, noticeID string) error {
	return d.setPendingAttr(id, store.AttrPendNoticeID, noticeID)
}

// setPendingAttr merges one attribute into a held call; a held call that is
// not there is an ERROR. Nothing deletes a pending row (a verdict moves its
// status), so a refusal is a row that was never written, and both callers act
// on the answer.
func (d *DB) setPendingAttr(id, name, value string) error {
	key, err := store.PendingActionKey(id)
	if err != nil {
		return err
	}
	err = d.store().WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: store.PendingActions, Key: key,
		Attrs: map[string]string{name: value},
	})
	return err
}

// VerdictForReaction reads a reaction as the verdict command it stands for, or
// "" when it is an ordinary reaction. The lookup is (chat, notice, still held):
// a reaction on anything else, or on a call already decided, is just a reaction.
func (d *DB) VerdictForReaction(chatJID, target, emoji string) string {
	verb := chanlib.VerdictEmoji(emoji)
	noticeID := platformID(target)
	if verb == "" || chatJID == "" || noticeID == "" {
		return ""
	}
	items, err := d.pendingItems(
		objstore.Cmp{Attr: store.AttrPendChatJID, Op: objstore.Eq, Value: chatJID},
		objstore.Cmp{Attr: store.AttrPendNoticeID, Op: objstore.Eq, Value: noticeID},
		objstore.Cmp{Attr: store.AttrPendStatus, Op: objstore.Eq, Value: PendingHeld})
	if err != nil || len(items) == 0 {
		return ""
	}
	return verb + " " + items[0].Key.Sort
}

func platformID(target string) string {
	if i := strings.LastIndexByte(target, '/'); i >= 0 {
		return target[i+1:]
	}
	return target
}

// ListPendingActions returns a folder's rows, newest first. An empty folder
// lists every folder's — the operator face; the caller binds the folder.
func (d *DB) ListPendingActions(folder, status string) ([]PendingAction, error) {
	var where []objstore.Cmp
	if folder != "" {
		where = append(where, objstore.Cmp{
			Attr: store.AttrPendGroupFolder, Op: objstore.Eq, Value: folder})
	}
	items, err := d.pendingItems(where...)
	if err != nil {
		return nil, err
	}
	var out []PendingAction
	for _, it := range items {

		// Expiry is applied BEFORE the status filter: filtering on the STORED
		// status made `status=expired` return nothing and `status=held` return
		// rows this function then relabelled expired (BUGS J7).
		p := pendingFrom(it)
		if status != "" && p.Status != status {
			continue
		}
		out = append(out, p)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

func applyExpiry(p PendingAction) PendingAction {
	if p.Status != PendingHeld || p.ExpiresAt == "" {
		return p
	}
	t, err := time.Parse(time.RFC3339, p.ExpiresAt)
	if err != nil {
		return p
	}
	if time.Now().UTC().After(t) {
		p.Status = PendingExpired
	}
	return p
}

// ErrPendingNotFound / ErrPendingResolved / ErrPendingForbidden let the REST
// face map the three verdict failures to 404/409/403.
var (
	ErrPendingNotFound  = errors.New("not found")
	ErrPendingResolved  = errors.New("already resolved")
	ErrPendingForbidden = errors.New("not permitted")
)

func (d *DB) resolvePendingActionInto(ws *store.WriteSet, id, status, reviewer, note string,
	lost error) (PendingAction, error) {
	if status != PendingApproved && status != PendingRejected {
		return PendingAction{}, fmt.Errorf("verdict must be approved or rejected, got %q", status)
	}
	key, err := store.PendingActionKey(id)
	if err != nil {
		return PendingAction{}, err
	}
	cur, ok := d.pendingAction(id)
	if !ok {
		return PendingAction{}, fmt.Errorf("no pending action %q: %w", id, ErrPendingNotFound)
	}
	if cur.Status != PendingHeld {
		return PendingAction{}, fmt.Errorf("pending action %q %w (%s)", id, ErrPendingResolved, cur.Status)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// The read above is advisory. The write's `status = held` CONDITION is what
	// serializes two operators, so its refusal IS the verdict: without it both
	// were told "approved" and the agent was asked twice to re-issue one call.
	// The loser learns that from the BATCH, so the answer travels with the write.
	if err := ws.AddGuarded(lost, objstore.Write{
		Op: objstore.Update, Entity: store.PendingActions, Key: key,
		Attrs: map[string]string{
			store.AttrPendStatus:       status,
			store.AttrPendReviewedBy:   reviewer,
			store.AttrPendReviewedAt:   now,
			store.AttrPendReviewerNote: note,
		},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: store.AttrPendStatus, Op: objstore.Eq, Value: PendingHeld},
		}},
	}); err != nil {
		return PendingAction{}, err
	}
	cur.Status, cur.ReviewedBy, cur.ReviewedAt, cur.ReviewerNote = status, reviewer, now, note
	return cur, nil
}

func skipApprovalRow(p PendingAction, reviewer string) core.ACLRow {
	return core.ACLRow{
		Principal: p.CallerAgent,
		Action:    auth.HoldPrefix + p.Tool,
		Scope:     p.GroupFolder,
		Effect:    auth.EffectSkipApproval,
		Params:    p.Matcher,
		GrantedBy: "always:" + reviewer,
	}
}

// resolveHoldInto is the one funnel both resolution faces run. The verdict
// and the skip-approval rule an `always` adds ride ONE batch, so a crash
// cannot leave a call approved with no rule or a rule with no approved call;
// the waiter that suspended the call runs it itself. `standing` is NOT a third
// status: the standing answer lives in acl. `never` is deliberately absent:
// acl rows carry no verdict identity, so a revoke could not tell its deny
// from standing policy (spec 6/26). WHO MAY ANSWER is decided HERE, on the
// HELD ROW's folder: testing the CALLER for `**` left a world creator unable
// to answer a hold in their own world (BUGS L271).
func (d *DB) resolveHoldInto(ctx context.Context, ws *store.WriteSet, id, verdict, reviewer,
	note string, standing bool, lost error) (PendingAction, error) {

	held, ok := d.pendingAction(id)
	if !ok {
		return PendingAction{}, fmt.Errorf("no pending action %q: %w", id, ErrPendingNotFound)
	}
	var mutations []auth.Mutation
	if verdict == PendingApproved && standing {
		mutations = append(mutations, auth.Mutation{Row: skipApprovalRow(held, reviewer)})
	}
	v, err := auth.CheckGrant(d.store(), auth.Caller{Principal: reviewer}, auth.Ask{Action: "admin", Scope: held.GroupFolder}, mutations...)
	if err != nil {
		return PendingAction{}, fmt.Errorf("%w: answering a held call in %q: %v", ErrPendingForbidden, held.GroupFolder, err)
	}
	ws.Prove(v)
	p, err := d.resolvePendingActionInto(ws, id, verdict, reviewer, note, lost)
	if err != nil {
		return PendingAction{}, err
	}
	if verdict != PendingApproved || !standing {
		return p, nil
	}

	if p.MatcherError != "" {
		return PendingAction{}, fmt.Errorf(
			"cannot make this a standing answer: %s", p.MatcherError)
	}
	if p.CallerAgent == "" || p.Tool == "" || p.GroupFolder == "" {
		return PendingAction{}, fmt.Errorf(
			"cannot make this a standing answer: the held row names no caller, tool or folder")
	}

	if err := d.grantACLInto(ctx, ws, skipApprovalRow(p, reviewer)); err != nil {
		return PendingAction{}, fmt.Errorf("record the skip-approval rule: %w", err)
	}
	return p, nil
}

func (d *DB) expirePendingAction(id string) (bool, error) {
	key, err := store.PendingActionKey(id)
	if err != nil {
		return false, err
	}
	return d.store().TryWriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: store.PendingActions, Key: key,
		Attrs: map[string]string{store.AttrPendStatus: PendingExpired},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: store.AttrPendStatus, Op: objstore.Eq, Value: PendingHeld},
		}},
	})
}

// NewPendingID is short on purpose: an operator types it into chat as
// `/approve <id>`, and Telegram's callback_data is capped at 64 bytes.
func NewPendingID() string {
	return randHex(4)
}

func (s *Server) holdGate(t turnMCP) func(string, map[string]any) (string, bool) {
	if t.elevated || s.db == nil {
		return nil
	}
	return func(tool string, args map[string]any) (string, bool) {
		hold, err := s.db.CheckHold("folder:"+t.folder, t.folder, tool, stringArgs(args))
		if err != nil {

			slog.Error("hitl could not read the hold policy; blocking the call",
				"folder", t.folder, "tool", tool, "err", err)
			s.notifyHoldFailed(t, tool, err)
			return "", true
		}
		if !hold {
			return "", false
		}
		id := NewPendingID()
		raw, _ := json.Marshal(args)
		p := PendingAction{
			ID: id, GroupFolder: t.folder, CallerAgent: "folder:" + t.folder,
			Tool: tool, Args: string(raw), Status: PendingHeld, ChatJID: t.chatJID,
		}
		if err := s.db.PutPendingAction(p); err != nil {
			// Fail CLOSED: the hold rule exists and the call is unrecorded. The
			// EMPTY refusal is what ipc renders as an error rather than a verdict
			// — no row, no id to /approve (BUGS J3).
			slog.Error("hitl could not record pending action; blocking the call",
				"folder", t.folder, "tool", tool, "err", err)
			s.notifyHoldFailed(t, tool, err)
			return "", true
		}

		s.notifyHeld(t, withMatcher(p))
		return s.awaitVerdict(id, tool)
	}
}

// HoldWait bounds how long a blocking hold suspends the agent's tool call. It
// must stay under RUNED_RUN_TIMEOUT (20m default): past that the container is
// killed and the wait ends as a dead turn rather than as an answer.
var HoldWait = chanlib.EnvDur("HITL_WAIT", 10*time.Minute)

func (s *Server) awaitVerdict(id, tool string) (string, bool) {
	changed, unsubscribe := s.db.waitForApproval(id)
	defer unsubscribe()
	deadline := time.NewTimer(HoldWait)
	defer deadline.Stop()
	for {
		p, ok, err := s.db.readPendingAction(id)
		if err == nil && !ok {
			return verdictRefusal(PendingAction{ID: id, Status: PendingExpired}, tool), true
		}
		if err == nil && ok && p.Status != PendingHeld {
			if p.Status == PendingApproved {
				return "", false
			}
			return verdictRefusal(p, tool), true
		}
		var retry <-chan time.Time
		var timer *time.Timer
		if err != nil {
			timer = time.NewTimer(hitlPoll)
			retry = timer.C
		}
		expired := false
		select {
		case <-deadline.C:
			expired = true
		case <-changed:
		case <-retry:
		}
		if timer != nil {
			timer.Stop()
		}
		if expired {
			break
		}
	}
	won, err := s.db.expirePendingAction(id)
	if err != nil {
		slog.Error("hitl wait ended and the row could not be closed; a verdict typed "+
			"now will look accepted and reach nobody", "pending", id, "err", err)
	}
	if !won {

		if p, ok := s.db.GetPendingAction(id); ok && p.Status != PendingHeld {
			if p.Status == PendingApproved {
				slog.Info("hitl approved as the wait ended; running inline", "pending", id)
				return "", false
			}
			return verdictRefusal(p, tool), true
		}
		slog.Warn("hitl wait ended and the row was neither closed nor answered",
			"pending", id)
	}
	return fmt.Sprintf(
		"NOT RUN (id %s): %s waited for human approval and nobody answered in time. "+
			"Tell the user it was not done. Do not retry — a retry is held again.",
		id, tool), true
}

func verdictRefusal(p PendingAction, tool string) string {
	switch p.Status {
	case PendingRejected:
		out := fmt.Sprintf("NOT RUN (id %s): a human rejected %s.", p.ID, tool)
		if p.ReviewerNote != "" {
			out += " Their note: " + p.ReviewerNote
		}
		return out + " Tell the user, and do not retry — a retry is held again."
	default:
		return fmt.Sprintf(
			"NOT RUN (id %s): %s needs human approval and the request is %s. "+
				"Tell the user, and do not retry — a retry is held again.",
			p.ID, tool, p.Status)
	}
}

var hitlPoll = 2 * time.Second

type holdShape struct {
	ask  string
	keys []string
}

// holdShapes declares the tools with a measured case. A tool with no entry
// reads as its name and its arguments. A tool with no keys gets no `always`:
// the row would pin nothing and run every call of the tool.
var holdShapes = map[string]holdShape{
	"like":    {ask: "react {reaction} to message {targetId}", keys: []string{"reaction"}},
	"dislike": {ask: "dislike message {targetId}"},
	"edit":    {ask: "edit message {targetId}"},
	"delete":  {ask: "delete message {targetId}"},
}

func withMatcher(p PendingAction) PendingAction {
	var args map[string]any
	if p.Args != "" {
		if err := json.Unmarshal([]byte(p.Args), &args); err != nil {
			p.MatcherError = "the recorded arguments are not readable as JSON: " + err.Error()
		}
	}
	scalars := stringArgs(args)
	p.Ask = p.GroupFolder + " wants to " + askText(p.Tool, scalars)
	if p.MatcherError == "" {
		spec, err := pinnedSpec(p.Tool, scalars)
		if err != nil {
			p.MatcherError = err.Error()
		}
		p.Matcher = spec
	}
	if p.MatcherError != "" {
		p.RuleText = "Always is not offered: " + p.MatcherError
		return p
	}
	p.RuleText = "Always: every " + p.Tool + " call with " + p.Matcher +
		" in " + p.GroupFolder + " runs without asking."
	return p
}

func pinnedSpec(tool string, scalars map[string]string) (string, error) {
	keys := holdShapes[tool].keys
	if len(keys) == 0 {
		return "", fmt.Errorf("%s declares no standing rule; an operator writes one", tool)
	}
	pinned := map[string]string{}
	for _, k := range keys {
		v, ok := scalars[k]
		if !ok {
			return "", fmt.Errorf("argument %q: the call carries no scalar value to pin", k)
		}
		pinned[k] = v
	}
	return auth.ParamsSpec(pinned)
}

func askText(tool string, scalars map[string]string) string {
	names := slices.Sorted(maps.Keys(scalars))
	if shape := holdShapes[tool]; shape.ask != "" {
		vars := make(map[string]string, len(scalars))
		for _, k := range names {
			vars[k] = shortValue(scalars[k])
		}
		if text, err := expand.Expand(shape.ask, vars); err == nil {
			return text
		}

	}
	out := "run " + tool
	for _, k := range names {
		out += " " + k + "=" + shortValue(scalars[k])
	}
	return out
}

func shortValue(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	if r := []rune(v); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return v
}

func stringArgs(args map[string]any) map[string]string {
	out := make(map[string]string, len(args))
	for k, v := range args {
		switch t := v.(type) {
		case string:
			out[k] = t
		case bool:
			out[k] = strconv.FormatBool(t)
		case float64:
			out[k] = strconv.FormatFloat(t, 'f', -1, 64)
		}
	}
	return out
}

func (s *Server) notifyHeld(t turnMCP, p PendingAction) {
	text := fmt.Sprintf("⏸ %s — ok? (%s)\n%s", p.Ask, p.ID, p.RuleText)
	if id := s.notifyHold(t, "hitl-"+p.ID, text, verdictOptions(p)); id != "" {
		if err := s.db.SetPendingNotice(p.ID, id); err != nil {
			slog.Error("hitl notice id not recorded; a reaction cannot resolve this call",
				"pending", p.ID, "err", err)
		}
	}
}

func verdictOptions(p PendingAction) []chanlib.Option {
	out := []chanlib.Option{
		{Label: "✅ Approve", Data: "/approve " + p.ID},
		{Label: "⛔ Reject", Data: "/reject " + p.ID},
	}
	if p.MatcherError == "" {
		out = append(out, chanlib.Option{Label: "♾ Always", Data: "/always " + p.ID})
	}
	return out
}

func (s *Server) notifyHoldFailed(t turnMCP, tool string, cause error) {
	_ = s.notifyHold(t, "hitl-fail-"+t.turnID, fmt.Sprintf(
		"⛔ %s could not be checked or held (%v). The call was "+
			"blocked and there is nothing to /approve — an operator must check routd.",
		tool, cause), nil)
}

func (s *Server) notifyHold(t turnMCP, idem, text string, options []chanlib.Option) string {
	if t.chatJID == "" || s.deliver == nil {
		return ""
	}
	id, err := s.deliver.SendOptions(t.chatJID, text, options, t.topic, idem)
	if err != nil {
		slog.Error("hitl notice not delivered", "jid", t.chatJID, "idem", idem, "err", err)
	}
	return id
}
