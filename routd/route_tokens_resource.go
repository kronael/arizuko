package routd

// route_tokens_resource.go puts the agent's route-token tools on ONE
// resreg.Resource instead of four hand-rolled ipc tool bodies (spec 5/16). resreg
// owns the plumbing; routd owns the auth POLICY. Two shapes are folded in:
//
//   - Custom actions: issue_chat/issue_hook/revoke are resource-specific verbs, so
//     Mutates() is true and each sends the mutation+audit batch; list reuses
//     ActionList. MCPNames maps every action back to the flat live tool name, so no
//     in-container tool is renamed.
//   - CONTAINMENT is per face. issue binds the token's owner_folder to the caller's
//     socket folder ALWAYS, never a client arg; the arg-carried target_folder is
//     only the JID's routing target, confined by the injected Gate on the agent
//     face and by the equal-or-descend check in the handler on REST. revoke
//     addresses a token by its JID but scopes the DELETE to owner_folder = the
//     caller's folder, so a cross-folder JID matches zero rows — the WHERE clause
//     IS the containment, and there is no per-token owner READ because the owner is
//     bound to the caller, never supplied.
//
// issue_pair (spec 5/31) is the one MCPOnly action: it mints a kind='pair' token
// instead of a delivery bearer, and its owner_folder is the folder the JID ROUTES
// to. The agent in the chat is the only caller, so it has no REST twin.
//
// One shared handler, two injected gates (CLAUDE.md "auth is a uniform
// middleware"). Only the REST-only resolve (URL token → jid, for webd) stays
// hand-rolled.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// Route-token actions are resource-specific verbs, not the CRUD shape. list reuses
// resreg.ActionList (== "list") so it stays read-only (Mutates() false, no tx);
// issue_chat/issue_hook/revoke are custom strings so Mutates() is true and each
// sends the mutation+audit batch.
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
// a store.Store over routd.db so resreg.invoke sends the mutation+audit batch there.
func (s *Server) routeTokensResource() resreg.Resource {
	r := resreg.Mount(resources.RouteTokensName)
	r.Handler = s.routeTokensHandler
	r.Store = s.db.store()
	return r
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
			out[i] = ipc.RouteTokenInfo{JID: r.JID, OwnerFolder: r.OwnerFolder,
				Context: r.Context, TargetFolder: r.TargetFolder}
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
		if x.Surface == audit.SurfaceREST && !groupfolder.Contains(folder, target) {
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
		raw, err := s.db.store().MintRouteTokenInto(ctx, x.Writes, jid, folder, target,
			strings.TrimSpace(argString(x.Args, "context")))
		if errors.Is(err, store.ErrRouteTokenTargetTaken) {
			return nil, resreg.Errorf(http.StatusConflict, "%v", err)
		}
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		url := ""
		if base := core.PublicBaseURL(s.webHost); base != "" {
			url = base + urlPrefix + raw
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
		base := core.PublicBaseURL(s.webHost)
		if base == "" {
			return nil, resreg.Errorf(http.StatusInternalServerError,
				"cannot mint a pairing link: WEB_HOST is unset, so there is no URL to hand out")
		}
		raw, err := s.db.store().IssuePairingLinkInto(x.Writes, jid, target)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("issue_pairing_link", "folder", folder, "target", target, "jid", jid)
		return map[string]any{"url": base + "/pair/" + raw, "jid": jid}, nil

	case routeTokensActionRevoke:
		jid := strings.TrimSpace(argString(x.Args, "jid"))
		if jid == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "jid required")
		}
		// Ownership containment: DELETE scoped to owner_folder = the caller's folder,
		// so a cross-folder JID matches zero rows (deleted=false) — never another
		// folder's token.
		deleted, err := s.db.store().RevokeRouteTokenInto(ctx, x.Writes, jid, folder)
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
		if !authorize(target, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s on %s: not permitted", name, target)
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, visible)
}

// pairingTargetFolder resolves the folder a pairing's JID routes to. That folder is
// BOTH what the Gate authorizes the caller against and the token's owner_folder, so
// the mint gate is the route-ownership rule outbound `send` already applies: a
// caller can only mint a pairing for a chat it handles. Resolved twice per call
// because resreg's Gate cannot hand a value to the handler; it is the same read of
// the same route table, so the two cannot disagree.
//
// An ingress surface is not an identity: web:/hook: JIDs name a route token's public
// endpoint and anon: names an IP hash whose next holder would inherit the account,
// so neither is pairable (spec 5/31 § Not in scope).
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
// stops short of the write so the handler can buffer it on resreg.invoke's set.
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
		jid, urlPrefix = groupfolder.WebJID(targetFolder), "/chat/"
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

// The mint and the revoke are store.MintRouteTokenTx and
// store.RevokeRouteTokenInto, on resreg.invoke's write set so the item and the call's
// single audit_log row commit together. They live there because the token's
// coordinate is its digest and its attribute names are store's, and because the
// pairing minter beside them (IssuePairingLink, which onbod's greeting also
// reaches) has to render the same item with a different `kind` — one renderer,
// two credentials, so neither can be redeemed as the other.
