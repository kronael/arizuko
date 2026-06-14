package routd

// Channel register/list gate under ES256: with a Verifier wired, an adapter's
// service:<adapter> token is admitted and a non-service token / no token is 401.
// The open-when-no-verifier (local dev) path is covered in channels_test.go.

import (
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanreg"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// es256Verifier is the in-package stand-in for cmd/routd's keysetVerifier: it
// verifies the bearer against a local KeySet and returns the subject.
type es256Verifier struct{ ks *auth.KeySet }

func (v es256Verifier) Verify(r *http.Request) (sub string, scope []string, folder string, err error) {
	s, verr := auth.VerifyHTTP(r, v.ks)
	if verr != nil {
		return "", nil, "", verr
	}
	return s.Sub, s.Scope, s.Extra["arz/folder"], nil
}

// mintFor signs an ES256 token for sub/typ and returns it + the verifying KeySet.
func mintFor(t *testing.T, sub, typ string) (string, *auth.KeySet) {
	t.Helper()
	key, err := auth.NewSigningKey("k-chan")
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	tok, err := key.Sign(auth.TokenClaims{Sub: sub, Typ: typ, Scope: []string{"messages:write"}}, time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tok, auth.NewKeySet(map[string]*ecdsa.PublicKey{"k-chan": &key.Priv.PublicKey})
}

// newES256ChannelRoutd builds a routd Server whose register/list gate verifies
// ES256 tokens against ks.
func newES256ChannelRoutd(t *testing.T, ks *auth.KeySet) http.Handler {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, nil, es256Verifier{ks}, 0, "")
	srv.SetChannelRegistry(chanreg.New(), nil, nil)
	return srv.Handler()
}

// TestChannelRegisterES256AdmitsAdapter: a valid service:teled token registers.
func TestChannelRegisterES256AdmitsAdapter(t *testing.T) {
	tok, ks := mintFor(t, "service:teled", "service")
	h := newES256ChannelRoutd(t, ks)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chanReq("POST", "/v1/channels/register", tok, map[string]any{
		"name": "teled", "url": "http://teled:8080",
		"jid_prefixes": []string{"telegram:"},
		"capabilities": map[string]bool{"send_text": true},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("register with service token: status=%d body=%s", rec.Code, rec.Body.String())
	}
	// list is gated the same way; the adapter token reads it too.
	lrec := httptest.NewRecorder()
	h.ServeHTTP(lrec, chanReq("GET", "/v1/channels", tok, nil))
	if lrec.Code != http.StatusOK {
		t.Fatalf("list with service token: status=%d", lrec.Code)
	}
}

// TestChannelRegisterES256RejectsBadToken: no bearer, a non-JWT string, and the
// legacy CHANNEL_SECRET are all 401 once the ES256 gate is active.
func TestChannelRegisterES256RejectsBadToken(t *testing.T) {
	_, ks := mintFor(t, "service:teled", "service")
	h := newES256ChannelRoutd(t, ks)

	for _, bearer := range []string{"", "not-a-jwt", testChanSecret} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, chanReq("POST", "/v1/channels/register", bearer, map[string]any{
			"name": "teled", "url": "http://teled:8080", "jid_prefixes": []string{"telegram:"},
		}))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("bearer %q: status=%d want 401", bearer, rec.Code)
		}
	}
}

// TestChannelRegisterES256RejectsNonService: a user token (valid signature but
// typ=user, sub without the service: prefix) is denied — only service principals
// register channels.
func TestChannelRegisterES256RejectsNonService(t *testing.T) {
	tok, ks := mintFor(t, "user:42", "user")
	h := newES256ChannelRoutd(t, ks)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chanReq("POST", "/v1/channels/register", tok, map[string]any{
		"name": "teled", "url": "http://teled:8080", "jid_prefixes": []string{"telegram:"},
	}))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("user token: status=%d want 401", rec.Code)
	}
}

// TestChannelRegisterES256OriginPinSurvivesRotation: re-registering the same
// name from the same caller succeeds even though the bearer token differs (the
// pin is on the verified service principal, not the rotating JWT). A different
// principal claiming the name is rejected.
func TestChannelRegisterES256OriginPinSurvivesRotation(t *testing.T) {
	// One key verifies both tokens; two distinct JWTs for the SAME principal
	// model a token refresh between registrations.
	key, err := auth.NewSigningKey("k-chan")
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	ks := auth.NewKeySet(map[string]*ecdsa.PublicKey{"k-chan": &key.Priv.PublicKey})
	mint := func(sub string) string {
		tok, err := key.Sign(auth.TokenClaims{Sub: sub, Typ: "service", Scope: []string{"messages:write"}}, time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return tok
	}
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, nil, es256Verifier{ks}, 0, "")
	srv.SetChannelRegistry(chanreg.New(), nil, nil)
	h := srv.Handler()

	body := map[string]any{
		"name": "teled", "url": "http://teled:8080", "jid_prefixes": []string{"telegram:"},
	}
	// First registration by service:teled.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chanReq("POST", "/v1/channels/register", mint("service:teled"), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("first register: status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Re-register after a token refresh (different JWT, same principal) → admitted.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, chanReq("POST", "/v1/channels/register", mint("service:teled"), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("re-register after rotation: status=%d body=%s", rec.Code, rec.Body.String())
	}
	// A different principal claiming the same name → rejected (hijack guard).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, chanReq("POST", "/v1/channels/register", mint("service:whapd"), body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("hijack by different principal: status=%d want 409", rec.Code)
	}
}

// TestIngressJIDOwnershipES256: ingress rejects when the verified adapter
// principal doesn't own the JID prefix. service:teled owns telegram: but not
// slack:, so a slack: inbound from teled is denied.
func TestIngressJIDOwnershipES256(t *testing.T) {
	tok, ks := mintFor(t, "service:teled", "service")
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, nil, es256Verifier{ks}, 0, "")
	srv.SetChannelRegistry(chanreg.New(), nil, nil)
	h := srv.Handler()

	// register service:teled owning telegram:
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chanReq("POST", "/v1/channels/register", tok, map[string]any{
		"name": "teled", "url": "http://teled:8080",
		"jid_prefixes": []string{"telegram:"},
		"capabilities": map[string]bool{"send_text": true},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}

	cases := []struct {
		name string
		jid  string
		want int
	}{
		{"owned telegram", "telegram:42", http.StatusOK},
		{"unowned slack", "slack:T1/C/U", http.StatusBadRequest},
		{"web exempt", "web:demo", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := doJSONWithBearer(t, h, "POST", "/v1/messages", tok, "",
				apiv1.Message{ChatJID: c.jid, Sender: "u", Content: "hi", Verb: "message"})
			if rec.Code != c.want {
				t.Fatalf("jid %q: code=%d want %d (%s)", c.jid, rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

func doJSONWithBearer(t *testing.T, h http.Handler, method, path, bearer, idemKey string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if idemKey != "" {
		req.Header.Set("X-Idempotency-Key", idemKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
