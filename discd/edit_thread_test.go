package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

func TestEditHTTPUsesExplicitThreadDestination(t *testing.T) {
	rec := &recorded{}
	srv := newRecordingDiscordServer(t, rec)
	defer srv.Close()
	defer mockAllDiscordEndpoints(srv.URL)()
	b := &bot{session: newTestSession(t)}
	mux := chanlib.NewAdapterMux("discord", []string{"discord:"}, b,
		func() bool { return true }, func() int64 { return 0 }, nil)
	for _, scenario := range []struct{ thread, channel string }{
		{"th-7", "th-7"}, {"", "ch-1"},
	} {
		t.Run(scenario.channel, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/edit", strings.NewReader(
				`{"chat_jid":"discord:g1/ch-1","target_id":"msg-1","content":"done","thread_id":"`+scenario.thread+`"}`))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("Edit status=%d body=%s", w.Code, w.Body.String())
			}
			requests := rec.snapshot()
			got := requests[len(requests)-1]
			if got.Method != http.MethodPatch || got.Path != "/channels/"+scenario.channel+"/messages/msg-1" {
				t.Fatalf("Edit reached %s %s, want channel %s", got.Method, got.Path, scenario.channel)
			}
		})
	}
}
