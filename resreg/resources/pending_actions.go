package resources

import (
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// PendingActionsRow mirrors routd's pending_actions: one tool call suspended
// until a human approves it (spec 5/19). The hold RULE is an `acl` row; this is
// the suspended CALL.
type PendingActionsRow struct {
	ID           string `db:"id"            yaml:"id"                      json:"id"`
	GroupFolder  string `db:"group_folder"  yaml:"group_folder"            json:"group_folder"`
	CallerAgent  string `db:"caller_agent"  yaml:"caller_agent,omitempty"  json:"caller_agent,omitempty"`
	Tool         string `db:"tool"          yaml:"tool"                    json:"tool"`
	Args         string `db:"args"          yaml:"args,omitempty"          json:"args,omitempty"`
	ArgsFinal    string `db:"args_final"    yaml:"args_final,omitempty"    json:"args_final,omitempty"`
	ArgsHash     string `db:"args_hash"     yaml:"args_hash,omitempty"     json:"args_hash,omitempty"`
	Status       string `db:"status"        yaml:"status"                  json:"status"`
	ChatJID      string `db:"chat_jid"      yaml:"chat_jid,omitempty"      json:"chat_jid,omitempty"`
	CreatedAt    string `db:"created_at"    yaml:"created_at,omitempty"    json:"created_at,omitempty"`
	ReviewedBy   string `db:"reviewed_by"   yaml:"reviewed_by,omitempty"   json:"reviewed_by,omitempty"`
	ReviewedAt   string `db:"reviewed_at"   yaml:"reviewed_at,omitempty"   json:"reviewed_at,omitempty"`
	ReviewerNote string `db:"reviewer_note" yaml:"reviewer_note,omitempty" json:"reviewer_note,omitempty"`
	Result       string `db:"result"        yaml:"result,omitempty"        json:"result,omitempty"`
	Error        string `db:"error"         yaml:"error,omitempty"         json:"error,omitempty"`
	ExpiresAt    string `db:"expires_at"    yaml:"expires_at,omitempty"    json:"expires_at,omitempty"`
}

// PendingActionsEndpoints — the operator reviews held calls over REST and the
// dashboard; `approve` and `reject` are the two verdicts, and both funnel to the
// same handler as the chat `/approve` command.
//
// There is deliberately no create/delete: a row is written by the gate at hold
// time and never by a caller, and deleting one would erase the record of a
// decision someone made.
var PendingActionsEndpoints = []resreg.Endpoint{
	{Action: resreg.ActionList, Verb: "GET", Path: "/v1/pending_actions"},
	{Action: resreg.Action("approve"), Verb: "POST", Path: "/v1/pending_actions/{id}/approve"},
	{Action: resreg.Action("reject"), Verb: "POST", Path: "/v1/pending_actions/{id}/reject"},
}

var PendingActionsMCPNames = map[resreg.Action]string{
	resreg.ActionList:        "list_pending_actions",
	resreg.Action("approve"): "approve_pending_action",
	resreg.Action("reject"):  "reject_pending_action",
}

var PendingActionsMCPDoc = map[resreg.Action]string{
	resreg.ActionList: "List tool calls held for human approval in `folder`. " +
		"Each row carries the tool, the exact arguments, who is waiting, and the status " +
		"(held | approved | rejected | released | expired). Use it to tell a user what is " +
		"waiting on them and why.",
	resreg.Action("approve"): "Approve a held tool call by `id`, releasing it one-shot. " +
		"The ORIGINAL agent re-issues the call in its own next turn with the approved arguments; " +
		"nothing runs out of turn. Different arguments are held again.",
	resreg.Action("reject"): "Reject a held tool call by `id`. The call is never run. " +
		"`note` records why, for the audit trail.",
}

var PendingActionsMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.ActionList: {
		{Name: "folder", Type: "string"},
		{Name: "status", Type: "string"},
	},
	resreg.Action("approve"): {
		{Name: "id", Type: "string", Required: true},
		{Name: "note", Type: "string"},
	},
	resreg.Action("reject"): {
		{Name: "id", Type: "string", Required: true},
		{Name: "note", Type: "string"},
	},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      PendingActionsName,
		Table:     "pending_actions",
		DB:        resreg.SubsystemRoutd,
		RowType:   reflect.TypeFor[PendingActionsRow](),
		PKFields:  []string{"ID"},
		Endpoints: PendingActionsEndpoints,
		MCPDoc:    PendingActionsMCPDoc,
		MCPArgs:   PendingActionsMCPArgs,
		MCPNames:  PendingActionsMCPNames,
		Scope:     resreg.ScopeSpec{Field: "GroupFolder"},
	})
}
