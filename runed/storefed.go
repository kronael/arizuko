package runed

import (
	"context"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	routdv1 "github.com/kronael/arizuko/routd/api/v1"
)

// storeFns builds the agent's read/manage tool surface for one spawn,
// mirroring gated's wireFns (gateway.go § wireFns) but federated for the
// split: routd-owned conversation/routing state over HTTP (stamped with the
// spawn's brokered token), runed-local session history from runed.db, and
// turn context derived from the RunSpec. The funcs left nil are documented
// deferrals (tasks/ACL/identity/audit have no split owner yet — see bugs.md
// "runed StoreFns federation — deferred tools").
func (d *dockerRuntime) storeFns(ctx context.Context, spec RunSpec) ipc.StoreFns {
	c := d.fed.routd
	tok := spec.Token
	return ipc.StoreFns{
		// --- message reads → routd /v1/messages/* ---
		MessagesBefore: func(jid string, before time.Time, limit int) ([]core.Message, error) {
			r, err := c.InspectMessages(ctx, tok, jid, tsArg(before), limit)
			return fromMessageRows(r.Messages), err
		},
		MessagesByThread: func(jid, topic string, before time.Time, limit int) ([]core.Message, error) {
			r, err := c.ThreadMessages(ctx, tok, jid, topic, tsArg(before), limit)
			return fromMessageRows(r.Messages), err
		},
		FindMessages: func(query, scope, sender, since string, limit int) ([]ipc.FoundMessage, error) {
			r, err := c.FindMessages(ctx, tok, query, scope, sender, since, limit)
			if err != nil {
				return nil, err
			}
			out := make([]ipc.FoundMessage, len(r.Messages))
			for i, h := range r.Messages {
				ts, _ := time.Parse(time.RFC3339Nano, h.Timestamp)
				out[i] = ipc.FoundMessage{ChatJID: h.ChatJID, Sender: h.Sender,
					Timestamp: ts, IsFromMe: h.IsFromMe, IsBotMessage: h.IsBotMessage,
					Content: h.Content, Rank: h.Rank}
			}
			return out, nil
		},

		// --- routing resolution → routd /v1/routing/* ---
		DefaultFolderForJID: func(jid string) string {
			r, err := c.ResolveRouting(ctx, tok, jid, "")
			if err != nil {
				return ""
			}
			return r.Folder
		},
		JIDRoutedToFolder: func(jid, folder string) bool {
			r, err := c.ResolveRouting(ctx, tok, jid, folder)
			return err == nil && r.Routed
		},
		ErroredChats: func(folder string, isRoot bool) []ipc.ErroredChat {
			q := folder
			if isRoot {
				q = ""
			}
			r, err := c.ErroredChats(ctx, tok, q)
			if err != nil {
				return nil
			}
			out := make([]ipc.ErroredChat, len(r.Chats))
			for i, e := range r.Chats {
				lastAt, _ := time.Parse(time.RFC3339Nano, e.LastAt)
				out[i] = ipc.ErroredChat{ChatJID: e.ChatJID, Count: e.Count, LastAt: lastAt, RoutedTo: e.RoutedTo}
			}
			return out
		},

		// --- routes CRUD → routd /v1/routes ---
		ListRoutes: func(folder string, isRoot bool) []core.Route {
			rs, err := c.ListRoutes(ctx, tok)
			if err != nil {
				return nil
			}
			out := make([]core.Route, len(rs))
			for i, r := range rs {
				out[i] = routeFromWire(r)
			}
			return out
		},
		GetRoute: func(id int64) (core.Route, bool) {
			r, ok, err := c.GetRoute(ctx, tok, id)
			if err != nil || !ok {
				return core.Route{}, false
			}
			return routeFromWire(r), true
		},
		AddRoute: func(r core.Route) (int64, error) {
			return c.AddRoute(ctx, tok, routeToWire(r))
		},
		SetRoutes: func(folder string, routes []core.Route) error {
			out := make([]routdv1.Route, len(routes))
			for i, r := range routes {
				out[i] = routeToWire(r)
			}
			return c.SetRoutes(ctx, tok, out)
		},
		DeleteRoute: func(id int64) error {
			return c.DeleteRoute(ctx, tok, id)
		},

		// --- web routes → routd /v1/web_routes ---
		ListWebRoutes: func(folder string) []ipc.WebRoute {
			rs, err := c.ListWebRoutes(ctx, tok, folder)
			if err != nil {
				return nil
			}
			out := make([]ipc.WebRoute, len(rs))
			for i, r := range rs {
				out[i] = ipc.WebRoute{PathPrefix: r.PathPrefix, Access: r.Access,
					RedirectTo: r.RedirectTo, Folder: r.Folder, CreatedAt: r.CreatedAt}
			}
			return out
		},
		WebRouteOwner: func(pathPrefix string) (string, bool) {
			owner, err := c.WebRouteOwner(ctx, tok, pathPrefix)
			return owner, err == nil && owner != ""
		},
		SetWebRoute: func(pathPrefix, access, redirectTo, folder string) error {
			return c.PutWebRoute(ctx, tok, routdv1.WebRoute{PathPrefix: pathPrefix,
				Access: access, RedirectTo: redirectTo, Folder: folder})
		},
		DelWebRoute: func(pathPrefix, folder string) (bool, error) {
			return c.DeleteWebRoute(ctx, tok, routdv1.WebRoute{PathPrefix: pathPrefix, Folder: folder})
		},

		// --- engagement → routd /v1/engagement ---
		EngagedFolder: func(jid, topic string) string {
			r, err := c.GetEngagement(ctx, tok, jid, topic)
			if err != nil {
				return ""
			}
			return r.Folder
		},
		SetEngagement: func(jid, topic, folder string, until time.Time) error {
			ttl := 0
			if !until.IsZero() {
				ttl = int(time.Until(until).Seconds())
			}
			return c.SetEngagement(ctx, tok, routdv1.EngagementRequest{
				JID: jid, Topic: topic, Folder: folder, TTLSeconds: ttl})
		},
		GetLastReplyID: func(jid, topic string) string {
			r, err := c.GetEngagement(ctx, tok, jid, topic)
			if err != nil {
				return ""
			}
			return r.LastReplyID
		},

		// --- sessions: runed-local history + routd resume id ---
		RecentSessions: func(folder string, n int) []core.SessionRecord {
			if d.db == nil {
				return nil
			}
			return d.db.RecentSessionRecords(folder, n)
		},
		GetSession: func(folder, topic string) (string, bool) {
			id, err := c.GetSession(ctx, tok, folder, topic)
			return id, err == nil && id != ""
		},

		// --- turn context: from the RunSpec (runed owns the binding) ---
		CurrentTriggerSender: func(string) string { return spec.TriggerSender },
		CurrentTopic:         func(string) string { return spec.Topic },

		// --- external cost → routd /v1/cost ---
		LogExternalCost: func(folder, provider, model string, inputTok, outputTok, costCents int) error {
			return c.LogCost(ctx, tok, routdv1.CostRequest{Folder: folder, Provider: provider,
				Model: model, InputTokens: inputTok, OutputTokens: outputTok, CostCents: costCents})
		},
	}
}

func routeFromWire(r routdv1.Route) core.Route {
	return core.Route{ID: r.ID, Seq: r.Seq, Match: r.Match, Target: r.Target,
		ObserveWindowMessages: r.ObserveWindowMessages, ObserveWindowChars: r.ObserveWindowChars}
}

func routeToWire(r core.Route) routdv1.Route {
	return routdv1.Route{ID: r.ID, Seq: r.Seq, Match: r.Match, Target: r.Target,
		ObserveWindowMessages: r.ObserveWindowMessages, ObserveWindowChars: r.ObserveWindowChars}
}

// tsArg formats a before-cursor for the routd query; zero → "" (server
// defaults to now).
func tsArg(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// fromMessageRows reconstructs core.Message values from the wire rows so the
// agent's router.FormatMessages renders them identically to the in-process
// path.
func fromMessageRows(rows []routdv1.MessageRow) []core.Message {
	out := make([]core.Message, len(rows))
	for i, r := range rows {
		ts, _ := time.Parse(time.RFC3339Nano, r.Timestamp)
		out[i] = core.Message{
			ID: r.ID, ChatJID: r.ChatJID, Sender: r.Sender, Name: r.SenderName,
			Content: r.Content, Timestamp: ts, FromMe: r.IsFromMe, BotMsg: r.IsBotMsg,
			ReplyToID: r.ReplyToID, Topic: r.Topic, RoutedTo: r.RoutedTo, Verb: r.Verb,
			Source: r.Source, Status: r.Status, PlatformID: r.PlatformID,
			ChatName: r.ChatName, ForwardedFrom: r.ForwardedFrom,
		}
	}
	return out
}
