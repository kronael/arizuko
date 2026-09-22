package routd

// route_tokens_resource.go puts the agent's route-token tools on ONE
// resreg.Resource (spec 5/16). issue binds the token's owner_folder to the
// caller's socket folder ALWAYS, never a client arg — target_folder is only
// the JID's routing target, confined by the Gate on the agent face and by the
// equal-or-descend check on REST. revoke scopes the DELETE to owner_folder =
// the caller's folder, so a cross-folder JID matches zero rows. issue_pair
// (spec 5/31-identity-pairing) is the one MCPOnly action: a kind='pair' token whose
// owner_folder is the folder the JID ROUTES to.

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

// list reuses resreg.ActionList so it stays read-only (no tx); the rest are
// custom strings so Mutates() is true and each sends the mutation+audit batch.
const (
	routeTokensActionIssueChat = resreg.Action("issue_chat")
	routeTokensActionIssueHook = resreg.Action("issue_hook")
	routeTokensActionIssuePair = resreg.Action("issue_pair")
	routeTokensActionRevoke    = resreg.Action("revoke")
)

// routeTokensMCPNames is the action→flat-tool-name map, single-sourced in
// resreg/resources; the Gate's lookup and the handler's slog tool-name source.
var routeTokensMCPNames = resources.RouteTokensMCPNames

// routeTokensResource is the single renderer for BOTH faces: the agent's token
// tools and the operator REST face tokens_http.go mounts from this resource.
func (s *Server) routeTokensResource() resreg.Resource {
	r := resreg.Mount(resources.RouteTokensName)
	r.Handler = s.routeTokensHandler
	r.Store = s.db.store()
	return r
}

// routeTokensHandler runs issue_chat/issue_hook/issue_pair/revoke/list. The
// token's owner_folder is ALWAYS x.Caller.Folder; target_folder is only the
// JID's routing target.
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
		// AGENT: the injected Gate (minting is default-deny: /root or an
		// operator-delegated grant only). REST: target must equal/descend from
		// the owner folder, already bound to the caller's JWT subtree.
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
		// owner_folder is the folder the JID ROUTES to — the same value the
		// Gate authorized the caller against, so the two cannot disagree.
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
		// DELETE scoped to owner_folder = the caller's folder: a cross-folder
		// JID matches zero rows, never another folder's token.
		deleted, err := s.db.store().RevokeRouteTokenInto(ctx, x.Writes, jid, folder)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("revoke_token", "folder", folder, "jid", jid, "deleted", deleted)
		return map[string]any{"deleted": deleted}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// routeTokensPostBuild mounts the token tools on the agent socket. The Gate
// runs db.Authorize on the mint target; the owner-scoped revoke lives in the
// handler.
func (s *Server) routeTokensPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	res := s.routeTokensResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := routeTokensMCPNames[x.Action]
		// The target is the folder a minted token points at, else the owner
		// folder (list/revoke are owner-scoped). Minting a public endpoint is
		// default-deny (not in role:member): /root or an operator-delegated
		// grant only.
		target := folder
		switch x.Action {
		case routeTokensActionIssueChat, routeTokensActionIssueHook:
			if t := strings.TrimSpace(argString(x.Args, "target_folder")); t != "" {
				target = t
			}
		case routeTokensActionIssuePair:
			// A pairing names an EXISTING chat, so its target is where that chat
			// routes — the containment outbound `send` applies (ipc.authorizeJID).
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

// pairingTargetFolder resolves the folder a pairing's JID routes to — BOTH
// what the Gate authorizes against and the token's owner_folder, so a caller
// can only mint a pairing for a chat it handles. Resolved twice per call
// because resreg's Gate cannot hand a value to the handler; same read, same
// table. An ingress surface is not an identity: web:/hook: name a public
// endpoint and anon: an IP hash whose next holder would inherit the account
// (spec 5/31-identity-pairing § Not in scope).
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

// routeTokenJID builds the (jid, url-prefix) for a mint: segRe-validate the
// agent-supplied segments (or an agent could inject `/`/`..` into the stored
// JID), then compose web:/hook: by kind. It stops short of the write so the
// handler can buffer it on resreg.invoke's set.
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
		jid, urlPrefix = groupfolder.HookJID(targetFolder, sourceLabel), "/hook/"
	default:
		return "", "", fmt.Errorf("kind must be chat|hook")
	}
	if jidSuffix != "" {
		jid += "/" + jidSuffix
	}
	return jid, urlPrefix, nil
}

// The mint and the revoke live in store, on resreg.invoke's write set: the
// token's coordinate is its digest and its attribute names are store's, and
// the pairing minter beside them (IssuePairingLink, which onbod's greeting also
// reaches) renders the same item with a different `kind` — one renderer, two
// credentials, so neither can be redeemed as the other.
