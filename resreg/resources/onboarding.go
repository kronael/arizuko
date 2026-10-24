package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// OnboardingRow mirrors the onboarding entity (store/onboarding.go): sort key
// JID, every other db:-tagged field one attribute, and the whole row.
// Absent attributes render as empty fields. It carries no credential
// column; the live setup link is a route_tokens row (BUGS F40).
type OnboardingRow struct {
	JID        string `db:"jid"         yaml:"jid"                   json:"jid"`
	Sender     string `db:"sender"      yaml:"sender,omitempty"      json:"sender,omitempty"`
	Status     string `db:"status"      yaml:"status"                json:"status"`
	UserSub    string `db:"user_sub"    yaml:"user_sub,omitempty"    json:"user_sub,omitempty"`
	Gate       string `db:"gate"        yaml:"gate,omitempty"        json:"gate,omitempty"`
	Created    string `db:"created"     yaml:"created"               json:"created"`
	PromptedAt string `db:"prompted_at" yaml:"prompted_at,omitempty" json:"prompted_at,omitempty"`
	QueuedAt   string `db:"queued_at"   yaml:"queued_at,omitempty"   json:"queued_at,omitempty"`
	AdmittedAt string `db:"admitted_at" yaml:"admitted_at,omitempty" json:"admitted_at,omitempty"`
}

// OnboardingEndpoints: approve and reprompt are state-machine transitions,
// not an `update` of arbitrary columns, so each is its own named action. The
// single declaration the /openapi.json doc and onbod's handler both read.
var OnboardingEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/onboarding", Action: resreg.ActionCreate},
	{Verb: "GET", Path: "/v1/onboarding", Action: resreg.ActionList},
	{Verb: "POST", Path: "/v1/onboarding/{jid}/approve", Action: resreg.Action("approve")},
	{Verb: "POST", Path: "/v1/onboarding/{jid}/reprompt", Action: resreg.Action("reprompt")},
	{Verb: "DELETE", Path: "/v1/onboarding/{jid}", Action: resreg.ActionDelete},
}

// OnboardingMCPDoc gives the agent face its one-liners; every REST action is
// reachable over MCP too (5/17).
var OnboardingMCPDoc = map[resreg.Action]string{
	resreg.ActionList:         "List pending and admitted onboarding rows — who is waiting to be let in, which gate queued them, and when each was greeted, queued and admitted. Filter with status= (awaiting_message, token_used, queued, approved). Never returns the setup link itself.",
	resreg.ActionCreate:       "Record an unrouted chat jid and its inbound sender as awaiting onboarding. Existing senders and admission verdicts stay fixed; expired awaiting greetings may re-arm.",
	resreg.Action("approve"):  "Admit a queued jid immediately, bypassing its gate's daily limit. Use when someone should not wait out the queue.",
	resreg.Action("reprompt"): "Reset a jid to awaiting_message so the next poll tick greets it again with a fresh setup link, bypassing the re-greet cooldown. Use when the link expired or never arrived.",
	resreg.ActionDelete:       "Deny a jid and drop its onboarding row entirely. The jid can start over by messaging again.",
}

// OnboardingMCPArgs is the explicit per-action arg list.
var OnboardingMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.ActionList: {
		{Name: "status", Type: "string", Description: "Only rows in this status (awaiting_message, token_used, queued, approved). Omit for all."},
	},
	resreg.ActionCreate: {
		{Name: "jid", Type: "string", Description: "Chat jid to record as awaiting onboarding, e.g. telegram:user/12345.", Required: true},
		{Name: "sender", Type: "string", Description: "Sender identity from the chat's inbound message.", Required: true},
	},
	resreg.Action("approve"): {
		{Name: "jid", Type: "string", Description: "Chat jid to admit immediately.", Required: true},
	},
	resreg.Action("reprompt"): {
		{Name: "jid", Type: "string", Description: "Chat jid whose setup link should be reissued.", Required: true},
	},
	resreg.ActionDelete: {
		{Name: "jid", Type: "string", Description: "Chat jid to deny and remove.", Required: true},
	},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:          OnboardingName,
		Table:         "onboarding",
		DB:            resreg.SubsystemOnbod,
		RowType:       reflect.TypeFor[OnboardingRow](),
		PKFields:      []string{"JID"},
		EntitySortKey: []string{"JID"},
		Endpoints:     OnboardingEndpoints,
		MCPDoc:        OnboardingMCPDoc,
		MCPArgs:       OnboardingMCPArgs,
		// Admissions are runtime state driven by chat traffic, never declared
		// in a manifest; an apply that rebuilt the table would null every live
		// setup link instance-wide (BUGS Z3).
		SkipApplyRebuild: true,
	})
}
