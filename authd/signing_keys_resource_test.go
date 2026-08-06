package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// authd is the sole ES256 signer, so a new read surface over signing_keys is
// tested for what it CANNOT say before what it can.

// TestSigningKeysNeverServeKeyMaterial is the content-level leak test. It reads
// the private PEM out of the DB first and asserts it is there, so "the body
// does not contain it" cannot pass because the fixture was empty — the failure
// mode that shipped four vacuous audit tests this week. Then it looks for the
// PEM itself, its armour, and the JWK private exponent, on the RAW body rather
// than on a decoded struct: decoding into SigningKeysRow would discard an extra
// field and report clean, which is precisely the leak worth catching.
func TestSigningKeysNeverServeKeyMaterial(t *testing.T) {
	_, a := auditTestAuthd(t)
	_, ts := newServer(t, a)

	var privPEM string
	if err := a.db.QueryRow(
		`SELECT priv_pem FROM signing_keys WHERE active = 1`).Scan(&privPEM); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(privPEM, "PRIVATE KEY") {
		t.Fatalf("fixture holds no private PEM (%q) — every assertion below would "+
			"pass vacuously", privPEM)
	}
	// The body of the PEM, not its header: the armour alone might plausibly
	// appear in prose, the base64 payload cannot.
	secret := strings.TrimSpace(strings.Split(privPEM, "-----")[2])
	if len(secret) < 32 {
		t.Fatalf("private PEM payload too short to be a meaningful needle: %q", secret)
	}

	tok := operatorToken(t, a, "signing_keys:read")
	code, body := auditGET(t, ts.URL, "/v1/signing_keys", tok)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", code, body)
	}
	// Positive control: the response really is the key metadata, so the
	// absences below are absences from a populated body.
	var rows []resources.SigningKeysRow
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("body is not a row array (%v): %s", err, body)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want the 1 boot key: %s", len(rows), body)
	}
	if rows[0].Kid == "" || rows[0].Alg != "ES256" || !rows[0].Active {
		t.Fatalf("boot key metadata is wrong: %+v", rows[0])
	}

	for _, needle := range []string{secret, "PRIVATE KEY", `"priv_pem"`, `"d":`} {
		if strings.Contains(body, needle) {
			t.Errorf("signing-key response leaked %q: %s", needle, body)
		}
	}
}

// TestAuthdOpenAPIAdvertisesReadsWithoutSecrets covers the discoverability half
// of both new resources at once: authd's doc must name the paths it actually
// mounts, and the engine-generated schemas must carry no private column.
//
// This is a real second surface, not a restatement of the handler tests. The
// doc is reflected off the Row STRUCTS, so a field added to SigningKeysRow or
// SessionsRow appears here whether or not any handler ever fills it — which is
// exactly how a private column would reach the wire without a handler test
// noticing.
func TestAuthdOpenAPIAdvertisesReadsWithoutSecrets(t *testing.T) {
	// The same resource list main.go passes to OpenAPIHandler.
	doc, err := resreg.OpenAPI("authd", "/", []string{"audit", "signing_keys", "sessions"})
	if err != nil {
		t.Fatal(err)
	}
	body := string(doc)
	for _, path := range []string{"/v1/signing_keys", "/v1/sessions", "/v1/sessions/{family_id}"} {
		if !strings.Contains(body, path) {
			t.Errorf("openapi.json does not advertise %s — it is mounted but undiscoverable", path)
		}
	}
	for _, secret := range []string{"priv_pem", "pub_pem", "token_hash", "used_at", "revoked_at"} {
		if strings.Contains(body, secret) {
			t.Errorf("openapi.json names the private column %q: %s", secret, body)
		}
	}
}

// TestSigningKeysRequireScope: a valid bearer holding a real, currently-granted
// scope is 403. identity:read is the adversarial choice — a no-scope token
// would be refused by any gate, including an inverted one.
func TestSigningKeysRequireScope(t *testing.T) {
	_, a := auditTestAuthd(t)
	_, ts := newServer(t, a)

	tok, err := a.MintForSubject("service:routd", "service", nil, []string{"identity:read"}, "")
	if err != nil {
		t.Fatal(err)
	}
	code, body := auditGET(t, ts.URL, "/v1/signing_keys", tok)
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 without signing_keys:read: %s", code, body)
	}
	if strings.Contains(body, "ES256") {
		t.Errorf("denied response leaked key metadata: %s", body)
	}
}

// TestSigningKeysUnauthenticatedIs401 pins that the mount sits behind the
// bearer check and not merely behind the scope check.
func TestSigningKeysUnauthenticatedIs401(t *testing.T) {
	_, a := auditTestAuthd(t)
	_, ts := newServer(t, a)

	if code, body := auditGET(t, ts.URL, "/v1/signing_keys", ""); code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 with no bearer: %s", code, body)
	}
}

// TestSigningKeysRefuseFolderClaim: signing keys are instance-global and the
// table has no folder column, so a folder-bound caller has nothing that could
// contain it. Serving it everything is the recorded cross-tenant list-all leak;
// this asserts the 403 instead. Adversarial by construction — the token holds
// the correct scope, so only the folder check can produce the refusal.
func TestSigningKeysRefuseFolderClaim(t *testing.T) {
	_, a := auditTestAuthd(t)
	_, ts := newServer(t, a)

	m, err := a.IssuerMint("service:dashd", "service", []string{"signing_keys:read"},
		[]string{"signing_keys:read"}, "acme", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	code, body := auditGET(t, ts.URL, "/v1/signing_keys", m.token)
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a folder-bound caller: %s", code, body)
	}
	if strings.Contains(body, "ES256") {
		t.Errorf("denied response leaked key metadata: %s", body)
	}
}

// TestSigningKeysStatusTracksTheServingWindow pins the derived half — the part
// /v1/keys drops and the reason this resource exists. After a rotation the old
// key is `retiring` (still verifying tokens it signed), and once the serving
// window has passed it is `retired`. Both are computed from retired_at +
// maxAccessTTL, the rule spec 5/1 § JWK rotation mechanics states.
func TestSigningKeysStatusTracksTheServingWindow(t *testing.T) {
	_, a := auditTestAuthd(t)
	if err := a.Rotate(); err != nil {
		t.Fatal(err)
	}

	at := time.Now()
	rows, err := selectSigningKeys(a.db, a.maxAccessTTL, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d keys after one rotation, want 2", len(rows))
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Status] = r.Kid
	}
	if got["active"] == "" || got["retiring"] == "" {
		t.Fatalf("want one active + one retiring key, got %+v", rows)
	}

	// Same rows, read past the retired key's serving window.
	later, err := selectSigningKeys(a.db, a.maxAccessTTL, at.Add(a.maxAccessTTL+time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var retired int
	for _, r := range later {
		if r.Status == "retired" {
			retired++
		}
	}
	if retired != 1 {
		t.Fatalf("past the window want exactly 1 retired key, got %d: %+v", retired, later)
	}
}
