package main

import (
	"context"
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

type pauseDashVerifier struct{ keys *auth.KeySet }

func (v pauseDashVerifier) Verify(r *http.Request) (string, []string, string, error) {
	claims, err := auth.VerifyHTTP(r, v.keys)
	if err != nil {
		return "", nil, "", err
	}
	return claims.Sub, claims.Scope, claims.Extra["arz/folder"], nil
}

func TestModelPausesDashboardOperatorClearAndAudit(t *testing.T) {
	obj := routdDB(t)
	db := routd.OpenOver(obj)
	st := store.NewOver(obj)
	if err := st.PutGroup(core.Group{Folder: "a"}); err != nil {
		t.Fatal(err)
	}
	_, err := st.OpenTurnContext("held", "a", "", "web:a", "u", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tc, _ := st.TurnContextOf("held")
	pause := &store.ModelPause{Folder: "a", Reason: "rate_limit", Evidence: "rate_limit", OpenedAt: core.NowTS(), Attempts: 1}
	_, err = st.RecordControlledProviderFailure(t.Context(), tc, nil, pause, nil,
		&store.TurnControlState{Phase: "open", Episode: "refusal", NextProbeAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	key, err := auth.NewSigningKey("pause-dash")
	if err != nil {
		t.Fatal(err)
	}
	token, err := key.Sign(auth.TokenClaims{Sub: "service:dashd", Typ: "service", Scope: []string{"model_pauses:read", "model_pauses:write"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	keys := auth.NewKeySet(map[string]*ecdsa.PublicKey{"pause-dash": &key.Priv.PublicKey})
	upstream := httptest.NewServer(routd.NewServer(db, nil, nil, pauseDashVerifier{keys}, 0, "").Handler())
	defer upstream.Close()
	d := &dash{routd: obj, routdURL: upstream.URL, svc: func(context.Context) (string, error) { return token, nil }}
	mux := newMux(d)
	get := httptest.NewRecorder()
	mux.ServeHTTP(get, asOperator(httptest.NewRequest("GET", "/dash/model-pauses/", nil)))
	if get.Code != 200 || !strings.Contains(get.Body.String(), "Clear pause") || !strings.Contains(get.Body.String(), pause.OpenedAt) {
		t.Fatalf("page=%d %s", get.Code, get.Body.String())
	}
	if !strings.Contains(get.Body.String(), ">attempts</th>") || !strings.Contains(get.Body.String(), ">1</td>") {
		t.Fatalf("pause attempt count missing: %s", get.Body.String())
	}
	for _, operator := range []bool{false, true} {
		for _, foreign := range []bool{false, true} {
			form := url.Values{"folder": {"a"}, "opened_at": {pause.OpenedAt}, "probe_attempt_id": {""}}
			req := httptest.NewRequest("POST", "http://dash.test/dash/model-pauses/clear", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", "http://dash.test")
			if foreign {
				req.Header.Set("Origin", "https://foreign.test")
			}
			if operator {
				req = asOperator(req)
			} else {
				req.Header.Set("X-User-Sub", "member")
				req.Header.Set("X-User-Groups", `["a/**"]`)
			}
			if operator && !foreign {
				continue
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != 403 {
				t.Fatalf("unauthorized clear=%d", w.Code)
			}
		}
	}
	form := url.Values{"folder": {"a"}, "opened_at": {pause.OpenedAt}, "probe_attempt_id": {""}}
	req := asOperator(httptest.NewRequest("POST", "http://dash.test/dash/model-pauses/clear", strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://dash.test")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 303 {
		t.Fatalf("clear=%d %s", w.Code, w.Body.String())
	}
	if p, err := st.ReadModelPause(t.Context(), "a"); err != nil || p != nil {
		t.Fatalf("pause=%+v %v", p, err)
	}
	rows, err := audit.Query(t.Context(), obj, audit.Filter{})
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
}
