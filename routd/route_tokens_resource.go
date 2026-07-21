package routd

// route_tokens_resource.go is the spec 5/16 step after web_routes +
// network_rules + scheduled_tasks + routes: the agent's route-token tools
// (issue_chat_link/issue_webhook/list_tokens/revoke_token) ride ONE
// resreg.Resource instead of four hand-rolled ipc/ipc.go tool bodies.
//
// resreg owns the plumbing (handler dispatch + one tx wrapping each mutation AND
// its audit_log row); routd owns the auth POLICY. Two shapes are folded in:
//
//   - Custom actions: issue_chat/issue_hook/revoke are resource-specific verbs
//     (not CRUD create/delete), so Mutates() is true and each opens the
//     mutation+audit tx; list reuses resreg.ActionList (read-only, no tx).
//     MCPNames maps every action back to the flat live tool name so no in-container
//     tool is renamed.
//   - CONTAINMENT lives in the HANDLER, per face (spec 5/W tier model). issue binds
//     the token's owner_folder to the caller's socket folder ALWAYS (never a client
//     arg); the arg-carried target_folder is only the JID's routing target, and the
//     tier cap (authorizeRouteTokenMint: tier ≤2 → self+descendants, tier ≥3 → none)
//     confines which folders a caller may point a token at. revoke addresses a token
//     by its JID but scopes the DELETE to owner_folder = the caller's folder, so a
//     folder can only revoke tokens IT minted — a cross-folder JID matches zero rows
//     (deleted=false), the exact ownership guard the deleted ipc body enforced via
//     DB.RevokeRouteTokens' `owner_folder=?` predicate. There is no per-token owner
//     READ (unlike scheduled_tasks' GetTask.Owner) because the owner is bound to the
//     caller, never supplied — the WHERE clause IS the containment.
//
// Only the AGENT face rides this resource (agent socket, route_tokensPostBuild). The
// operator REST twin (/v1/route_tokens/{chat,hook,resolve}, list, {jid}) stays
// hand-rolled in tokens_http.go — its chat/hook/resolve request shapes predate the
// fold and folding them is a separate 5/16 REST-face step. The injected Gate adds the
// uniform tool grant (grants.CheckAction + db.Authorize) the old registerRaw path
// omitted (it gated on VISIBILITY only); for a normally-granted folder the decision
// is identical, and an ungranted one is now denied at call time, not just hidden.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// Route-token actions are resource-specific verbs, not the CRUD shape. list reuses
// resreg.ActionList (== "list") so it stays read-only (Mutates() false, no tx);
// issue_chat/issue_hook/revoke are custom strings so Mutates() is true and each
// opens the mutation+audit tx.
const (
	routeTokensActionIssueChat = resreg.Action("issue_chat")
	routeTokensActionIssueHook = resreg.Action("issue_hook")
	routeTokensActionRevoke    = resreg.Action("revoke")
)

// routeTokensMCPNames is the action→flat-tool-name map, single-sourced in resreg/
// resources (RouteTokensMCPNames). Aliased here for the Gate's action→policy-name
// lookup and the handler's slog tool-name source.
var routeTokensMCPNames = resources.RouteTokensMCPNames

// routeTokensResource is the single renderer for the agent's four token tools.
// Endpoints exist only to drive deriveMCPTools (Action ∩ MCPDoc) — the REST face
// (/v1/route_tokens/*) is NOT mounted from this resource (see file header). Store is
// a store.Store over routd.db so resreg.invoke opens the mutation+audit tx there.
func (s *Server) routeTokensResource() resreg.Resource {
	return resreg.Resource{
		Name:      "route_tokens",
		Endpoints: resources.RouteTokensEndpoints, // single source: doc + MCP read one list
		MCPDoc:    resources.RouteTokensMCPDoc,    // single source (resreg/resources)
		MCPArgs:   resources.RouteTokensMCPArgs,
		MCPNames:  routeTokensMCPNames,
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Handler: s.routeTokensHandler,
		Store:   store.New(s.db.SQL()),
	}
}

// routeTokensHandler runs issue_chat/issue_hook/revoke/list against routd.db,
// folding in the bespoke semantics the deleted ipc bodies enforced: the mint tier
// cap, segRe JID-segment validation, hex token mint, and the owner-scoped revoke.
// The token's owner_folder is ALWAYS the caller's socket folder (x.Caller.Folder);
// target_folder is only the JID's routing target.
func (s *Server) routeTokensHandler(ctx context.Context, x resreg.Execution) (any, error) {
	folder := x.Caller.Folder
	switch x.Action {
	case resreg.ActionList:
		rows, err := s.db.ListRouteTokens(folder)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		out := make([]ipc.RouteTokenInfo, len(rows))
		for i, r := range rows {
			out[i] = ipc.RouteTokenInfo{JID: r.JID, OwnerFolder: r.OwnerFolder, Context: r.Context}
			out[i].CreatedAt, _ = time.Parse(time.RFC3339Nano, r.CreatedAt)
		}
		return map[string]any{"tokens": out}, nil

	case routeTokensActionIssueChat, routeTokensActionIssueHook:
		target := strings.TrimSpace(argString(x.Args, "target_folder"))
		if target == "" {
			target = folder
		}
		// Mint tier cap: the arg-carried target must be within the caller's reach
		// (tier ≤2 → self+descendants, tier ≥3 → none). Exactly the deleted ipc
		// authorizeMint closure. The token's owner_folder stays the socket folder.
		if err := authorizeRouteTokenMint(auth.Resolve(folder), folder, target); err != nil {
			return nil, resreg.Errorf(http.StatusForbidden, "%v", err)
		}
		suffix := strings.TrimSpace(argString(x.Args, "jid_suffix"))
		kind, source := "chat", ""
		if x.Action == routeTokensActionIssueHook {
			kind = "hook"
			source = strings.TrimSpace(argString(x.Args, "source_label"))
			if source == "" {
				return nil, resreg.Errorf(http.StatusBadRequest, "source_label required")
			}
		}
		jid, urlPrefix, err := routeTokenJID(kind, target, source, suffix)
		if err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		raw, err := issueRouteTokenTx(ctx, x.Tx, jid, folder, strings.TrimSpace(argString(x.Args, "context")))
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		url := ""
		if s.webHost != "" {
			url = s.webHost + urlPrefix + raw
			if kind == "chat" {
				url += "/"
			}
		}
		slog.Info(routeTokensMCPNames[x.Action], "folder", folder, "target", target, "jid", jid)
		return map[string]any{"token": raw, "jid": jid, "url": url}, nil

	case routeTokensActionRevoke:
		jid := strings.TrimSpace(argString(x.Args, "jid"))
		if jid == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "jid required")
		}
		// Ownership containment: DELETE scoped to owner_folder = the caller's folder,
		// so a cross-folder JID matches zero rows (deleted=false) — never another
		// folder's token. Mirrors the deleted ipc body's DB.RevokeRouteTokens.
		deleted, err := revokeRouteTokenTx(ctx, x.Tx, jid, folder)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("revoke_token", "folder", folder, "jid", jid, "deleted", deleted)
		return map[string]any{"deleted": deleted}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// routeTokensPostBuild returns the ServeMCP seam that mounts the token tools on the
// agent socket, with the tool-grant Gate + MatchingRules visibility for this folder's
// grant rules injected. The Gate does the TOOL grant (CheckAction + db.Authorize);
// the mint tier cap + owner-scoped revoke live in the handler (see header). Only rules
// the socket already carries can widen visibility, so a denied tier still sees nothing.
func (s *Server) routeTokensPostBuild(folder, callerSub string, rules []string, authorize authorizeFn) func(*mcpserver.MCPServer) {
	res := s.routeTokensResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		return toolGrant(rules, authorize, callerSub, folder, routeTokensMCPNames[x.Action])
	}
	return mountAgentResource(res, callerSub, folder, rules)
}

// authorizeRouteTokenMint is the mint tier cap the deleted ipc authorizeMint closure
// enforced: tier ≥3 may not mint at all; tier ≤2 may point a token only at its own
// folder or a descendant. An empty target defaults to the caller's own folder.
func authorizeRouteTokenMint(id auth.Identity, folder, target string) error {
	if target == "" {
		target = folder
	}
	if id.Tier >= 3 {
		return fmt.Errorf("unauthorized: tier %d cannot issue route tokens", id.Tier)
	}
	if id.Tier <= 2 && target != folder && !strings.HasPrefix(target, folder+"/") {
		return fmt.Errorf("unauthorized: can only mint for self+descendants")
	}
	return nil
}

// routeTokenJID builds the (jid, url-prefix) for a mint exactly as the deleted
// mcpIssueRouteToken did: segRe-validate the agent-supplied segments (or an agent
// could inject `/`/`..` into the stored JID), then compose web:/hook: by kind. It
// stops short of the DB write so the handler can do it on resreg.invoke's tx.
func routeTokenJID(kind, targetFolder, sourceLabel, jidSuffix string) (jid, urlPrefix string, err error) {
	if targetFolder == "" {
		return "", "", fmt.Errorf("target_folder required")
	}
	if sourceLabel != "" && !segRe.MatchString(sourceLabel) {
		return "", "", fmt.Errorf("source_label must match %s", segRe.String())
	}
	if jidSuffix != "" && !segRe.MatchString(jidSuffix) {
		return "", "", fmt.Errorf("jid_suffix must match %s", segRe.String())
	}
	switch kind {
	case "chat":
		jid, urlPrefix = "web:"+targetFolder, "/chat/"
	case "hook":
		if sourceLabel == "" {
			return "", "", fmt.Errorf("source_label required for webhook")
		}
		jid, urlPrefix = "hook:"+targetFolder+"/"+sourceLabel, "/hook/"
	default:
		return "", "", fmt.Errorf("kind must be chat|hook")
	}
	if jidSuffix != "" {
		jid += "/" + jidSuffix
	}
	return jid, urlPrefix, nil
}

// issueRouteTokenTx mints a 32-byte hex token for jid under owner and inserts
// sha256(token) on tx (mirrors DB.IssueRouteToken so the mutation lands in
// resreg.invoke's tx alongside its audit_log row), returning the raw token once.
// context is the optional per-link processing instructions; "" stores NULL.
func issueRouteTokenTx(ctx context.Context, tx *sql.Tx, jid, owner, context string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	raw := hex.EncodeToString(b)
	h := sha256.Sum256([]byte(raw))
	_, err := tx.ExecContext(ctx,
		`INSERT INTO route_tokens(token_hash, jid, owner_folder, created_at, context) VALUES(?,?,?,?,?)`,
		h[:], jid, owner, nowTS(), nullStr(context))
	if err != nil {
		return "", err
	}
	return raw, nil
}

// revokeRouteTokenTx deletes tokens for jid owned by owner on tx (mirrors
// DB.RevokeRouteTokens), returning whether any row was removed. The owner_folder
// predicate is the ownership containment — a cross-folder jid matches nothing.
func revokeRouteTokenTx(ctx context.Context, tx *sql.Tx, jid, owner string) (bool, error) {
	res, err := tx.ExecContext(ctx,
		`DELETE FROM route_tokens WHERE jid=? AND owner_folder=?`, jid, owner)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
