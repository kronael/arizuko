package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/core"
)

// anonSender derives a stable, privacy-preserving sender ID from client IP.
// Format: "anon:<8-char hex prefix of sha256(ip)>"
func anonSender(r *http.Request) string {
	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	if ip == "" {
		ip = "unknown"
	}
	sum := sha256.Sum256([]byte(ip))
	return fmt.Sprintf("anon:%x", sum[:4])
}

// handleSlinkPage renders the browser-based chat UI for anonymous slink users.
// GET /slink/<token>
func (s *server) handleSlinkPage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	g, ok := s.st.GroupBySlinkToken(token)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, slinkPageHTML, htmlEscape(g.Name), htmlEscape(g.Name), htmlEscape(g.Folder), htmlEscape(token))
}

const slinkPageHTML = `<!DOCTYPE html><html><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<style>
:root{--bg:#0a0a0a;--fg:#e0e0e0;--accent:#4ade80;--accent2:#a78bfa;--accent3:#58a6ff;--dim:#666;--border:#222;--card:#111;--card-hover:#161616}
[data-theme=light]{--bg:#fafafa;--fg:#1a1a1a;--accent:#16a34a;--accent2:#7c3aed;--accent3:#0969da;--dim:#888;--border:#ddd;--card:#fff;--card-hover:#f5f5f5}
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
body{font-family:"SF Mono","Fira Code","JetBrains Mono",Consolas,monospace;font-size:14px;color:var(--fg);background:var(--bg);height:100vh;display:flex;flex-direction:column}
header{display:flex;align-items:center;gap:.6rem;padding:.6rem 1rem;border-bottom:1px solid var(--border);background:var(--card)}
header .name{color:var(--accent);font-weight:bold;font-size:1.05em}
header .dim{color:var(--dim);font-size:.85em}
#thread{flex:1;overflow-y:auto;padding:1rem;display:flex;flex-direction:column;gap:.4rem}
.msg{max-width:75%%;padding:.5rem .8rem;border-radius:8px;line-height:1.5;white-space:pre-wrap;word-break:break-word;font-size:.9em}
.msg.user{align-self:flex-end;background:var(--accent);color:var(--bg)}
.msg.assistant{align-self:flex-start;background:var(--card);border:1px solid var(--border)}
.msg .meta{font-size:.7em;color:var(--dim);margin-top:.2em}
.msg.user .meta{color:rgba(0,0,0,.5)}
.typing{align-self:flex-start;color:var(--dim);font-size:.85em;padding:.3rem .8rem}
footer{padding:.6rem 1rem;border-top:1px solid var(--border);background:var(--card)}
footer form{display:flex;gap:.5rem}
footer textarea{flex:1;resize:none;padding:.5rem .7rem;border:1px solid var(--border);border-radius:6px;background:var(--bg);color:var(--fg);font-family:inherit;font-size:.9em;height:2.6rem}
footer textarea:focus{outline:none;border-color:var(--accent3)}
footer button{padding:.5rem 1.2rem;background:var(--accent);color:var(--bg);border:none;border-radius:6px;cursor:pointer;font-family:inherit;font-weight:bold;font-size:.9em}
footer button:hover{opacity:.9}
footer button:disabled{opacity:.4;cursor:default}
@media(max-width:600px){body{font-size:13px}.msg{max-width:88%%}}
</style>
<script>(function(){var t=localStorage.getItem('hub-theme')||(matchMedia('(prefers-color-scheme:dark)').matches?'dark':'light');document.documentElement.setAttribute('data-theme',t)})()</script>
</head><body>
<header>
  <span class="name">%s</span>
  <span class="dim">web chat</span>
</header>
<div id="thread"></div>
<footer>
  <form id="f" onsubmit="send(event)">
    <textarea id="m" placeholder="type a message..." onkeydown="if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();send(event)}"></textarea>
    <button type="submit" id="btn">send</button>
  </form>
</footer>
<script>
var folder='%s',token='%s',topic='t'+Date.now(),es;
function esc(s){var d=document.createElement('div');d.textContent=s;return d.innerHTML}
function addMsg(role,content,id){
  var d=document.createElement('div');
  d.className='msg '+role;
  if(id)d.id='msg-'+id;
  d.textContent=content;
  document.getElementById('thread').appendChild(d);
  d.scrollIntoView({behavior:'smooth'});
}
function connect(){
  if(es)es.close();
  es=new EventSource('/slink/stream?group='+encodeURIComponent(folder)+'&topic='+encodeURIComponent(topic));
  es.addEventListener('message',function(e){
    try{
      var m=JSON.parse(e.data);
      if(document.getElementById('msg-'+m.id))return;
      var el=document.querySelector('.typing');if(el)el.remove();
      addMsg(m.role||'assistant',m.content,m.id);
    }catch(x){}
  });
}
connect();
async function send(e){
  e.preventDefault();
  var input=document.getElementById('m'),btn=document.getElementById('btn');
  var content=input.value.trim();
  if(!content)return;
  input.value='';btn.disabled=true;
  try{
    var resp=await fetch('/slink/'+token,{
      method:'POST',
      headers:{'Content-Type':'application/x-www-form-urlencoded'},
      body:'content='+encodeURIComponent(content)+'&topic='+encodeURIComponent(topic)
    });
    if(!resp.ok){addMsg('assistant','Error: '+resp.statusText);return}
    var html=await resp.text();
    var tmp=document.createElement('div');tmp.innerHTML=html;
    document.getElementById('thread').appendChild(tmp.firstChild);
    tmp.firstChild&&tmp.firstChild.scrollIntoView({behavior:'smooth'});
  }finally{btn.disabled=false;input.focus()}
}
</script>
</body></html>`

// handleSlinkPost accepts user messages via token-authenticated POST.
// POST /slink/<token>   body: content=Hello&topic=abc123
//
// Content negotiation via Accept header:
//   - text/event-stream: upgrade to SSE, hold open, stream own bubble
//     plus any subsequent assistant/user events on (folder, topic).
//   - application/json:  return JSON {user, [assistant]}. Optional
//     ?wait=<sec> blocks up to N seconds for the first assistant reply
//     before responding (useful for curl-style sync clients).
//   - default:           return an HTMX-friendly <div class="msg user">
//     bubble fragment.
func (s *server) handleSlinkPost(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	g, ok := s.st.GroupBySlinkToken(token)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if r.ParseForm() != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	content := strings.TrimSpace(r.FormValue("content"))
	topic := strings.TrimSpace(r.FormValue("topic"))
	if content == "" {
		http.Error(w, "content required", http.StatusBadRequest)
		return
	}
	if topic == "" {
		topic = fmt.Sprintf("t%d", time.Now().UnixMilli())
	}

	sender := userSub(r)
	senderName := userName(r)
	if sender == "" {
		sender = anonSender(r)
		senderName = "Anonymous"
	}

	accept := r.Header.Get("Accept")
	wantSSE := strings.Contains(accept, "text/event-stream")
	wantJSON := strings.Contains(accept, "application/json")
	wait := 0
	if wantJSON {
		if n, err := strconv.Atoi(r.URL.Query().Get("wait")); err == nil {
			wait = min(max(n, 0), 120)
		}
	}

	// Subscribe BEFORE publishing so streamers and sync callers see the
	// user's own bubble + any assistant reply without a race.
	var ch <-chan string
	var unsub func()
	if wantSSE || wait > 0 {
		ch, unsub = s.hub.subscribe(g.Folder, topic)
		defer unsub()
	}

	id := core.MsgID("msg")
	m := core.Message{
		ID:        id,
		ChatJID:   "web:" + g.Folder,
		Sender:    sender,
		Name:      senderName,
		Content:   content,
		Timestamp: time.Now(),
		Topic:     topic,
		Source:    "web",
	}
	if err := s.st.PutMessage(m); err != nil {
		http.Error(w, "store failed", http.StatusInternalServerError)
		return
	}

	if err := s.rc.SendMessage(chanlib.InboundMsg{
		ID:         m.ID,
		ChatJID:    m.ChatJID,
		Sender:     sender,
		SenderName: senderName,
		Content:    content,
		Timestamp:  m.Timestamp.Unix(),
	}); err != nil {
		http.Error(w, "router unavailable", http.StatusBadGateway)
		return
	}

	userPayload := map[string]any{
		"id":         m.ID,
		"role":       "user",
		"content":    m.Content,
		"sender":     senderName,
		"topic":      topic,
		"folder":     g.Folder,
		"created_at": m.Timestamp.Format(time.RFC3339),
	}
	userPayloadJSON, _ := json.Marshal(userPayload)
	s.hub.publish(g.Folder, topic, "message", string(userPayloadJSON))

	switch {
	case wantSSE:
		serveSSE(w, r, ch)
	case wantJSON:
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"user": userPayload}
		if wait > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), time.Duration(wait)*time.Second)
			if a, ok := waitForAssistant(ctx, ch); ok {
				resp["assistant"] = a
			}
			cancel()
		}
		_ = json.NewEncoder(w).Encode(resp)
	default:
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="msg user" id="msg-%s">%s</div>`, m.ID, htmlEscape(content))
	}
}

// waitForAssistant blocks until an assistant-role event arrives on ch,
// the context ends, or the channel closes. Non-assistant frames
// (e.g. the caller's own user bubble) are consumed and ignored.
func waitForAssistant(ctx context.Context, ch <-chan string) (map[string]any, bool) {
	for {
		select {
		case frame, ok := <-ch:
			if !ok {
				return nil, false
			}
			_, data, _ := strings.Cut(frame, "data: ")
			data, _, _ = strings.Cut(data, "\n")
			var m map[string]any
			if json.Unmarshal([]byte(data), &m) != nil {
				continue
			}
			if role, _ := m["role"].(string); role == "assistant" {
				return m, true
			}
		case <-ctx.Done():
			return nil, false
		}
	}
}

// handleSlinkStream opens an SSE connection for a group/topic.
// GET /slink/stream?group=<folder>&topic=<t>
func (s *server) handleSlinkStream(w http.ResponseWriter, r *http.Request) {
	folder := r.URL.Query().Get("group")
	topic := r.URL.Query().Get("topic")
	if folder == "" || topic == "" {
		http.Error(w, "group and topic required", http.StatusBadRequest)
		return
	}

	// Replay missed messages on reconnect.
	if lastID := r.Header.Get("Last-Event-Id"); lastID != "" {
		if t, ok := s.st.MessageTimestampByID(lastID, "web:"+folder); ok {
			msgs, _ := s.st.MessagesSinceTopic(folder, topic, t, 50)
			flusher, _ := w.(http.Flusher)
			for _, m := range msgs {
				role := "user"
				if m.BotMsg {
					role = "assistant"
				}
				data, _ := json.Marshal(map[string]any{
					"id": m.ID, "role": role, "content": m.Content,
					"created_at": m.Timestamp.Format(time.RFC3339),
				})
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	}

	ch, unsub := s.hub.subscribe(folder, topic)
	defer unsub()
	serveSSE(w, r, ch)
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&#34;")
	return s
}
