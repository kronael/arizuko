package main

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/kronael/arizuko/groupfolder"
)

const htmlHead = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>arizuko</title>
<script src="https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js"></script>
<script src="https://unpkg.com/htmx-ext-sse@2.2.2/sse.js"></script>
<link rel="stylesheet" href="/static/style.css">
</head>
`

// GET /
func (s *server) handleGroupsPage(w http.ResponseWriter, r *http.Request) {
	name := userName(r)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, htmlHead)
	fmt.Fprintf(w, `<body>
<header>
  <span class="logo">arizuko</span><span class="tagline">Claude agent gateway</span>
  <span class="user">%s</span>
  <form action="/auth/logout" method="POST" style="display:inline">
    <button type="submit">logout</button>
  </form>
</header>
<main>
  <div id="groups-grid"
       hx-get="/x/groups"
       hx-trigger="load"
       hx-swap="innerHTML">
    <p>loading...</p>
  </div>
</main>
</body></html>`, htmlEscape(name))
}

// GET /panel/<folder> — operator-authenticated chat panel for a folder.
// Token-less; sends via /api/groups/<folder>/messages POST (authenticated
// via proxyd-signed user identity). Spec 5/W moved the public chat
// surface to /chat/<token>/ and renamed this operator view to /panel.
func (s *server) handleChatPage(w http.ResponseWriter, r *http.Request) {
	folder := folderParam(r)
	g, ok := s.stRoutd.GroupByFolder(folder)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	folderURL := url.PathEscape(folder)
	folderQ := url.QueryEscape(folder)
	fmt.Fprint(w, htmlHead)
	fmt.Fprintf(w, `<body>
<header>
  <a href="/">&#8592; arizuko</a>
  <span class="agent-name">%s</span>
  <select id="topic-select"
          hx-get="/x/groups/%s/topics"
          hx-trigger="load"
          hx-target="#topic-select"
          hx-swap="innerHTML"
          onchange="switchTopic(this.value)">
  </select>
  <button onclick="newTopic()">+</button>
</header>
<main>
  <div id="thread"
       hx-get="/x/groups/%s/messages"
       hx-trigger="load"
       hx-swap="innerHTML"
       hx-include="#topic-select"
       hx-vals="js:{topic: currentTopic()}"
       hx-ext="sse"
       sse-connect="/chat/stream?group=%s&topic="
       sse-swap:message="beforeend">
  </div>
</main>
<footer>
  <form id="msg-form" onsubmit="sendMsg(event)">
    <textarea id="msg-input" placeholder="type a message..."
              autocomplete="off" autocorrect="off" autocapitalize="sentences" spellcheck="false"
              enterkeyhint="send" rows="1"
              onkeydown="if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();sendMsg(event)}"></textarea>
    <button type="submit">send</button>
  </form>
</footer>
<script>
var _topic = '';
var folder = %q;
function currentTopic() { return _topic; }
function switchTopic(t) {
  _topic = t;
  htmx.ajax('GET', '/x/groups/' + encodeURIComponent(folder) + '/messages?topic=' + encodeURIComponent(t), {target:'#thread', swap:'innerHTML'});
  var sse = document.getElementById('thread');
  sse.setAttribute('sse-connect', '/chat/stream?group=' + encodeURIComponent(folder) + '&topic=' + encodeURIComponent(t));
  htmx.process(sse);
}
function newTopic() {
  _topic = 't' + Date.now();
  document.getElementById('thread').innerHTML = '';
}
async function sendMsg(e) {
  e.preventDefault();
  var input = document.getElementById('msg-input');
  var content = input.value.trim();
  if (!content) return;
  input.value = '';
  await fetch('/api/groups/' + encodeURIComponent(folder) + '/messages', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({content: content, topic: _topic})
  });
}
</script>
</body></html>`,
		htmlEscape(groupfolder.NameOf(g.Folder)), folderURL, folderURL, folderQ, folder)
}
