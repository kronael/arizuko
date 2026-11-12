package routd

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg/resources"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// A submitted turn's usage is one record: the row GET /v1/turn_usage serves
// and the log line `agent self-reported model usage` carry the same fields,
// codex credits included, one per model.
func TestTurnUsageIsOneRecordForTheLogAndTheRow(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.store().PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutTurnContext("t9", "demo", "main", "web:demo", "u1", ""); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	spent, balance, window := 1734.108289, 28070.39, 100.0
	srv := NewServer(db, nil, nil, nil, 0, "")
	body := apiv1.TurnResult{TurnID: "t9", SessionID: "sX", Status: "success", Harness: "codex",
		Models: map[string]apiv1.ModelUsage{
			"gpt-5.5": {Input: 41113, Output: 1500, CacheRead: 400000, CreditsSpent: &spent,
				CreditsBalance: &balance, WindowUsedPercent: &window, PlanType: "pro"},
			"gpt-5.5-mini": {Input: 5, Output: 1},
		}}
	if ack, err := srv.recordTurnResult("t9", body); err != nil || !ack.OK {
		t.Fatalf("record: %+v %v", ack, err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/turn_usage", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/turn_usage = %d: %s", rec.Code, rec.Body)
	}
	var rows []resources.TurnUsageRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].RecordedAt == "" {
		t.Fatalf("rows = %+v, want two, one per model, with the recorded instant", rows)
	}
	rows[0].RecordedAt = ""
	want := resources.TurnUsageRow{Folder: "demo", TurnID: "t9", Model: "gpt-5.5", Harness: "codex",
		Input: 41113, Output: 1500, CacheRead: 400000, CreditsSpent: &spent,
		CreditsBalance: &balance, WindowUsedPercent: &window, PlanType: "pro"}
	if !reflect.DeepEqual(rows[0], want) {
		t.Errorf("row = %+v, want %+v", rows[0], want)
	}

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	var usageLines []string
	for _, l := range lines {
		if strings.Contains(l, "agent self-reported model usage") {
			usageLines = append(usageLines, l)
		}
	}
	if len(usageLines) != 2 {
		t.Fatalf("usage log lines = %q, want one per model", usageLines)
	}
	for _, field := range []string{"folder=demo", "turn_id=t9", "model=gpt-5.5", "harness=codex",
		"input=41113", "cache_read=400000", "credits_spent=1734.108289",
		"credits_balance=28070.39", "window_used_percent=100", "plan_type=pro", "metered=false"} {
		if !strings.Contains(usageLines[0], field) {
			t.Errorf("log line %q lacks %s", usageLines[0], field)
		}
	}
}

// The usage ledger answers an operator only.
func TestTurnUsageRESTRefusesANonOperator(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := NewServer(db, nil, nil, scopeVerifier{sub: "service:x", scope: []string{"**"}}, 0, "")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/turn_usage", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /v1/turn_usage as a non-operator = %d, want 403: %s", rec.Code, rec.Body)
	}
}
