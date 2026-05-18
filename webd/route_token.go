package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

// route-token surface — spec 5/W. One handler set serves both:
//
//   /chat/<token>/    (web: tokens — human chat widget + SSE)
//   /hook/<token>     (hook: tokens — fire-and-forget webhook ingest)
//
// Both URLs accept any valid token regardless of kind; kind is metadata
// for the agent, not a URL access gate (per spec 5/W's "any URL +
// any valid token" policy locked at planning time).
//
// All errors are 404 to avoid leaking which tokens exist.

const routeTokenBodyMax = 1 << 20 // 1 MiB

var (
	errRouteTokenStore  = fmt.Errorf("route-token: store failed")
	errRouteTokenRouter = fmt.Errorf("route-token: router unavailable")
)

// anonSender derives a stable "anon:<hex>" from the client IP.
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

// lookupRouteToken fetches the row or writes 404 and returns ok=false.
func (s *server) lookupRouteToken(w http.ResponseWriter, r *http.Request) (store.RouteToken, bool) {
	token := r.PathValue("token")
	row, ok := s.st.LookupRouteToken(token)
	if !ok {
		slog.Warn("route token not found",
			"path", r.URL.Path, "token_hash", chanlib.ShortHash(token))
		http.Error(w, "not found", http.StatusNotFound)
		return store.RouteToken{}, false
	}
	return row, true
}

// jidFolder extracts the folder from a route_tokens JID. For web:<folder>
// returns the folder; for hook:<folder>/<source>[/<suffix>] returns
// <folder>. Used for engagement bookkeeping + the chat widget header.
func jidFolder(jid string) string {
	switch {
	case strings.HasPrefix(jid, "web:"):
		// web:<folder>[/<suffix>] — strip the prefix and take everything;
		// folder may contain "/" itself (acme/eng).
		return strings.TrimPrefix(jid, "web:")
	case strings.HasPrefix(jid, "hook:"):
		// hook:<folder>/<source>[/<suffix>]: folder is everything up to
		// the last segment(s). Heuristic: a single-segment folder ("acme")
		// means "acme/<source>"; multi-segment ("acme/eng") means
		// "acme/eng/<source>". We can't disambiguate without DB lookup,
		// so callers that need the exact folder should resolve via
		// store.DefaultFolderForJID.
		rest := strings.TrimPrefix(jid, "hook:")
		if i := strings.LastIndexByte(rest, '/'); i > 0 {
			return rest[:i]
		}
		return rest
	}
	return ""
}

// GET /chat/<token>/  → built-in widget.
// Hook tokens hitting /chat/ still serve the widget (kind is metadata).
func (s *server) handleChatTokenRoot(w http.ResponseWriter, r *http.Request) {
	row, ok := s.lookupRouteToken(w, r)
	if !ok {
		return
	}
	folder := jidFolder(row.JID)
	name := groupfolder.NameOf(folder)
	token := r.PathValue("token")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, chatWidgetHTML,
		htmlEscape(name), htmlEscape(name),
		htmlEscape(folder), htmlEscape(token))
}

// GET /chat/<token>/config — JSON bootstrap for the SDK.
func (s *server) handleChatTokenConfig(w http.ResponseWriter, r *http.Request) {
	row, ok := s.lookupRouteToken(w, r)
	if !ok {
		return
	}
	folder := jidFolder(row.JID)
	token := r.PathValue("token")
	chanlib.WriteJSON(w, map[string]any{
		"token":  token,
		"folder": folder,
		"name":   groupfolder.NameOf(folder),
		"endpoints": map[string]string{
			"post":   "/chat/" + token,
			"stream": "/chat/" + token + "/{turn_id}/sse",
			"status": "/chat/" + token + "/{turn_id}/status",
		},
		"sdk": "/assets/arizuko-client.js",
	})
}

// POST /chat/<token>  — append a user message, optionally stream the reply.
// Accept header drives response shape: text/event-stream → SSE,
// text/html → HTMX bubble, anything else → JSON.
func (s *server) handleChatTokenPost(w http.ResponseWriter, r *http.Request) {
	row, ok := s.lookupRouteToken(w, r)
	if !ok {
		return
	}
	folder := jidFolder(row.JID)

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	content, topic, err := parseChatBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if topic == "" {
		topic = fmt.Sprintf("t%d", time.Now().UnixMilli())
	}
	if len(topic) > maxTopicLen {
		http.Error(w, "topic too long", http.StatusBadRequest)
		return
	}

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
	wantHTML := strings.Contains(accept, "text/html")
	wantJSON := !wantSSE && !wantHTML
	wait := 0
	if wantJSON {
		if n, err := strconv.Atoi(r.URL.Query().Get("wait")); err == nil {
			wait = min(max(n, 0), 120)
		}
	}

	var ch <-chan string
	var unsub func()
	if wantSSE || wait > 0 {
		ch, unsub = s.hub.subscribe(folder, topic)
		defer unsub()
	}

	jid := row.JID
	m, userPayload, err := s.injectRouteMessage(jid, folder, content, topic, sender, senderName, r.PathValue("token"))
	if err != nil {
		switch err {
		case errRouteTokenStore:
			http.Error(w, "store failed", http.StatusInternalServerError)
		case errRouteTokenRouter:
			http.Error(w, "router unavailable", http.StatusBadGateway)
		default:
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	switch {
	case wantSSE:
		serveSSE(w, r, ch)
	case wantHTML:
		w.Header().Set("Content-Type", "text/html")
		escaped := strings.ReplaceAll(htmlEscape(content), "\n", "<br>")
		fmt.Fprintf(w, `<div class="msg user" id="msg-%s">%s</div>`, m.ID, escaped)
	default:
		resp := map[string]any{
			"user":    userPayload,
			"turn_id": m.ID,
			"status":  "pending",
		}
		if wait > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), time.Duration(wait)*time.Second)
			if a, ok := waitForAssistant(ctx, ch); ok {
				resp["assistant"] = a
			}
			cancel()
		}
		chanlib.WriteJSON(w, resp)
	}
}

// POST /hook/<token>  — append the body verbatim as an inbound message.
// Fire-and-forget; no response channel. Returns 204.
func (s *server) handleHookTokenPost(w http.ResponseWriter, r *http.Request) {
	row, ok := s.lookupRouteToken(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, routeTokenBodyMax)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}

	sender := senderFromJID(row.JID)
	jid := row.JID
	folder := jidFolder(jid)

	m := core.Message{
		ID:        core.MsgID("hook"),
		ChatJID:   jid,
		Sender:    sender,
		Name:      sender,
		Content:   string(body),
		Timestamp: time.Now(),
		Source:    "web",
	}
	if err := s.st.PutMessage(m); err != nil {
		slog.Warn("hook store", "folder", folder, "err", err)
		http.Error(w, "store failed", http.StatusInternalServerError)
		return
	}
	slog.Info("hook inbound", "folder", folder, "sender", sender,
		"token_hash", chanlib.ShortHash(r.PathValue("token")), "bytes", len(body))

	if err := s.rc.SendMessage(chanlib.InboundMsg{
		ID:         m.ID,
		ChatJID:    jid,
		Sender:     sender,
		SenderName: sender,
		Content:    m.Content,
		Timestamp:  m.Timestamp.Unix(),
		IsGroup:    false,
	}); err != nil {
		slog.Warn("hook router", "folder", folder, "err", err)
		// Already stored; don't fail the webhook on router unavailability.
	}
	w.WriteHeader(http.StatusNoContent)
}

// senderFromJID pulls the source segment out of a hook: jid.
// hook:<folder>/<source>[/<suffix>] → <source>. Returns "" for web: jids.
func senderFromJID(jid string) string {
	rest := strings.TrimPrefix(jid, "hook:")
	if rest == jid {
		return ""
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return ""
	}
	// Source is the segment after the folder. Folder may itself contain "/".
	// We assume the source is the second-to-last segment when a suffix is
	// present, last segment otherwise. Simple heuristic: take the last segment.
	return parts[len(parts)-1]
}

func parseChatBody(r *http.Request) (content, topic string, err error) {
	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	if strings.TrimSpace(ct) == "application/json" {
		var body struct {
			Content string `json:"content"`
			Topic   string `json:"topic"`
		}
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			return "", "", fmt.Errorf("bad request")
		}
		content = strings.TrimSpace(body.Content)
		topic = strings.TrimSpace(body.Topic)
	} else {
		if r.ParseForm() != nil {
			return "", "", fmt.Errorf("bad request")
		}
		content = strings.TrimSpace(r.FormValue("content"))
		topic = strings.TrimSpace(r.FormValue("topic"))
	}
	if content == "" {
		return "", "", fmt.Errorf("content required")
	}
	return content, topic, nil
}

// injectRouteMessage is the shared writer for chat-token POSTs (the
// hook-token surface inlines its own simpler path: no topic, no SSE).
func (s *server) injectRouteMessage(jid, folder, content, topic, sender, senderName, token string) (core.Message, map[string]any, error) {
	m := core.Message{
		ID:        core.MsgID("msg"),
		ChatJID:   jid,
		Sender:    sender,
		Name:      senderName,
		Content:   content,
		Timestamp: time.Now(),
		Topic:     topic,
		Source:    "web",
	}
	if err := s.st.PutMessage(m); err != nil {
		slog.Warn("route-token store", "folder", folder, "err", err)
		return core.Message{}, nil, errRouteTokenStore
	}
	slog.Info("route-token inbound", "folder", folder, "anon_sender", sender,
		"token_hash", chanlib.ShortHash(token), "topic", topic)

	if err := s.rc.SendMessage(chanlib.InboundMsg{
		ID:         m.ID,
		ChatJID:    jid,
		Sender:     sender,
		SenderName: senderName,
		Content:    content,
		Timestamp:  m.Timestamp.Unix(),
		IsGroup:    false,
	}); err != nil {
		slog.Warn("route-token router", "folder", folder, "err", err)
		return core.Message{}, nil, errRouteTokenRouter
	}

	payload := map[string]any{
		"id":         m.ID,
		"role":       "user",
		"content":    m.Content,
		"sender":     senderName,
		"topic":      topic,
		"folder":     folder,
		"created_at": m.Timestamp.Format(time.RFC3339),
	}
	payloadJSON, _ := json.Marshal(payload)
	s.hub.publish(folder, topic, "message", string(payloadJSON))
	return m, payload, nil
}

// GET /chat/stream?token=<t>&group=<folder>&topic=<t>
// Authenticated by user sig (operator viewing widget) OR by valid
// route-token + matching folder header — same shape as the legacy slink
// stream but reading from route_tokens.
func (s *server) handleRouteTokenStream(w http.ResponseWriter, r *http.Request) {
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

	okChat := auth.VerifyChatSig(s.cfg.hmacSecret, r) && r.Header.Get("X-Folder") == folder
	okUser := auth.VerifyUserSig(s.cfg.hmacSecret, r) && userAllowedFolder(userGroups(r), folder)
	if !okChat && !okUser {
		slog.Warn("chat stream forbidden", "folder", folder,
			"sub", r.Header.Get("X-User-Sub"),
			"token_hash", chanlib.ShortHash(r.URL.Query().Get("token")))
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if !s.hub.canSubscribe() {
		http.Error(w, "too many subscriptions", http.StatusServiceUnavailable)
		return
	}

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

// chatCORS sets permissive CORS for /chat/* and /hook/* responses; the
// route token is the public credential (spec 5/W). Short-circuits OPTIONS.
func chatCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Accept, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
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
func htmlEscape(s string) string { return htmlReplacer.Replace(s) }

const chatWidgetHTML = `<!DOCTYPE html><html><head>
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
  es=new EventSource('/chat/stream?token='+encodeURIComponent(token)+'&group='+encodeURIComponent(folder)+'&topic='+encodeURIComponent(topic));
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
    var resp=await fetch('/chat/'+token,{
      method:'POST',
      headers:{'Content-Type':'application/x-www-form-urlencoded','Accept':'text/html'},
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
