package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

func TestHandleLinkCode_CreatesIdentityFirstTime(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	defer s.Close()

	h := handleLinkCode(s)
	r := httptest.NewRequest("POST", "/auth/link-code", nil)
	r.Header.Set("X-User-Sub", "github:42")
	r.Header.Set("X-User-Name", "alice")
	w := httptest.NewRecorder()

	h(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Code     string `json:"code"`
		TTL      int    `json:"ttl"`
		Identity string `json:"identity"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, w.Body.String())
	}
	if !strings.HasPrefix(resp.Code, "link-") {
		t.Errorf("code = %q, want prefix link-", resp.Code)
	}
	if resp.Identity == "" {
		t.Error("identity empty")
	}

	// The caller's sub should have been auto-claimed.
	idn, subs, ok := s.GetIdentityForSub("github:42")
	if !ok {
		t.Fatal("self-sub not auto-claimed")
	}
	if idn.ID != resp.Identity {
		t.Errorf("identity mismatch: %s vs %s", idn.ID, resp.Identity)
	}
	if len(subs) != 1 || subs[0] != "github:42" {
		t.Errorf("subs: got %v, want [github:42]", subs)
	}
}

func TestHandleLinkCode_RotatesOnSecondCall(t *testing.T) {
	s, _ := store.OpenMem()
	defer s.Close()

	h := handleLinkCode(s)
	mint := func() string {
		r := httptest.NewRequest("POST", "/auth/link-code", nil)
		r.Header.Set("X-User-Sub", "github:42")
		r.Header.Set("X-User-Name", "alice")
		w := httptest.NewRecorder()
		h(w, r)
		var resp struct {
			Code     string `json:"code"`
			Identity string `json:"identity"`
		}
		json.Unmarshal(w.Body.Bytes(), &resp)
		return resp.Code
	}
	a, b := mint(), mint()
	if a == b {
		t.Errorf("second mint should rotate; got %q twice", a)
	}
}

func TestHandleLinkCode_RequiresSub(t *testing.T) {
	s, _ := store.OpenMem()
	defer s.Close()

	r := httptest.NewRequest("POST", "/auth/link-code", nil)
	w := httptest.NewRecorder()
	handleLinkCode(s)(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}
