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

// GET /chat/<token>/  → built-in widget.
// Hook tokens hitting /chat/ still serve the widget (kind is metadata).
func (s *server) handleChatTokenRoot(w http.ResponseWriter, r *http.Request) {
	row, ok := s.lookupRouteToken(w, r)
	if !ok {
		return
	}
	folder := groupfolder.JidFolder(row.JID)
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
	folder := groupfolder.JidFolder(row.JID)
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

// GET /chat/<token>/<topic>/messages — prior messages for this token's JID
// and topic, oldest→newest, capped at the last 100 (store ceiling). Powers
// the widget's history-on-open. Empty/unknown topic → empty list (not error).
func (s *server) handleChatTokenHistory(w http.ResponseWriter, r *http.Request) {
	row, ok := s.lookupRouteToken(w, r)
	if !ok {
		return
	}
	topic := r.PathValue("topic")
	if len(topic) > maxTopicLen {
		http.Error(w, "topic too long", http.StatusBadRequest)
		return
	}
	type msgOut struct {
		ID      string `json:"id"`
		Role    string `json:"role"`
		Content string `json:"content"`
		TS      string `json:"ts"`
	}
	out := []msgOut{}
	if topic != "" {
		msgs, err := s.st.MessagesByThread(row.JID, topic, time.Now(), 100)
		if err != nil {
			http.Error(w, "store failed", http.StatusInternalServerError)
			return
		}
		// MessagesByThread returns newest→oldest; reverse to oldest→newest.
		out = make([]msgOut, len(msgs))
		for i, m := range msgs {
			out[len(msgs)-1-i] = msgOut{m.ID, messageRole(m), m.Content, m.Timestamp.Format(time.RFC3339)}
		}
	}
	chanlib.WriteJSON(w, out)
}

// POST /chat/<token>  — append a user message, optionally stream the reply.
// Accept header drives response shape: text/event-stream → SSE,
// text/html → HTMX bubble, anything else → JSON.
func (s *server) handleChatTokenPost(w http.ResponseWriter, r *http.Request) {
	row, ok := s.lookupRouteToken(w, r)
	if !ok {
		return
	}
	folder := groupfolder.JidFolder(row.JID)

	if !s.limiter.allow(row.JID) {
		slog.Warn("route-token rate limit", "folder", folder,
			"token_hash", chanlib.ShortHash(r.PathValue("token")))
		http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
		return
	}

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
	if s.identified(r) {
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
	if !s.limiter.allow(row.JID) {
		slog.Warn("hook rate limit", "folder", groupfolder.JidFolder(row.JID),
			"token_hash", chanlib.ShortHash(r.PathValue("token")))
		http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
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
	folder := groupfolder.JidFolder(jid)

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
	okUser := s.identified(r) && userAllowedFolder(userGroups(r), folder)
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
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<title>%s</title>
<style>
:root{--bg:#0a0a0a;--fg:#e0e0e0;--accent:#4ade80;--accent2:#a78bfa;--accent3:#58a6ff;--dim:#666;--border:#222;--card:#111;--card-hover:#161616}
[data-theme=light]{--bg:#fafafa;--fg:#1a1a1a;--accent:#16a34a;--accent2:#7c3aed;--accent3:#0969da;--dim:#888;--border:#ddd;--card:#fff;--card-hover:#f5f5f5}
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
html,body{height:100%%}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:14px;color:var(--fg);background:transparent}
/* dock — slim bar bottom-right on desktop; expands UPWARD into the panel */
#dock{position:fixed;right:1rem;bottom:1rem;width:380px;max-width:calc(100vw - 2rem);background:var(--card);border:1px solid var(--border);border-radius:14px;box-shadow:0 8px 32px rgba(0,0,0,.4);display:flex;flex-direction:column;overflow:hidden;transition:height .18s ease}
#dock.collapsed{height:52px}
#dock.open{height:560px;max-height:calc(100dvh - 2rem)}
header{display:flex;align-items:center;gap:.6rem;padding:.7rem 1rem;cursor:pointer;flex-shrink:0;user-select:none}
header .name{color:var(--accent);font-weight:600;font-size:1.05em;flex:1;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
header .ico{color:var(--dim);font-size:1.1em;line-height:1;background:none;border:none;cursor:pointer;padding:.15rem .3rem;border-radius:6px}
header .ico:hover{color:var(--fg);background:var(--card-hover)}
/* thread switcher drawer */
#threads{display:none;flex-direction:column;border-bottom:1px solid var(--border);background:var(--bg);max-height:200px;overflow-y:auto}
#dock.show-threads #threads{display:flex}
#threads .row{display:flex;align-items:center;gap:.5rem;padding:.5rem .9rem;cursor:pointer;font-size:.88em;border-bottom:1px solid var(--border)}
#threads .row:hover{background:var(--card-hover)}
#threads .row.active{color:var(--accent)}
#threads .row .ttl{flex:1;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
#threads .row .del{color:var(--dim);background:none;border:none;cursor:pointer;font-size:.95em;padding:0 .2rem}
#threads .row .del:hover{color:var(--accent2)}
#threads .new{padding:.55rem .9rem;color:var(--accent3);cursor:pointer;font-weight:600;font-size:.88em}
#threads .new:hover{background:var(--card-hover)}
#body{flex:1;display:flex;flex-direction:column;min-height:0}
#dock.collapsed #body{display:none}
#thread{flex:1;overflow-y:auto;padding:1rem;display:flex;flex-direction:column;gap:.5rem}
@keyframes pop{from{opacity:0;transform:translateY(4px)}to{opacity:1;transform:none}}
.msg{max-width:80%%;padding:.55rem .85rem;border-radius:12px;line-height:1.55;white-space:pre-wrap;word-break:break-word;font-size:.9em;animation:pop .15s ease}
.msg.user{align-self:flex-end;background:var(--accent);color:var(--bg);border-bottom-right-radius:3px}
.msg.assistant{align-self:flex-start;background:var(--bg);border:1px solid var(--border);border-bottom-left-radius:3px}
.typing{align-self:flex-start;color:var(--dim);font-size:.85em;padding:.3rem .8rem;animation:pop .15s ease}
footer{padding:.6rem .8rem;padding-bottom:max(.6rem,env(safe-area-inset-bottom));border-top:1px solid var(--border);flex-shrink:0}
footer form{display:flex;gap:.5rem;align-items:flex-end}
footer textarea{flex:1;resize:none;padding:.5rem .7rem;border:1px solid var(--border);border-radius:8px;background:var(--bg);color:var(--fg);font-family:inherit;font-size:.9em;height:2.6rem;transition:border-color .15s}
footer textarea:focus{outline:none;border-color:var(--accent3)}
footer button{padding:.5rem 1.1rem;background:var(--accent);color:var(--bg);border:none;border-radius:8px;cursor:pointer;font-family:inherit;font-weight:600;font-size:.9em;min-height:44px;transition:opacity .15s}
footer button:hover{opacity:.85}
footer button:disabled{opacity:.35;cursor:default}
/* mobile — full screen; when collapsed only a top bar shows */
@media(max-width:640px){
  #dock{right:0;left:0;bottom:0;top:auto;width:auto;max-width:none;border:none;border-radius:0;box-shadow:none}
  #dock.open{height:100dvh;max-height:100dvh;border-radius:0}
  #dock.collapsed{height:calc(52px + env(safe-area-inset-bottom));padding-bottom:env(safe-area-inset-bottom)}
  .msg{max-width:88%%}
  footer textarea{font-size:16px}
}
</style>
<script>(function(){var t=localStorage.getItem('hub-theme')||(matchMedia('(prefers-color-scheme:dark)').matches?'dark':'light');document.documentElement.setAttribute('data-theme',t)})()</script>
</head><body>
<div id="dock" class="collapsed">
  <header id="bar">
    <button class="ico" id="listBtn" title="threads" onclick="event.stopPropagation();toggleThreads()">&#9776;</button>
    <span class="name">%s</span>
    <button class="ico" id="toggleBtn" title="expand">&#9650;</button>
  </header>
  <div id="threads"></div>
  <div id="body">
    <div id="thread"></div>
    <footer>
      <form id="f" onsubmit="send(event)">
        <textarea id="m" placeholder="type a message..." onkeydown="if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();send(event)}"></textarea>
        <button type="submit" id="btn">send</button>
      </form>
    </footer>
  </div>
</div>
<script>
var folder="%s",token="%s";
var VKEY='arz_chat_visitor',TKEY='arz_chat_threads',MAXAGE=30*864e5;
var es,reconnectTimer,cur=null;

function esc(s){return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')}
function visitor(){var v=localStorage.getItem(VKEY);if(!v){v=rnd();localStorage.setItem(VKEY,v)}return v}
function rnd(){return Math.random().toString(36).slice(2,10)+Math.random().toString(36).slice(2,6)}
function loadThreads(){
  var a=[];try{a=JSON.parse(localStorage.getItem(TKEY))||[]}catch(x){}
  var now=Date.now();a=a.filter(function(t){return now-(t.updated||0)<MAXAGE});
  return a;
}
function saveThreads(a){localStorage.setItem(TKEY,JSON.stringify(a))}
function topicFor(id){return visitor()+'-'+id}

function renderThreads(){
  var a=loadThreads(),box=document.getElementById('threads');box.innerHTML='';
  a.sort(function(x,y){return (y.updated||0)-(x.updated||0)});
  a.forEach(function(t){
    var row=document.createElement('div');row.className='row'+(cur&&cur.id===t.id?' active':'');
    var ttl=document.createElement('span');ttl.className='ttl';ttl.textContent=t.title||'New chat';
    var del=document.createElement('button');del.className='del';del.innerHTML='&#215;';del.title='delete';
    del.onclick=function(e){e.stopPropagation();delThread(t.id)};
    row.appendChild(ttl);row.appendChild(del);
    row.onclick=function(){openThread(t);document.getElementById('dock').classList.remove('show-threads')};
    box.appendChild(row);
  });
  var nw=document.createElement('div');nw.className='new';nw.textContent='+ new chat';
  nw.onclick=function(){newThread()};box.appendChild(nw);
}
function toggleThreads(){
  expand();document.getElementById('dock').classList.toggle('show-threads');renderThreads();
}
function upsertThread(t){
  var a=loadThreads(),i=a.findIndex(function(x){return x.id===t.id});
  if(i<0)a.push(t);else a[i]=t;saveThreads(a);
}
function delThread(id){
  var a=loadThreads().filter(function(x){return x.id!==id});saveThreads(a);
  if(cur&&cur.id===id){cur=null;if(a.length)openThread(a[0]);else newThread()}
  renderThreads();
}
function newThread(){
  var t={id:rnd(),title:'New chat',topic:'',updated:Date.now()};
  t.topic=topicFor(t.id);upsertThread(t);openThread(t);
  document.getElementById('dock').classList.remove('show-threads');
  document.getElementById('m').focus();
}

async function openThread(t){
  cur=t;t.updated=Date.now();upsertThread(t);renderThreads();
  var thread=document.getElementById('thread');thread.innerHTML='';
  try{
    var resp=await fetch('/chat/'+token+'/'+encodeURIComponent(t.topic)+'/messages');
    if(resp.ok){(await resp.json()).forEach(function(m){addMsg(m.role||'assistant',m.content,m.id)})}
  }catch(x){}
  connect(t.topic);
}

function addMsg(role,content,id){
  var d=document.createElement('div');d.className='msg '+role;
  if(id)d.id='msg-'+id;
  d.innerHTML=esc(content).replace(/\n/g,'<br>');
  document.getElementById('thread').appendChild(d);
  d.scrollIntoView({behavior:'smooth'});
}
function showTyping(){
  if(document.querySelector('.typing'))return;
  var d=document.createElement('div');d.className='typing';d.textContent='…';
  document.getElementById('thread').appendChild(d);d.scrollIntoView({behavior:'smooth'});
}
function connect(topic){
  clearTimeout(reconnectTimer);if(es)es.close();
  es=new EventSource('/chat/stream?token='+encodeURIComponent(token)+'&group='+encodeURIComponent(folder)+'&topic='+encodeURIComponent(topic));
  es.addEventListener('message',function(e){
    if(!cur||cur.topic!==topic)return;
    try{
      var m=JSON.parse(e.data);
      if(document.getElementById('msg-'+m.id))return;
      var el=document.querySelector('.typing');if(el)el.remove();
      addMsg(m.role||'assistant',m.content,m.id);
    }catch(x){}
  });
  es.onerror=function(){es.close();reconnectTimer=setTimeout(function(){connect(topic)},3000)};
}

async function send(e){
  e.preventDefault();if(!cur)newThread();
  var input=document.getElementById('m'),btn=document.getElementById('btn');
  var content=input.value.trim();if(!content)return;
  input.value='';btn.disabled=true;
  if(cur.title==='New chat'){cur.title=content.slice(0,40);}
  cur.updated=Date.now();upsertThread(cur);renderThreads();
  try{
    var resp=await fetch('/chat/'+token,{
      method:'POST',
      headers:{'Content-Type':'application/x-www-form-urlencoded','Accept':'text/html'},
      body:'content='+encodeURIComponent(content)+'&topic='+encodeURIComponent(cur.topic)
    });
    if(!resp.ok){addMsg('assistant','Error: '+resp.statusText);return}
    var html=await resp.text();
    var tmp=document.createElement('div');tmp.innerHTML=html;
    var bubble=tmp.firstChild;
    if(bubble&&!document.getElementById(bubble.id)){document.getElementById('thread').appendChild(bubble);bubble.scrollIntoView({behavior:'smooth'})}
    showTyping();
  }finally{btn.disabled=false;input.focus()}
}

var dock=document.getElementById('dock');
function expand(){
  if(!dock.classList.contains('collapsed'))return;
  dock.classList.remove('collapsed');dock.classList.add('open');
  document.getElementById('toggleBtn').innerHTML='&#9660;';
  document.getElementById('toggleBtn').title='collapse';
  document.getElementById('m').focus();
}
function collapse(){
  dock.classList.add('collapsed');dock.classList.remove('open','show-threads');
  document.getElementById('toggleBtn').innerHTML='&#9650;';
  document.getElementById('toggleBtn').title='expand';
}
function toggleDock(){dock.classList.contains('collapsed')?expand():collapse()}
document.getElementById('bar').onclick=toggleDock;
document.getElementById('toggleBtn').onclick=function(e){e.stopPropagation();toggleDock()};

(function init(){
  var a=loadThreads();saveThreads(a);
  if(a.length){a.sort(function(x,y){return (y.updated||0)-(x.updated||0)});openThread(a[0])}
  else newThread();
})();
</script>
</body></html>`
