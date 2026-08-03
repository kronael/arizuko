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
//   - CONTAINMENT is per face. issue binds the token's owner_folder to the caller's
//     socket folder ALWAYS (never a client arg); the arg-carried target_folder is
//     only the JID's routing target. Which folders a caller may point a token at is
//     confined by the injected Gate on the agent face (the grant scope must cover
//     the target; minting is default-deny) and by the equal-or-descend check in the
//     handler on the REST face. revoke addresses a token
//     by its JID but scopes the DELETE to owner_folder = the caller's folder, so a
//     folder can only revoke tokens IT minted — a cross-folder JID matches zero rows
//     (deleted=false), the exact ownership guard the deleted ipc body enforced via
//     DB.RevokeRouteTokens' `owner_folder=?` predicate. There is no per-token owner
//     READ (unlike scheduled_tasks' GetTask.Owner) because the owner is bound to the
//     caller, never supplied — the WHERE clause IS the containment.
//
// issue_pair (issue_pairing_link, spec 5/31) is the one MCPOnly action: it mints a
// kind='pair' token instead of a delivery bearer, and its owner_folder is the folder
// the JID ROUTES to rather than the caller's socket folder. The agent in the chat is
// the only caller, so it has no REST twin and /openapi.json does not advertise one.
//
// BOTH faces ride this resource: the AGENT face (agent socket, route_tokensPostBuild,
// injected tool-grant Gate) and the operator REST face (tokens_http.go mountRouteTokens,
// injected routeTokensRESTGate — scope + JWT-folder containment). One shared handler,
// two injected gates (CLAUDE.md "auth is a uniform middleware"). The REST wire shape is
// now the handler's ({token,jid,url} / {tokens:[…]} / {deleted}), unified with the MCP
// tools. Only the REST-only resolve (URL token → jid, webd; no MCP twin) stays
// hand-rolled. The Gate adds a uniform tool grant on top of tools/list visibility.

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

	"github.com/kronael/arizuko/audit"
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
	routeTokensActionIssuePair = resreg.Action("issue_pair")
	routeTokensActionRevoke    = resreg.Action("revoke")
)

// routeTokensMCPNames is the action→flat-tool-name map, single-sourced in resreg/
// resources (RouteTokensMCPNames). Aliased here for the Gate's action→policy-name
// lookup and the handler's slog tool-name source.
var routeTokensMCPNames = resources.RouteTokensMCPNames

// routeTokensResource is the single renderer for BOTH faces: the agent's four
// token tools AND the operator REST face (/v1/route_tokens/*), mounted from this
// same resource by tokens_http.go mountRouteTokens (5/16 fold). Endpoints drive
// deriveMCPTools (Action ∩ MCPDoc) and the REST routes + /openapi.json. Store is
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
// folding in the bespoke semantics the deleted ipc bodies enforced: the mint
// containment, segRe JID-segment validation, hex token mint, and the owner-scoped revoke.
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
		// Containment. AGENT (MCP): rides the injected Gate — one evaluator, the grant
		// scope covers target (minting is default-deny, so /root or an operator-delegated
		// grant only). OPERATOR (REST): target must equal/descend from the owner folder,
		// already bound to the caller's JWT subtree by routeTokensRESTGate.
		if x.Surface == audit.SurfaceREST && target != folder && !strings.HasPrefix(target, folder+"/") {
			return nil, resreg.Errorf(http.StatusForbidden, "target_folder must equal or descend from owner_folder")
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
		raw, err := issueRouteTokenTx(ctx, x.Tx, jid, folder,
			strings.TrimSpace(argString(x.Args, "context")), store.RouteTokenKindRoute)
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

	case routeTokensActionIssuePair:
		// The pairing's owner_folder is the folder the JID ROUTES to, not the
		// caller's socket folder: the same value the Gate authorized the caller
		// against, so the two cannot disagree.
		jid := strings.TrimSpace(argString(x.Args, "jid"))
		target, err := pairingTargetFolder(s.db, jid)
		if err != nil {
			return nil, err
		}
		if s.webHost == "" {
			return nil, resreg.Errorf(http.StatusInternalServerError,
				"cannot mint a pairing link: WEB_HOST is unset, so there is no URL to hand out")
		}
		raw, err := issueRouteTokenTx(ctx, x.Tx, jid, target, "", store.RouteTokenKindPair)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("issue_pairing_link", "folder", folder, "target", target, "jid", jid)
		return map[string]any{"url": s.webHost + "/pair/" + raw, "jid": jid}, nil

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
// agent socket, with the tool-grant Gate + the turn's visibility view injected. The
// Gate runs db.Authorize on the mint target; the owner-scoped revoke lives in the
// handler (see header). Only grants the caller already holds can widen visibility,
// so a denied caller sees nothing.
func (s *Server) routeTokensPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	res := s.routeTokensResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := routeTokensMCPNames[x.Action]
		// Target: the folder a minted token points at, else the owner folder itself
		// (list/revoke are owner-scoped). One evaluator — the caller must hold the tool
		// scoped to cover it. Minting a public unauthenticated endpoint is default-deny
		// (not in role:member): only /root or an operator-delegated grant authorizes it.
		target := folder
		switch x.Action {
		case routeTokensActionIssueChat, routeTokensActionIssueHook:
			if t := strings.TrimSpace(argString(x.Args, "target_folder")); t != "" {
				target = t
			}
		case routeTokensActionIssuePair:
			// A pairing names an EXISTING chat, so its target is where that chat
			// already routes — never a client-supplied folder. Authorizing the
			// caller against it is the containment outbound `send` applies
			// (ipc.authorizeJID's route-ownership rule), evaluated once here.
			var err error
			if target, err = pairingTargetFolder(s.db, strings.TrimSpace(argString(x.Args, "jid"))); err != nil {
				return err
			}
		}
		if !authorize(callerSub, target, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s on %s: not permitted", name, target)
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, visible)
}

// pairingTargetFolder resolves the folder a pairing's JID routes to. That folder
// is BOTH what the Gate authorizes the caller against and the token's
// owner_folder, so the mint gate is the route-ownership rule outbound `send`
// already applies (ipc.authorizeJID): a caller can only mint a pairing for a
// chat it handles. Resolved twice per call — once in the Gate, once in the
// handler — because resreg's Gate cannot hand a value to the handler; it is the
// same read of the same route table, so the two cannot disagree.
//
// An ingress surface is not an identity: web:/hook: JIDs name a route token's
// public endpoint and anon: names an IP hash whose next holder would inherit the
// account, so neither is pairable (spec 5/31 § Not in scope).
func pairingTargetFolder(db *DB, jid string) (string, error) {
	if jid == "" {
		return "", resreg.Errorf(http.StatusBadRequest, "jid required")
	}
	if store.RouteTokenJIDKind(jid) != "" || strings.HasPrefix(jid, "anon:") {
		return "", resreg.Errorf(http.StatusBadRequest,
			"%s is an ingress surface, not a channel identity — pair the person's platform identity", jid)
	}
	target := db.DefaultFolderForJID(jid)
	if target == "" {
		return "", resreg.Errorf(http.StatusForbidden,
			"forbidden: chat %s has no route in this instance", jid)
	}
	return target, nil
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
// kind is store.RouteTokenKindRoute for a delivery bearer or
// store.RouteTokenKindPair for a pairing link (spec 5/31) — one minter, so the
// two credentials cannot drift apart in hashing or storage.
func issueRouteTokenTx(ctx context.Context, tx *sql.Tx, jid, owner, context, kind string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	raw := hex.EncodeToString(b)
	h := sha256.Sum256([]byte(raw))
	_, err := tx.ExecContext(ctx,
		`INSERT INTO route_tokens(token_hash, jid, owner_folder, created_at, context, kind) VALUES(?,?,?,?,?,?)`,
		h[:], jid, owner, nowTS(), nullStr(context), kind)
	if err != nil {
		return "", err
	}
	return raw, nil
}

// revokeRouteTokenTx deletes delivery tokens for jid owned by owner on tx
// (mirrors DB.RevokeRouteTokens), returning whether any row was removed. The
// owner_folder predicate is the ownership containment — a cross-folder jid
// matches nothing.
func revokeRouteTokenTx(ctx context.Context, tx *sql.Tx, jid, owner string) (bool, error) {
	res, err := tx.ExecContext(ctx,
		`DELETE FROM route_tokens WHERE jid=? AND owner_folder=? AND kind=?`,
		jid, owner, store.RouteTokenKindRoute)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
