package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
)

// Spec 8/15. Operator-only re-pair surface — dashd renders the page, proxies
// POST to whapd /v1/pair/start presenting its service:dashd ES256 token (whapd
// verifies it; HMAC retire step 5), writes an audit row to messages.

const whapdDefaultURL = "http://whapd:8080"

func (d *dash) whapdURL() string {
	if u := os.Getenv("WHAPD_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return whapdDefaultURL
}

// pairAuth sets the service:dashd bearer on a whapd pair request. nil svc (local
// dev, no AUTHD_URL) → no header. A token-exchange error is logged and the call
// goes out unauthenticated, surfacing as whapd's 401 rather than a silent skip.
func (d *dash) pairAuth(req *http.Request) {
	if d.svc == nil {
		return
	}
	tok, err := d.svc(req.Context())
	if err != nil {
		slog.Warn("whapd pair: service token unavailable", "err", err)
		return
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

type pairStatus struct {
	State     string `json:"state"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Since     string `json:"since,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type pairStartResp struct {
	Code      string `json:"code,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

// GET /dash/channels/whatsapp/pair — render the form + live status.
func (d *dash) handleWhatsappPair(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.requireAdmin(w, r, "**"); !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "WhatsApp re-pair",
		struct{ Href, Label string }{"", "Channels"},
		struct{ Href, Label string }{"", "WhatsApp"},
		struct{ Href, Label string }{"", "re-pair"},
	)

	st := d.fetchPairStatus()
	phone := os.Getenv("WHATSAPP_PHONE")

	fmt.Fprintf(w, `<p class="dim">Operator-only. Re-pair binds the instance's WhatsApp account to a new linked device. <a href="/pub/arizuko/components/channels.html">channels overview</a>.</p>`)
	fmt.Fprintf(w, `<div id="pair-status">%s</div>`, renderPairStatus(st))
	fmt.Fprintf(w, `<form method="post" action="/dash/channels/whatsapp/pair/start" hx-post="/dash/channels/whatsapp/pair/start" hx-target="#pair-result" hx-swap="innerHTML">
<p><label>Phone <input type="text" name="phone" value="%s" placeholder="+420..." required size="20"></label></p>
<p><button type="submit">Start pairing</button></p>
</form>
<div id="pair-result"></div>
<script>
(function(){
  // Light polling without bringing in htmx for the read view: refresh
  // the status block every 2s while the page is open.
  function refresh() {
    fetch('/dash/channels/whatsapp/pair/status').then(r => r.json())
      .then(s => { document.getElementById('pair-status').innerHTML = renderStatus(s); })
      .catch(() => {});
  }
  function renderStatus(s) {
    let html = '<p>session: <strong>' + (s.state||'?') + '</strong>';
    if (s.expires_at) html += ' expires_at ' + s.expires_at;
    if (s.since) html += ' since ' + s.since;
    html += '</p>';
    return html;
  }
  setInterval(refresh, 2000);
})();
</script>`, esc(phone))
	pageClose(w, r)
}

// GET /dash/channels/whatsapp/pair/status — JSON pass-through.
func (d *dash) handleWhatsappPairStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.requireAdmin(w, r, "**"); !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d.fetchPairStatus())
}

// POST /dash/channels/whatsapp/pair/start — call whapd, render code once.
func (d *dash) handleWhatsappPairStart(w http.ResponseWriter, r *http.Request) {
	sub, ok := d.requireAdmin(w, r, "**")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	phone := strings.TrimSpace(r.Form.Get("phone"))
	if phone == "" {
		http.Error(w, "phone required", 400)
		return
	}
	body, _ := json.Marshal(map[string]string{"phone": phone})
	req, _ := http.NewRequestWithContext(r.Context(), "POST",
		d.whapdURL()+"/v1/pair/start", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	d.pairAuth(req)
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		fmt.Fprintf(w, `<div class="err">pair-start failed: %s</div>`, esc(err.Error()))
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var pr pairStartResp
	json.Unmarshal(raw, &pr)
	if resp.StatusCode != 200 || pr.Code == "" {
		msg := pr.Error
		if msg == "" {
			msg = fmt.Sprintf("whapd returned %d", resp.StatusCode)
		}
		fmt.Fprintf(w, `<div class="err">%s</div>`, esc(msg))
		return
	}
	d.auditPairStart(sub, phone)
	fmt.Fprintf(w, `<div class="ok"><p>code: <strong class="code-xl">%s</strong> &middot; expires_at %s</p>
<ol><li>Open WhatsApp on +%s</li><li>Settings &rsaquo; Linked Devices &rsaquo; Link a Device</li><li>Tap "Link with phone number instead"</li><li>Enter the code above.</li></ol></div>`,
		esc(pr.Code), esc(pr.ExpiresAt), esc(strings.TrimPrefix(phone, "+")))
}

func (d *dash) fetchPairStatus() pairStatus {
	req, _ := http.NewRequest("GET", d.whapdURL()+"/v1/pair/status", nil)
	d.pairAuth(req)
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return pairStatus{State: "unreachable", Reason: err.Error()}
	}
	defer resp.Body.Close()
	var st pairStatus
	json.NewDecoder(resp.Body).Decode(&st)
	if st.State == "" {
		st.State = "unknown"
	}
	return st
}

func renderPairStatus(st pairStatus) string {
	out := fmt.Sprintf(`<p>session: <strong>%s</strong>`, esc(st.State))
	if st.ExpiresAt != "" {
		out += " expires_at " + esc(st.ExpiresAt)
	}
	if st.Since != "" {
		out += " since " + esc(st.Since)
	}
	out += "</p>"
	return out
}

func (d *dash) auditPairStart(sub, phone string) {
	if d.dbRW == nil {
		return
	}
	_, err := d.dbRW.Exec(`
		INSERT INTO messages (id, chat_jid, sender, content, timestamp, verb,
		                     is_from_me, is_bot_message)
		VALUES (?, ?, ?, ?, ?, ?, 0, 0)`,
		core.MsgID("pair"),
		"arizuko:admin/whapd",
		"whapd:pair",
		fmt.Sprintf("operator %s started pairing for %s", sub, phone),
		time.Now().UTC().Format(time.RFC3339Nano),
		"admin.pair",
	)
	if err != nil {
		slog.Warn("pair audit write failed", "err", err)
	}
}
