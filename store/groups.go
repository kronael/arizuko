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

	_, err := s.db.Exec(
		`INSERT INTO registered_groups
		 (jid, name, folder, trigger_word, added_at, container_config,
		  requires_trigger, slink_token, parent)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(jid) DO UPDATE SET
		   name=excluded.name, folder=excluded.folder,
		   trigger_word=excluded.trigger_word,
		   container_config=excluded.container_config,
		   requires_trigger=excluded.requires_trigger,
		   slink_token=excluded.slink_token,
		   parent=excluded.parent`,
		jid, g.Name, g.Folder, g.Trigger,
		g.AddedAt.Format(time.RFC3339),
		string(cfgJSON), btoi(g.NeedTrig), g.SlinkToken,
		g.Parent,
	)
	return err
}

func (s *Store) GetGroup(jid string) (core.Group, bool) {
	row := s.db.QueryRow(
		`SELECT jid, name, folder, trigger_word, added_at,
		        container_config, requires_trigger, slink_token, parent
		 FROM registered_groups WHERE jid = ?`, jid)
	g, ok := scanGroup(row)
	return g, ok
}

func (s *Store) DeleteGroup(jid string) error {
	_, err := s.db.Exec(`DELETE FROM registered_groups WHERE jid = ?`, jid)
	return err
}

func (s *Store) AllGroups() map[string]core.Group {
	rows, err := s.db.Query(
		`SELECT jid, name, folder, trigger_word, added_at,
		        container_config, requires_trigger, slink_token, parent
		 FROM registered_groups`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make(map[string]core.Group)
	for rows.Next() {
		g, ok := scanGroup(rows)
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

func scanGroup(r rowScanner) (core.Group, bool) {
	var g core.Group
	var addedAt string
	var cfgJSON, slinkToken, parent *string
	var needTrig int

	err := r.Scan(&g.JID, &g.Name, &g.Folder, &g.Trigger, &addedAt,
		&cfgJSON, &needTrig, &slinkToken, &parent)
	if err != nil {
		return g, false
	}

	g.AddedAt, _ = time.Parse(time.RFC3339, addedAt)
	g.NeedTrig = needTrig != 0
	if slinkToken != nil {
		g.SlinkToken = *slinkToken
	}
	if parent != nil {
		g.Parent = *parent
	}
	if cfgJSON != nil {
		json.Unmarshal([]byte(*cfgJSON), &g.Config)
	}
	return g, true
}
