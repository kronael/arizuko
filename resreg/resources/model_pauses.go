package resources

import (
	"net/http"
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

type ModelPauseRow struct {
	Reason         string  `json:"reason"`
	OpenedAt       string  `db:"opened_at" json:"opened_at"`
	NextAttemptAt  *string `json:"next_attempt_at"`
	Attempts       int     `json:"attempts"`
	Evidence       string  `json:"evidence"`
	ProbeTurnID    string  `json:"probe_turn_id"`
	ProbeAttemptID string  `json:"probe_attempt_id"`
	QueuedCount    int     `json:"queued_count"`
}

var ModelPausesMCPNames = map[resreg.Action]string{
	resreg.ActionList: "list_model_pauses", resreg.Action("clear"): "clear_model_pause",
}

func init() {
	resreg.Register(resreg.Resource{
		Name: ModelPausesName, Table: "model_pauses", RowType: reflect.TypeFor[ModelPauseRow](),
		PKFields: []string{"OpenedAt"},
		Endpoints: []resreg.Endpoint{
			{Action: resreg.ActionList, Verb: "GET", Path: "/v1/model_pauses", Status: http.StatusOK},
			{Action: resreg.Action("clear"), Verb: "POST", Path: "/v1/model_pauses/clear", Status: http.StatusOK},
		},
		Responses: map[resreg.Action]reflect.Type{resreg.ActionList: reflect.TypeFor[[]ModelPauseRow]()},
		MCPNames:  ModelPausesMCPNames,
		MCPDoc: map[resreg.Action]string{
			resreg.ActionList:      "Show the instance model pause and saved input count. Operator only.",
			resreg.Action("clear"): "Clear the listed model pause and resume saved input. Operator only.",
		},
		MCPArgs: map[resreg.Action][]resreg.MCPArg{
			resreg.Action("clear"): {
				{Name: "opened_at", Type: "string", Required: true, Description: "Episode from list_model_pauses."},
				{Name: "probe_attempt_id", Type: "string", Description: "Bound attempt from list_model_pauses, empty while waiting."},
			},
		},
	})
}
