package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// GET /x/groups
func (s *server) handleXGroups(w http.ResponseWriter, r *http.Request) {
	groups := s.groupList()
	w.Header().Set("Content-Type", "text/html")
	for _, g := range groups {
		dot := `<span class="dot grey"></span>`
		if g.Active {
			dot = `<span class="dot green"></span>`
		}
		fmt.Fprintf(w, `<a class="card" href="/chat/%s">%s<strong>%s</strong></a>`,
			g.Folder, dot, htmlEscape(g.Name))
	}
}

// GET /x/groups/<folder>/topics
func (s *server) handleXTopics(w http.ResponseWriter, r *http.Request) {
	folder := folderParam(r)
	topics, err := s.st.Topics(folder)
	w.Header().Set("Content-Type", "text/html")
	if err != nil || len(topics) == 0 {
		fmt.Fprint(w, `<option value="">+ new conversation</option>`)
		return
	}
	for _, t := range topics {
		label := t.LastAt.Format("Jan 2")
		if time.Since(t.LastAt) < 24*time.Hour {
			label = t.LastAt.Format("15:04")
		}
		fmt.Fprintf(w, `<option value="%s">%s — %s</option>`,
			htmlEscape(t.ID), label, htmlEscape(truncate(t.Preview, 40)))
	}
	fmt.Fprint(w, `<option value="">+ new conversation</option>`)
}

// GET /x/groups/<folder>/messages?topic=<t>&before=<ts>
func (s *server) handleXMessages(w http.ResponseWriter, r *http.Request) {
	folder := folderParam(r)
	topic := r.URL.Query().Get("topic")
	before := time.Now()
	if bs := r.URL.Query().Get("before"); bs != "" {
		if t, err := time.Parse(time.RFC3339, bs); err == nil {
			before = t
		}
	}
	msgs, _ := s.st.MessagesByTopic(folder, topic, before, 50)
	w.Header().Set("Content-Type", "text/html")
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		cls := "user"
		if m.BotMsg {
			cls = "assistant"
		}
		fmt.Fprintf(w, `<div class="msg %s" id="msg-%s"><p>%s</p><time>%s</time></div>`,
			cls, m.ID, htmlEscape(m.Content), m.Timestamp.Format("15:04"))
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
