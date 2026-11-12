package routd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// turnUsage is what routd stores of one submission's usage: the harness's
// per-model report as its JSON, beside the harness and whether the instance is
// metered (specs/6/39-gateway-owns-spend.md §"The turn usage record").
func turnUsage(folder, turnID, harness string, metered bool,
	models map[string]apiv1.ModelUsage) (store.TurnUsage, error) {
	u := store.TurnUsage{Folder: folder, TurnID: turnID, Harness: harness, Metered: metered}
	if len(models) == 0 {
		return u, nil
	}
	raw, err := json.Marshal(models)
	if err != nil {
		return u, fmt.Errorf("encode the usage of %s: %w", turnID, err)
	}
	u.Models = string(raw)
	return u, nil
}

// usageRows renders one turn's stored usage as one row per model, in model
// order. It is the record's one renderer: the log line, the turn_usage
// resource and `arizuko usage` all read its rows.
func usageRows(u store.TurnUsage) ([]resources.TurnUsageRow, error) {
	if u.Models == "" {
		return nil, nil
	}
	var models map[string]apiv1.ModelUsage
	if err := json.Unmarshal([]byte(u.Models), &models); err != nil {
		return nil, fmt.Errorf("decode the usage of %s in %s: %w", u.TurnID, u.Folder, err)
	}
	rows := make([]resources.TurnUsageRow, 0, len(models))
	for _, model := range slices.Sorted(maps.Keys(models)) {
		m := models[model]
		rows = append(rows, resources.TurnUsageRow{
			Folder: u.Folder, TurnID: u.TurnID, Model: model, RecordedAt: u.RecordedAt,
			Harness: u.Harness, Metered: u.Metered,
			Input: m.Input, Output: m.Output, CacheRead: m.CacheRead, CacheWrite: m.CacheWrite,
			CostCents: m.CostCents, CreditsSpent: m.CreditsSpent, CreditsBalance: m.CreditsBalance,
			WindowUsedPercent: m.WindowUsedPercent, PlanType: m.PlanType,
		})
	}
	return rows, nil
}

// logTurnUsage writes the record's rows as the log line `agent self-reported
// model usage`. On a metered instance the gateway holds the cost, and a row
// that disagrees with it is the shape a bypassed gateway takes.
func logTurnUsage(rows []resources.TurnUsageRow) {
	for _, r := range rows {
		attrs := []any{"folder", r.Folder, "turn_id", r.TurnID, "model", r.Model,
			"harness", r.Harness, "input", r.Input, "output", r.Output,
			"cache_read", r.CacheRead, "cache_write", r.CacheWrite,
			"cost_cents", r.CostCents, "metered", r.Metered}
		if r.CreditsSpent != nil {
			attrs = append(attrs, "credits_spent", *r.CreditsSpent)
		}
		if r.CreditsBalance != nil {
			attrs = append(attrs, "credits_balance", *r.CreditsBalance)
		}
		if r.WindowUsedPercent != nil {
			attrs = append(attrs, "window_used_percent", *r.WindowUsedPercent)
		}
		if r.PlanType != "" {
			attrs = append(attrs, "plan_type", r.PlanType)
		}
		slog.Info("agent self-reported model usage", attrs...)
		obs.RecordModelTokens(r.Model, r.Folder, "in", r.Input)
		obs.RecordModelTokens(r.Model, r.Folder, "out", r.Output)
	}
}

// TurnUsage answers every recorded turn's usage rows at or after since; the
// zero time reads them all.
func (d *DB) TurnUsage(ctx context.Context, since time.Time) ([]resources.TurnUsageRow, error) {
	from := ""
	if !since.IsZero() {
		from = core.TS(since)
	}
	turns, err := d.store().ListTurnUsage(ctx, from)
	if err != nil {
		return nil, err
	}
	out := []resources.TurnUsageRow{}
	for _, u := range turns {
		rows, err := usageRows(u)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

func (s *Server) turnUsageResource() resreg.Resource {
	r := resreg.Mount(resources.TurnUsageName)
	r.Store, r.Handler = s.db.store(), s.turnUsageHandler
	return r
}

func (s *Server) mountTurnUsage(mux *http.ServeMux) {
	r := s.turnUsageResource()
	r.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		if s.verify == nil || (x.Caller.Folder == "" && s.db.IsOperator(x.Caller.Sub)) {
			return nil
		}
		return resreg.Errorf(http.StatusForbidden, "turn usage requires an operator")
	}
	resreg.RegisterREST(mux, r, s.restCaller(nil))
}

func (s *Server) turnUsagePostBuild(folder, caller string, authorize authorizeFn,
	visible func(string) bool) func(*mcpserver.MCPServer) {
	r := s.turnUsageResource()
	r.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		if authorize("**", "admin", nil) &&
			authorize("**", "mcp:"+resources.TurnUsageMCPNames[x.Action], nil) {
			return nil
		}
		return resreg.Errorf(http.StatusForbidden, "turn usage requires an operator")
	}
	return mountAgentResource(r, caller, folder, visible)
}

func (s *Server) turnUsageHandler(ctx context.Context, x resreg.Execution) (any, error) {
	if x.Action != resreg.ActionList {
		return nil, resreg.Errorf(http.StatusBadRequest, "unknown turn usage action")
	}
	var since time.Time
	if v := x.Args.Str("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "since: %v", err)
		}
		since = t
	}
	return s.db.TurnUsage(ctx, since)
}
