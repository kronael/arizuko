package routd

// pending_actions.go — spec 5/19's one genuinely new entity: a tool call suspended
// until a human approves it. The hold RULE lives in `acl` as a `hold:mcp:<tool>` row
// evaluated by auth.CheckHold; this file is only the suspended CALL's state.
//
// Release matches on (folder, tool, args_hash). The agent re-issues the call in its
// own next turn, so the released call runs as the ORIGINAL agent — same container,
// session, grants and secrets — with no second path around ipc's discipline.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
)

// PendingAction is one suspended call.
type PendingAction struct {
	ID           string `json:"id"`
	GroupFolder  string `json:"group_folder"`
	CallerAgent  string `json:"caller_agent"`
	Tool         string `json:"tool"`
	Args         string `json:"args"`
	ArgsFinal    string `json:"args_final"`
	ArgsHash     string `json:"args_hash"`
	Status       string `json:"status"`
	ChatJID      string `json:"chat_jid"`
	CreatedAt    string `json:"created_at"`
	ReviewedBy   string `json:"reviewed_by"`
	ReviewedAt   string `json:"reviewed_at"`
	ReviewerNote string `json:"reviewer_note"`
	Result       string `json:"result"`
	Error        string `json:"error"`
	ExpiresAt    string `json:"expires_at"`
	// NoticeID is the platform id of the chat notice this row was announced in.
	// A reaction names the message it targets, so this is what turns a ✅ into
	// this call's verdict.
	NoticeID string `json:"notice_id"`
	// Mode is the hold row's effect: auth.EffectHoldBlock means the agent's tool
	// call is WAITING on this row right now, EffectHoldAsync that it already
	// returned. It decides who runs the approved call — the waiter, or the agent
	// re-issuing from resolutionMessage — so exactly one of them does.
	Mode string `json:"mode"`

	// The three fields below are DERIVED from ArgsFinal, never stored: they are
	// what an `always` verdict would write, computed by one renderer
	// (withMatcher → holdMatcher) so the notice, the REST row and the writer
	// cannot disagree about the rule. Spec 6/26 § "The predicate is part of the
	// verdict": a rule nobody was shown is a grant nobody agreed to.
	Matcher string `json:"matcher"`
	// Unmatchable names the arguments no rule can constrain — stringArgs skips
	// every non-scalar, so a rule is silent about them and they are free to
	// change on the next call. Stated in words wherever Matcher is shown.
	Unmatchable []string `json:"unmatchable,omitempty"`
	// MatcherError is why no exact rule can be written for this call. Non-empty
	// means `always` is refused rather than approximated.
	MatcherError string `json:"matcher_error,omitempty"`
}

const (
	PendingHeld     = "held"
	PendingApproved = "approved"
	PendingRejected = "rejected"
	PendingReleased = "released"
	PendingExpired  = "expired"
)

// ArgsHash is the canonical hash release matches on. Keys are sorted and BOTH key
// and value are JSON-encoded, so an agent re-issuing the same call with its map in a
// different order still matches while a changed VALUE does not — the edited-args
// rule, enforced by the key rather than by a comparison someone must remember.
//
// The key is encoded, not written raw: the agent chooses the argument NAMES, so a
// raw key containing the separators reproduces another map's serialization —
// `{"a=\"1\"\nb": "x"}` hashed identically to `{"a":"1","b":"x"}`, a collision an
// agent could steer an approval onto.
func ArgsHash(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		v, err := json.Marshal(args[k])
		if err != nil {
			// An unmarshalable arg must not collide with a well-formed one.
			v = []byte(fmt.Sprintf("%q", fmt.Sprint(args[k])))
		}
		kj, err := json.Marshal(k)
		if err != nil {
			kj = []byte(fmt.Sprintf("%q", k))
		}
		b.Write(kj)
		b.WriteByte('=')
		b.Write(v)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// PutPendingAction records a held call and returns its id.
func (d *DB) PutPendingAction(p PendingAction) error {
	if p.ID == "" || p.GroupFolder == "" || p.Tool == "" {
		return fmt.Errorf("pending action needs id, folder and tool")
	}
	if p.Status == "" {
		p.Status = PendingHeld
	}
	if p.CreatedAt == "" {
		p.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if p.Mode == "" {
		p.Mode = auth.EffectHoldAsync
	}
	_, err := d.db.Exec(`
		INSERT INTO pending_actions
		  (id, group_folder, caller_agent, tool, args, args_final, args_hash,
		   status, chat_jid, created_at, expires_at, mode, notice_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.GroupFolder, p.CallerAgent, p.Tool, p.Args, p.ArgsFinal, p.ArgsHash,
		p.Status, p.ChatJID, p.CreatedAt, p.ExpiresAt, p.Mode, p.NoticeID)
	return err
}

const pendingCols = `id, group_folder, caller_agent, tool, args, args_final, args_hash,
	status, chat_jid, created_at, reviewed_by, reviewed_at, reviewer_note, result, error, expires_at,
	mode, notice_id`

func scanPending(sc interface{ Scan(...any) error }) (PendingAction, error) {
	var p PendingAction
	err := sc.Scan(&p.ID, &p.GroupFolder, &p.CallerAgent, &p.Tool, &p.Args, &p.ArgsFinal,
		&p.ArgsHash, &p.Status, &p.ChatJID, &p.CreatedAt, &p.ReviewedBy, &p.ReviewedAt,
		&p.ReviewerNote, &p.Result, &p.Error, &p.ExpiresAt, &p.Mode, &p.NoticeID)
	return p, err
}

// queryExecer is the *sql.DB / *sql.Tx subset the resolve path runs on, so the
// chat command and the REST face share one implementation across their
// different transaction scopes.
type queryExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

func pendingActionOn(q queryExecer, id string) (PendingAction, bool) {
	p, err := scanPending(q.QueryRow(
		`SELECT `+pendingCols+` FROM pending_actions WHERE id = ?`, id))
	if err != nil {
		return PendingAction{}, false
	}
	return withMatcher(applyExpiry(p)), true
}

// GetPendingAction reads one row by id, for the blocking waiter.
func (d *DB) GetPendingAction(id string) (PendingAction, bool) { return pendingActionOn(d.db, id) }

// SetPendingNotice records which chat message announced this row, so a reaction
// on that message can be read as its verdict.
func (d *DB) SetPendingNotice(id, noticeID string) error {
	_, err := d.db.Exec(`UPDATE pending_actions SET notice_id = ? WHERE id = ?`, noticeID, id)
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
	var id string
	err := d.db.QueryRow(
		`SELECT id FROM pending_actions
		  WHERE chat_jid = ? AND notice_id = ? AND status = ?`,
		chatJID, noticeID, PendingHeld).Scan(&id)
	if err != nil {
		return ""
	}
	return verb + " " + id
}

// platformID reduces a reaction's target to the bare platform message id the
// delivery returned. Adapters disagree about the shape: teled reports a reacted
// message as `<chat-jid>/<id>` while discd and slakd report the id alone, and a
// notice recorded as `6335` would never match `telegram:user/…/6335`. No
// platform id in use contains a slash, so the last segment is the id.
func platformID(target string) string {
	if i := strings.LastIndexByte(target, '/'); i >= 0 {
		return target[i+1:]
	}
	return target
}

// SetPendingMode records that nobody is waiting on this row any more, so the
// approval has to travel as a re-issue trigger instead.
func (d *DB) SetPendingMode(id, mode string) error {
	_, err := d.db.Exec(`UPDATE pending_actions SET mode = ? WHERE id = ?`, mode, id)
	return err
}

// ListPendingActions returns a folder's rows, newest first. An empty folder
// lists every folder's — the operator face; the caller binds the folder.
func (d *DB) ListPendingActions(folder, status string) ([]PendingAction, error) {
	q := `SELECT ` + pendingCols + ` FROM pending_actions WHERE 1=1`
	var args []any
	if folder != "" {
		q += ` AND group_folder = ?`
		args = append(args, folder)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingAction
	for rows.Next() {
		p, err := scanPending(rows)
		if err != nil {
			return nil, err
		}
		// Expiry is applied BEFORE the status filter. Filtering in SQL compared
		// the STORED status, so `status=expired` returned nothing while
		// `status=held` returned rows this function then relabelled expired —
		// each filter answering with the other's rows (BUGS J7).
		p = applyExpiry(p)
		if status != "" && p.Status != status {
			continue
		}
		out = append(out, withMatcher(p))
	}
	return out, rows.Err()
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

// ErrPendingNotFound / ErrPendingResolved let the REST face map the two
// verdict failures to 404/409 without string-matching the operator text.
var (
	ErrPendingNotFound = errors.New("not found")
	ErrPendingResolved = errors.New("already resolved")
)

func resolvePendingActionOn(q queryExecer, id, status, reviewer, note string) (PendingAction, error) {
	if status != PendingApproved && status != PendingRejected {
		return PendingAction{}, fmt.Errorf("verdict must be approved or rejected, got %q", status)
	}
	cur, ok := pendingActionOn(q, id)
	if !ok {
		return PendingAction{}, fmt.Errorf("no pending action %q: %w", id, ErrPendingNotFound)
	}
	if cur.Status != PendingHeld {
		return PendingAction{}, fmt.Errorf("pending action %q %w (%s)", id, ErrPendingResolved, cur.Status)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// The read above is advisory — two operators can both pass it. The UPDATE's
	// `status = held` predicate is what actually serializes them, so the row
	// count IS the verdict: without checking it, both callers were told
	// "approved", two resolution messages were enqueued, and the agent was asked
	// twice to re-issue one call.
	res, err := q.Exec(`
		UPDATE pending_actions
		   SET status = ?, reviewed_by = ?, reviewed_at = ?, reviewer_note = ?
		 WHERE id = ? AND status = ?`,
		status, reviewer, now, note, id, PendingHeld)
	if err != nil {
		return PendingAction{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return PendingAction{}, err
	}
	if n != 1 {
		return PendingAction{}, fmt.Errorf("pending action %q %w (by someone else)", id, ErrPendingResolved)
	}
	cur.Status, cur.ReviewedBy, cur.ReviewedAt, cur.ReviewerNote = status, reviewer, now, note
	return cur, nil
}

// resolutionMessage is the approved-call trigger the agent re-issues from (spec 5/19
// §Resolution step 2). One renderer for both resolution faces. The target is the
// HELD call's own chat — not the chat the verdict arrived in, which routed an
// approval typed elsewhere to the wrong agent — falling back to the bare folder.
func resolutionMessage(p PendingAction, reviewer string) core.Message {
	// The HELD CALL's chat, never the chat the operator typed the verdict in.
	// Those differ whenever an operator approves from anywhere else, and using
	// the command's chat woke a folder that was not waiting while the folder
	// that was stayed asleep, holding an approval it could never consume
	// (BUGS J5).
	target := p.ChatJID
	if target == "" {
		target = p.GroupFolder
	}
	return core.Message{
		ID:      core.MsgID("hitl-" + p.ID),
		ChatJID: target,
		Sender:  reviewer,
		Content: fmt.Sprintf(
			"Approved held call %s: re-issue %s with exactly these arguments and "+
				"report the result.\n%s", p.ID, p.Tool, p.ArgsFinal),
		Timestamp: time.Now().UTC(),
	}
}

// skipApprovalRow is the acl row an `always` verdict writes: on the `hold:`
// action, never on the tool. `deny` there means DO NOT HOLD this shape —
// Authorize reads every other effect as ALLOW, so a third effect name would be
// spec 5/19 Hazard 1 in friendlier words, and a plain allow on `mcp:<tool>`
// would grant the tool while the hold still fired. Every surface says "skip
// approval"; only the wire says deny (spec 6/26 § Decision 5).
//
// Params is the matcher withMatcher already rendered and the notice already
// displayed — never one built here, which is what makes "the surface refuses to
// write a rule it did not display" structural rather than a habit.
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

// resolveHoldTx is the one funnel both resolution faces run — chat /approve and REST
// POST /v1/pending_actions/{id}/approve|reject. The verdict and, on approval, its
// resolution message commit in the SAME transaction, so the poll can never dispatch
// the re-issue before ConsumeApprovedAction can see the approval. Returns the chat
// to enqueue ("" when nothing to trigger).
//
// `standing` is the `always` verdict: approve exactly as `yes`, and additionally
// write the skip-approval row so the next call of this shape runs inline. It is
// NOT a third status — pending_actions keeps its states and the standing answer
// lives in acl, where every other standing answer already lives — and the row
// commits in THIS transaction, so a crash cannot leave a call approved with no
// rule or a rule with no approved call.
//
// `never` is deliberately absent. Undoing it means deleting the tool deny it
// also writes, and acl rows carry no verdict identity, so a revoke could not
// tell that deny from standing policy that predates it (spec 6/26 § "Revocation
// needs provenance"). That is an unresolved design question, not an omission.
func resolveHoldTx(ctx context.Context, tx *sql.Tx, id, verdict, reviewer, note string, standing bool) (PendingAction, string, error) {
	p, err := resolvePendingActionOn(tx, id, verdict, reviewer, note)
	if err != nil {
		return PendingAction{}, "", err
	}
	if verdict != PendingApproved {
		return p, "", nil
	}
	if standing {
		// Refuse rather than approximate: an `always` whose full effective match
		// could not be stated is a rule the operator was never shown.
		if p.MatcherError != "" {
			return PendingAction{}, "", fmt.Errorf(
				"cannot make this a standing answer: %s", p.MatcherError)
		}
		if p.CallerAgent == "" || p.Tool == "" || p.GroupFolder == "" {
			return PendingAction{}, "", fmt.Errorf(
				"cannot make this a standing answer: the held row names no caller, tool or folder")
		}
		if err := grantACLTx(ctx, tx, skipApprovalRow(p, reviewer)); err != nil {
			return PendingAction{}, "", fmt.Errorf("record the skip-approval rule: %w", err)
		}
	}
	if p.Mode == auth.EffectHoldBlock {
		// The agent's call is still suspended on this row: the waiter consumes the
		// approval and runs it inline. A trigger here would make the agent re-issue
		// a call the waiter already ran. A waiter that times out flips the row to
		// async first, so a late approval still lands here.
		return p, "", nil
	}
	msg := resolutionMessage(p, reviewer)
	if err := putMessage(tx, msg); err != nil {
		return PendingAction{}, "", fmt.Errorf("record resolution message: %w", err)
	}
	return p, msg.ChatJID, nil
}

// ConsumeApprovedAction is the one-shot release. It flips exactly one approved
// row matching (folder, tool, args_hash) to `released` and reports whether it
// found one — the LoadAndDelete shape, so two concurrent re-issues cannot both
// pass on a single approval.
func (d *DB) ConsumeApprovedAction(folder, tool, argsHash string) (string, bool) {
	tx, err := d.db.Begin()
	if err != nil {
		return "", false
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRow(`
		SELECT id FROM pending_actions
		 WHERE group_folder = ? AND tool = ? AND args_hash = ? AND status = ?
		 ORDER BY created_at DESC LIMIT 1`,
		folder, tool, argsHash, PendingApproved).Scan(&id)
	if err != nil {
		return "", false
	}
	res, err := tx.Exec(`UPDATE pending_actions SET status = ? WHERE id = ? AND status = ?`,
		PendingReleased, id, PendingApproved)
	if err != nil {
		return "", false
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", false
	}
	if err := tx.Commit(); err != nil {
		return "", false
	}
	return id, true
}

// NewPendingID is short on purpose: an operator types it into chat as
// `/approve <id>`, and Telegram's callback_data is capped at 64 bytes.
func NewPendingID() string {
	return randHex(4)
}

// holdGate builds the tools/call hold check for one turn (spec 5/19). Nil when the
// turn cannot be held, so the ipc layer pays nothing.
//
// Order matters: the approved-row consume comes FIRST, because the agent re-issuing
// an approved call must pass even though the hold rule still matches. Any argument
// deviation misses the hash and falls through to the hold.
func (s *Server) holdGate(t turnMCP) func(string, map[string]any) (string, bool) {
	if t.elevated || s.db == nil {
		return nil
	}
	return func(tool string, args map[string]any) (string, bool) {
		hash := ArgsHash(args)
		if id, ok := s.db.ConsumeApprovedAction(t.folder, tool, hash); ok {
			slog.Info("hitl released", "folder", t.folder, "tool", tool, "pending", id)
			return "", false
		}
		mode := s.db.CheckHold("folder:"+t.folder, t.folder, tool, stringArgs(args))
		if mode == "" {
			return "", false
		}
		id := NewPendingID()
		raw, _ := json.Marshal(args)
		p := PendingAction{
			ID: id, GroupFolder: t.folder, CallerAgent: "folder:" + t.folder,
			Tool: tool, Args: string(raw), ArgsFinal: string(raw), ArgsHash: hash,
			Status: PendingHeld, ChatJID: t.chatJID, Mode: mode,
		}
		if err := s.db.PutPendingAction(p); err != nil {
			// Fail CLOSED: a hold rule exists and we could not record the call, so letting
			// it run would silently defeat the gate. The EMPTY id is the signal ipc renders
			// as an error rather than a pending result: with no row there is no id to
			// /approve (BUGS J3). The chat notice is the operator's only sign it happened.
			slog.Error("hitl could not record pending action; blocking the call",
				"folder", t.folder, "tool", tool, "err", err)
			s.notifyHoldFailed(t, tool, err)
			return "", true
		}
		// The notice quotes the rule an `always` would write, so it runs through
		// the same renderer the writer will read it from.
		s.notifyHeld(t, withMatcher(p))
		if mode == auth.EffectHoldAsync || s.awaitVerdict(id) {
			return id, true
		}
		// Approved while the call waited, and the waiter consumed it: fall through
		// and run the call the operator just said yes to.
		return "", false
	}
}

// HoldWait bounds how long a blocking hold suspends the agent's tool call. It has
// to stay under RUNED_RUN_TIMEOUT (20m default): past that the container is killed
// and the wait ends as a dead turn rather than as an answer.
var HoldWait = chanlib.EnvDur("HITL_WAIT", 10*time.Minute)

// awaitVerdict suspends a blocking hold until a human answers it, and reports
// whether the call stays held. It returns false ONLY when the approval was
// consumed here, which is the caller's signal to run the call inline.
//
// Giving up flips the row to async so a late approval still reaches the agent as
// a re-issue trigger — otherwise an approval typed after the timeout would sit
// unused until some future call of the same shape silently consumed it.
func (s *Server) awaitVerdict(id string) bool {
	deadline := time.Now().Add(HoldWait)
	for time.Now().Before(deadline) {
		time.Sleep(hitlPoll)
		p, ok := s.db.GetPendingAction(id)
		if !ok || p.Status == PendingHeld {
			continue
		}
		if p.Status != PendingApproved {
			slog.Info("hitl verdict", "pending", id, "status", p.Status)
			return true
		}
		if _, ok := s.db.ConsumeApprovedAction(p.GroupFolder, p.Tool, p.ArgsHash); !ok {
			return true
		}
		slog.Info("hitl released inline", "pending", id, "tool", p.Tool)
		return false
	}
	if err := s.db.SetPendingMode(id, auth.EffectHoldAsync); err != nil {
		slog.Error("hitl wait expired and the row could not be marked async; a later "+
			"approval will not reach the agent", "pending", id, "err", err)
	}
	return true
}

// hitlPoll is how often the waiter re-reads the row. A verdict arrives at human
// speed, so a tighter loop only costs reads.
var hitlPoll = 2 * time.Second

// withMatcher fills the derived verdict fields — the ONE renderer of the rule an
// `always` would write. Every sink reads it here: the chat notice, the REST row
// the dashboard renders, and resolveHoldTx's writer. A second renderer anywhere
// would let a surface display one rule and store another, which spec 6/26 calls
// a grant nobody agreed to.
func withMatcher(p PendingAction) PendingAction {
	raw := p.ArgsFinal
	if raw == "" {
		raw = p.Args
	}
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

// stringArgs flattens tool args for the acl `params` matcher, which compares
// globs against strings. A non-scalar arg has no glob meaning and is skipped
// rather than stringified into something a rule could match by accident.
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

// notifyHeld tells the chat a call is waiting. Delivery failure is logged, not fatal:
// the row is already recorded.
//
// The notice carries the chat commands AND the deep link to the approvals page, which
// names WHICH decision while the page's own operator gate names WHO. Deliberately not
// a bearer token: this notice lands in the chat the agent shares with its users, so a
// link carrying the verdict would hand it to every reader (spec 6/26 § Gap 3).
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

// verdictOptions are the verdict commands as pickable answers. `/always` is
// offered on the condition alwaysOffer states in the text: an exact rule exists.
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
// would write, plus the two properties that widen it past what an operator
// assumes (spec 6/26 § "The predicate is part of the verdict"). Both are stated
// in words because neither is visible in the rule text:
//
//   - a rule constrains only the parameters it NAMES, so an argument it does not
//     name is free to be anything on the next call;
//   - a non-scalar argument is skipped by stringArgs and can never be matched.
//
// When no exact rule can be written the offer is replaced by the reason, so the
// operator is told why the button is missing rather than left to wonder.
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
// WEB_HOST is unset — the chat commands are the whole notice then, rather than a
// broken link the operator cannot use.
func (s *Server) approvalURL(id string) string {
	base := core.PublicBaseURL(s.webHost)
	if base == "" {
		return ""
	}
	return base + "/dash/approvals/?id=" + url.QueryEscape(id)
}

// notifyHoldFailed reports a call the gate blocked but could not record. Unlike
// a held call this leaves NO row, so nothing lists it and no id can resolve it —
// this notice and the agent's error are the whole trace. Keyed per turn, like
// the other dispatch notices, so a failing turn reports once.
func (s *Server) notifyHoldFailed(t turnMCP, tool string, cause error) {
	_ = s.notifyHold(t, "hitl-fail-"+t.turnID, fmt.Sprintf(
		"⛔ %s needs approval and the hold could not be recorded (%v). The call was "+
			"blocked and there is nothing to /approve — an operator must check routd.",
		tool, cause), nil)
}

// notifyHold delivers one notice and returns the platform id it landed as, which
// is what a reaction on it will name.
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
