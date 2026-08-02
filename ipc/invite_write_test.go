package ipc

import (
	"testing"
	"time"
)

// invite_create/invite_list/invite_revoke are the MCP twins of REST
// POST/GET/DELETE /v1/invites (spec 5/5). A tier-0 (operator) caller drives all
// three; the closures funnel to onbod (faked here as the GatedFns funcs).
func TestServeMCP_InviteWrite_Tier0(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	var createdGlob, createdIssuer string
	var revoked string
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	gated := GatedFns{
		CreateInvite: func(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (InviteInfo, error) {
			createdGlob, createdIssuer = targetGlob, issuedBySub
			return InviteInfo{Token: "tok-1", TargetGlob: targetGlob, IssuedBySub: issuedBySub, MaxUses: maxUses, ExpiresAt: expiresAt}, nil
		},
		ListInvites: func(issuedBy string) ([]InviteInfo, error) {
			if issuedBy != "agent:root" {
				t.Fatalf("ListInvites issuedBy = %q, want agent:root (folder-scoped)", issuedBy)
			}
			return []InviteInfo{{Token: "tok-1", TargetGlob: "root/", MaxUses: 1, ExpiresAt: &exp}}, nil
		},
		RevokeInvite: func(token string) error { revoked = token; return nil },
	}
	// folder "root" → tier 0 (operator).
	stop, err := ServeMCP(sock, gated, StoreFns{}, "root", true, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	// create
	if _, errText := callTool(t, sock, "invite_create", map[string]any{
		"target_glob": "root/", "max_uses": 3,
	}); errText != "" {
		t.Fatalf("invite_create: %s", errText)
	}
	if createdGlob != "root/" {
		t.Fatalf("CreateInvite glob = %q, want root/", createdGlob)
	}
	if createdIssuer != "agent:root" {
		t.Fatalf("CreateInvite issuer = %q, want agent:root", createdIssuer)
	}

	// list — folder-scoped, returns this folder's invites
	res, errText := callTool(t, sock, "invite_list", nil)
	if errText != "" {
		t.Fatalf("invite_list: %s", errText)
	}
	invs, _ := res["invites"].([]any)
	if len(invs) != 1 {
		t.Fatalf("invite_list invites = %v, want one row", res["invites"])
	}
	row, _ := invs[0].(map[string]any)
	if row["token"] != "tok-1" {
		t.Fatalf("invite_list row token = %v, want tok-1", row["token"])
	}

	// revoke
	if _, errText := callTool(t, sock, "invite_revoke", map[string]any{"token": "tok-1"}); errText != "" {
		t.Fatalf("invite_revoke: %s", errText)
	}
	if revoked != "tok-1" {
		t.Fatalf("RevokeInvite token = %q, want tok-1", revoked)
	}
}

// A folder that holds NO invite_create/invite_revoke grant is denied by the injected
// Authorize (5/33: invite management is a delegated grant, not a floor tool); invite_list
// (read-only, self-filtered) is granted here and stays allowed.
func TestServeMCP_InviteWrite_Denied(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	createCalls, revokeCalls := 0, 0
	gated := GatedFns{
		CreateInvite: func(_, _ string, _ int, _ *time.Time) (InviteInfo, error) {
			createCalls++
			return InviteInfo{}, nil
		},
		ListInvites:  func(_ string) ([]InviteInfo, error) { return nil, nil },
		RevokeInvite: func(_ string) error { revokeCalls++; return nil },
	}
	// The folder holds invite_list but NOT invite_create/invite_revoke → the latter two
	// are denied at call time by db.Authorize; the read stays allowed.
	db := StoreFns{Authorize: func(_, _, action string, _ map[string]string) bool {
		return action == "mcp:invite_list"
	}}
	stop, err := ServeMCP(sock, gated, db, "world/a/b", false, 0, "folder:world/a/b")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	if _, errText := callTool(t, sock, "invite_create", map[string]any{"target_glob": "world/a/b/"}); errText == "" {
		t.Fatalf("invite_create (tier2) should be denied")
	}
	if _, errText := callTool(t, sock, "invite_revoke", map[string]any{"token": "tok-x"}); errText == "" {
		t.Fatalf("invite_revoke (tier2) should be denied")
	}
	if createCalls != 0 || revokeCalls != 0 {
		t.Fatalf("writers ran despite authz denial: create=%d revoke=%d", createCalls, revokeCalls)
	}

	// invite_list is read-only + self-filtered: allowed at any tier.
	if _, errText := callTool(t, sock, "invite_list", nil); errText != "" {
		t.Fatalf("invite_list (tier2) should be allowed: %s", errText)
	}
}
