package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/emaid/emaistore"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/runed/runstore"
	"github.com/kronael/arizuko/store"
)

type folderRename struct{ Source, Destination string }
type renameMap []folderRename

func newRenameMap(source, destination string, exceptions []string) (renameMap, error) {
	mapping := renameMap{{source, destination}}
	for _, raw := range exceptions {
		old, next, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, fmt.Errorf("map %q needs old=new", raw)
		}
		mapping = append(mapping, folderRename{old, next})
	}
	for i, entry := range mapping {
		if !groupfolder.IsValidFolder(entry.Source) || !groupfolder.IsValidFolder(entry.Destination) {
			return nil, fmt.Errorf("invalid folder mapping %q=%q", entry.Source, entry.Destination)
		}
		if !renameContains(source, entry.Source) || !renameContains(destination, entry.Destination) {
			return nil, fmt.Errorf("map %q=%q must stay inside %q=%q", entry.Source, entry.Destination, source, destination)
		}
		if renameContains(source, destination) || renameContains(destination, source) {
			return nil, fmt.Errorf("source and destination subtrees overlap")
		}
		for _, other := range mapping[:i] {
			if (!renameContains(entry.Source, other.Source) && !renameContains(other.Source, entry.Source)) && (renameContains(entry.Destination, other.Destination) || renameContains(other.Destination, entry.Destination)) {
				return nil, fmt.Errorf("sibling mappings cannot overlap destination subtrees")
			}
			if entry.Source == other.Source || entry.Destination == other.Destination {
				return nil, fmt.Errorf("duplicate mapping %q=%q", entry.Source, entry.Destination)
			}
		}
	}
	slices.SortFunc(mapping, func(a, b folderRename) int { return len(b.Source) - len(a.Source) })
	return mapping, nil
}

func renameContains(parent, child string) bool {
	return parent == child || strings.HasPrefix(child, parent+"/")
}

func (mapping renameMap) folder(value string) string {
	for _, entry := range mapping {
		if value == entry.Source || strings.HasPrefix(value, entry.Source+"/") ||
			strings.HasPrefix(value, entry.Source+"#") {
			return entry.Destination + strings.TrimPrefix(value, entry.Source)
		}
	}
	return value
}

func (mapping renameMap) identity(value string) string {
	if folder := groupfolder.JidFolder(value); folder != "" {
		next := mapping.folder(folder)
		if value == groupfolder.WebJID(folder) {
			return groupfolder.WebJID(next)
		}
		before := strings.TrimSuffix(groupfolder.HookJID(folder, ""), "/")
		return strings.TrimSuffix(groupfolder.HookJID(next, ""), "/") + strings.TrimPrefix(value, before)
	}
	for _, prefix := range []string{"agent:", "folder:"} {
		if strings.HasPrefix(value, prefix) {
			return prefix + mapping.folder(strings.TrimPrefix(value, prefix))
		}
	}
	if !strings.Contains(value, ":") {
		return mapping.folder(value)
	}
	return value
}

func (mapping renameMap) principal(value string) string {
	prefix := auth.FolderPrincipal("")
	if strings.HasPrefix(value, prefix) {
		return auth.FolderPrincipal(mapping.folder(strings.TrimPrefix(value, prefix)))
	}
	return value
}

func (mapping renameMap) web(value string) string {
	for _, prefix := range []string{"/pub/", "/priv/", "/dav/"} {
		if strings.HasPrefix(value, prefix) {
			return prefix + mapping.folder(strings.TrimPrefix(value, prefix))
		}
	}
	return value
}

func renameEntities(owner string) ([]string, error) {
	entities, ok := store.EntitiesByOwner[owner]
	if ok {
		return append(slices.Clone(entities), store.ScopeEntity, audit.Entity), nil
	}
	switch owner {
	case "runed":
		return append(slices.Clone(runstore.Entities), audit.Entity), nil
	case "authd":
		return append(slices.Clone(authstore.Entities), audit.Entity), nil
	case "emaid":
		return append(slices.Clone(emaistore.Entities), chanstore.Entities...), nil
	case "discd", "slakd", "teled", "gitd", "whapd":
		return slices.Clone(chanstore.Entities), nil
	default:
		return nil, fmt.Errorf("owner %q has no classified rename schema", owner)
	}
}

func (mapping renameMap) structured(raw, root string) (string, error) {
	if raw == "" {
		return raw, nil
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("invalid structured reference: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", fmt.Errorf("structured reference must contain one JSON document")
	}
	changed, err := mapping.jsonValue(value, "", root)
	if err != nil {
		return "", err
	}
	if !changed {
		return raw, nil
	}
	data, err := json.Marshal(value)
	return string(data), err
}

func (mapping renameMap) jsonValue(value any, field, root string) (bool, error) {
	changed := false
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if text, ok := child.(string); ok {
				next := text
				switch strings.ToLower(key) {
				case "folder", "group_folder", "owner_folder", "target_folder", "routed_to", "target_glob", "scope":
					next = mapping.folder(text)
				case "scope_id":
					if node["scope_kind"] == "folder" {
						next = mapping.folder(text)
					}
				case "host", "host_path":
					next = mapping.filePath(root, text)
				case "container":
				case "sender":
					if node["is_bot_message"] == true || node["is_bot_message"] == "1" {
						next = mapping.identity(text)
					}
				case "trigger_sender":
					if strings.HasPrefix(text, "agent:") || strings.HasPrefix(text, "folder:") {
						next = mapping.identity(text)
					}
				case "params", "predicate":
					var err error
					next, err = mapping.matcher(text, root, key == "params")
					if err != nil {
						return false, err
					}
				case "principal", "child", "parent":
					next = mapping.principal(text)
				case "jid", "chat_jid", "return_to", "caller_agent":
					next = mapping.identity(text)
				case "path", "path_prefix", "redirect_to", "url":
					next = mapping.web(text)
				default:
					if mapping.hasReference(text) && !renameHistoricalField(key) {
						return false, fmt.Errorf("unclassified structured field %q carries an old coordinate", key)
					}
				}
				node[key] = next
				changed = changed || next != text
			} else {
				altered, err := mapping.jsonValue(child, key, root)
				if err != nil {
					return false, err
				}
				changed = changed || altered
			}
		}
	case []any:
		for _, child := range node {
			altered, err := mapping.jsonValue(child, field, root)
			if err != nil {
				return false, err
			}
			changed = changed || altered
		}
	case string:
		if mapping.hasReference(node) && !renameHistoricalField(field) {
			return false, fmt.Errorf("unclassified structured array %q carries an old coordinate", field)
		}
	}
	return changed, nil
}

func renameHistoricalField(field string) bool {
	if strings.HasSuffix(field, "_id") || strings.HasSuffix(field, "_hash") {
		return true
	}
	switch field {
	case "user_sub", "model", "product", "provider", "scope_list", "tool", "content", "body", "prompt", "text", "description", "message", "result", "error", "preview", "context", "reviewer_note", "name", "sender_name", "reply_to_text", "chat_name", "last_error", "id", "task_id", "turn_id", "run_id", "session_id", "session_log_id", "parent_topic", "topic", "sticky_topic", "agent_cursor", "observed_cursor", "last_reply_id", "reply_to_id", "trigger_msg_id", "platform_id", "notice_id", "branch", "revision", "asset_hashes", "value", "refresh_val", "priv_pem", "pub_pem", "secret", "provider_sub", "from_address", "root_msg_id", "thread_id", "issued_by_sub", "added_by", "granted_by", "created_by", "source", "key", "url", "container_name", "forwarded_from", "link_context", "attachments", "reply_to_sender":
		return true
	default:
		return false
	}
}

func (mapping renameMap) hasReference(text string) bool {
	for _, entry := range mapping {
		offset := 0
		for {
			idx := strings.Index(text[offset:], entry.Source)
			if idx < 0 {
				break
			}
			idx += offset
			end := idx + len(entry.Source)
			left := idx == 0 || strings.ContainsRune("/:=\"' \t\n([{,", rune(text[idx-1]))
			right := end == len(text) || strings.ContainsRune("/#*?\"' \t\n)]},:;", rune(text[end]))
			if left && right {
				return true
			}
			offset = end
		}
	}
	return false
}

func (mapping renameMap) match(raw string) (string, error) {
	terms := strings.Fields(raw)
	webRoom := slices.Contains(terms, "platform=web")
	hookRoom := slices.Contains(terms, "platform=hook")
	changed := false
	for i, term := range terms {
		field, value, ok := strings.Cut(term, "=")
		if !ok {
			if mapping.hasReference(term) {
				return "", fmt.Errorf("unclassified route match")
			}
			continue
		}
		next := value
		switch field {
		case "chat_jid":
			next = mapping.identity(value)
		case "room":
			if webRoom {
				next = core.JidRoom(mapping.identity(groupfolder.WebJID(value)))
			} else if hookRoom {
				jid := strings.TrimSuffix(groupfolder.HookJID(value, ""), "/")
				next = core.JidRoom(mapping.identity(jid))
			}
		case "sender":
			if strings.HasPrefix(value, "agent:") || strings.HasPrefix(value, "folder:") {
				next = mapping.identity(value)
			}
		case "folder", "routed_to":
			next = mapping.folder(value)
		default:
			if mapping.hasReference(value) {
				return "", fmt.Errorf("unclassified route operand %q", field)
			}
		}
		if value != next {
			terms[i] = field + "=" + next
			changed = true
		}
	}
	if !changed {
		return raw, nil
	}
	return strings.Join(terms, " "), nil
}

func renameExactACLCoordinates(raw string) error {
	for clause := range strings.SplitSeq(raw, ",") {
		field, value, ok := strings.Cut(clause, "=")
		if !ok {
			continue
		}
		field, value = strings.TrimSpace(field), strings.TrimSpace(value)
		coordinate := false
		switch field {
		case "folder", "group_folder", "owner", "owner_folder", "target_folder", "routed_to", "scope", "scope_id", "target_glob", "route", "path", "path_prefix", "redirect_to", "url", "host_path":
			coordinate = true
		case "jid", "chat_jid", "context_jid", "return_to":
			coordinate = !strings.Contains(value, ":") || groupfolder.JidFolder(value) != "" || strings.HasPrefix(value, "agent:") || strings.HasPrefix(value, "folder:")
		case "principal", "child", "parent", "sub", "sender", "trigger_sender":
			coordinate = strings.HasPrefix(value, "agent:") || strings.HasPrefix(value, auth.FolderPrincipal(""))
		}
		if coordinate {
			if _, err := auth.ParamsSpec(map[string]string{field: value}); err != nil {
				return fmt.Errorf("coordinate ACL matcher %q requires exact values for offline rename: %w", clause, err)
			}
		}
	}
	return nil
}

func (mapping renameMap) matcher(raw, root string, params bool) (string, error) {
	if params {
		if err := auth.ValidateParams(raw); err != nil {
			return "", err
		}
	}
	parts := strings.Split(raw, ",")
	folderScope := false
	for _, part := range parts {
		name, value, ok := strings.Cut(part, "=")
		if ok && strings.TrimSpace(name) == "scope_kind" && strings.TrimSpace(value) == "folder" {
			folderScope = true
		}
	}
	changed := false
	for i, part := range parts {
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		field, original := strings.TrimSpace(name), strings.TrimSpace(value)
		next := original
		switch field {
		case "folder", "group_folder", "owner", "owner_folder", "target_folder", "routed_to", "scope", "target_glob", "route":
			next = mapping.folder(original)
		case "scope_id":
			if folderScope {
				next = mapping.folder(original)
			} else if strings.Contains(raw, "scope_kind=user") {
			} else if mapping.hasReference(original) {
				return "", fmt.Errorf("scope_id matcher needs an explicit scope_kind")
			}
		case "principal", "child", "parent":
			next = mapping.principal(original)
		case "sub", "sender", "trigger_sender":
			if strings.HasPrefix(original, "agent:") || strings.HasPrefix(original, auth.FolderPrincipal("")) {
				next = mapping.identity(original)
			}
		case "jid", "chat_jid", "context_jid", "return_to":
			next = mapping.identity(original)
		case "path", "path_prefix", "redirect_to", "url":
			next = mapping.web(original)
		case "host_path":
			next = mapping.filePath(root, original)
		default:
			if mapping.hasReference(original) && !renameHistoricalField(field) {
				return "", fmt.Errorf("unclassified matcher operand %q", field)
			}
		}
		if next != original {
			parts[i] = name + "=" + strings.Replace(value, original, next, 1)
			changed = true
		}
	}
	if !changed {
		return raw, nil
	}
	return strings.Join(parts, ","), nil
}

func (mapping renameMap) row(owner, entity string, before objstore.Item, root string) (objstore.Item, error) {
	after := objstore.Item{Key: before.Key, Attrs: maps.Clone(before.Attrs)}
	if entity == audit.Entity || entity == store.SecretUseLog {
		return after, nil
	}
	parts := strings.Split(before.Key.Sort, store.KeySep)
	arity := 0
	switch entity {
	case store.MessageFolderCounts:
		after.Key.Sort = mapping.identity(before.Key.Sort)
	case store.Groups:
		after.Key.Sort = mapping.folder(before.Key.Sort)
	case store.Chats, store.ChatProactive, store.Onboarding:
		after.Key.Sort = mapping.identity(before.Key.Sort)
	case store.Sessions, store.SystemMessages, store.TurnResults, store.NetworkRules, store.InstalledPackages:
		arity = 2
		if len(parts) == arity {
			parts[0] = mapping.folder(parts[0])
		}
	case store.ChatReplyState:
		arity = 2
		if len(parts) == arity {
			parts[0] = mapping.identity(parts[0])
		}
	case store.GroupWatchers:
		arity = 2
		if len(parts) == arity {
			parts[0] = mapping.folder(parts[0])
			parts[1] = mapping.folder(parts[1])
		}
	case store.ACLMembership:
		arity = 2
		if len(parts) == arity {
			parts[0] = mapping.principal(parts[0])
			parts[1] = mapping.principal(parts[1])
		}
	case store.Routes:
		arity = 3
		if len(parts) == arity {
			parts[1] = mapping.folder(parts[1])
			var err error
			parts[2], err = mapping.match(parts[2])
			if err != nil {
				return after, err
			}
		}
	case store.ACL:
		arity = 6
		if len(parts) == arity {
			parts[0] = mapping.principal(parts[0])
			parts[2] = mapping.folder(parts[2])
			for _, i := range []int{3, 4} {
				if err := renameExactACLCoordinates(parts[i]); err != nil {
					return after, err
				}
				if mapping.hasReference(parts[i]) {
					next, err := mapping.matcher(parts[i], root, i == 3)
					if err != nil {
						return after, err
					}
					parts[i] = next
				}
			}
		}
	case store.Secrets:
		arity = 3
		if len(parts) == arity && parts[0] == store.SecretScopeFolder {
			parts[1] = mapping.folder(parts[1])
		}
	case store.ProxydRoutes, store.WebRoutes:
		after.Key.Sort = mapping.web(before.Key.Sort)
	case store.Messages:
		after.Key.Partition = mapping.identity(before.Key.Partition)
	case store.MessageFolders:
		after.Key.Partition = mapping.identity(before.Key.Partition)
	case runstore.SessionLog:
		after.Key.Partition = mapping.folder(before.Key.Partition)
	case store.RouteTokens, store.ScheduledTasks, store.PendingActions, store.GitHubSources, store.PaneSessions, store.Counters, store.UserProfiles, store.TaskRunLogs, store.TurnContexts, store.MessageActivity, store.MessageDelivery, store.OnboardingGates, store.Invites, store.InviteRedemptions, store.ScopeEntity, runstore.Spawns, authstore.SigningKeys, authstore.SigningKeySlot, authstore.AuthUsers, authstore.OAuthIdentities, authstore.RefreshTokens, authstore.RefreshFamilies, chanstore.OutboundDedup, chanstore.InboundPending, chanstore.FileURL, emaistore.EmailThreads, emaistore.EmailMsgIDs:
	case runstore.CircuitBreaker:
		after.Key.Partition = mapping.folder(before.Key.Partition)
		after.Key.Sort = mapping.folder(before.Key.Sort)
	default:
		return after, fmt.Errorf("unclassified coordinate schema %s.%s", owner, entity)
	}
	if arity > 0 {
		if len(parts) != arity {
			return after, fmt.Errorf("%s key has %d components, expected %d", entity, len(parts), arity)
		}
		after.Key.Sort = strings.Join(parts, store.KeySep)
	}
	for field, value := range before.Attrs {
		next := value
		switch entity {
		case store.ScopeEntity:
			if field == "holder" {
				return after, fmt.Errorf("%s replace is in flight; settle it before rename", owner)
			}
		}
		switch field {
		case "folder", "group_folder", "owner", "owner_folder", "target_folder", "routed_to", "sticky_group", "engaged_folder", "target_glob":
			next = mapping.folder(value)
		case "sender":
			if (entity == store.Messages || entity == store.MessageActivity || entity == store.MessageFolders) && before.Attrs["is_bot_message"] == "1" {
				next = mapping.identity(value)
			}
		case "sub", "trigger_sender":
			if strings.HasPrefix(value, "agent:") || strings.HasPrefix(value, "folder:") {
				next = mapping.identity(value)
			}
		case "principal":
			next = mapping.principal(value)
		case "jid", "chat_jid", "context_jid", "return_to", "caller_agent":
			next = mapping.identity(value)
		case "redirect_to", "path_prefix", "path":
			next = mapping.web(value)
		case "scope":
			if owner == "authd" {
				scopes := strings.Split(value, ",")
				for i, v := range scopes {
					scopes[i] = mapping.identity(v)
				}
				next = strings.Join(scopes, ",")
			} else {
				next = mapping.folder(value)
			}
		case "params", "predicate":
			if mapping.hasReference(value) {
				var err error
				next, err = mapping.matcher(value, root, field == "params")
				if err != nil {
					return after, err
				}
			}
		case "args", "payload", "config", "container_config", "manifest":
			if mapping.hasReference(value) {
				var err error
				next, err = mapping.structured(value, root)
				if err != nil {
					return after, fmt.Errorf("%s.%s: %w", entity, field, err)
				}
			}
		default:
			if mapping.hasReference(value) && !renameHistoricalField(field) {
				return after, fmt.Errorf("unclassified reference in %s.%s", entity, field)
			}
		}
		after.Attrs[field] = next
	}

	if entity == store.RouteTokens && before.Attrs["jid"] != after.Attrs["jid"] {
		target := before.Attrs["target_folder"]
		if target != "" && groupfolder.JidFolder(before.Attrs["jid"]) != target {
			return after, fmt.Errorf("route token %q has unsupported JID ownership: %q disagrees with recorded target %q", before.Key.Sort, before.Attrs["jid"], target)
		}
	}
	if !reflect.DeepEqual(before, after) {
		state := before.Attrs["state"]
		switch entity {
		case runstore.Spawns:
			if state != "exited" && state != "killed" && state != "error" {
				return after, fmt.Errorf("run %q is %q; stop and drain it", before.Key.Sort, state)
			}
		case store.TurnContexts:
			if state != store.TurnDone && state != store.TurnExpired {
				return after, fmt.Errorf("turn %q is %q; stop and drain it", before.Key.Sort, state)
			}
		}
	}
	return after, nil
}
