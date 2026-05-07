package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onvos/arizuko/auth"
	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/core"
)

// anonSender returns "anon:<hex>" derived from the proxyd-trusted client
// IP. X-Forwarded-For is set by proxyd; webd listens only on the internal net.
func anonSender(r *http.Request) string {
	ip := strings.TrimSpace(strings.SplitN(r.Header.Get("X-Forwarded-For"), ",", 2)[0])
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
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:14px;color:var(--fg);background:var(--bg);height:100dvh;display:flex;flex-direction:column;overflow:hidden}
header{display:flex;align-items:center;gap:.6rem;padding:.6rem 1rem;border-bottom:1px solid var(--border);background:var(--card)}
header .name{color:var(--accent);font-weight:600;font-size:1.05em}
header .dim{color:var(--dim);font-size:.85em}
#thread{flex:1;overflow-y:auto;padding:1rem;display:flex;flex-direction:column;gap:.5rem}
@keyframes pop{from{opacity:0;transform:translateY(4px)}to{opacity:1;transform:none}}
.msg{max-width:75%%;padding:.55rem .85rem;border-radius:12px;line-height:1.55;white-space:pre-wrap;word-break:break-word;font-size:.9em;animation:pop .15s ease}
.msg.user{align-self:flex-end;background:var(--accent);color:var(--bg);border-bottom-right-radius:3px}
.msg.assistant{align-self:flex-start;background:var(--card);border:1px solid var(--border);border-bottom-left-radius:3px}
.msg .meta{font-size:.7em;color:var(--dim);margin-top:.25em}
.msg.user .meta{color:rgba(0,0,0,.45)}
.typing{align-self:flex-start;color:var(--dim);font-size:.85em;padding:.3rem .8rem;animation:pop .15s ease}
footer{padding:.6rem 1rem;padding-bottom:max(.6rem,env(safe-area-inset-bottom));border-top:1px solid var(--border);background:var(--card)}
footer form{display:flex;gap:.5rem;align-items:flex-end}
footer textarea{flex:1;resize:none;padding:.5rem .7rem;border:1px solid var(--border);border-radius:8px;background:var(--bg);color:var(--fg);font-family:inherit;font-size:.9em;height:2.6rem;transition:border-color .15s}
footer textarea:focus{outline:none;border-color:var(--accent3)}
footer button{padding:.5rem 1.2rem;background:var(--accent);color:var(--bg);border:none;border-radius:8px;cursor:pointer;font-family:inherit;font-weight:600;font-size:.9em;min-height:44px;transition:opacity .15s}
footer button:hover{opacity:.85}
footer button:disabled{opacity:.35;cursor:default}
@media(max-width:600px){body{font-size:13px}.msg{max-width:90%%}footer textarea{font-size:16px}}
</style>
<script>(function(){var t=localStorage.getItem('hub-theme')||(matchMedia('(prefers-color-scheme:dark)').matches?'dark':'light');document.documentElement.setAttribute('data-theme',t)})()</script>
</head><body>
<header>
  <span class="name">%s</span>
  <span class="dim">ant link</span>
</header>
<div id="thread"></div>
<footer>
  <form id="f" onsubmit="send(event)">
    <textarea id="m" placeholder="type a message..." onkeydown="if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();send(event)}"></textarea>
    <button type="submit" id="btn">send</button>
  </form>
</footer>
<script>
var folder="%s",token="%s",topic='t'+Date.now(),es,reconnectTimer;
function esc(s){return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')}
function addMsg(role,content,id){
  var d=document.createElement('div');
  d.className='msg '+role;
  if(id)d.id='msg-'+id;
  d.innerHTML=esc(content).replace(/\n/g,'<br>');
  document.getElementById('thread').appendChild(d);
  d.scrollIntoView({behavior:'smooth'});
}
function showTyping(){
  if(document.querySelector('.typing'))return;
  var d=document.createElement('div');d.className='typing';d.textContent='…';
  document.getElementById('thread').appendChild(d);
  d.scrollIntoView({behavior:'smooth'});
}
function connect(){
  clearTimeout(reconnectTimer);
  if(es)es.close();
  es=new EventSource('/slink/stream?token='+encodeURIComponent(token)+'&group='+encodeURIComponent(folder)+'&topic='+encodeURIComponent(topic));
  es.addEventListener('message',function(e){
    try{
      var m=JSON.parse(e.data);
      if(document.getElementById('msg-'+m.id))return;
      var el=document.querySelector('.typing');if(el)el.remove();
      addMsg(m.role||'assistant',m.content,m.id);
    }catch(x){}
  });
  es.onerror=function(){es.close();reconnectTimer=setTimeout(connect,3000)};
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
    var bubble=tmp.firstChild;
    if(bubble&&!document.getElementById(bubble.id)){document.getElementById('thread').appendChild(bubble);bubble.scrollIntoView({behavior:'smooth'})}
    showTyping();
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
		slog.Warn("slink token not found", "token_hash", chanlib.ShortHash(token))
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
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

	// Round-handle steering: POST /slink/<token>/<turn_id> reuses the
	// in-flight round's topic so the new message lands in the same chat
	// queue. The gateway's per-folder queue serializes runs, so the steer
	// effectively becomes the immediate next round (chained_from set in
	// the response). When the round has already finished, the steer falls
	// back to a fresh round with a new topic.
	steerTurn := strings.TrimSpace(r.PathValue("id"))
	chainedFrom := ""
	if steerTurn != "" {
		jid := "web:" + g.Folder
		stTopic := s.st.TopicByMessageID(steerTurn, jid)
		if stTopic != "" {
			info, _ := s.st.GetTurnResult(g.Folder, steerTurn)
			if info.Status == "pending" {
				topic = stTopic
				chainedFrom = steerTurn
			}
		}
	}

	if topic == "" {
		topic = fmt.Sprintf("t%d", time.Now().UnixMilli())
	}
	if len(topic) > maxTopicLen {
		http.Error(w, "topic too long", http.StatusBadRequest)
		return
	}

	// Authed identity only when signed by proxyd; else anon from trusted IP.
	sender, senderName := "", ""
	if auth.VerifyUserSig(s.cfg.hmacSecret, r) {
		sender = userSub(r)
		senderName = userName(r)
	}
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

	// Subscribe before publishing to avoid a race on the user's own bubble.
	var ch <-chan string
	var unsub func()
	if wantSSE || wait > 0 {
		ch, unsub = s.hub.subscribe(g.Folder, topic)
		defer unsub()
	}

	m, userPayload, err := s.injectSlink(g, content, topic, sender, senderName, token)
	if err != nil {
		switch err {
		case errSlinkStore:
			http.Error(w, "store failed", http.StatusInternalServerError)
		case errSlinkRouter:
			http.Error(w, "router unavailable", http.StatusBadGateway)
		default:
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	switch {
	case wantSSE:
		serveSSE(w, r, ch)
	case wantJSON:
		resp := map[string]any{
			"user":    userPayload,
			"turn_id": m.ID,
			"status":  "pending",
		}
		if chainedFrom != "" {
			resp["chained_from"] = chainedFrom
		}
		if wait > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), time.Duration(wait)*time.Second)
			if a, ok := waitForAssistant(ctx, ch); ok {
				resp["assistant"] = a
			}
			cancel()
		}
		chanlib.WriteJSON(w, resp)
	default:
		w.Header().Set("Content-Type", "text/html")
		escaped := strings.ReplaceAll(htmlEscape(content), "\n", "<br>")
		fmt.Fprintf(w, `<div class="msg user" id="msg-%s">%s</div>`, m.ID, escaped)
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

// handleSlinkStream: SSE for a group/topic. Access requires either a valid
// proxyd-signed slink token matching `group`, or proxyd-signed user identity
// with folder ACL. GET /slink/stream?token=<t>&group=<folder>&topic=<t>
func (s *server) handleSlinkStream(w http.ResponseWriter, r *http.Request) {
	folder := r.URL.Query().Get("group")
	topic := r.URL.Query().Get("topic")
	if folder == "" || topic == "" {
		http.Error(w, "group and topic required", http.StatusBadRequest)
		return
	}
	if len(topic) > maxTopicLen {
		http.Error(w, "topic too long", http.StatusBadRequest)
		return
	}

	okSlink := auth.VerifySlinkSig(s.cfg.hmacSecret, r) && r.Header.Get("X-Folder") == folder
	okUser := auth.VerifyUserSig(s.cfg.hmacSecret, r) && userAllowedFolder(userGroups(r), folder)
	if !okSlink && !okUser {
		slog.Warn("slink stream forbidden", "folder", folder,
			"sub", r.Header.Get("X-User-Sub"),
			"token_hash", chanlib.ShortHash(r.URL.Query().Get("token")))
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if !s.hub.canSubscribe() {
		http.Error(w, "too many subscriptions", http.StatusServiceUnavailable)
		return
	}

	// Replay missed messages on reconnect.
	if lastID := r.Header.Get("Last-Event-Id"); lastID != "" {
		if t, ok := s.st.MessageTimestampByID(lastID, "web:"+folder); ok {
			msgs, _ := s.st.MessagesSinceTopic(folder, topic, t, 50)
			flusher, _ := w.(http.Flusher)
			for _, m := range msgs {
				data, _ := json.Marshal(map[string]any{
					"id": m.ID, "role": messageRole(m), "content": m.Content,
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

// errSlinkStore / errSlinkRouter let HTTP and MCP callers translate
// injectSlink failures into transport-appropriate errors.
var (
	errSlinkStore  = &slinkErr{kind: "store"}
	errSlinkRouter = &slinkErr{kind: "router"}
)

type slinkErr struct{ kind string }

func (e *slinkErr) Error() string { return "slink: " + e.kind + " failed" }

// injectSlink writes one inbound slink message to the store, hands it
// to the router, and publishes the user-bubble frame to the SSE hub.
// Returns the persisted Message + the JSON payload that was published
// (so the caller can include it in its response). The token is used
// for log correlation only — auth must be checked by the caller.
func (s *server) injectSlink(g core.Group, content, topic, sender, senderName, token string) (core.Message, map[string]any, error) {
	m := core.Message{
		ID:        core.MsgID("msg"),
		ChatJID:   "web:" + g.Folder,
		Sender:    sender,
		Name:      senderName,
		Content:   content,
		Timestamp: time.Now(),
		Topic:     topic,
		Source:    "web",
	}
	if err := s.st.PutMessage(m); err != nil {
		slog.Warn("slink store", "folder", g.Folder, "err", err)
		return core.Message{}, nil, errSlinkStore
	}

	slog.Info("slink inbound", "folder", g.Folder,
		"anon_sender", sender, "token_hash", chanlib.ShortHash(token), "topic", topic)

	if err := s.rc.SendMessage(chanlib.InboundMsg{
		ID:         m.ID,
		ChatJID:    m.ChatJID,
		Sender:     sender,
		SenderName: senderName,
		Content:    content,
		Timestamp:  m.Timestamp.Unix(),
		// slink: one token == one anonymous human; always single-user.
		IsGroup: false,
	}); err != nil {
		slog.Warn("slink router", "folder", g.Folder, "err", err)
		return core.Message{}, nil, errSlinkRouter
	}

	payload := map[string]any{
		"id":         m.ID,
		"role":       "user",
		"content":    m.Content,
		"sender":     senderName,
		"topic":      topic,
		"folder":     g.Folder,
		"created_at": m.Timestamp.Format(time.RFC3339),
	}
	payloadJSON, _ := json.Marshal(payload)
	s.hub.publish(g.Folder, topic, "message", string(payloadJSON))
	return m, payload, nil
}

var htmlReplacer = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&#34;",
	"'", "&#39;",
	`\`, "&#92;",
)

// htmlEscape covers HTML, attribute, and JS-string-literal contexts.
// These pages use fmt.Sprintf rather than html/template's contextual encoding.
func htmlEscape(s string) string { return htmlReplacer.Replace(s) }
