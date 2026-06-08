package auth

import (
	"fmt"
	"strings"
)

type AuthzTarget struct {
	TaskOwner    string
	RouteTarget  string
	TargetFolder string
}

// AuthorizeStructural checks tree-shape invariants (caller-folder prefix,
// tier bounds on actions, task-owner-must-match-caller). Spec 6/9's
// row-grant model lives in auth.Authorize (authorize.go); this function
// covers the orthogonal structural concern. Many tools require both.
func AuthorizeStructural(id Identity, tool string, target AuthzTarget) error {
	switch tool {
	case "list_tasks":
		return nil
	case "inspect_tasks":
		// Read-only: caller may inspect a task whose owner is its own
		// folder or a descendant. Callers gate tier 0 themselves.
		if target.TaskOwner != id.Folder &&
			!strings.HasPrefix(target.TaskOwner, id.Folder+"/") {
			return fmt.Errorf("unauthorized: can only inspect own or descendant tasks")
		}
		return nil
	case "send", "send_file", "send_voice", "reply", "post", "like", "dislike",
		"delete", "edit", "forward", "quote", "repost",
		"pin_message", "unpin_message", "unpin_all",
		"pane_set_prompts", "pane_set_title":
		return authorizeOutbound(id, tool, target)
	case "reset_session", "fork_topic":
		if target.TargetFolder != id.Folder &&
			!strings.HasPrefix(target.TargetFolder, id.Folder+"/") {
			return fmt.Errorf("unauthorized: can only %s own or descendant folders", "act on")
		}
		return nil
	case "inject_message":
		if id.Tier > 1 {
			return fmt.Errorf("unauthorized: tier %d cannot inject messages", id.Tier)
		}
		return nil
	case "register_group":
		if id.Tier >= 2 {
			return fmt.Errorf("unauthorized: tier %d cannot register groups", id.Tier)
		}
		if id.Tier == 0 && !strings.Contains(target.TargetFolder, "/") {
			return fmt.Errorf("unauthorized: worlds are CLI-only")
		}
		if id.Tier == 1 && !IsDirectChild(id.Folder, target.TargetFolder) {
			return fmt.Errorf("unauthorized: can only create children in own world")
		}
		return nil
	case "escalate_group":
		if id.Tier < 2 {
			return fmt.Errorf("unauthorized: tier %d cannot escalate", id.Tier)
		}
		return nil
	case "delegate_group":
		if id.Tier == 3 {
			return fmt.Errorf("unauthorized: tier 3 cannot delegate")
		}
		if !strings.HasPrefix(target.TargetFolder, id.Folder+"/") {
			return fmt.Errorf("unauthorized: %s cannot delegate to %s",
				id.Folder, target.TargetFolder)
		}
		return nil
	case "list_routes", "set_routes", "add_route", "delete_route":
		if id.Tier >= 2 {
			return fmt.Errorf("unauthorized: tier %d cannot manage routes", id.Tier)
		}
		if id.Tier == 1 && target.RouteTarget != "" &&
			!strings.HasPrefix(target.RouteTarget, id.Folder+"/") {
			return fmt.Errorf("unauthorized")
		}
		return nil
	case "network_allow", "network_deny", "network_list":
		// Egress allowlist management: tier 0/1 only. Tier 0 (root)
		// unrestricted; tier 1 confined to its own subtree (own folder or a
		// descendant). Tier 2+ cannot touch egress.
		if id.Tier >= 2 {
			return fmt.Errorf("unauthorized: tier %d cannot manage egress", id.Tier)
		}
		if id.Tier == 1 &&
			target.TargetFolder != id.Folder &&
			!strings.HasPrefix(target.TargetFolder, id.Folder+"/") {
			return fmt.Errorf("unauthorized: egress target outside own subtree")
		}
		return nil
	case "schedule_task", "pause_task", "resume_task", "cancel_task":
		if id.Tier == 3 {
			return fmt.Errorf("unauthorized")
		}
		if id.Tier == 2 && target.TaskOwner != id.Folder {
			return fmt.Errorf("unauthorized")
		}
		if id.Tier == 1 && !isInWorld(id.Folder, target.TaskOwner) {
			return fmt.Errorf("unauthorized")
		}
		return nil
	case "set_group_open", "set_observe_window":
		if id.Tier > 1 {
			return fmt.Errorf("unauthorized: tier %d cannot edit group config", id.Tier)
		}
		if target.TargetFolder != "" &&
			target.TargetFolder != id.Folder &&
			!strings.HasPrefix(target.TargetFolder, id.Folder+"/") {
			return fmt.Errorf("unauthorized: target outside own subtree")
		}
		return nil
	case "observe_group", "unobserve_group":
		// Tier 0/1 can observe any folder. Tier 2 agents can observe
		// their parent (escalation path) or their own subtree. Tier 3
		// cannot subscribe cross-folder observations.
		if id.Tier >= 3 {
			return fmt.Errorf("unauthorized: tier %d cannot use observe_group", id.Tier)
		}
		if id.Tier == 2 {
			src := target.TargetFolder
			if src != "" && src != id.Folder &&
				!strings.HasPrefix(src, id.Folder+"/") &&
				!strings.HasPrefix(id.Folder, src+"/") {
				return fmt.Errorf("unauthorized: observe source outside own subtree or parent chain")
			}
		}
		return nil
	case "get_grants", "set_grants", "list_acl":
		if id.Tier > 1 {
			return fmt.Errorf("unauthorized: tier %d cannot manage grants", id.Tier)
		}
		if id.Tier == 1 && !isInWorld(id.Folder, target.TargetFolder) {
			return fmt.Errorf("unauthorized: can only manage grants in own world")
		}
		return nil
	case "invite_create":
		if id.Tier >= 2 {
			return fmt.Errorf("unauthorized: tier %d cannot issue invites", id.Tier)
		}
		if id.Tier == 1 && !isInWorld(id.Folder, target.TargetFolder) {
			return fmt.Errorf("unauthorized: target outside own world")
		}
		return nil
	case "invite_revoke":
		// Mirrors invite_create: tier 2+ can't manage invites; tier 1 is
		// confined to its own world. Per-token ownership (the token was issued
		// by THIS folder) is enforced downstream in routd, not here — this gate
		// is the structural tier/world check. invite_list is read-only +
		// self-filtered, so it carries no structural case.
		if id.Tier >= 2 {
			return fmt.Errorf("unauthorized: tier %d cannot revoke invites", id.Tier)
		}
		if id.Tier == 1 && !isInWorld(id.Folder, target.TargetFolder) {
			return fmt.Errorf("unauthorized: target outside own world")
		}
		return nil
	case "add_acl", "remove_acl":
		if id.Tier >= 2 {
			return fmt.Errorf("unauthorized: tier %d cannot manage acl", id.Tier)
		}
		// Scope "**" has no world (WorldOf returns "**"), so a tier-1 caller's
		// isInWorld check fails for it — only tier-0/operator can grant the
		// operator role.
		if id.Tier == 1 && !isInWorld(id.Folder, target.TargetFolder) {
			return fmt.Errorf("unauthorized: scope outside own world")
		}
		return nil
	default:
		return fmt.Errorf("unknown tool: %s", tool)
	}
}

func authorizeOutbound(id Identity, tool string, target AuthzTarget) error {
	if target.TargetFolder == "" {
		return fmt.Errorf("forbidden: chat has no route in this instance (%s)", tool)
	}
	if target.TargetFolder == id.Folder ||
		strings.HasPrefix(target.TargetFolder, id.Folder+"/") {
		return nil
	}
	return fmt.Errorf("forbidden: chat belongs to folder %s, not in subtree of %s (%s)",
		target.TargetFolder, id.Folder, tool)
}
