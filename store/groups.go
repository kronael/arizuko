package store

import (
	"encoding/json"
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

func (s *Store) PutGroup(jid string, g core.Group) error {
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
		`INSERT INTO registered_groups
		 (jid, name, folder, added_at, container_config, slink_token, parent,
		  state, spawn_ttl_days, archive_closed_days, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(jid) DO UPDATE SET
		   name=excluded.name, folder=excluded.folder,
		   container_config=excluded.container_config,
		   slink_token=excluded.slink_token,
		   parent=excluded.parent,
		   state=excluded.state,
		   spawn_ttl_days=excluded.spawn_ttl_days,
		   archive_closed_days=excluded.archive_closed_days,
		   updated_at=excluded.updated_at`,
		jid, g.Name, g.Folder,
		g.AddedAt.Format(time.RFC3339),
		string(cfgJSON), g.SlinkToken,
		g.Parent,
		state, ttl, archiveDays,
		time.Now().Format(time.RFC3339),
	)
	return err
}

func (s *Store) MarkGroupClosed(folder string) error {
	_, err := s.db.Exec(
		`UPDATE registered_groups SET state='closed', updated_at=? WHERE folder=?`,
		time.Now().Format(time.RFC3339), folder,
	)
	return err
}

func (s *Store) GroupsToArchive(minClosedAge time.Duration) []core.Group {
	cutoff := time.Now().Add(-minClosedAge).Format(time.RFC3339)
	rows, err := s.db.Query(
		`SELECT jid, name, folder, added_at, container_config, slink_token, parent,
		        state, spawn_ttl_days, archive_closed_days
		 FROM registered_groups
		 WHERE state='closed' AND updated_at < ?`,
		cutoff,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []core.Group
	for rows.Next() {
		g, ok := scanGroupFull(rows)
		if ok {
			out = append(out, g)
		}
	}
	return out
}

func (s *Store) DeleteGroup(jid string) error {
	_, err := s.db.Exec(`DELETE FROM registered_groups WHERE jid = ?`, jid)
	return err
}

func (s *Store) AllGroups() map[string]core.Group {
	rows, err := s.db.Query(
		`SELECT jid, name, folder, added_at, container_config, slink_token, parent,
		        state, spawn_ttl_days, archive_closed_days
		 FROM registered_groups`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make(map[string]core.Group)
	for rows.Next() {
		g, ok := scanGroupFull(rows)
		if ok {
			out[g.JID] = g
		}
	}
	return out
}

func (s *Store) GetAgentCursor(jid string) time.Time {
	var raw *string
	s.db.QueryRow(
		`SELECT agent_cursor FROM registered_groups WHERE jid = ?`, jid,
	).Scan(&raw)
	if raw == nil || *raw == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, *raw)
	return t
}

func (s *Store) SetAgentCursor(jid string, ts time.Time) {
	s.db.Exec(
		`UPDATE registered_groups SET agent_cursor = ? WHERE jid = ?`,
		ts.Format(time.RFC3339Nano), jid,
	)
}

func (s *Store) AllAgentCursors() map[string]time.Time {
	rows, err := s.db.Query(
		`SELECT jid, agent_cursor FROM registered_groups WHERE agent_cursor IS NOT NULL`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make(map[string]time.Time)
	for rows.Next() {
		var jid, raw string
		rows.Scan(&jid, &raw)
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			out[jid] = t
		}
	}
	return out
}

// UnroutedChatJIDs returns distinct chat JIDs that have messages since `since`
// but have no entry in the routes table. Used by gated when onboarding is enabled
// to surface new users to the onboarding handler.
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

func derefStr(p *string) string {
	if p != nil {
		return *p
	}
	return ""
}

func derefInt(p *int) int {
	if p != nil {
		return *p
	}
	return 0
}

func scanGroupFull(r rowScanner) (core.Group, bool) {
	var g core.Group
	var addedAt string
	var cfgJSON, slinkToken, parent, state *string
	var spawnTTL, archiveDays *int

	if err := r.Scan(&g.JID, &g.Name, &g.Folder, &addedAt, &cfgJSON, &slinkToken, &parent,
		&state, &spawnTTL, &archiveDays); err != nil {
		return g, false
	}

	g.AddedAt, _ = time.Parse(time.RFC3339, addedAt)
	g.SlinkToken = derefStr(slinkToken)
	g.Parent = derefStr(parent)
	g.SpawnTTLDays = derefInt(spawnTTL)
	g.ArchiveClosedDays = derefInt(archiveDays)
	if cfgJSON != nil {
		json.Unmarshal([]byte(*cfgJSON), &g.Config)
	}
	g.State = derefStr(state)
	if g.State == "" {
		g.State = "active"
	}
	return g, true
}

// SetStickyGroup sets the sticky routing group for a chat (empty string clears).
func (s *Store) SetStickyGroup(jid, folder string) error {
	_, err := s.db.Exec(
		`INSERT INTO chats (jid, sticky_group) VALUES (?, ?)
		 ON CONFLICT(jid) DO UPDATE SET sticky_group = excluded.sticky_group`,
		jid, nilIfEmpty(folder),
	)
	return err
}

// GetStickyGroup returns the sticky routing group for a chat, or empty string.
func (s *Store) GetStickyGroup(jid string) string {
	var folder *string
	s.db.QueryRow(`SELECT sticky_group FROM chats WHERE jid = ?`, jid).Scan(&folder)
	if folder == nil {
		return ""
	}
	return *folder
}

// SetStickyTopic sets the sticky topic for a chat (empty string clears).
func (s *Store) SetStickyTopic(jid, topic string) error {
	_, err := s.db.Exec(
		`INSERT INTO chats (jid, sticky_topic) VALUES (?, ?)
		 ON CONFLICT(jid) DO UPDATE SET sticky_topic = excluded.sticky_topic`,
		jid, nilIfEmpty(topic),
	)
	return err
}

// GetStickyTopic returns the sticky topic for a chat, or empty string.
func (s *Store) GetStickyTopic(jid string) string {
	var topic *string
	s.db.QueryRow(`SELECT sticky_topic FROM chats WHERE jid = ?`, jid).Scan(&topic)
	if topic == nil {
		return ""
	}
	return *topic
}
