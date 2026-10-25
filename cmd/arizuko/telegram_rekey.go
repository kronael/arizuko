package main

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/runed/runstore"
	"github.com/kronael/arizuko/store"
)

const telegramOldDM = "telegram:user/"

type telegramChat struct {
	Messages map[string]string
	JIDs     []string
	Latest   string
}

func telegramRekeyPreflight(ctx context.Context, instance, dataDir string,
	accounts map[string]string, inventory renameInventory,
	drivers map[string]objstore.Store) (renamePlan, error) {
	plan := renamePlan{Instance: instance, DataDir: dataDir,
		Inventory: inventory, Accounts: maps.Clone(accounts)}
	for _, owner := range []string{"routd", "runed", "authd", "onbod", "teled"} {
		if _, ok := inventory.Owners[owner]; !ok {
			return plan, fmt.Errorf("inventory is missing owner %s", owner)
		}
	}
	rows := []renameRow{}
	markers := map[string]objstore.Item{}
	for _, owner := range slices.Sorted(maps.Keys(inventory.Owners)) {
		driver := drivers[owner]
		if driver == nil {
			return plan, fmt.Errorf("missing driver for %s", owner)
		}
		entities, err := renameEntities(owner)
		if err != nil {
			return plan, err
		}
		for entity := range inventory.Owners[owner] {
			if !slices.Contains(entities, entity) {
				return plan, fmt.Errorf("unclassified table %s.%s", owner, entity)
			}
		}
		for _, entity := range entities {
			partitions, ok := inventory.Owners[owner][entity]
			if !ok {
				return plan, fmt.Errorf("missing inventory table %s.%s", owner, entity)
			}
			for _, partition := range slices.Sorted(maps.Keys(partitions)) {
				if partition == "" || partitions[partition] < 0 {
					return plan, fmt.Errorf("invalid partition/count in %s.%s", owner, entity)
				}
				query := objstore.Query{Entity: entity, Partition: partition}
				if entity == audit.Entity || entity == store.SecretUseLog {
					query.Project = []string{"__rename_count"}
				}
				items, err := driver.Query(ctx, query)
				if err != nil {
					return plan, fmt.Errorf("read %s.%s/%q: %w", owner, entity, partition, err)
				}
				if len(items) != partitions[partition] {
					return plan, fmt.Errorf("%s.%s/%q count changed: got %d, inventory %d",
						owner, entity, partition, len(items), partitions[partition])
				}
				if entity == audit.Entity || entity == store.SecretUseLog {
					continue
				}
				for _, item := range items {
					if entity == store.ScopeEntity {
						if _, held := item.Attrs["holder"]; held {
							return plan, fmt.Errorf("%s.%s/%q/%q: replace is in flight",
								owner, entity, partition, item.Key.Sort)
						}
						if item.Key == store.OwnerScope(owner).Key {
							markers[owner] = item
						}
					}
					rows = append(rows, renameRow{Owner: owner, Entity: entity, Before: item})
				}
			}
		}
	}
	chats, err := telegramChats(rows, accounts)
	if err != nil {
		return plan, err
	}
	type coordinate struct {
		owner, entity string
		key           objstore.Key
	}
	images := map[coordinate]bool{}
	destinations := map[coordinate]bool{}
	checked := map[[3]string]bool{}
	for _, row := range rows {
		images[coordinate{row.Owner, row.Entity, row.Before.Key}] = true
	}
	for _, row := range rows {
		items, err := telegramRekeyRow(row, chats)
		if err != nil {
			return plan, fmt.Errorf("%s.%s/%q/%q: %w", row.Owner,
				row.Entity, row.Before.Key.Partition, row.Before.Key.Sort, err)
		}
		for i, item := range items {
			if reflect.DeepEqual(row.Before, item) {
				continue
			}
			if _, cold := store.EntityScope(row.Entity); cold && markers[row.Owner].Attrs["version"] == "" {
				return plan, fmt.Errorf("%s.%s/%q/%q: owner has no cold version marker",
					row.Owner, row.Entity, row.Before.Key.Partition, row.Before.Key.Sort)
			}
			dest := coordinate{row.Owner, row.Entity, item.Key}
			if destinations[dest] || (item.Key != row.Before.Key && images[dest]) {
				return plan, fmt.Errorf("%s.%s/%q/%q: destination row exists",
					row.Owner, row.Entity, item.Key.Partition, item.Key.Sort)
			}
			destinations[dest] = true
			partition := [3]string{row.Owner, row.Entity, item.Key.Partition}
			_, inventoried := inventory.Owners[row.Owner][row.Entity][item.Key.Partition]
			if !inventoried && !checked[partition] {
				found, err := drivers[row.Owner].Query(ctx, objstore.Query{
					Entity: row.Entity, Partition: item.Key.Partition})
				if err != nil {
					return plan, err
				}
				if len(found) > 0 {
					return plan, fmt.Errorf("%s.%s/%q/%q: destination partition exists outside inventory",
						row.Owner, row.Entity, item.Key.Partition, found[0].Key.Sort)
				}
				checked[partition] = true
			}
			next := renameRow{Owner: row.Owner, Entity: row.Entity, After: item}
			if i == 0 {
				next.Before = row.Before
			}
			plan.Rows = append(plan.Rows, next)
		}
	}
	return plan, nil
}

func telegramChats(rows []renameRow, accounts map[string]string) (map[string]telegramChat, error) {
	chats := map[string]telegramChat{}
	for _, row := range rows {
		if row.Owner != "routd" {
			continue
		}
		jid := ""
		switch row.Entity {
		case store.Chats:
			jid = row.Before.Key.Sort
		case store.Messages:
			jid = row.Before.Key.Partition
		}
		if jid != "" && !strings.HasPrefix(jid, telegramOldDM) {
			chats[jid] = telegramChat{JIDs: []string{jid}}
		}
	}
	for _, row := range rows {
		jid := row.Before.Key.Partition
		if row.Owner != "routd" || row.Entity != store.Messages || !strings.HasPrefix(jid, telegramOldDM) {
			continue
		}
		chat, ok := chats[jid]
		if !ok {
			chat.Messages = map[string]string{}
		}
		source := row.Before.Attrs["source"]
		inbound := source != "" && row.Before.Attrs["is_bot_message"] != "1"
		if !inbound {
			source = chat.Latest
			if source == "" {
				source = "telegram"
			}
		}
		bot := accounts[source]
		if bot == "" {
			return nil, fmt.Errorf("%s.%s/%q/%q: source %q has no --account mapping",
				row.Owner, row.Entity, jid, row.Before.Key.Sort, source)
		}
		if inbound {
			chat.Latest = source
		}
		next := "telegram:" + bot + "/user/" + strings.TrimPrefix(jid, telegramOldDM)
		chat.Messages[row.Before.Key.Sort] = next
		if !slices.Contains(chat.JIDs, next) {
			chat.JIDs = append(chat.JIDs, next)
		}
		chats[jid] = chat
	}
	for jid, chat := range chats {
		if !strings.HasPrefix(jid, telegramOldDM) {
			continue
		}
		source := chat.Latest
		if source == "" {
			source = "telegram"
		}
		chat.Latest = "telegram:" + accounts[source] + "/user/" + strings.TrimPrefix(jid, telegramOldDM)
		slices.Sort(chat.JIDs)
		chats[jid] = chat
	}
	return chats, nil
}

func telegramRouteMatch(raw string) string {
	terms := strings.Fields(raw)
	for _, term := range terms {
		field, _, _ := strings.Cut(term, "=")
		if field == "platform" && !router.RouteMatches(core.Route{Match: term},
			core.Message{ChatJID: "telegram:"}) {
			return raw
		}
	}
	changed := false
	for i, term := range terms {
		field, value, _ := strings.Cut(term, "=")
		next := value
		if field == "chat_jid" && strings.HasPrefix(value, telegramOldDM) {
			next = "telegram:*/user/" + strings.TrimPrefix(value, telegramOldDM)
		} else if field == "room" && strings.HasPrefix(value, "user/") {
			next = "*/" + value
		}
		if next != value {
			terms[i], changed = field+"="+next, true
		}
	}
	if !changed {
		return raw
	}
	return strings.Join(terms, " ")
}

func telegramRouteCoordinates(raw string) core.Route {
	terms := []string{}
	for term := range strings.FieldsSeq(raw) {
		field, _, _ := strings.Cut(term, "=")
		if field == "chat_jid" || field == "platform" || field == "room" {
			terms = append(terms, term)
		}
	}
	return core.Route{Match: strings.Join(terms, " ")}
}

func telegramRouteVerdicts(before, after string, chats map[string]telegramChat) error {
	oldRoute, newRoute := telegramRouteCoordinates(before), telegramRouteCoordinates(after)
	for _, jid := range slices.Sorted(maps.Keys(chats)) {
		old := router.RouteMatches(oldRoute, core.Message{ChatJID: jid})
		for _, next := range chats[jid].JIDs {
			if old != router.RouteMatches(newRoute, core.Message{ChatJID: next}) {
				return fmt.Errorf("route verdict changes for %q -> %q", jid, next)
			}
		}
	}
	return nil
}

func telegramACLMatcher(raw string) (string, error) {
	parts := strings.Split(raw, ",")
	for i, part := range parts {
		field, value, ok := strings.Cut(part, "=")
		operand := strings.TrimSpace(value)
		if !strings.Contains(part, telegramOldDM) {
			continue
		}
		switch strings.TrimSpace(field) {
		case "sender", "trigger_sender", "principal", "sub", "child", "parent":
			continue
		case "chat_jid", "jid":
		default:
			return "", fmt.Errorf("unknown ACL matcher reference %q", part)
		}
		if ok && strings.HasPrefix(operand, telegramOldDM) {
			next := "telegram:*/user/" + strings.TrimPrefix(operand, telegramOldDM)
			parts[i] = field + "=" + strings.Replace(value, operand, next, 1)
		}
		if strings.Contains(parts[i], telegramOldDM) {
			return "", fmt.Errorf("unknown ACL matcher reference %q", part)
		}
	}
	return strings.Join(parts, ","), nil
}

func telegramRekeyRow(row renameRow, chats map[string]telegramChat) ([]objstore.Item, error) {
	before := row.Before
	after := objstore.Item{Key: before.Key, Attrs: maps.Clone(before.Attrs)}
	if row.Entity == runstore.Spawns || row.Entity == audit.Entity || row.Entity == store.SecretUseLog {
		return []objstore.Item{after}, nil
	}
	if row.Owner == "teled" && (row.Entity == chanstore.InboundPending || row.Entity == chanstore.AckMark) {
		return nil, fmt.Errorf("adapter queue is not drained")
	}
	if row.Entity == store.PendingActions || row.Entity == store.TurnContexts {
		if row.Entity == store.TurnContexts &&
			(before.Attrs["state"] == store.TurnDone || before.Attrs["state"] == store.TurnExpired) {
			return []objstore.Item{after}, nil
		}
		values := []string{before.Key.Partition, before.Key.Sort}
		for field, value := range before.Attrs {
			if field == "sender" || field == "trigger_sender" || field == "principal" || renameHistoricalField(field) {
				continue
			}
			values = append(values, value)
		}
		for _, value := range values {
			if strings.Contains(value, telegramOldDM) {
				return nil, fmt.Errorf("pending action or live turn names an old JID; stop and drain it")
			}
		}
	}
	copyJID, suffix := "", ""
	keyKnown := false
	switch row.Entity {
	case store.Messages:
		keyKnown = true
		if chat, ok := chats[before.Key.Partition]; ok && chat.Messages != nil {
			after.Key.Partition = chat.Messages[before.Key.Sort]
		}
	case store.MessageFolders, store.MessageActivity, store.MessageDelivery:
		keyKnown = true
	case store.Chats, store.ChatProactive, store.Onboarding:
		copyJID = before.Key.Sort
	case store.ChatReplyState:
		var ok bool
		copyJID, suffix, ok = strings.Cut(before.Key.Sort, store.KeySep)
		if !ok {
			return nil, fmt.Errorf("reply state key must contain jid and topic")
		}
		suffix = store.KeySep + suffix
	case store.Routes:
		parts := strings.Split(before.Key.Sort, store.KeySep)
		if len(parts) != 3 {
			return nil, fmt.Errorf("route key must contain seq, target and match")
		}
		next := telegramRouteMatch(parts[2])
		for jid := range chats {
			if core.JidPlatform(jid) != "telegram" &&
				router.RouteMatches(telegramRouteCoordinates(parts[2]), core.Message{ChatJID: jid}) {
				next = parts[2]
				break
			}
		}
		if err := telegramRouteVerdicts(parts[2], next, chats); err != nil {
			return nil, err
		}
		for _, term := range strings.Fields(parts[2]) {
			field, _, _ := strings.Cut(term, "=")
			if strings.Contains(term, telegramOldDM) && field != "chat_jid" &&
				field != "sender" && field != "trigger_sender" {
				return nil, fmt.Errorf("unknown route operand %q", term)
			}
		}
		if strings.Contains(parts[0]+parts[1], telegramOldDM) {
			return nil, fmt.Errorf("unknown route key reference")
		}
		parts[2] = next
		after.Key.Sort, keyKnown = strings.Join(parts, store.KeySep), true
	case store.ACL:
		parts := strings.Split(before.Key.Sort, store.KeySep)
		if len(parts) != 6 {
			return nil, fmt.Errorf("ACL key must contain six fields")
		}
		for _, i := range []int{1, 2, 5} {
			if strings.Contains(parts[i], telegramOldDM) {
				return nil, fmt.Errorf("unknown ACL key reference")
			}
		}
		for _, i := range []int{3, 4} {
			next, err := telegramACLMatcher(parts[i])
			if err != nil {
				return nil, err
			}
			parts[i] = next
		}
		after.Key.Sort, keyKnown = strings.Join(parts, store.KeySep), true
	case store.ACLMembership:
		keyKnown = true
	}
	for field, value := range before.Attrs {
		switch field {
		case "sender", "trigger_sender", "principal":
			continue
		case "jid":
			if row.Entity == store.RouteTokens {
				continue
			}
		case "chat_jid":
			if strings.HasPrefix(value, telegramOldDM) {
				chat, ok := chats[value]
				if !ok {
					return nil, fmt.Errorf("chat_jid %q has no messages in inventory", value)
				}
				switch row.Entity {
				case store.MessageFolders, store.MessageActivity, store.MessageDelivery:
					next := chat.Messages[before.Key.Sort]
					if next == "" {
						return nil, fmt.Errorf("chat_jid %q has no message at this sort key", value)
					}
					after.Attrs[field] = next
					continue
				case store.ScheduledTasks:
					after.Attrs[field] = chat.Latest
					continue
				}
			}
		}
		if strings.Contains(value, telegramOldDM) && !renameHistoricalField(field) {
			return nil, fmt.Errorf("unknown field %q carries an old JID", field)
		}
	}
	if strings.HasPrefix(copyJID, telegramOldDM) {
		chat, ok := chats[copyJID]
		if !ok {
			return nil, fmt.Errorf("chat %q has no messages in inventory", copyJID)
		}
		items := []objstore.Item{}
		for _, jid := range chat.JIDs {
			item := objstore.Item{Key: after.Key, Attrs: maps.Clone(after.Attrs)}
			item.Key.Sort = jid + suffix
			items = append(items, item)
		}
		return items, nil
	}
	if (!keyKnown && strings.Contains(before.Key.Sort, telegramOldDM)) ||
		(row.Entity != store.Messages && strings.Contains(before.Key.Partition, telegramOldDM)) {
		return nil, fmt.Errorf("unknown key carries an old JID")
	}
	return []objstore.Item{after}, nil
}
