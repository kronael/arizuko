package main

// Spec 8/15 — dashd /dash/channels/whatsapp/pair surface. Operator-only
// (** super-grant). Non-admin gets 403; admin POST proxies to whapd and
// writes an audit row.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestDash_WhatsappPair_NonAdminDenied(t *testing.T) {
	srv, _, _ := newRWDashServer(t)

	for _, p := range []string{
		"/dash/channels/whatsapp/pair",
		"/dash/channels/whatsapp/pair/status",
	} {
		req, _ := http.NewRequest("GET", srv.URL+p, nil)
		req.Header.Set("X-User-Sub", "bob@nogrant")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Errorf("%s: got 200, want 403 for non-admin", p)
		}
	}

	form := url.Values{"phone": {"+420735544891"}}
	req, _ := http.NewRequest("POST", srv.URL+"/dash/channels/whatsapp/pair/start",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "bob@nogrant")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Errorf("pair-start: got 200, want 403 for non-admin")
	}
}

func TestDash_WhatsappPairStart_AdminProxiesAndAudits(t *testing.T) {
	// Spin a mock whapd that returns a code.
	mockWhapd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/pair/start" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":"ABC12345","expires_at":"2026-05-17T15:00:00Z"}`))
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(mockWhapd.Close)
	t.Setenv("WHAPD_URL", mockWhapd.URL)

	srv, inst, _ := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"phone": {"+420735544891"}}
	req, _ := http.NewRequest("POST", srv.URL+"/dash/channels/whatsapp/pair/start",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "ABC12345") {
		t.Errorf("body missing code: %q", body)
	}

	// Audit row landed in messages with the operator sub + phone.
	var auditCount int
	row := inst.DB.QueryRow(
		`SELECT COUNT(*) FROM messages
		  WHERE chat_jid='arizuko:admin/whapd' AND verb='admin.pair'
		    AND content LIKE 'operator alice@x started pairing for +420735544891%'`)
	if err := row.Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Errorf("audit rows = %d, want 1", auditCount)
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}
