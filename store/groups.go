package store

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/onvos/arizuko/core"
)

func (s *Store) PutChat(jid, name, ch string, group bool) error {
	_, err := s.db.Exec(
		`INSERT INTO chats (jid, name, channel, is_group, last_message_time)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(jid) DO UPDATE SET
		   name = COALESCE(excluded.name, chats.name),
		   channel = COALESCE(excluded.channel, chats.channel),
		   is_group = excluded.is_group,
		   last_message_time = excluded.last_message_time`,
		jid, name, ch, btoi(group), time.Now().Format(time.RFC3339),
	)
	return err
}

func (s *Store) MarkChatErrored(jid string) error {
	_, err := s.db.Exec(`UPDATE chats SET errored = 1 WHERE jid = ?`, jid)
	return err
}

func (s *Store) ClearChatErrored(jid string) error {
	_, err := s.db.Exec(`UPDATE chats SET errored = 0 WHERE jid = ?`, jid)
	return err
}

func (s *Store) IsChatErrored(jid string) bool {
	var errored int
	s.db.QueryRow(`SELECT errored FROM chats WHERE jid = ?`, jid).Scan(&errored)
	return errored != 0
}

func (s *Store) CountErroredChats() int {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM chats WHERE errored = 1`).Scan(&n)
	return n
}

func (s *Store) PutGroup(g core.Group) error {
	cfgJSON, _ := json.Marshal(g.Config)

	state := g.State
	if state == "" {
		state = "active"
	}
	ttl := g.SpawnTTLDays
	if ttl == 0 {
		ttl = 7
	}
	archiveDays := g.ArchiveClosedDays
	if archiveDays == 0 {
		archiveDays = 1
	}

	_, err := s.db.Exec(
		`INSERT INTO groups
		 (folder, name, added_at, container_config, slink_token, parent,
		  state, spawn_ttl_days, archive_closed_days, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(folder) DO UPDATE SET
		   name=excluded.name,
		   container_config=excluded.container_config,
		   slink_token=excluded.slink_token,
		   parent=excluded.parent,
		   state=excluded.state,
		   spawn_ttl_days=excluded.spawn_ttl_days,
		   archive_closed_days=excluded.archive_closed_days,
		   updated_at=excluded.updated_at`,
		g.Folder, g.Name,
		g.AddedAt.Format(time.RFC3339),
		string(cfgJSON), g.SlinkToken,
		g.Parent,
		state, ttl, archiveDays,
		time.Now().Format(time.RFC3339),
	)
	return err
}

func (s *Store) DeleteGroup(folder string) error {
	_, err := s.db.Exec(`DELETE FROM groups WHERE folder = ?`, folder)
	return err
}

const groupCols = `folder, name, added_at, container_config, slink_token, parent,
		        state, spawn_ttl_days, archive_closed_days`

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
	s.db.QueryRow(
		`SELECT agent_cursor FROM chats WHERE jid = ?`, jid,
	).Scan(&raw)
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

func (s *Store) UnroutedChatJIDs(since time.Time) []string {
	rows, err := s.db.Query(
		`SELECT DISTINCT chat_jid FROM messages
		 WHERE timestamp > ?
		   AND is_bot_message = 0
		   AND chat_jid NOT IN (SELECT jid FROM routes)`,
		since.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var jids []string
	for rows.Next() {
		var jid string
		rows.Scan(&jid)
		jids = append(jids, jid)
	}
	return jids
}

func (s *Store) JIDFolderMap() map[string]string {
	rows, err := s.db.Query(
		`SELECT jid, target FROM routes WHERE type = 'default' AND (match IS NULL OR match = '')`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var jid, target string
		rows.Scan(&jid, &target)
		out[jid] = target
	}
	return out
}

func scanGroupFull(r rowScanner) (core.Group, bool) {
	var g core.Group
	var addedAt string
	var cfgJSON, slinkToken, parent, state *string
	var spawnTTL, archiveDays *int

	if err := r.Scan(&g.Folder, &g.Name, &addedAt, &cfgJSON, &slinkToken, &parent,
		&state, &spawnTTL, &archiveDays); err != nil {
		return g, false
	}

	g.AddedAt, _ = time.Parse(time.RFC3339, addedAt)
	if slinkToken != nil {
		g.SlinkToken = *slinkToken
	}
	if parent != nil {
		g.Parent = *parent
	}
	if spawnTTL != nil {
		g.SpawnTTLDays = *spawnTTL
	}
	if archiveDays != nil {
		g.ArchiveClosedDays = *archiveDays
	}
	if cfgJSON != nil {
		json.Unmarshal([]byte(*cfgJSON), &g.Config)
	}
	g.State = "active"
	if state != nil && *state != "" {
		g.State = *state
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

func (s *Store) SetStickyGroup(jid, folder string) error {
	_, err := s.db.Exec(
		`INSERT INTO chats (jid, sticky_group) VALUES (?, ?)
		 ON CONFLICT(jid) DO UPDATE SET sticky_group = excluded.sticky_group`,
		jid, nilIfEmpty(folder),
	)
	return err
}

func (s *Store) GetStickyGroup(jid string) string {
	var folder *string
	s.db.QueryRow(`SELECT sticky_group FROM chats WHERE jid = ?`, jid).Scan(&folder)
	if folder == nil {
		return ""
	}
	return *folder
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
	var topic *string
	s.db.QueryRow(`SELECT sticky_topic FROM chats WHERE jid = ?`, jid).Scan(&topic)
	if topic == nil {
		return ""
	}
	return *topic
}
