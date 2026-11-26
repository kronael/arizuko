package main

import (
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/anteval/pkg/spec"
	"github.com/kronael/arizuko/store"
)

// The two web-route cases of the anteval catalog each want the status the
// gate gives an unauthenticated client on a routed page of the public slot: a
// `public` row serves it and an `auth` row sends it to the login. A case that
// wants another status can never pass, whatever the agent does.
func TestAntevalWebRouteCasesWantTheGateAnswer(t *testing.T) {
	cases, err := spec.Load("../anteval/cases")
	if err != nil {
		t.Fatal(err)
	}
	access := map[string]string{"web-route": "public", "web-route-private": "auth"}
	for _, c := range cases {
		mode, ok := access[c.ID]
		if !ok {
			continue
		}
		delete(access, c.ID)
		s, up := testServerWithUpstream(t)
		s.rr.installManualWeb([]store.WebRoute{
			{PathPrefix: "/pub/eval/page/", Access: mode, Folder: "eval"},
		})
		w := httptest.NewRecorder()
		s.route(w, httptest.NewRequest("GET", "/pub/eval/page/index.html", nil))
		up.Close()
		if w.Code != c.Check.Want {
			t.Errorf("%s wants %d; the gate answers %d on an access=%s route", c.ID, c.Check.Want, w.Code, mode)
		}
	}
	for id := range access {
		t.Errorf("%s case missing", id)
	}
}
