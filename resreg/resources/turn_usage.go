package resources

import (
	"net/http"
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// TurnUsageRow is one model's usage in one turn, as the harness reported it
// (specs/6/39-gateway-owns-spend.md §"The turn usage record"). Input is the
// uncached input; CacheRead the cached. CostCents is the harness's own figure,
// which the gateway's supersedes on a Metered row. The credit fields are the
// codex account's and absent elsewhere: CreditsSpent is the balance drop
// across the turn, so a turn on the same account that overlaps it lands in it.
type TurnUsageRow struct {
	Folder            string   `db:"folder" json:"folder"`
	TurnID            string   `db:"turn_id" json:"turn_id"`
	Model             string   `db:"model" json:"model"`
	RecordedAt        string   `json:"recorded_at"`
	Harness           string   `json:"harness"`
	Metered           bool     `json:"metered"`
	Input             int      `json:"input"`
	Output            int      `json:"output"`
	CacheRead         int      `json:"cache_read"`
	CacheWrite        int      `json:"cache_write"`
	CostCents         int      `json:"cost_cents"`
	CreditsSpent      *float64 `json:"credits_spent,omitempty"`
	CreditsBalance    *float64 `json:"credits_balance,omitempty"`
	WindowUsedPercent *float64 `json:"window_used_percent,omitempty"`
	PlanType          string   `json:"plan_type,omitempty"`
}

var TurnUsageMCPNames = map[resreg.Action]string{resreg.ActionList: "list_turn_usage"}

func init() {
	resreg.Register(resreg.Resource{
		Name: TurnUsageName, Table: "turn_results", RowType: reflect.TypeFor[TurnUsageRow](),
		PKFields: []string{"Folder", "TurnID", "Model"},
		Endpoints: []resreg.Endpoint{
			{Action: resreg.ActionList, Verb: "GET", Path: "/v1/turn_usage", Status: http.StatusOK},
		},
		Responses: map[resreg.Action]reflect.Type{resreg.ActionList: reflect.TypeFor[[]TurnUsageRow]()},
		MCPNames:  TurnUsageMCPNames,
		MCPDoc: map[resreg.Action]string{
			resreg.ActionList: "List each turn's tokens, cost and codex credits per model. Operator only.",
		},
		MCPArgs: map[resreg.Action][]resreg.MCPArg{
			resreg.ActionList: {
				{Name: "since", Type: "string", Description: "Only turns recorded at or after this RFC 3339 instant."},
			},
		},
	})
}
