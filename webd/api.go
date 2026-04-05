package main

import (
	"net/http"
	"time"

	"github.com/onvos/arizuko/chanlib"
)

// GET /api/groups
func (s *server) handleAPIGroups(w http.ResponseWriter, r *http.Request) {
	groups := s.groupList()
	chanlib.WriteJSON(w, map[string]any{"data": groups, "has_more": false})
}

// GET /api/groups/<folder>/topics
func (s *server) handleAPITopics(w http.ResponseWriter, r *http.Request) {
	folder := folderParam(r)
	topics, err := s.st.Topics(folder)
	if err != nil {
		chanlib.WriteErr(w, 500, "query failed")
		return
	}
	type topicOut struct {
		ID           string    `json:"id"`
		Preview      string    `json:"preview"`
		LastAt       time.Time `json:"last_at"`
		MessageCount int       `json:"message_count"`
	}
	out := make([]topicOut, len(topics))
	for i, t := range topics {
		out[i] = topicOut{t.ID, t.Preview, t.LastAt, t.MessageCount}
	}
	chanlib.WriteJSON(w, map[string]any{"data": out, "has_more": false})
}

// GET /api/groups/<folder>/messages?topic=<t>&before=<ts>&limit=50
func (s *server) handleAPIMessages(w http.ResponseWriter, r *http.Request) {
	folder := folderParam(r)
	topic := r.URL.Query().Get("topic")
	before := time.Now()
	if bs := r.URL.Query().Get("before"); bs != "" {
		if t, err := time.Parse(time.RFC3339, bs); err == nil {
			before = t
		}
	}
	msgs, err := s.st.MessagesByTopic(folder, topic, before, 50)
	if err != nil {
		chanlib.WriteErr(w, 500, "query failed")
		return
	}
	type msgOut struct {
		ID        string    `json:"id"`
		Role      string    `json:"role"`
		Content   string    `json:"content"`
		Sender    string    `json:"sender"`
		CreatedAt time.Time `json:"created_at"`
		ReplyToID string    `json:"reply_to_id,omitempty"`
	}
	out := make([]msgOut, len(msgs))
	for i, m := range msgs {
		role := "user"
		if m.BotMsg {
			role = "assistant"
		}
		out[i] = msgOut{m.ID, role, m.Content, m.Name, m.Timestamp, m.ReplyToID}
	}
	chanlib.WriteJSON(w, map[string]any{"data": out, "has_more": len(msgs) == 50})
}

// POST /api/groups/<folder>/typing
func (s *server) handleAPITyping(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusAccepted)
}

type groupOut struct {
	Folder string `json:"folder"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

func (s *server) groupList() []groupOut {
	groups := s.st.AllGroups()
	out := make([]groupOut, 0, len(groups))
	for _, g := range groups {
		out = append(out, groupOut{
			Folder: g.Folder,
			Name:   g.Name,
			Active: g.State == "active" || g.State == "",
		})
	}
	return out
}
