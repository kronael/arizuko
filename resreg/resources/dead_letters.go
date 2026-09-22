package resources

import (
	"net/http"
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// DeadLetterRow is one outbound message routd gave up on: `messages` with
// status='failed' AND is_bot_message=1. There is no dead-letter TABLE;
// 'failed' IS the state, and `delivery_attempts` tells the two causes apart:
// 0 means the platform refused it permanently, the cap means the retries ran
// out (BUGS L23).
type DeadLetterRow struct {
	ID               string `db:"id"                json:"id"`
	ChatJID          string `db:"chat_jid"          json:"chat_jid"`
	Content          string `db:"content"           json:"content"`
	Timestamp        string `db:"timestamp"         json:"timestamp"`
	Topic            string `db:"topic"             json:"topic,omitempty"`
	RoutedTo         string `db:"routed_to"         json:"routed_to,omitempty"`
	Source           string `db:"source"            json:"source,omitempty"`
	TurnID           string `db:"turn_id"           json:"turn_id,omitempty"`
	DeliveryAttempts int    `db:"delivery_attempts" json:"delivery_attempts"`
}

// DeadLettersResponses: the list handler PROJECTS core.Message onto the row;
// replay answers an ad-hoc map and declares nothing.
var DeadLettersResponses = map[resreg.Action]reflect.Type{
	resreg.ActionList: reflect.TypeFor[[]DeadLetterRow](),
}

// DeadLettersEndpoints: no create/update/delete, since the row is the only
// evidence a message was lost. `replay` is the ONE mutation and not a second
// delivery mechanism: it flips the row back to 'pending' for routd's existing
// outbound sweep.
var DeadLettersEndpoints = []resreg.Endpoint{
	{Action: resreg.ActionList, Verb: "GET", Path: "/v1/dead_letters", Status: http.StatusOK},
	{Action: resreg.Action("replay"), Verb: "POST", Path: "/v1/dead_letters/{id}/replay", Status: http.StatusOK},
}

var DeadLettersMCPNames = map[resreg.Action]string{
	resreg.ActionList:       "list_dead_letters",
	resreg.Action("replay"): "replay_dead_letter",
}

var DeadLettersMCPDoc = map[resreg.Action]string{
	resreg.ActionList: "List outbound messages routd gave up delivering in `folder` — " +
		"what was lost, to which chat, and how many attempts it took before the give-up. " +
		"`delivery_attempts` 0 means the platform refused the message permanently; the " +
		"configured cap means the retries ran out. Use it to answer 'did my reply " +
		"actually reach them' when a user says they never saw one.",
	resreg.Action("replay"): "Put one dead-lettered message back on the delivery queue by `id`. " +
		"The row returns to `pending` with a fresh attempt budget and routd's outbound " +
		"sweep sends it again — nothing is re-composed, the original text goes out. " +
		"Replay a message the adapter refused permanently (a deleted chat, a bad " +
		"target) and it will simply dead-letter again with the same reason.",
}

var DeadLettersMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.ActionList: {
		{Name: "folder", Type: "string",
			Description: "Restrict to this group folder and everything under it. " +
				"An agent socket is pinned to its own folder and this cannot widen it."},
		{Name: "limit", Type: "number",
			Description: "Rows to return, newest first. Default 50, maximum 200."},
	},
	resreg.Action("replay"): {
		{Name: "id", Type: "string", Required: true,
			Description: "The dead-lettered message id, as returned by list_dead_letters."},
	},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      DeadLettersName,
		Table:     "messages",
		RowType:   reflect.TypeFor[DeadLetterRow](),
		PKFields:  []string{"ID"},
		Endpoints: DeadLettersEndpoints,
		Responses: DeadLettersResponses,
		MCPDoc:    DeadLettersMCPDoc,
		MCPArgs:   DeadLettersMCPArgs,
		MCPNames:  DeadLettersMCPNames,
		// DB is deliberately EMPTY, as audit's is: a dead letter is EVIDENCE,
		// not declarative config, and a VIEW of `messages`, which no manifest
		// owns.
		DB: "",
	})
}
