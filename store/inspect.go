package store

// inspect.go is the `messages` aggregate an operator's inspection reads: which
// chats a run has failed in, how often, and when last.
//
// It is a `messages` read and nothing else. The task run history that shared
// this file is an entity now and lives in task_run_logs.go; the two were here
// together because one operator page reads both, and no statement ever joined
// them.

import (
	"time"

	"github.com/kronael/arizuko/groupfolder"
)

type ErroredChat struct {
	ChatJID  string    `json:"chat_jid"`
	Count    int       `json:"count"`
	LastAt   time.Time `json:"last_at"`
	RoutedTo string    `json:"routed_to"`
}

func (s *Store) ErroredChats(folder string, isRoot bool) []ErroredChat {
	scope := folder
	if isRoot {
		scope = ""
	}
	rows, err := s.db.Query(
		`SELECT chat_jid, COUNT(*), MAX(timestamp), COALESCE(MAX(routed_to),'')
		 FROM messages WHERE errored = 1
		   AND (? = '' OR `+groupfolder.SQLSubtree("routed_to", "/")+`)
		 GROUP BY chat_jid ORDER BY MAX(timestamp) DESC`,
		append([]any{scope}, groupfolder.SubtreeArgs(scope, "/")...)...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ErroredChat
	for rows.Next() {
		var e ErroredChat
		var ts string
		if err := rows.Scan(&e.ChatJID, &e.Count, &ts, &e.RoutedTo); err != nil {
			continue
		}
		e.LastAt, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, e)
	}
	return out
}
