package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/resreg/resources"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

func (d *dash) authorizeChatSettings(w http.ResponseWriter, r *http.Request, folder, jid string) bool {
	if jid == "" {
		http.Error(w, "jid required", http.StatusBadRequest)
		return false
	}
	body, status, err := d.routdCall(r.Context(), http.MethodGet, "/v1/routing/resolve?jid="+url.QueryEscape(jid), nil)
	if err != nil || status != http.StatusOK {
		http.Error(w, fmt.Sprintf("routd route lookup: status %d: %s %v", status, body, err), http.StatusBadGateway)
		return false
	}
	var resolved apiv1.RoutingResolveResponse
	if err := json.Unmarshal(body, &resolved); err != nil || resolved.Folder == "" {
		http.Error(w, "chat has no resolved route", http.StatusBadRequest)
		return false
	}
	if !groupfolder.Contains(folder, resolved.Folder) {
		http.Error(w, "chat routes outside this group", http.StatusForbidden)
		return false
	}
	_, ok := d.requireAdmin(w, r, resolved.Folder)
	return ok
}

func (d *dash) readChatSettings(w http.ResponseWriter, r *http.Request, folder, jid string) (*resources.ChatSettingsResponse, bool) {
	if !d.authorizeChatSettings(w, r, folder, jid) {
		return nil, false
	}
	body, status, err := d.routdCall(r.Context(), http.MethodGet, "/v1/chats/"+url.PathEscape(jid), nil)
	if err != nil || status != http.StatusOK {
		http.Error(w, fmt.Sprintf("routd chat settings: status %d: %s %v", status, body, err), http.StatusBadGateway)
		return nil, false
	}
	var settings resources.ChatSettingsResponse
	if err := json.Unmarshal(body, &settings); err != nil || settings.JID != jid {
		http.Error(w, "invalid chat settings response", http.StatusBadGateway)
		return nil, false
	}
	return &settings, true
}

func (d *dash) handleChatSettingsSave(w http.ResponseWriter, r *http.Request) {
	folder := groupFromPath(r, "/settings/chat")
	if folder == "" || !groupfolder.IsValidFolder(folder) {
		http.Error(w, "bad folder", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	jid := strings.TrimSpace(r.FormValue("jid"))
	if !d.authorizeChatSettings(w, r, folder, jid) {
		return
	}
	body := map[string]any{}
	switch r.FormValue("thread_replies") {
	case "on":
		body["thread_replies"] = true
	case "off":
		body["thread_replies"] = false
	case "default":
	default:
		http.Error(w, "invalid thread preference", http.StatusBadRequest)
		return
	}
	result, status, err := d.routdCall(r.Context(), http.MethodPut, "/v1/chats/"+url.PathEscape(jid), body)
	if err != nil || status != http.StatusOK {
		http.Error(w, fmt.Sprintf("routd chat settings: status %d: %s %v", status, result, err), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/dash/groups/"+folderPath(folder)+"/settings?jid="+url.QueryEscape(jid), http.StatusSeeOther)
}
