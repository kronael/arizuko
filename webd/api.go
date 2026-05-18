package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/kronael/arizuko/chanlib"
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
		out[i] = msgOut{m.ID, messageRole(m), m.Content, m.Name, m.Timestamp, m.ReplyToID}
	}
	chanlib.WriteJSON(w, map[string]any{"data": out, "has_more": len(msgs) == 50})
}

// POST /api/groups/<folder>/typing
func (s *server) handleAPITyping(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusAccepted)
}

// POST /api/groups/<folder>/messages — authenticated operator message
// injection from the /panel/<folder> chat page. JSON {content, topic}.
func (s *server) handleAPIMessagesPost(w http.ResponseWriter, r *http.Request) {
	folder := folderParam(r)
	if _, ok := s.st.GroupByFolder(folder); !ok {
		chanlib.WriteErr(w, 404, "group not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	var body struct {
		Content string `json:"content"`
		Topic   string `json:"topic"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Content == "" {
		chanlib.WriteErr(w, 400, "content required")
		return
	}
	topic := body.Topic
	if topic == "" {
		topic = fmt.Sprintf("t%d", time.Now().UnixMilli())
	}
	jid := "web:" + folder
	sender := userSub(r)
	senderName := userName(r)
	if sender == "" {
		sender = "operator"
		senderName = "Operator"
	}
	if _, _, err := s.injectRouteMessage(jid, folder, body.Content, topic, sender, senderName, ""); err != nil {
		chanlib.WriteErr(w, 500, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

type groupOut struct {
	Folder string `json:"folder"`
}

func (s *server) groupList() []groupOut {
	groups := s.st.AllGroups()
	out := make([]groupOut, 0, len(groups))
	for _, g := range groups {
		out = append(out, groupOut{Folder: g.Folder})
	}
	return out
}
