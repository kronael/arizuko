package routd

import (
	"net/http"
	"testing"

	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// TestSenderSchemeGuard pins the ingress sender-ownership guard added in
// 3cb63410. A registered adapter may not assert another platform's identity,
// but "anon:" is exempt — route tokens (5/W) mint anon:<hash> for public web
// chat and webd holds a registry entry for "web:", so constraining it took the
// whole web-chat surface down on krons the first time the guard reached a
// deployed instance (2026-08-05, "adapter may not assert sender anon:f883d5fa").
func TestSenderSchemeGuard(t *testing.T) {
	authd := newStubAuthd(t)
	db, srv, _ := newVerifiedRoutd(t, authd)

	reg := chanreg.New()
	// byPrincipal is keyed on the presented principal, so the guard only has an
	// entry to consult when registration supplies one.
	if _, err := reg.RegisterWithOrigin("slakd", "http://slakd:8080",
		[]string{"slack:T1/"}, map[string]bool{"send_text": true},
		"", "service:slakd"); err != nil {
		t.Fatalf("register slakd: %v", err)
	}
	srv.SetChannelRegistry(reg, nil, nil)
	h := srv.Handler()

	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	if _, err := db.AddRoute(core.Route{Match: "platform=slack", Target: "demo"}); err != nil {
		t.Fatalf("add route: %v", err)
	}

	tok := authd.mint(t, "service:slakd", "demo", "messages:write")

	cases := []struct {
		name   string
		sender string
		want   int
	}{
		{"own scheme accepted", "slack:U1", http.StatusOK},
		{"bare sender exempt", "u1", http.StatusOK},
		{"anon sender exempt", "anon:f883d5fa", http.StatusOK},
		{"foreign scheme rejected", "telegram:user/42", http.StatusBadRequest},
		{"oauth sub rejected", "google:1234567890", http.StatusBadRequest},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := apiv1.Message{
				ID:      "sd" + string(rune('a'+i)),
				ChatJID: "web:demo",
				Sender:  c.sender,
				Content: "hi",
				Verb:    "message",
			}
			got := serveBearer(t, h, "POST", "/v1/messages", tok, in)
			if got.Code != c.want {
				t.Fatalf("sender %q: code=%d want %d (%s)",
					c.sender, got.Code, c.want, got.Body.String())
			}
		})
	}
}
