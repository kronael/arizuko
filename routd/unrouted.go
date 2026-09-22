package routd

// unrouted.go is the held set (spec 6/35): every chat whose latest inbound
// message the loop drops today. Nothing here changes what the loop does with a
// miss; this file names that set and serves it to the operator, who claims a
// chat with the route write that already exists.

import (
	"context"
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

// UnroutedChats lists the held chats, most recent activity first. The verdict
// is the loop's own resolve over the chat's latest inbound — the call pollOnce
// makes — so this reader cannot disagree with the drop it describes. The route
// table is read up front so a store fault is an error to the caller: resolve
// treats it as a miss, which would render as "everything is unrouted".
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

// inboundChatSummary aggregates every chat's inbound human rows; bot rows and
// the operator's own phone (is_from_me) are not traffic that needs a route.
func (d *DB) inboundChatSummary() ([]UnroutedChat, error) {
	rows, err := d.store().InboundChatSummary(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]UnroutedChat, 0, len(rows))
	for _, r := range rows {
		out = append(out, UnroutedChat{
			ChatJID: r.ChatJID, Count: r.Count, FirstAt: r.FirstAt, LastAt: r.LastAt,
		})
	}
	return out, nil
}

// lastInboundMessage is the chat's newest inbound human row — the message the
// loop resolved last.
func (d *DB) lastInboundMessage(chatJID string) (core.Message, bool, error) {
	return d.store().LastInboundMessage(context.Background(), chatJID)
}

// handleUnroutedChats serves GET /v1/routing/unrouted — a plain read, not a
// resreg resource: it must not become an agent tool. A held chat belongs to no
// folder, so only an unscoped bearer may list them; a folder-scoped token is
// refused rather than answered with an empty list that reads as "nothing is
// arriving".
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
