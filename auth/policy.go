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
	case "send", "send_file", "reply", "post", "like", "dislike",
		"delete", "edit", "forward", "quote", "repost":
		return authorizeOutbound(id, tool, target)
	case "reset_session":
		if target.TargetFolder != id.Folder &&
			!strings.HasPrefix(target.TargetFolder, id.Folder+"/") {
			return fmt.Errorf("unauthorized: can only reset own or descendant sessions")
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
	case "get_routes", "set_routes", "add_route", "delete_route":
		if id.Tier >= 2 {
			return fmt.Errorf("unauthorized: tier %d cannot manage routes", id.Tier)
		}
		if id.Tier == 1 && target.RouteTarget != "" &&
			!strings.HasPrefix(target.RouteTarget, id.Folder+"/") {
			return fmt.Errorf("unauthorized")
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
	case "get_grants", "set_grants":
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
