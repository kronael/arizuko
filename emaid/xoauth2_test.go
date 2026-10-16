package main

import (
	"bufio"
	"encoding/base64"
	"net"
	"net/smtp"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2/imapclient"
)

// Google's documented example (xoauth2-protocol page): the base64 below was
// computed outside the code under test, from the byte layout the page gives.
const (
	exUser = "someuser@example.com"
	exTok  = "ya29.vF9dft4qmTc2Nvb3RlckBhdHRhdmlzdGEuY29tCg"
	exB64  = "dXNlcj1zb21ldXNlckBleGFtcGxlLmNvbQFhdXRoPUJlYXJlciB5YTI5LnZGOWRmdDRxbVRjMk52YjNSbGNrQmhkSFJoZG1semRHRXVZMjl0Q2cBAQ=="
)

func TestXOAUTH2ResponseExact(t *testing.T) {
	got := xoauth2Response(exUser, exTok)
	if want := "user=" + exUser + "\x01auth=Bearer " + exTok + "\x01\x01"; string(got) != want {
		t.Errorf("response = %q, want %q", got, want)
	}
	if b := base64.StdEncoding.EncodeToString(got); b != exB64 {
		t.Errorf("base64 = %s, want %s", b, exB64)
	}
}

func TestXOAUTH2SMTPAuth(t *testing.T) {
	a := xoauth2SMTP{exUser, exTok}
	mech, ir, err := a.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: true})
	if err != nil || mech != "XOAUTH2" || string(ir) != string(xoauth2Response(exUser, exTok)) {
		t.Errorf("Start = %q %q %v", mech, ir, err)
	}
	// The token must not cross a connection that is not yet TLS.
	if _, _, err := a.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: false}); err == nil {
		t.Error("Start over plaintext must refuse")
	}
	// The error challenge gets the empty response the mechanism requires;
	// success (more=false) sends nothing further.
	if r, err := a.Next([]byte(`{"status":"401"}`), true); err != nil || r == nil || len(r) != 0 {
		t.Errorf("Next(more) = %q, %v; want empty non-nil", r, err)
	}
	if r, err := a.Next(nil, false); err != nil || r != nil {
		t.Errorf("Next(done) = %q, %v; want nil", r, err)
	}
}

// sendReply picks the auth from the config: token -> XOAUTH2, else PLAIN.
func TestSendReplyAuthSelection(t *testing.T) {
	orig := smtpSender
	defer func() { smtpSender = orig }()
	var got smtp.Auth
	smtpSender = func(_ string, a smtp.Auth, _ string, _ []string, _ []byte) error {
		got = a
		return nil
	}
	info := &smtp.ServerInfo{Name: "smtp.example.com", TLS: true}

	cfg := config{Account: exUser, SMTPHost: "smtp.example.com", SMTPPort: "587", AccessToken: exTok}
	if err := sendReply(cfg, "to@example.com", "", "", "hi"); err != nil {
		t.Fatal(err)
	}
	if mech, _, _ := got.Start(info); mech != "XOAUTH2" {
		t.Errorf("token config used %q, want XOAUTH2", mech)
	}

	cfg = config{Account: exUser, SMTPHost: "smtp.example.com", SMTPPort: "587", Password: "pw"}
	if err := sendReply(cfg, "to@example.com", "", "", "hi"); err != nil {
		t.Fatal(err)
	}
	if mech, _, _ := got.Start(info); mech != "PLAIN" {
		t.Errorf("password config used %q, want PLAIN", mech)
	}
}

// fakeAuthIMAP answers one authentication command on conn and reports the
// command line it saw. With reject it sends the XOAUTH2 error challenge, reads
// the client's empty response, then refuses with AUTHENTICATIONFAILED.
func fakeAuthIMAP(conn net.Conn, reject bool, seen chan<- []string) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	w := func(s string) { conn.Write([]byte(s)) } //nolint:errcheck // the test asserts on the result
	w("* OK [CAPABILITY IMAP4rev1 SASL-IR AUTH=XOAUTH2] ready\r\n")
	var lines []string
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			return
		}
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		switch strings.ToUpper(f[1]) {
		case "CAPABILITY":
			w("* CAPABILITY IMAP4rev1 SASL-IR AUTH=XOAUTH2\r\n" + f[0] + " OK done\r\n")
		case "LOGIN", "AUTHENTICATE":
			lines = append(lines, strings.TrimSpace(l))
			if reject {
				w("+ " + base64.StdEncoding.EncodeToString([]byte(`{"status":"401"}`)) + "\r\n")
				blank, _ := br.ReadString('\n')
				lines = append(lines, strings.TrimSpace(blank)+"<blank>")
				w(f[0] + " NO [AUTHENTICATIONFAILED] Invalid credentials\r\n")
			} else {
				w(f[0] + " OK authenticated\r\n")
			}
			seen <- lines
			return
		}
	}
}

func authOverPipe(t *testing.T, cfg config, reject bool) ([]string, error) {
	t.Helper()
	s, c := net.Pipe()
	seen := make(chan []string, 1)
	go fakeAuthIMAP(s, reject, seen)
	ic := imapclient.New(c, nil)
	defer ic.Close()
	err := imapAuth(ic, cfg)
	return <-seen, err
}

func TestIMAPAuthXOAUTH2OnTheWire(t *testing.T) {
	lines, err := authOverPipe(t, config{Account: exUser, AccessToken: exTok}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "AUTHENTICATE XOAUTH2 " + exB64; !strings.HasSuffix(lines[0], want) {
		t.Errorf("wire line = %q, want suffix %q", lines[0], want)
	}
}

func TestIMAPAuthPasswordStillUsesLogin(t *testing.T) {
	lines, err := authOverPipe(t, config{Account: "bot", Password: "pw"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lines[0], "LOGIN") || strings.Contains(lines[0], "AUTHENTICATE") {
		t.Errorf("wire line = %q, want LOGIN", lines[0])
	}
}

// A refused (e.g. expired) token ends in a permanent auth failure, so the poll
// loop stops and /health goes 503 instead of retrying a dead credential.
func TestIMAPAuthXOAUTH2RefusalIsPermanent(t *testing.T) {
	lines, err := authOverPipe(t, config{Account: exUser, AccessToken: exTok}, true)
	if err == nil {
		t.Fatal("a refused token must return an error")
	}
	if !permanentAuthFailure(err) {
		t.Errorf("err = %v, want a permanent auth failure", err)
	}
	// go-imap writes an empty SASL response as "=".
	if len(lines) != 2 || (lines[1] != "<blank>" && lines[1] != "=<blank>") {
		t.Errorf("client must answer the error challenge with an empty response: %q", lines)
	}
	if strings.Contains(err.Error(), exTok) {
		t.Errorf("the error leaks the token: %v", err)
	}
}

func TestCheckAuth(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pw, tok  string
		wantFail bool
	}{
		{"password only", "pw", "", false},
		{"token only", "", "tok", false},
		{"both", "pw", "tok", true},
		{"neither", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := config{Password: tc.pw, AccessToken: tc.tok}.checkAuth()
			if (err != nil) != tc.wantFail {
				t.Errorf("checkAuth = %v, wantFail %v", err, tc.wantFail)
			}
		})
	}
}

// With a token and no password, loadConfig must boot: the old MustEnv on
// EMAIL_PASSWORD would have exited here.
func TestLoadConfigTokenOnly(t *testing.T) {
	for _, k := range []string{"EMAIL_IMAP_HOST", "EMAIL_SMTP_HOST", "EMAIL_ACCOUNT", "ROUTER_URL"} {
		t.Setenv(k, "x-"+k)
	}
	t.Setenv("EMAIL_PASSWORD", "")
	t.Setenv("EMAIL_ACCESS_TOKEN", "tok")
	if c := loadConfig(); c.AccessToken != "tok" || c.Password != "" {
		t.Errorf("config = %+v", c)
	}
}
