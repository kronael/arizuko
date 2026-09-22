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
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
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

	// DERIVED from Args, never stored: what an `always` verdict would write,
	// computed by the one renderer (withMatcher) so the notice, the REST row
	// and the writer cannot disagree about the rule (spec 6/26).
	Matcher string `json:"matcher"`
	// Unmatchable names the arguments no rule can constrain (stringArgs skips
	// every non-scalar). Stated in words wherever Matcher is shown.
	Unmatchable []string `json:"unmatchable,omitempty"`
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

// pendingAttrs renders the attributes a held call carries; `id` is the
// coordinate and is not stored twice. An empty field is left OFF the item —
// absent is the only null, the same rule resreg's own writer follows
// (resreg/entity.go itemFor), so a row written by either reads the same.
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

// pendingFrom reads one item back with the expiry applied and the derived
// verdict fields filled — the shape every reader of a held call wants.
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

// pendingItems reads the partition: every selection but the id is on an
// attribute, so the callers filter in Go.
func (d *DB) pendingItems(where ...objstore.Cmp) ([]objstore.Item, error) {
	return d.Obj().Query(context.Background(), objstore.Query{
		Entity: store.PendingActions, Partition: store.OwnerRoutd, Where: where,
	})
}

// pendingAction reads one held call by its coordinate.
func (d *DB) pendingAction(id string) (PendingAction, bool) {
	key, err := store.PendingActionKey(id)
	if err != nil {
		return PendingAction{}, false
	}
	items, err := d.Obj().Query(context.Background(), objstore.Query{
		Entity: store.PendingActions, Partition: store.OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil || len(items) == 0 {
		return PendingAction{}, false
	}
	return pendingFrom(items[0]), true
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

// platformID reduces a reaction's target to the bare platform message id:
// teled reports a reacted message as `<chat-jid>/<id>` while discd and slakd
// report the id alone. No platform id in use contains a slash.
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
	// `created_at` is an attribute, not a key fragment, so the order is applied
	// here — over fixed-width RFC3339 text, which orders by byte comparison.
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

// resolvePendingActionInto buffers one verdict on the caller's write set — the
// chat command's and the REST face's, which differ only in whose set it is and
// in what a LOST race is called there (lost).
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

// skipApprovalRow is the acl row an `always` verdict writes: on the `hold:`
// action, never on the tool. `deny` there means DO NOT HOLD this shape —
// Authorize reads every other effect as ALLOW, so a third effect name would be
// spec 5/19 Hazard 1, and a plain allow on `mcp:<tool>` would grant the tool
// while the hold still fired (spec 6/26 § Decision 5). Params is the matcher
// withMatcher rendered and the notice displayed — never one built here.
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
	// The folder comes off the ROW, never off the reviewer's chat or the
	// transport. The read is advisory; the conditional write serializes two
	// reviewers.
	held, ok := d.pendingAction(id)
	if !ok {
		return PendingAction{}, fmt.Errorf("no pending action %q: %w", id, ErrPendingNotFound)
	}
	if !d.Authorize(reviewer, held.GroupFolder, "admin", nil) {
		return PendingAction{}, fmt.Errorf(
			"%w: answering a held call in %q needs an admin grant covering that folder",
			ErrPendingForbidden, held.GroupFolder)
	}
	p, err := d.resolvePendingActionInto(ws, id, verdict, reviewer, note, lost)
	if err != nil {
		return PendingAction{}, err
	}
	if verdict != PendingApproved || !standing {
		return p, nil
	}
	// Refuse rather than approximate: an `always` whose full effective match
	// could not be stated is a rule the operator was never shown.
	if p.MatcherError != "" {
		return PendingAction{}, fmt.Errorf(
			"cannot make this a standing answer: %s", p.MatcherError)
	}
	if p.CallerAgent == "" || p.Tool == "" || p.GroupFolder == "" {
		return PendingAction{}, fmt.Errorf(
			"cannot make this a standing answer: the held row names no caller, tool or folder")
	}
	// The rule is a Merge and refuses nothing, so it rides beside the verdict's
	// conditional write.
	if err := d.grantACLInto(ctx, ws, skipApprovalRow(p, reviewer)); err != nil {
		return PendingAction{}, fmt.Errorf("record the skip-approval rule: %w", err)
	}
	return p, nil
}

// expirePendingAction closes a call nobody answered in time. The write
// CONDITIONS on the row still being held, so a verdict that landed in the same
// instant stands. Closed rather than left waiting: an approval typed against a
// row nobody is suspended on would look accepted and reach nobody.
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

// holdGate builds the tools/call hold check for one turn (spec 5/19); nil when
// the turn cannot be held, so the ipc layer pays nothing. Nothing is deferred:
// the call the human approves is the call this gate is still holding, so the
// approved arguments are the arguments that run.
func (s *Server) holdGate(t turnMCP) func(string, map[string]any) (string, bool) {
	if t.elevated || s.db == nil {
		return nil
	}
	return func(tool string, args map[string]any) (string, bool) {
		if !s.db.CheckHold("folder:"+t.folder, t.folder, tool, stringArgs(args)) {
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
		// The notice quotes the rule an `always` would write, through the same
		// renderer the writer reads it from.
		s.notifyHeld(t, withMatcher(p))
		return s.awaitVerdict(id, tool)
	}
}

// HoldWait bounds how long a blocking hold suspends the agent's tool call. It
// must stay under RUNED_RUN_TIMEOUT (20m default): past that the container is
// killed and the wait ends as a dead turn rather than as an answer.
var HoldWait = chanlib.EnvDur("HITL_WAIT", 10*time.Minute)

// awaitVerdict suspends the agent's tool call until a human answers it:
// ("", false) on approval (the caller runs the call), else the reason the call
// did not run, in words the agent can repeat. Giving up CLOSES the row: nobody
// is suspended on it any more, and a row left `held` would take a later
// verdict and look accepted.
func (s *Server) awaitVerdict(id, tool string) (string, bool) {
	deadline := time.Now().Add(HoldWait)
	for time.Now().Before(deadline) {
		time.Sleep(hitlPoll)
		p, ok := s.db.GetPendingAction(id)
		if !ok || p.Status == PendingHeld {
			continue
		}
		if p.Status == PendingApproved {
			slog.Info("hitl approved; running inline", "pending", id, "tool", tool)
			return "", false
		}
		slog.Info("hitl verdict", "pending", id, "status", p.Status)
		return verdictRefusal(p, tool), true
	}
	won, err := s.db.expirePendingAction(id)
	if err != nil {
		slog.Error("hitl wait ended and the row could not be closed; a verdict typed "+
			"now will look accepted and reach nobody", "pending", id, "err", err)
	}
	if !won {
		// Losing the conditional expiry means a human answered as the wait
		// ended; the ROW is the record.
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

// verdictRefusal is what the agent is told about a call that will not run. The
// reviewer's note rides along so the user hears the reason, not "it did not
// work".
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

// hitlPoll is how often the waiter re-reads the row; a verdict arrives at
// human speed.
var hitlPoll = 2 * time.Second

// withMatcher fills the derived verdict fields — the ONE renderer of the rule
// an `always` would write, read by the chat notice, the REST row and the
// writer; a second renderer would let a surface display one rule and store
// another (spec 6/26). It reads the recorded arguments, which are the
// arguments that will run: nothing edits them between the notice and the call.
func withMatcher(p PendingAction) PendingAction {
	raw := p.Args
	var args map[string]any
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			p.MatcherError = "the recorded arguments are not readable as JSON: " + err.Error()
			return p
		}
	}
	scalars := stringArgs(args)
	for k := range args {
		if _, ok := scalars[k]; !ok {
			p.Unmatchable = append(p.Unmatchable, k)
		}
	}
	sort.Strings(p.Unmatchable)
	spec, err := auth.ParamsSpec(scalars)
	if err != nil {
		p.MatcherError = err.Error()
		return p
	}
	p.Matcher = spec
	return p
}

// stringArgs flattens tool args for the acl `params` glob matcher. A
// non-scalar arg has no glob meaning and is skipped rather than stringified
// into something a rule could match by accident.
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

// notifyHeld tells the chat a call is waiting; delivery failure is logged, the
// row is already recorded. The deep link names WHICH decision while the page's
// operator gate names WHO — deliberately not a bearer, because the notice
// lands in a chat every reader shares (spec 6/26 § Gap 3).
func (s *Server) notifyHeld(t turnMCP, p PendingAction) {
	text := fmt.Sprintf("⏸ %s held for approval (id %s)\n/approve %s   /reject %s"+
		"\nor react ✅ / ❌ to this message",
		p.Tool, p.ID, p.ID, p.ID)
	text += "\n" + alwaysOffer(p)
	if url := s.approvalURL(p.ID); url != "" {
		text += "\n" + url
	}
	if id := s.notifyHold(t, "hitl-"+p.ID, text, verdictOptions(p)); id != "" {
		if err := s.db.SetPendingNotice(p.ID, id); err != nil {
			slog.Error("hitl notice id not recorded; a reaction cannot resolve this call",
				"pending", p.ID, "err", err)
		}
	}
}

// verdictOptions are the verdict commands as pickable answers; `/always` is
// offered only when an exact rule exists, the condition alwaysOffer states.
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

// alwaysOffer is the `always` half of the notice: the EXACT row the verdict
// would write, plus in words the two ways it is wider than it looks — a rule
// constrains only the parameters it NAMES, and a non-scalar argument can never
// be matched (spec 6/26). With no exact rule the offer is replaced by the
// reason, so the operator knows why the button is missing.
func alwaysOffer(p PendingAction) string {
	if p.MatcherError != "" {
		return "/always is not offered for this call: " + p.MatcherError
	}
	rule := p.Tool
	if p.Matcher != "" {
		rule += "(" + p.Matcher + ")"
	}
	out := fmt.Sprintf("/always %s → skip approval for\n  %s  in folder %s",
		p.ID, rule, p.GroupFolder)
	if p.Matcher == "" {
		out += "\n  (no argument constrains it: EVERY call to this tool will run)"
	} else {
		out += "\n  (arguments it does not name are unconstrained)"
	}
	if len(p.Unmatchable) > 0 {
		out += "\n  (no rule can constrain " + strings.Join(p.Unmatchable, ", ") + ")"
	}
	out += "\n  (skips approval only; the mcp: grant still has to allow the call)"
	return out
}

// approvalURL is the operator's browser route to one held call. Empty when
// WEB_HOST is unset — the chat commands are the whole notice then, not a
// broken link.
func (s *Server) approvalURL(id string) string {
	base := core.PublicBaseURL(s.webHost)
	if base == "" {
		return ""
	}
	return base + "/dash/approvals/?id=" + url.QueryEscape(id)
}

// notifyHoldFailed reports a call the gate blocked but could not record: no
// row, so nothing lists it and no id can resolve it — this notice and the
// agent's error are the whole trace. Keyed per turn so a failing turn reports
// once.
func (s *Server) notifyHoldFailed(t turnMCP, tool string, cause error) {
	_ = s.notifyHold(t, "hitl-fail-"+t.turnID, fmt.Sprintf(
		"⛔ %s needs approval and the hold could not be recorded (%v). The call was "+
			"blocked and there is nothing to /approve — an operator must check routd.",
		tool, cause), nil)
}

// notifyHold delivers one notice and returns the platform id it landed as,
// which is what a reaction on it will name.
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
