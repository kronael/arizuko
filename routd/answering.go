package routd

// answering.go answers one operator question: are agents ANSWERING people?
// `turns` is what the dispatcher did, `waiting` is what the people got, and
// neither implies the other. A PLAIN /v1 READ, NOT A RESREG RESOURCE: a
// resource would hand every agent a cross-folder tool; it is an AGGREGATE,
// never message content. A reply is a bot row routd stored, not a message a
// person received — a fleet whose adapter cannot send looks answered here;
// that half is `/v1/routing/errored` and the dead-letter surface. The verdict
// is routd's own resolve, the same call the loop makes, so this reader cannot
// disagree with the behaviour it describes.

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// answeringWindow is the default window: the week the eval's check 21 asks by
// hand. The response echoes it.
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
// A chat qualifies on the OUTCOME, not the routing: newest human message in
// the window and no newer agent reply — wider than the held set (unrouted.go),
// which lists only chats the loop drops; a route that simply stopped answering
// is exactly the failure this read is for. The route table is read up front so
// a store fault reaches the caller: resolve treats it as a miss, which would
// label every chat `held`. It walks the chat list, and the second read is
// bounded by the window; an operator's read, not a hot path.
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
		// The window is applied to the QUESTION.
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

// countTurn folds one turn's state into the window's counts; anything the
// four names miss lands in Other, so they always sum.
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

// visibleTo reports whether a bearer claiming tokenFolder may see a row of
// rowFolder. descendant(), NOT ownsFolder(): ownsFolder counts an EMPTY target
// as owned by anyone, and a held chat's folder is empty, so it would hand every
// scoped bearer every unclaimed chat. An empty claim sees everything.
func visibleTo(tokenFolder, rowFolder string) bool {
	if tokenFolder == "" {
		return true
	}
	return rowFolder != "" && descendant(rowFolder, tokenFolder)
}

// answeringSince reads the window bound: absent is the default week, and an
// unparseable one is refused — a window nobody chose would report the wrong
// period under a timestamp that looks deliberate.
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
	// No loop, no verdict: 503, not a partial answer.
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
