package routd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/ipc"
)

// presenceSrv builds a server with a known web host + vhost config and the
// given verifier, returning its handler. Mirrors authSrv but exercises the
// /v1/web_presence reporting surface.
func presenceSrv(t *testing.T, v Verifier) http.Handler {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, &recDeliverer{}, v, 0, "krons.fiu.wtf")
	srv.SetVhosts("fiu.wtf", map[string]string{"fab.krons.cx": "atlas"})
	return srv.Handler()
}

func getPresence(t *testing.T, h http.Handler, path string) (int, ipc.WebPresence) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	var out ipc.WebPresence
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal %s: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

// The REST twin returns the same shape as the get_web_presence MCP tool and
// enforces the same folder containment (one renderer, many sinks).
func TestRESTWebPresence(t *testing.T) {
	read := []string{"routes:read:own_group"}

	// Scoped caller, own folder (derived host).
	h := presenceSrv(t, fakeVerifier{sub: "user:u", scope: read, folder: "world"})
	code, p := getPresence(t, h, "/v1/web_presence")
	if code != 200 {
		t.Fatalf("own presence = %d want 200", code)
	}
	if p.DerivedHost != "world.fiu.wtf" || p.CanonicalHost != "world.fiu.wtf" {
		t.Errorf("derived/canonical = %q/%q want world.fiu.wtf", p.DerivedHost, p.CanonicalHost)
	}
	if p.PubPath != "https://krons.fiu.wtf/pub/world/" {
		t.Errorf("pub_path = %q", p.PubPath)
	}

	// Cross-folder query denied for a scoped caller.
	if code, _ := getPresence(t, h, "/v1/web_presence?folder=other"); code != 403 {
		t.Fatalf("cross-folder presence = %d want 403", code)
	}

	// Root (empty token folder) may query any folder; alias overrides canonical.
	hr := presenceSrv(t, fakeVerifier{sub: "user:r", scope: read, folder: ""})
	code, p = getPresence(t, hr, "/v1/web_presence?folder=atlas")
	if code != 200 {
		t.Fatalf("root presence = %d want 200", code)
	}
	if p.AliasHost != "fab.krons.cx" || p.CanonicalHost != "fab.krons.cx" {
		t.Errorf("alias/canonical = %q/%q want fab.krons.cx", p.AliasHost, p.CanonicalHost)
	}
}
