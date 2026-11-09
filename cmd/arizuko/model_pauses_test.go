package main

import (
	"bytes"
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

type pauseCLIVerifier struct{ keys *auth.KeySet }

func (v pauseCLIVerifier) Verify(r *http.Request) (string, []string, string, error) {
	claims, err := auth.VerifyHTTP(r, v.keys)
	if err != nil {
		return "", nil, "", err
	}
	return claims.Sub, claims.Scope, claims.Extra["arz/folder"], nil
}

func TestModelPausesCLIUsesOperatorResourceAndAudit(t *testing.T) {
	st := routdStore(t)
	if err := st.PutGroup(core.Group{Folder: "a"}); err != nil {
		t.Fatal(err)
	}
	_, err := st.OpenTurnContext("held", "a", "", "web:a", "u", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tc, _ := st.TurnContextOf("held")
	pause := &store.ModelPause{Folder: "a", Reason: "spend_limit", Evidence: "spend_limit", OpenedAt: core.NowTS(), Attempts: 1}
	_, err = st.RecordControlledProviderFailure(t.Context(), tc, nil, pause, nil,
		&store.TurnControlState{Phase: "open", Episode: "refusal", NextProbeAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	key, err := auth.NewSigningKey("pause-cli")
	if err != nil {
		t.Fatal(err)
	}
	keys := auth.NewKeySet(map[string]*ecdsa.PublicKey{"pause-cli": &key.Priv.PublicKey})
	srv := httptest.NewServer(routd.NewServer(routd.OpenOver(st.Obj()), nil, nil, pauseCLIVerifier{keys}, 0, "").Handler())
	defer srv.Close()
	mint := func(sub string, scope []string) string {
		token, err := key.Sign(auth.TokenClaims{Sub: sub, Typ: "service", Scope: scope}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	op := mint("service:dashd", []string{"model_pauses:read", "model_pauses:write"})
	member := mint("member", []string{"a/**"})
	var out bytes.Buffer
	for _, action := range []string{"list", "clear"} {
		if err := runModelPauses(srv.Client(), srv.URL, member, action, "a", &out); err == nil {
			t.Fatalf("member %s allowed", action)
		}
	}
	if err := runModelPauses(srv.Client(), srv.URL, op, "list", "", &out); err != nil || !strings.Contains(out.String(), "spend_limit") {
		t.Fatalf("list=%s %v", out.String(), err)
	}
	if err := runModelPauses(srv.Client(), srv.URL, op, "clear", "", &out); err == nil {
		t.Fatal("clear without folder succeeded")
	}
	if err := runModelPauses(srv.Client(), srv.URL, op, "clear", "b", &out); err == nil {
		t.Fatal("clear selected another folder")
	}
	if err := runModelPauses(srv.Client(), srv.URL, op, "clear", "a", &out); err != nil {
		t.Fatal(err)
	}
	if p, err := st.ReadModelPause(t.Context(), "a"); err != nil || p != nil {
		t.Fatalf("clear=%+v %v", p, err)
	}
	rows, err := audit.Query(t.Context(), st.Obj(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, row := range rows {
		if row.Action == "model_pauses:clear" && row.Outcome == audit.OutcomeOK {
			n++
			if row.Actor != "service:dashd" || row.Surface != audit.SurfaceREST {
				t.Fatalf("audit=%+v", row)
			}
		}
	}
	if n != 1 {
		t.Fatalf("clear audits=%d", n)
	}
	if err := runModelPauses(srv.Client(), srv.URL, op, "clear", "a", &out); err == nil {
		t.Fatal("clear without pause claimed success")
	}
}
