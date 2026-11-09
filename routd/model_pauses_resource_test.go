package routd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg/resources"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

func TestModelPausesListCountsHeldAndLaterInput(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "rate_limit")
	jid := "telegram:group/count"
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a?engaged"}})
	if err := db.SetEngagement(jid, "", "a", time.Hour); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	pauseInput(t, db, jid, "count-held", "sender", "saved", now)
	processPauseChat(t, l, jid)
	if err := db.SetEngagement(jid, "", "a", -time.Hour); err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a"}})
	pauseInput(t, db, jid, "count-later", "sender", "later", now.Add(time.Second))
	w := doJSON(t, p.srv.Handler(), "GET", "/v1/model_pauses", "", nil)
	var rows []resources.ModelPauseRow
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows) != 1 || rows[0].QueuedCount != 2 {
		t.Fatalf("list=%d %s", w.Code, w.Body.String())
	}
	control, _, _ := db.store().ReadTurnControl(t.Context())
	if rows[0].NextAttemptAt == nil || *rows[0].NextAttemptAt != core.TS(control.NextProbeAt) ||
		rows[0].ProbeTurnID != control.ProbeTurnID || rows[0].ProbeAttemptID != control.ProbeAttemptID {
		t.Fatalf("list did not project the instance breaker: %+v", rows[0])
	}
}

func TestModelPausesRESTMCPOperatorAndAuditParity(t *testing.T) {
	for _, surface := range []string{"rest", "mcp"} {
		t.Run(surface, func(t *testing.T) {
			db, l, p, _ := pauseFixture(t, "rate_limit")
			pauseInput(t, db, "web:a", "held", "u", "saved", time.Now())
			processPauseChat(t, l, "web:a")
			control, _, _ := db.store().ReadTurnControl(t.Context())
			pause, _ := db.store().ReadModelPause(t.Context(), "a")
			req := map[string]any{"folder": "a", "opened_at": pause.OpenedAt, "probe_attempt_id": ""}
			if surface == "rest" {
				for _, v := range []fakeVerifier{
					{sub: "member", scope: []string{"a/**"}, folder: "a"},
					{sub: "service:other", scope: []string{"model_pauses:read", "model_pauses:write"}},
				} {
					p.srv.verify = v
					for _, verb := range []string{"GET", "POST"} {
						path := "/v1/model_pauses"
						if verb == "POST" {
							path += "/clear"
						}
						w := doJSON(t, p.srv.Handler(), verb, path, "", req)
						if w.Code != http.StatusForbidden {
							t.Fatalf("member %s=%d %s", verb, w.Code, w.Body.String())
						}
					}
				}
				p.srv.verify = fakeVerifier{sub: "op", scope: []string{"**"}}
				if err := addMembership(db, "op", store.RoleOperator, "test"); err != nil {
					t.Fatal(err)
				}
				w := doJSON(t, p.srv.Handler(), "GET", "/v1/model_pauses", "", nil)
				var rows []resources.ModelPauseRow
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows) != 1 || rows[0].QueuedCount != 1 {
					t.Fatalf("list=%d %s", w.Code, w.Body.String())
				}
				w = doJSON(t, p.srv.Handler(), "POST", "/v1/model_pauses/clear", "", req)
				if w.Code != 200 {
					t.Fatalf("clear=%d %s", w.Code, w.Body.String())
				}
				if again := doJSON(t, p.srv.Handler(), "POST", "/v1/model_pauses/clear", "", req); again.Code != 409 {
					t.Fatal("stale clear succeeded")
				}
			} else {
				for _, elevated := range []bool{false, true} {
					binding := turnMCP{folder: "a", elevated: elevated}
					sock := groupfolder.IpcSocket(t.TempDir())
					post := p.srv.modelPausesPostBuild("a", "folder:a", p.srv.turnAuthorize(binding), nil)
					stop, err := ipc.ServeMCP(sock, p.srv.buildRoutdFns(binding), p.srv.buildStoreFns(binding), "a", elevated, 0, "folder:a", post)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(stop)
					text, e := callToolText(t, sock, "list_model_pauses", nil)
					if !elevated {
						if e == "" {
							t.Fatalf("member list allowed: %s", text)
						}
						if text, e := callToolText(t, sock, "clear_model_pause", req); e == "" {
							t.Fatalf("member clear allowed: %s", text)
						}
						continue
					}
					if e != "" || !strings.Contains(text, `"queued_count":1`) {
						t.Fatalf("operator list: %s %s", text, e)
					}
					if text, e := callToolText(t, sock, "clear_model_pause", req); e != "" {
						t.Fatalf("operator clear: %s %s", text, e)
					}
				}
			}
			if pause, err := db.store().ReadModelPause(t.Context(), "a"); err != nil || pause != nil {
				t.Fatalf("clear failed: %+v %v", pause, err)
			}
			after, _, _ := db.store().ReadTurnControl(t.Context())
			if after != control {
				t.Fatal("audited metadata clear changed the breaker")
			}
			rows := auditRowsFor(t, db, "model_pauses:clear")
			n := 0
			for _, row := range rows {
				if row.Outcome == audit.OutcomeOK {
					n++
					if row.Surface != surface {
						t.Fatalf("audit surface=%s", row.Surface)
					}
				}
			}
			if n != 1 {
				t.Fatalf("successful clear audits=%d", n)
			}
		})
	}
}

func TestModelPausesListCountsAndClearByFolder(t *testing.T) {
	db, _, p, _ := pauseFixture(t, "rate_limit", "outage")
	now := time.Now()
	for _, folder := range []string{"a", "b"} {
		if _, err := db.store().OpenTurnContext("held-"+folder, folder, "", "web:"+folder, "u", "", nil); err != nil {
			t.Fatal(err)
		}
		tc, _ := db.GetTurnContext("held-" + folder)
		fns := p.srv.buildRoutdFns(turnMCP{folder: folder, turnID: tc.TurnID, attempt: tc.AttemptID})
		if _, err := fns.SubmitTurn(folder, ipc.TurnResult{TurnID: tc.TurnID, Status: "error", ProviderFailure: &apiv1.ProviderFailure{Reason: "outage"}}); err != nil {
			t.Fatal(err)
		}
	}
	pauseInput(t, db, "web:a", "a1", "u", "one", now)
	pauseInput(t, db, "web:b", "b1", "u", "one", now)
	pauseInput(t, db, "web:b", "b2", "u", "two", now.Add(time.Second))
	w := doJSON(t, p.srv.Handler(), "GET", "/v1/model_pauses", "", nil)
	var rows []resources.ModelPauseRow
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows) != 2 {
		t.Fatalf("list=%d %s", w.Code, w.Body.String())
	}
	counts := map[string]int{}
	for _, row := range rows {
		counts[row.Folder] = row.QueuedCount
	}
	if counts["a"] != 1 || counts["b"] != 2 {
		t.Fatalf("counts=%v", counts)
	}
	a, err := db.store().ReadModelPause(t.Context(), "a")
	if err != nil || a == nil {
		t.Fatalf("pause a=%+v %v", a, err)
	}
	control, _, _ := db.store().ReadTurnControl(t.Context())
	w = doJSON(t, p.srv.Handler(), "POST", "/v1/model_pauses/clear", "", map[string]string{"folder": "a", "opened_at": a.OpenedAt, "probe_attempt_id": control.ProbeAttemptID})
	if w.Code != 200 {
		t.Fatalf("clear=%d %s", w.Code, w.Body.String())
	}
	if a, _ := db.store().ReadModelPause(t.Context(), "a"); a != nil {
		t.Fatalf("a remains: %+v", a)
	}
	if b, _ := db.store().ReadModelPause(t.Context(), "b"); b == nil {
		t.Fatal("clearing a removed b")
	}
}

type refusePauseAudit struct{ objstore.Store }

func (s refusePauseAudit) GuardedBatch(ctx context.Context, c []objstore.Check, w []objstore.Write) (bool, error) {
	for _, write := range w {
		if write.Entity == audit.Entity {
			return false, errors.New("audit unavailable")
		}
	}
	return s.Store.GuardedBatch(ctx, c, w)
}

func TestModelPausesClearAuditFailureIsAtomic(t *testing.T) {
	db, l, _, _ := pauseFixture(t, "outage")
	pauseInput(t, db, "web:a", "held", "u", "saved", time.Now())
	processPauseChat(t, l, "web:a")
	pause, _ := db.store().ReadModelPause(t.Context(), "a")
	broken := OpenOver(refusePauseAudit{db.store().Obj()})
	srv := NewServer(broken, nil, nil, nil, 0, "")
	w := doJSON(t, srv.Handler(), "POST", "/v1/model_pauses/clear", "", map[string]string{"folder": "a", "opened_at": pause.OpenedAt})
	if w.Code == 200 {
		t.Fatal("clear succeeded without audit")
	}
	if after, err := db.store().ReadModelPause(t.Context(), "a"); err != nil || after == nil {
		t.Fatalf("audit failure lost pause: %+v %v", after, err)
	}
}
