package routd

import (
	"database/sql"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/router"
)

// reads.go holds the routd.DB query methods backing the agent's read/manage
// MCP tools (federated from runed via the new /v1/messages, /v1/routing,
// /v1/engagement, /v1/cost surfaces). These mirror the equivalent gated
// store methods (store/messages.go, store/groups.go, store/inspect.go)
// against routd.db — routd owns the conversation/routing state post-split.

const msgReadCols = `id, chat_jid, sender, sender_name, content, timestamp, is_from_me,
	is_bot_message, reply_to_id, topic, routed_to, verb, source, turn_id, status,
	platform_id, chat_name, forwarded_from`

// MessagesBefore returns rows for one chat_jid older than `before`, oldest
// first (inspect_messages / get_history). before="" → now.
func (d *DB) MessagesBefore(jid, before string, limit int) ([]core.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if before == "" {
		before = time.Now().UTC().Format(time.RFC3339Nano)
	}
	rows, err := d.db.Query(`SELECT `+msgReadCols+` FROM messages
		WHERE chat_jid=? AND timestamp < ? ORDER BY timestamp DESC LIMIT ?`,
		jid, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, _, err := scanMessages(rows, "")
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// MessagesByThread returns rows for one (chat_jid, topic), newest first
// (get_thread). before="" → now.
func (d *DB) MessagesByThread(jid, topic, before string, limit int) ([]core.Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if before == "" {
		before = time.Now().UTC().Format(time.RFC3339Nano)
	}
	rows, err := d.db.Query(`SELECT `+msgReadCols+` FROM messages
		WHERE chat_jid=? AND topic=? AND timestamp < ? ORDER BY timestamp DESC LIMIT ?`,
		jid, topic, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, _, err := scanMessages(rows, "")
	return msgs, err
}

// FoundMessage is one find_messages hit. Content is the FTS5 snippet
// (matched fragment with «»-highlight); Rank is BM25 (lower is better).
type FoundMessage struct {
	ChatJID      string    `json:"chat_jid"`
	Sender       string    `json:"sender"`
	Timestamp    time.Time `json:"timestamp"`
	IsFromMe     bool      `json:"is_from_me"`
	IsBotMessage bool      `json:"is_bot_message"`
	Content      string    `json:"content"`
	Rank         float64   `json:"rank"`
}

// FindMessages runs an FTS5 MATCH over messages_fts (mirrors
// store.FindMessages). `scope` is polymorphic: contains ':' → chat_jid,
// else a `routed_to` folder subtree. sender is exact; since is an RFC3339
// lower bound. ACL filtering is the caller's job.
func (d *DB) FindMessages(query, scope, sender, since string, limit int) ([]FoundMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var scopeJID, scopeFolder any
	if scope != "" {
		if strings.Contains(scope, ":") {
			scopeJID = scope
		} else {
			scopeFolder = scope
		}
	}
	var senderArg, sinceArg any
	if sender != "" {
		senderArg = sender
	}
	if since != "" {
		sinceArg = since
	}
	rows, err := d.db.Query(
		`SELECT m.chat_jid, m.sender, m.timestamp, m.is_from_me, m.is_bot_message,
		        snippet(messages_fts, 0, '«', '»', '…', 32) AS content,
		        bm25(messages_fts) AS rank
		 FROM messages_fts f JOIN messages m ON m.rowid = f.rowid
		 WHERE messages_fts MATCH ?
		   AND (? IS NULL OR m.chat_jid = ?)
		   AND (? IS NULL OR m.routed_to = ? OR m.routed_to LIKE ? || '/%')
		   AND (? IS NULL OR m.sender = ?)
		   AND (? IS NULL OR m.timestamp >= ?)
		 ORDER BY rank, m.timestamp DESC LIMIT ?`,
		query, scopeJID, scopeJID, scopeFolder, scopeFolder, scopeFolder,
		senderArg, senderArg, sinceArg, sinceArg, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FoundMessage
	for rows.Next() {
		var fm FoundMessage
		var ts string
		var fromMe, botMsg int
		if err := rows.Scan(&fm.ChatJID, &fm.Sender, &ts, &fromMe, &botMsg, &fm.Content, &fm.Rank); err != nil {
			return out, err
		}
		fm.IsFromMe = fromMe == 1
		fm.IsBotMessage = botMsg == 1
		fm.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, fm)
	}
	return out, rows.Err()
}

// --- routing resolution ---

// DefaultFolderForJID resolves the routing target folder for jid against the
// current route table (mirrors store.DefaultFolderForJID). "" when no route
// matches.
func (d *DB) DefaultFolderForJID(jid string) string {
	routes, err := d.Routes()
	if err != nil {
		return ""
	}
	t := router.ResolveRoute(core.Message{ChatJID: jid, Verb: "message"}, routes)
	return core.ParseRouteTarget(t).Folder
}

// JIDRoutedToFolder reports whether jid's default route target is folder or a
// descendant of it (mirrors store.JIDRoutedToFolder).
func (d *DB) JIDRoutedToFolder(jid, folder string) bool {
	target := d.DefaultFolderForJID(jid)
	if target == "" {
		return false
	}
	return target == folder || strings.HasPrefix(target, folder+"/")
}

// JIDRoutableToFolder reports whether folder (or a descendant) is the target of
// any route matching jid when the verb predicate is ignored (mirrors
// store.JIDRoutableToFolder). Lets a mention-only sub-folder agent reply.
func (d *DB) JIDRoutableToFolder(jid, folder string) bool {
	routes, err := d.Routes()
	if err != nil {
		return false
	}
	msg := core.Message{ChatJID: jid}
	for _, r := range routes {
		if !router.RouteMatchesIgnoreVerb(r, msg) {
			continue
		}
		t := core.ParseRouteTarget(r.Target).Folder
		if t == folder || strings.HasPrefix(t, folder+"/") {
			return true
		}
	}
	return false
}

// GetRoute returns one route by id; ok=false when absent.
// GetRoute does a point lookup by id. The error is sql.ErrNoRows when the route
// doesn't exist (caller → 404) vs a real store error (caller → 500) — callers
// MUST distinguish them with errors.Is so a DB fault isn't reported as not-found.
func (d *DB) GetRoute(id int64) (core.Route, error) {
	var r core.Route
	err := d.db.QueryRow(`SELECT id, seq, match, target,
		COALESCE(observe_window_messages,0), COALESCE(observe_window_chars,0)
		FROM routes WHERE id=?`, id).Scan(&r.ID, &r.Seq, &r.Match, &r.Target,
		&r.ObserveWindowMessages, &r.ObserveWindowChars)
	if err != nil {
		return core.Route{}, err
	}
	return r, nil
}

// WebRouteOwner reports which folder owns an exact path_prefix row, if any
// (mirrors store.WebRouteOwner; backs set_web_route first-claim).
func (d *DB) WebRouteOwner(pathPrefix string) (string, bool) {
	var folder string
	err := d.db.QueryRow("SELECT folder FROM web_routes WHERE path_prefix=?", pathPrefix).Scan(&folder)
	if err != nil {
		return "", false
	}
	return folder, true
}

// --- errored chats ---

// ErroredChat is one chat in the errored set with its inbound-since-error
// count and resolved routing target (mirrors store.ErroredChat).
type ErroredChat struct {
	ChatJID  string    `json:"chat_jid"`
	Count    int       `json:"count"`
	LastAt   time.Time `json:"last_at"`
	RoutedTo string    `json:"routed_to"`
}

// ErroredChats lists chats flagged errored=1 whose resolved folder is owned
// by `folder` (isRoot sees all). Mirrors store.ErroredChats but scopes by the
// live route resolution since routd has no per-chat folder column.
func (d *DB) ErroredChats(folder string, isRoot bool) ([]ErroredChat, error) {
	rows, err := d.db.Query(`SELECT c.jid,
		(SELECT COUNT(*) FROM messages m WHERE m.chat_jid=c.jid AND m.is_from_me=0),
		(SELECT MAX(m.timestamp) FROM messages m WHERE m.chat_jid=c.jid)
		FROM chats c WHERE c.errored=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ErroredChat
	for rows.Next() {
		var ec ErroredChat
		var lastAt sql.NullString
		if err := rows.Scan(&ec.ChatJID, &ec.Count, &lastAt); err != nil {
			return out, err
		}
		ec.RoutedTo = d.DefaultFolderForJID(ec.ChatJID)
		if !isRoot && ec.RoutedTo != folder && !strings.HasPrefix(ec.RoutedTo, folder+"/") {
			continue
		}
		if lastAt.Valid {
			ec.LastAt, _ = time.Parse(time.RFC3339Nano, lastAt.String)
		}
		out = append(out, ec)
	}
	return out, rows.Err()
}

// RouteSourceJIDsInWorld returns the distinct source jids of every route whose
// target folder is worldFolder or under it (mirrors store.RouteSourceJIDsInWorld
// against routd's routes table). grants.DeriveRules uses this to scope tier-1/2
// platform rules to the platforms actually routed into the world.
func (d *DB) RouteSourceJIDsInWorld(worldFolder string) []string {
	routes, err := d.Routes()
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, r := range routes {
		f := core.ParseRouteTarget(r.Target).Folder
		if f != worldFolder && !strings.HasPrefix(f, worldFolder+"/") {
			continue
		}
		for _, jid := range routeSourceJIDs(r.Match) {
			if _, dup := seen[jid]; dup {
				continue
			}
			seen[jid] = struct{}{}
			out = append(out, jid)
		}
	}
	return out
}

// routeSourceJIDs extracts the concrete source jids a route match selects
// (platform:room or chat_jid), skipping glob/empty values. Mirrors store's
// routeSourceJIDs.
func routeSourceJIDs(match string) []string {
	var platform string
	var rooms []string
	for _, tok := range strings.Fields(match) {
		k, v, ok := strings.Cut(tok, "=")
		if !ok || v == "" || strings.ContainsAny(v, "*?[") {
			continue
		}
		switch k {
		case "platform":
			platform = v
		case "room":
			rooms = append(rooms, v)
		case "chat_jid":
			rooms = append(rooms, v)
			return rooms
		}
	}
	if platform == "" {
		return rooms
	}
	out := make([]string, len(rooms))
	for i, r := range rooms {
		out[i] = platform + ":" + r
	}
	return out
}

// --- external cost ---

// LogExternalCost records one cost_log row for a non-Anthropic LLM call
// (oracle/codex/openai). The model column carries provider:model; turn_id is
// a fresh ext-<rand> so each call is its own row (cost_log's PK is
// (folder,turn_id,model), and PutCost is INSERT OR IGNORE). Mirrors gated's
// logCost external path.
func (d *DB) LogExternalCost(folder, provider, model string, inputTok, outputTok, costCents int) error {
	return d.PutCost(folder, "ext-"+randHex(8), provider+":"+model, inputTok, outputTok, costCents)
}
