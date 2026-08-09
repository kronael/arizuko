package routd

// pending_actions.go — spec 5/19's one genuinely new entity: a tool call
// suspended until a human approves it. The hold RULE lives in `acl` as a
// `hold:mcp:<tool>` row and is evaluated by auth.CheckHold; this file is only
// the suspended CALL's state.
//
// Release matches on (folder, tool, args_hash). The agent re-issues the call in
// its own next turn, so the released call runs as the ORIGINAL agent — same
// container, session, grants and secrets — with no out-of-turn dispatcher to
// build and no second path around ipc's grant and audit discipline.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

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
}

const (
	PendingHeld     = "held"
	PendingApproved = "approved"
	PendingRejected = "rejected"
	PendingReleased = "released"
	PendingExpired  = "expired"
)

// ArgsHash is the canonical hash release matches on. Keys are sorted and BOTH
// key and value are JSON-encoded, so an agent re-issuing the same call with its
// map in a different order still matches, while a changed VALUE does not — the
// edited-args rule, enforced by the key rather than by a comparison someone has
// to remember to write.
//
// The key is encoded, not written raw: the agent chooses the argument NAMES, so
// a raw key containing the separators reproduces another map's serialization.
// `{"a=\"1\"\nb": "x"}` hashed identically to `{"a":"1","b":"x"}` until this was
// fixed — a collision an agent could steer an approval onto. JSON-encoding both
// sides escapes the quote and the newline, so no key can forge a separator.
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
	_, err := d.db.Exec(`
		INSERT INTO pending_actions
		  (id, group_folder, caller_agent, tool, args, args_final, args_hash,
		   status, chat_jid, created_at, expires_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.GroupFolder, p.CallerAgent, p.Tool, p.Args, p.ArgsFinal, p.ArgsHash,
		p.Status, p.ChatJID, p.CreatedAt, p.ExpiresAt)
	return err
}

const pendingCols = `id, group_folder, caller_agent, tool, args, args_final, args_hash,
	status, chat_jid, created_at, reviewed_by, reviewed_at, reviewer_note, result, error, expires_at`

func scanPending(sc interface{ Scan(...any) error }) (PendingAction, error) {
	var p PendingAction
	err := sc.Scan(&p.ID, &p.GroupFolder, &p.CallerAgent, &p.Tool, &p.Args, &p.ArgsFinal,
		&p.ArgsHash, &p.Status, &p.ChatJID, &p.CreatedAt, &p.ReviewedBy, &p.ReviewedAt,
		&p.ReviewerNote, &p.Result, &p.Error, &p.ExpiresAt)
	return p, err
}

// queryExecer is the *sql.DB / *sql.Tx subset the resolve path runs on, so the
// chat command and the REST face share one implementation across their
// different transaction scopes.
type queryExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// PendingAction reads one row. Expiry is applied on read: a held row past
// expires_at reports `expired`, so no GC job has to run for the status to be
// true.
func (d *DB) PendingAction(id string) (PendingAction, bool) {
	return pendingActionOn(d.db, id)
}

func pendingActionOn(q queryExecer, id string) (PendingAction, bool) {
	p, err := scanPending(q.QueryRow(
		`SELECT `+pendingCols+` FROM pending_actions WHERE id = ?`, id))
	if err != nil {
		return PendingAction{}, false
	}
	return applyExpiry(p), true
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
		out = append(out, p)
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

// ResolvePendingAction records a verdict. It moves only a `held` row, so a
// second approval of the same id is a no-op rather than a re-arm.
func (d *DB) ResolvePendingAction(id, status, reviewer, note string) (PendingAction, error) {
	return resolvePendingActionOn(d.db, id, status, reviewer, note)
}

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

// resolutionMessage is the approved-call trigger the agent re-issues from
// (spec 5/19 §Resolution step 2). One renderer for both resolution faces. The
// target is the HELD call's own chat — not the chat the verdict arrived in,
// which routed an approval typed elsewhere to the wrong agent — falling back
// to the bare folder (the delegate JID shape) when the call was held outside
// any chat.
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

// resolveHoldTx is the one funnel both resolution faces run — chat /approve
// (cmdResolveHold) and REST POST /v1/pending_actions/{id}/approve|reject
// (pendingActionsHandler), the "both funnel to one handler" spec 5/19 names.
// The verdict and, on approval, its resolution message commit in the SAME
// transaction: the approved row and its re-issue trigger become visible at
// once, so the poll can never dispatch the re-issue before
// ConsumeApprovedAction can see the approval. Returns the chat to enqueue
// ("" when nothing to trigger).
func resolveHoldTx(q queryExecer, id, verdict, reviewer, note string) (PendingAction, string, error) {
	p, err := resolvePendingActionOn(q, id, verdict, reviewer, note)
	if err != nil {
		return PendingAction{}, "", err
	}
	if verdict != PendingApproved {
		return p, "", nil
	}
	msg := resolutionMessage(p, reviewer)
	if err := putMessage(q, msg); err != nil {
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

// holdGate builds the tools/call hold check for one turn (spec 5/19). Nil when
// the turn cannot be held, so the ipc layer pays nothing.
//
// Order matters: the approved-row consume comes FIRST. The agent re-issuing an
// approved call must pass even though the hold rule still matches — that is
// what "one-shot release" means. Any argument deviation misses the hash and
// falls through to the hold, which is the edited-args rule.
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
		if !s.db.CheckHold("folder:"+t.folder, t.folder, tool, stringArgs(args)) {
			return "", false
		}
		id := NewPendingID()
		raw, _ := json.Marshal(args)
		if err := s.db.PutPendingAction(PendingAction{
			ID: id, GroupFolder: t.folder, CallerAgent: "folder:" + t.folder,
			Tool: tool, Args: string(raw), ArgsFinal: string(raw), ArgsHash: hash,
			Status: PendingHeld, ChatJID: t.chatJID,
		}); err != nil {
			// Fail CLOSED: a hold rule exists and we could not record the call, so
			// letting it run would silently defeat the gate the operator asked for.
			// The EMPTY id is the signal ipc renders as an error rather than as a
			// pending result: with no row there is no id to /approve, and the
			// pending shape told the agent to wait on one that does not exist
			// (BUGS J3). The chat notice is the operator's only sign it happened.
			slog.Error("hitl could not record pending action; blocking the call",
				"folder", t.folder, "tool", tool, "err", err)
			s.notifyHoldFailed(t, tool, err)
			return "", true
		}
		s.notifyHeld(t, id, tool)
		return id, true
	}
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

// notifyHeld tells the chat a call is waiting. Delivery failure is logged, not
// fatal: the row is already recorded and `pending_actions list` still finds it.
func (s *Server) notifyHeld(t turnMCP, id, tool string) {
	s.notifyHold(t, "hitl-"+id, fmt.Sprintf(
		"⏸ %s held for approval (id %s)\n/approve %s   /reject %s", tool, id, id, id))
}

// notifyHoldFailed reports a call the gate blocked but could not record. Unlike
// a held call this leaves NO row, so nothing lists it and no id can resolve it —
// this notice and the agent's error are the whole trace. Keyed per turn, like
// the other dispatch notices, so a failing turn reports once.
func (s *Server) notifyHoldFailed(t turnMCP, tool string, cause error) {
	s.notifyHold(t, "hitl-fail-"+t.turnID, fmt.Sprintf(
		"⛔ %s needs approval and the hold could not be recorded (%v). The call was "+
			"blocked and there is nothing to /approve — an operator must check routd.",
		tool, cause))
}

func (s *Server) notifyHold(t turnMCP, idem, text string) {
	if t.chatJID == "" || s.deliver == nil {
		return
	}
	if _, err := s.deliver.Send(t.chatJID, text, "", t.topic, "", idem); err != nil {
		slog.Error("hitl notice not delivered", "jid", t.chatJID, "idem", idem, "err", err)
	}
}
