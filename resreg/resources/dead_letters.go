package resources

import (
	"net/http"
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// DeadLetterRow is one outbound message routd gave up on: `messages` with
// status='failed' AND is_bot_message=1. There is no dead-letter TABLE and this
// resource does not add one — 'failed' IS the dead-letter state (core/types.go),
// the row keeps its content, and `delivery_attempts` tells the two causes apart:
// 0 means the platform refused it permanently, the cap means the retries ran
// out.
//
// Before this resource an operator could see THAT delivery failed —
// dashd's routd page renders a `dead-lettered` COUNT — and not WHAT was lost,
// to whom, or with what error. A count is not a recovery path (BUGS L23).
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

// DeadLettersResponses: the list handler PROJECTS core.Message onto the row
// before answering, which is what makes the declared schema true. replay
// answers {"ok","id","status"} and declares nothing.
var DeadLettersResponses = map[resreg.Action]reflect.Type{
	resreg.ActionList: reflect.TypeFor[[]DeadLetterRow](),
}

// DeadLettersEndpoints — read the lost messages, and put one back on the wire.
//
// There is deliberately no create/update/delete. The row is written by the
// delivery sweep and is the record that a message was lost; a caller editing or
// erasing it would destroy the only evidence. `replay` is the ONE mutation, and
// it is not a second delivery mechanism: it flips the row back to 'pending' and
// zeroes delivery_attempts, so routd's existing outbound sweep picks it up on
// its next pass.
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
		// DB is deliberately EMPTY, for the same reason audit's is: BySubsystem
		// never returns this resource, so Export/Plan/Apply never touch it. A
		// dead letter is EVIDENCE that a message was lost, not declarative
		// config — round-tripping it through a manifest would let `arizuko
		// apply` rewrite or erase the record. It is also a VIEW of `messages`,
		// which no manifest owns.
		DB: "",
	})
}
