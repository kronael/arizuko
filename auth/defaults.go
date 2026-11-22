package auth

import (
	"slices"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// defaults.go is the release's access policy, the ONLY place it is written.
// The evaluator reads these rows under the local rows in `acl`: for one call,
// a local row that matches decides, and these decide only when none does. An
// operator overrides a default with a local row and gets it back by revoking
// that row. The docs name this file and never copy it.

// memberVerbs are what every folder may do: answer the chats it is routed to.
// Scope ** because the route, not the scope, bounds where a message goes.
var memberVerbs = []string{
	"reply", "send", "send_file", "send_voice", "post", "forward", "quote",
	"repost", "like", "dislike", "edit", "delete", "disengage",
}

var skipApproval = []string{
	"reply", "send", "send_file", "send_voice", "post", "forward", "quote",
	"repost", "like", "dislike", "edit", "delete", "disengage",
	"inspect_routing", "inspect_tasks", "inspect_session", "inspect_identity",
	"inspect_messages", "find_messages", "get_thread", "fetch_history",
	"get_work", "set_work",
}

// defaultRows is the default layer. Every row is on a role or a service, so
// it reaches exactly the principals the operator puts in that role, and a
// folder taken out of a role loses its rows.
var defaultRows = func() []core.ACLRow {
	rows := []core.ACLRow{
		// The delegation root: an operator holds everything, with the option.
		{Principal: store.RoleOperator, Action: "*", Scope: "**",
			Effect: store.EffectAllow, GrantOption: true},
		// dashd reads and writes chat settings on every folder.
		{Principal: "service:dashd", Action: "chats:get", Scope: "**", Effect: store.EffectAllow},
		{Principal: "service:dashd", Action: "chats:update", Scope: "**", Effect: store.EffectAllow},
		// A world holds every call for a person to see before it runs.
		{Principal: store.RoleWorld, Action: HoldPrefix + "*", Scope: "**",
			Effect: store.EffectAllow},
	}
	for _, v := range memberVerbs {
		row := core.ACLRow{Principal: store.RoleMember,
			Action: "mcp:" + v, Scope: "**", Effect: store.EffectAllow}
		if v == "send" {
			row.Params = "jid"
		}
		rows = append(rows, row)
	}
	for _, tool := range skipApproval {
		rows = append(rows, core.ACLRow{Principal: store.RoleMember,
			Action: HoldPrefix + tool, Scope: "**", Effect: store.EffectDeny})
	}
	return rows
}()

// SkipApprovalTools returns the tools that skip approval by default.
func SkipApprovalTools() []string {
	return slices.Clone(skipApproval)
}
