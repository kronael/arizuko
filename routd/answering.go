package routd

// answering.go answers one operator question: are agents ANSWERING people?
//
// Two facts, one window. `turns` is what the dispatcher did — how many turns
// finished and how many expired. `waiting` is what the people got — every chat
// whose newest human message is newer than its newest agent reply. Both are
// needed and neither implies the other: a fleet can finish every turn it starts
// and answer nobody, because a turn only runs when a route says so.
//
// A PLAIN /v1 READ, NOT A RESREG RESOURCE, for the reason dash_reads_http.go
// records: resreg gives REST and MCP from one declaration, so registering this
// would hand every agent a cross-folder tool for reading who is waiting on whom.
// The operator needs that; an agent does not. It is an AGGREGATE — counts,
// timestamps and a verdict, never message content — so the surface cannot become
// a way to read another folder's conversations.
//
// WHAT IT DOES NOT CLAIM. A reply is a bot row routd stored, not a message a
// person received: delivery failures are bot rows too (dead_letters_db.go is a
// view of `messages` on status='failed'). So this answers "did the agent produce
// an answer", and a fleet whose adapter cannot send looks answered here. That
// half belongs to `/v1/routing/errored` and the dead-letter surface, and the
// two are read together.
//
// THE VERDICT IS WHY THIS IS A SURFACE AND NOT A QUERY. "Nobody answered" alone
// cannot separate a stalled turn from a chat the operator configured not to
// answer, and the two want opposite responses. A `verb=mention` rule means plain
// messages fire no turn: that chat waits by design and is reported as `held`,
// not as a fault. The verdict is routd's own resolve over the chat's newest
// human message — the same call the loop makes — so this reader cannot disagree
// with the behaviour it describes. Nothing here changes that behaviour.

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// answeringWindow is the window a caller that names none gets. It is the week
// the eval's check 21 asks by hand, so the served answer and the hand query
// describe the same period. The response echoes it, so the window a row was
// judged against is always on the wire.
const answeringWindow = 7 * 24 * time.Hour

// WaitingChat is one chat where a person wrote and nothing answered.
type WaitingChat struct {
	ChatJID    string
	ChatName   string
	Folder     string
	Verdict    string
	LastSender string
	LastHuman  time.Time
	// LastReply is zero when the agent has never answered this chat at all.
	LastReply time.Time
}

// WaitingChats lists the chats a person is waiting on, newest question first.
//
// A chat qualifies on the OUTCOME, not on the routing: it is listed when its
// newest human message falls in the window and no agent reply is newer. That is
// deliberately wider than the held set (unrouted.go), which lists only chats the
// loop drops. A chat with a working route that simply stopped answering is
// invisible to the held set and is exactly the failure this read is for.
//
// The route table is read once up front so a store fault reaches the caller:
// resolve treats that fault as a route miss, which here would label every chat
// `held` and read as "the instance has no routes".
//
// IT WALKS THE CHAT LIST — 136 partitions on krons — and reads each chat's
// newest human row, which is the same shape and cost as the held set's
// InboundChatSummary. The second read, for the reply, is made only for a chat
// that asked something inside the window, so the expensive half is bounded by
// the window rather than by the instance. This is an operator's read, not a hot
// path; nothing on the message path calls it.
func (l *Loop) WaitingChats(since time.Time) ([]WaitingChat, error) {
	if _, err := l.db.Routes(); err != nil {
		return nil, err
	}
	jids, err := l.db.ChatJIDs()
	if err != nil {
		return nil, err
	}
	var out []WaitingChat
	for _, jid := range jids {
		human, asked, err := l.db.LastHumanMessage(jid)
		if err != nil {
			return nil, err
		}
		// The window is applied to the QUESTION. A chat whose newest human
		// message predates it is not waiting now, whatever its reply state.
		if !asked || human.Timestamp.Before(since) {
			continue
		}
		reply, answered, err := l.db.LastBotMessage(jid)
		if err != nil {
			return nil, err
		}
		if answered && !reply.Timestamp.Before(human.Timestamp) {
			continue
		}
		w := WaitingChat{
			ChatJID: jid, ChatName: human.ChatName, LastSender: human.Sender,
			LastHuman: human.Timestamp,
		}
		if answered {
			w.LastReply = reply.Timestamp
		}
		w.Verdict, w.Folder = answeringVerdict(l.resolve(jid, human))
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastHuman.After(out[j].LastHuman) })
	return out, nil
}

// answeringVerdict names a resolution: what the loop does with this message
// today, and the folder that would answer it.
func answeringVerdict(r resolution) (verdict, folder string) {
	switch {
	case r.ok:
		return apiv1.VerdictRoute, r.Folder
	case r.Observe != "":
		return apiv1.VerdictObserve, r.Observe
	default:
		return apiv1.VerdictHeld, ""
	}
}

// countTurn folds one turn's state into the window's counts.
//
// The four states are named explicitly and anything else lands in Other, so the
// counts always sum to the turns in the window. A state added later would
// otherwise be dropped silently, and a surface built to expose silence is the
// last place that should keep any.
func countTurn(out *apiv1.TurnOutcomes, state string) {
	switch state {
	case store.TurnDone:
		out.Done++
	case store.TurnExpired:
		out.Expired++
	case store.TurnRunning:
		out.Running++
	case store.TurnPendingRetry:
		out.PendingRetry++
	default:
		out.Other++
	}
}

// visibleTo reports whether a bearer whose claim is tokenFolder may see a row
// belonging to rowFolder.
//
// It is descendant(), NOT ownsFolder(). ownsFolder counts an EMPTY target as
// owned by anyone, which is right for a resource no folder binds and wrong here:
// a held chat's folder is empty, so ownsFolder would hand every folder-scoped
// bearer every unclaimed chat on the instance. An empty claim is the root or a
// service token and sees everything; a scoped one sees its own subtree and no
// unclaimed rows.
func visibleTo(tokenFolder, rowFolder string) bool {
	if tokenFolder == "" {
		return true
	}
	return rowFolder != "" && descendant(rowFolder, tokenFolder)
}

// answeringSince reads the window bound. An absent bound is the default week,
// which the response then echoes; an unparseable one is refused rather than
// silently replaced, because a window nobody chose would report the wrong period
// under a timestamp that looks deliberate.
func answeringSince(q string) (time.Time, error) {
	if q == "" {
		return time.Now().UTC().Add(-answeringWindow), nil
	}
	t, err := time.Parse(time.RFC3339Nano, q)
	if err != nil {
		return time.Time{}, fmt.Errorf("since %q is not an RFC3339 instant", q)
	}
	return t.UTC(), nil
}

// handleAnswering serves GET /v1/routing/answering?since=.
func (s *Server) handleAnswering(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, scopeRoutesRead...)
	if !ok {
		return
	}
	since, err := answeringSince(r.URL.Query().Get("since"))
	if err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	// The verdict is the loop's own resolve, so without a loop there is no
	// verdict to report — and a waiting list with no verdicts is the ambiguity
	// this read exists to remove. 503, not a partial answer.
	if s.loop == nil {
		writeErr(w, 503, "unavailable", "routing loop not running")
		return
	}
	turns, err := s.db.TurnsSince(since)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	out := apiv1.AnsweringResponse{
		Since:   since.Format(time.RFC3339Nano),
		Waiting: []apiv1.WaitingChat{},
	}
	for _, t := range turns {
		if !visibleTo(folder, t.Folder) {
			continue
		}
		countTurn(&out.Turns, t.State)
	}
	chats, err := s.loop.WaitingChats(since)
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	now := time.Now().UTC()
	for _, c := range chats {
		if !visibleTo(folder, c.Folder) {
			continue
		}
		row := apiv1.WaitingChat{
			ChatJID: c.ChatJID, Platform: core.JidPlatform(c.ChatJID),
			ChatName: c.ChatName, Folder: c.Folder, Verdict: c.Verdict,
			LastSender:  c.LastSender,
			LastHumanAt: c.LastHuman.UTC().Format(time.RFC3339Nano),
			WaitingSec:  int(now.Sub(c.LastHuman).Seconds()),
		}
		if !c.LastReply.IsZero() {
			row.LastReplyAt = c.LastReply.UTC().Format(time.RFC3339Nano)
		}
		out.Waiting = append(out.Waiting, row)
	}
	writeJSON(w, 200, out)
}
