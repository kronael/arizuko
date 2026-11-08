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
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

func TestModelPausesRESTMCPOperatorAndAuditParity(t *testing.T) {
	for _, surface := range []string{"rest", "mcp"} {
		t.Run(surface, func(t *testing.T) {
			db, l, p, _ := pauseFixture(t, "rate_limit")
			pauseInput(t, db, "web:a", "held", "u", "saved", time.Now())
			processPauseChat(t, l, "web:a")
			pause, _ := db.store().ReadModelPause(t.Context())
			req := map[string]any{"opened_at": pause.OpenedAt, "probe_attempt_id": ""}
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
			if pause, err := db.store().ReadModelPause(t.Context()); err != nil || pause != nil {
				t.Fatalf("clear failed: %+v %v", pause, err)
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
	pause, _ := db.store().ReadModelPause(t.Context())
	broken := OpenOver(refusePauseAudit{db.store().Obj()})
	srv := NewServer(broken, nil, nil, nil, 0, "")
	w := doJSON(t, srv.Handler(), "POST", "/v1/model_pauses/clear", "", map[string]string{"opened_at": pause.OpenedAt})
	if w.Code == 200 {
		t.Fatal("clear succeeded without audit")
	}
	if after, err := db.store().ReadModelPause(t.Context()); err != nil || after == nil {
		t.Fatalf("audit failure lost pause: %+v %v", after, err)
	}
}
