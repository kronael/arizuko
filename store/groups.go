package store

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/router"
)

func (s *Store) CountErroredChats() int {
	var n int
	s.db.QueryRow(`SELECT COUNT(DISTINCT chat_jid) FROM messages WHERE errored = 1`).Scan(&n)
	return n
}

func (s *Store) PutGroup(g core.Group) error {
	if g.SlinkToken == "" {
		g.SlinkToken = core.GenSlinkToken()
	}
	cfgJSON, _ := json.Marshal(g.Config)
	product := g.Product
	if product == "" {
		product = core.DefaultProduct
	}
	_, err := s.db.Exec(
		`INSERT INTO groups
		 (folder, added_at, container_config, slink_token, product, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(folder) DO UPDATE SET
		   container_config=excluded.container_config,
		   slink_token=excluded.slink_token,
		   product=excluded.product,
		   updated_at=excluded.updated_at`,
		g.Folder,
		g.AddedAt.Format(time.RFC3339),
		string(cfgJSON), g.SlinkToken,
		product,
		time.Now().Format(time.RFC3339),
	)
	return err
}

func (s *Store) DeleteGroup(folder string) error {
	_, err := s.db.Exec(`DELETE FROM groups WHERE folder = ?`, folder)
	return err
}

const groupCols = `folder, added_at, container_config, slink_token, product`

func (s *Store) AllGroups() map[string]core.Group {
	rows, err := s.db.Query(`SELECT ` + groupCols + ` FROM groups`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make(map[string]core.Group)
	for rows.Next() {
		g, ok := scanGroupFull(rows)
		if ok {
			out[g.Folder] = g
		}
	}
	return out
}

func (s *Store) GetAgentCursor(jid string) time.Time {
	var raw *string
	s.db.QueryRow(`SELECT agent_cursor FROM chats WHERE jid = ?`, jid).Scan(&raw)
	if raw == nil || *raw == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, *raw)
	return t
}

func (s *Store) SetAgentCursor(jid string, ts time.Time) {
	res, err := s.db.Exec(
		`INSERT INTO chats (jid, agent_cursor) VALUES (?, ?)
		 ON CONFLICT(jid) DO UPDATE SET agent_cursor = excluded.agent_cursor`,
		jid, ts.Format(time.RFC3339Nano),
	)
	if err != nil {
		slog.Error("SetAgentCursor failed", "jid", jid, "ts", ts, "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		slog.Warn("SetAgentCursor matched no rows", "jid", jid, "ts", ts)
	}
}

func (s *Store) PendingChatJIDs(botName string) []string {
	rows, err := s.db.Query(
		`SELECT DISTINCT m.chat_jid FROM messages m
		 LEFT JOIN chats c ON m.chat_jid = c.jid
		 WHERE m.is_bot_message = 0
		   AND m.sender NOT LIKE ?
		   AND m.timestamp > COALESCE(c.agent_cursor, '')`,
		botName+"%",
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var jids []string
	for rows.Next() {
		var jid string
		if rows.Scan(&jid) == nil {
			jids = append(jids, jid)
		}
	}
	return jids
}

// routeSourceJIDs reconstructs "platform:room" JIDs from a route's match.
// Glob values are skipped. Missing platform → room literal alone.
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

func (s *Store) RouteSourceJIDsInWorld(worldFolder string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, r := range s.AllRoutes() {
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

func (s *Store) DefaultFolderForJID(jid string) string {
	msg := core.Message{ChatJID: jid, Verb: "message"}
	t := router.ResolveRoute(msg, s.AllRoutes())
	return core.ParseRouteTarget(t).Folder
}

func scanGroupFull(r rowScanner) (core.Group, bool) {
	var g core.Group
	var addedAt string
	var cfgJSON, slinkToken *string

	if err := r.Scan(&g.Folder, &addedAt, &cfgJSON, &slinkToken, &g.Product); err != nil {
		return g, false
	}

	g.AddedAt, _ = time.Parse(time.RFC3339, addedAt)
	if slinkToken != nil {
		g.SlinkToken = *slinkToken
	}
	if cfgJSON != nil {
		json.Unmarshal([]byte(*cfgJSON), &g.Config)
	}
	return g, true
}

func (s *Store) GroupBySlinkToken(token string) (core.Group, bool) {
	row := s.db.QueryRow(`SELECT `+groupCols+` FROM groups WHERE slink_token = ? LIMIT 1`, token)
	return scanGroupFull(row)
}

func (s *Store) GroupByFolder(folder string) (core.Group, bool) {
	row := s.db.QueryRow(`SELECT `+groupCols+` FROM groups WHERE folder = ?`, folder)
	return scanGroupFull(row)
}

// SetChatIsGroup upserts the is_group classification for a chat_jid.
// The WHERE on the UPDATE branch suppresses no-op writes once stable.
func (s *Store) SetChatIsGroup(jid string, isGroup bool) error {
	_, err := s.db.Exec(
		`INSERT INTO chats (jid, is_group) VALUES (?, ?)
		 ON CONFLICT(jid) DO UPDATE SET is_group = excluded.is_group
		 WHERE chats.is_group != excluded.is_group`,
		jid, btoi(isGroup),
	)
	return err
}

func (s *Store) GetChatIsGroup(jid string) bool {
	var n int
	s.db.QueryRow(`SELECT is_group FROM chats WHERE jid = ?`, jid).Scan(&n)
	return n != 0
}

func (s *Store) SetStickyGroup(jid, folder string) error {
	_, err := s.db.Exec(
		`INSERT INTO chats (jid, sticky_group) VALUES (?, ?)
		 ON CONFLICT(jid) DO UPDATE SET sticky_group = excluded.sticky_group`,
		jid, nilIfEmpty(folder),
	)
	return err
}

func (s *Store) GetStickyGroup(jid string) string {
	var folder sql.NullString
	s.db.QueryRow(`SELECT sticky_group FROM chats WHERE jid = ?`, jid).Scan(&folder)
	return folder.String
}

func (s *Store) SetStickyTopic(jid, topic string) error {
	_, err := s.db.Exec(
		`INSERT INTO chats (jid, sticky_topic) VALUES (?, ?)
		 ON CONFLICT(jid) DO UPDATE SET sticky_topic = excluded.sticky_topic`,
		jid, nilIfEmpty(topic),
	)
	return err
}

func (s *Store) GetStickyTopic(jid string) string {
	var topic sql.NullString
	s.db.QueryRow(`SELECT sticky_topic FROM chats WHERE jid = ?`, jid).Scan(&topic)
	return topic.String
}
