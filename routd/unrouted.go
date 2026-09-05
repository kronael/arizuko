package routd

// unrouted.go is the held set (spec 6/35): every chat whose latest inbound
// message the loop drops today. Nothing here changes what the loop does with a
// miss — the messages already sit in `messages` with routed_to='' and the
// cursor already moved past them. This file names that set and serves it to
// the operator, who claims a chat with the route write that already exists.

import (
	"net/http"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// UnroutedChat is one held chat: no route claims it, no engagement window keeps
// it, and it addresses no folder directly. Count and the two timestamps cover
// its inbound human rows.
type UnroutedChat struct {
	ChatJID    string
	ChatName   string
	Count      int
	FirstAt    time.Time
	LastAt     time.Time
	LastSender string
}

// UnroutedChats lists the held chats, most recent activity first.
//
// The verdict per chat is the loop's own resolve over the chat's latest
// inbound message — the call pollOnce makes — so this reader cannot disagree
// with the drop it describes: a chat leaves the set the moment a route,
// a direct address or an engagement window claims it. The route table is read
// once up front so a store fault is an error to the caller; resolve treats that
// fault as a miss, which here would render as "everything is unrouted".
func (l *Loop) UnroutedChats() ([]UnroutedChat, error) {
	if _, err := l.db.Routes(); err != nil {
		return nil, err
	}
	candidates, err := l.db.inboundChatSummary()
	if err != nil {
		return nil, err
	}
	var out []UnroutedChat
	for _, c := range candidates {
		last, ok, err := l.db.lastInboundMessage(c.ChatJID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if r := l.resolve(c.ChatJID, last); r.ok || r.Observe != "" {
			continue
		}
		c.ChatName = last.ChatName
		c.LastSender = last.Sender
		out = append(out, c)
	}
	return out, nil
}

// inboundChatSummary aggregates every chat's inbound human rows: count, first
// and last timestamp. Bot rows and the operator's own phone (is_from_me) are
// not traffic that needs a route.
func (d *DB) inboundChatSummary() ([]UnroutedChat, error) {
	rows, err := d.db.Query(`SELECT chat_jid, COUNT(*), MIN(timestamp), MAX(timestamp)
		FROM messages WHERE is_from_me=0 AND is_bot_message=0
		GROUP BY chat_jid ORDER BY MAX(timestamp) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnroutedChat
	for rows.Next() {
		var c UnroutedChat
		var first, last string
		if err := rows.Scan(&c.ChatJID, &c.Count, &first, &last); err != nil {
			return nil, err
		}
		c.FirstAt, _ = time.Parse(time.RFC3339Nano, first)
		c.LastAt, _ = time.Parse(time.RFC3339Nano, last)
		out = append(out, c)
	}
	return out, rows.Err()
}

// lastInboundMessage is the chat's newest inbound human row — the message the
// loop resolved last, and the one resolve is asked about again here.
func (d *DB) lastInboundMessage(chatJID string) (core.Message, bool, error) {
	rows, err := d.db.Query(`SELECT `+msgReadCols+` FROM messages
		WHERE chat_jid=? AND is_from_me=0 AND is_bot_message=0
		ORDER BY timestamp DESC LIMIT 1`, chatJID)
	if err != nil {
		return core.Message{}, false, err
	}
	defer rows.Close()
	msgs, _, err := scanMessages(rows, "")
	if err != nil || len(msgs) == 0 {
		return core.Message{}, false, err
	}
	return msgs[0], true, nil
}

// handleUnroutedChats serves GET /v1/routing/unrouted.
//
// A plain read beside /v1/routing/errored, not a resreg resource, for the
// reason dash_reads_http.go records: it must not become an agent tool. A held
// chat belongs to no folder, so only an unscoped (root / service) bearer may
// list them — a folder-scoped token owns none of these chats and is refused,
// rather than answered with an empty list that reads as "nothing is arriving".
func (s *Server) handleUnroutedChats(w http.ResponseWriter, r *http.Request) {
	_, folder, ok := s.authz(w, r, scopeRoutesRead...)
	if !ok {
		return
	}
	if folder != "" {
		writeErr(w, 403, "forbidden", "unrouted chats belong to no folder")
		return
	}
	if s.loop == nil {
		writeErr(w, 503, "unavailable", "routing loop not running")
		return
	}
	chats, err := s.loop.UnroutedChats()
	if err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	out := apiv1.UnroutedChatsResponse{Chats: make([]apiv1.UnroutedChat, len(chats))}
	for i, c := range chats {
		out.Chats[i] = apiv1.UnroutedChat{
			ChatJID: c.ChatJID, Platform: core.JidPlatform(c.ChatJID), ChatName: c.ChatName,
			Count: c.Count, LastSender: c.LastSender,
			FirstAt: c.FirstAt.UTC().Format(time.RFC3339Nano),
			LastAt:  c.LastAt.UTC().Format(time.RFC3339Nano),
		}
	}
	writeJSON(w, 200, out)
}
