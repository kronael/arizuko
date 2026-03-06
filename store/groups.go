package store

import (
	"encoding/json"
	"time"

	"github.com/onvos/kanipi/core"
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

func (s *Store) AllChats() []core.ChatInfo {
	rows, err := s.db.Query(
		`SELECT jid, name, channel, is_group, last_message_time
		 FROM chats ORDER BY last_message_time DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []core.ChatInfo
	for rows.Next() {
		var c core.ChatInfo
		var isGroup int
		var name, ch, lastTime *string
		rows.Scan(&c.JID, &name, &ch, &isGroup, &lastTime)
		if name != nil {
			c.Name = *name
		}
		if ch != nil {
			c.Channel = *ch
		}
		if lastTime != nil {
			c.LastTime = *lastTime
		}
		c.IsGroup = isGroup != 0
		out = append(out, c)
	}
	return out
}

func (s *Store) PutGroup(jid string, g core.Group) error {
	cfgJSON, _ := json.Marshal(g.Config)
	rulesJSON, _ := json.Marshal(g.Rules)

	_, err := s.db.Exec(
		`INSERT INTO registered_groups
		 (jid, name, folder, trigger_word, added_at, container_config,
		  requires_trigger, slink_token, parent, routing_rules)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(jid) DO UPDATE SET
		   name=excluded.name, folder=excluded.folder,
		   trigger_word=excluded.trigger_word,
		   container_config=excluded.container_config,
		   requires_trigger=excluded.requires_trigger,
		   slink_token=excluded.slink_token,
		   parent=excluded.parent,
		   routing_rules=excluded.routing_rules`,
		jid, g.Name, g.Folder, g.Trigger,
		g.AddedAt.Format(time.RFC3339),
		string(cfgJSON), btoi(g.NeedTrig), g.SlinkToken,
		g.Parent, string(rulesJSON),
	)
	return err
}

func (s *Store) GetGroup(jid string) (core.Group, bool) {
	row := s.db.QueryRow(
		`SELECT jid, name, folder, trigger_word, added_at,
		        container_config, requires_trigger, slink_token,
		        parent, routing_rules
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
		        container_config, requires_trigger, slink_token,
		        parent, routing_rules
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

func scanGroup(r rowScanner) (core.Group, bool) {
	var g core.Group
	var addedAt string
	var cfgJSON, rulesJSON, slinkToken, parent *string
	var needTrig int

	err := r.Scan(&g.JID, &g.Name, &g.Folder, &g.Trigger, &addedAt,
		&cfgJSON, &needTrig, &slinkToken, &parent, &rulesJSON)
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
	if rulesJSON != nil {
		json.Unmarshal([]byte(*rulesJSON), &g.Rules)
	}
	return g, true
}
