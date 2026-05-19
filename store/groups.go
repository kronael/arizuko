package store

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/router"
)

func (s *Store) CountErroredChats() int {
	var n int
	s.db.QueryRow(`SELECT COUNT(DISTINCT chat_jid) FROM messages WHERE errored = 1`).Scan(&n)
	return n
}

func (s *Store) PutGroup(g core.Group) error {
	cfgJSON, _ := json.Marshal(g.Config)
	product := g.Product
	if product == "" {
		product = core.DefaultProduct
	}
	_, err := s.db.Exec(
		`INSERT INTO groups
		 (folder, added_at, container_config, product, model, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(folder) DO UPDATE SET
		   container_config=excluded.container_config,
		   product=excluded.product,
		   model=excluded.model,
		   updated_at=excluded.updated_at`,
		g.Folder,
		g.AddedAt.Format(time.RFC3339),
		string(cfgJSON),
		product,
		nilIfEmpty(g.Model),
		time.Now().Format(time.RFC3339),
	)
	return err
}

func (s *Store) DeleteGroup(folder string) error {
	_, err := s.db.Exec(`DELETE FROM groups WHERE folder = ?`, folder)
	return err
}

const groupCols = `folder, added_at, container_config, product, model`

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
	var cfgJSON *string
	var model sql.NullString

	if err := r.Scan(&g.Folder, &addedAt, &cfgJSON, &g.Product, &model); err != nil {
		return g, false
	}

	g.AddedAt, _ = time.Parse(time.RFC3339, addedAt)
	if cfgJSON != nil {
		json.Unmarshal([]byte(*cfgJSON), &g.Config)
	}
	g.Model = model.String
	return g, true
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

// IsGroupOpen reports the folder's `open` flag. Missing rows default to
// true so a freshly-created or pre-migration group stays visible to its
// siblings until an operator explicitly closes it. Spec 6/F.
func (s *Store) IsGroupOpen(folder string) bool {
	var open sql.NullInt64
	s.db.QueryRow(`SELECT open FROM groups WHERE folder = ?`, folder).Scan(&open)
	if !open.Valid {
		return true
	}
	return open.Int64 != 0
}

// SetGroupOpen flips the visibility bit. The row must exist (groups are
// created via SetupGroup/PutGroup before any caller can flip the flag).
func (s *Store) SetGroupOpen(folder string, open bool) error {
	_, err := s.db.Exec(`UPDATE groups SET open = ? WHERE folder = ?`,
		btoi(open), folder)
	return err
}

// GroupObserveWindow returns the per-group observe-window caps. (-1,-1)
// means "not set; fall back to env defaults". Per-route caps still win
// over per-group; per-group still wins over env. Spec 6/F.
func (s *Store) GroupObserveWindow(folder string) (msgs, chars int) {
	var m, c sql.NullInt64
	s.db.QueryRow(
		`SELECT observe_window_messages, observe_window_chars FROM groups WHERE folder = ?`,
		folder,
	).Scan(&m, &c)
	msgs = -1
	chars = -1
	if m.Valid {
		msgs = int(m.Int64)
	}
	if c.Valid {
		chars = int(c.Int64)
	}
	return
}

// SetGroupModel persists the per-group model override. Empty string clears it.
func (s *Store) SetGroupModel(folder, model string) error {
	_, err := s.db.Exec(`UPDATE groups SET model = ? WHERE folder = ?`,
		nilIfEmpty(model), folder)
	return err
}

// SetGroupObserveWindow writes per-group caps; pass -1 to clear (the
// gateway then falls back to the env default for that field).
func (s *Store) SetGroupObserveWindow(folder string, msgs, chars int) error {
	var mv, cv any
	if msgs >= 0 {
		mv = msgs
	}
	if chars >= 0 {
		cv = chars
	}
	_, err := s.db.Exec(
		`UPDATE groups
		   SET observe_window_messages = ?, observe_window_chars = ?
		 WHERE folder = ?`,
		mv, cv, folder,
	)
	return err
}

// SiblingFolders returns folders that share folder's immediate parent,
// excluding folder itself and any closed sibling. Root folders (no
// parent) have no siblings — returns nil. Spec 6/F ambient join.
func (s *Store) SiblingFolders(folder string) []string {
	parent := groupfolder.ParentOf(folder)
	if parent == "" {
		return nil
	}
	rows, err := s.db.Query(
		`SELECT folder FROM groups
		 WHERE folder LIKE ? AND folder != ? AND open = 1`,
		parent+"/%", folder,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var f string
		if rows.Scan(&f) != nil {
			continue
		}
		// Direct children only — exclude grandchildren like "parent/x/y".
		if groupfolder.ParentOf(f) != parent {
			continue
		}
		out = append(out, f)
	}
	return out
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
