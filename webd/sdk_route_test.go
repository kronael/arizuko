package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/compose"
	"github.com/kronael/arizuko/store"
)

// webd advertises the browser SDK's URL in /config, and proxyd decides whether
// that URL reaches webd at all. The two live in different packages, so the path
// was advertised for months with no route carrying it (BUGS L322). This binds
// them: the advertised path must be covered by a core proxyd route.
func TestAdvertisedSDKPathHasAProxydRoute(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	raw := store.GenRouteToken()
	if err := st.InsertRouteToken(raw, store.RouteToken{
		JID: "web:main", OwnerFolder: "main", TargetFolder: "main",
	}); err != nil {
		t.Fatalf("InsertRouteToken: %v", err)
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	req := httptest.NewRequest("GET", "/chat/"+raw+"/config", nil)
	req.SetPathValue("token", raw)
	w := httptest.NewRecorder()
	s.handleChatTokenConfig(w, req)
	if w.Code != 200 {
		t.Fatalf("config status = %d", w.Code)
	}
	var cfg struct {
		SDK string `json:"sdk"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.SDK == "" {
		t.Fatal("config advertises no sdk path; this test no longer checks what it names")
	}

	// Longest-prefix is proxyd's rule, but coverage is what matters here: some
	// core row must claim the path, or the request never leaves proxyd.
	for _, p := range compose.ReservedRoutePaths() {
		if strings.HasPrefix(cfg.SDK, p) {
			return
		}
	}
	t.Errorf("no core proxyd route covers the advertised SDK path %q; it is unreachable through proxyd", cfg.SDK)
}
