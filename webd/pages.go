package main

import (
	"fmt"
	"net/http"
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
	fmt.Fprintf(w, htmlHead)
	fmt.Fprintf(w, `<body>
<header>
  <span class="logo">arizuko</span>
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

// GET /chat/<folder>
func (s *server) handleChatPage(w http.ResponseWriter, r *http.Request) {
	folder := folderParam(r)
	g, ok := s.st.GroupByFolder(folder)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, htmlHead)
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
       sse-connect="/slink/stream?group=%s&topic="
       sse-swap:message="beforeend">
  </div>
</main>
<footer>
  <form id="msg-form" onsubmit="sendMsg(event)">
    <textarea id="msg-input" placeholder="type a message..."
              onkeydown="if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();sendMsg(event)}"></textarea>
    <button type="submit">send</button>
  </form>
</footer>
<script>
var _topic = '';
function currentTopic() { return _topic; }
function switchTopic(t) {
  _topic = t;
  htmx.ajax('GET', '/x/groups/%s/messages?topic=' + encodeURIComponent(t), {target:'#thread', swap:'innerHTML'});
  var sse = document.getElementById('thread');
  sse.setAttribute('sse-connect', '/slink/stream?group=%s&topic=' + encodeURIComponent(t));
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
  var token = '%s';
  var resp = await fetch('/slink/' + token, {
    method: 'POST',
    headers: {'Content-Type': 'application/x-www-form-urlencoded'},
    body: 'content=' + encodeURIComponent(content) + '&topic=' + encodeURIComponent(_topic)
  });
  var html = await resp.text();
  document.getElementById('thread').insertAdjacentHTML('beforeend', html);
}
</script>
</body></html>`,
		htmlEscape(g.Name), folder, folder, folder, folder, folder,
		htmlEscape(g.SlinkToken))
}
