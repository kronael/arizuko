package routd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/store"
)

func (d *DB) QueueReleaseAnnouncements(changelogPath string) error {
	routes, err := d.Routes()
	if err != nil {
		return err
	}
	var tagged []core.Route
	for _, route := range routes {
		if core.ParseRouteTarget(route.Target).Announce {
			tagged = append(tagged, route)
		}
	}
	if len(tagged) == 0 {
		return nil
	}
	data, err := os.ReadFile(changelogPath)
	if err != nil {
		return err
	}
	version, content, err := releaseAnnouncement(string(data))
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, route := range tagged {
		jid, key, err := announcementDestination(route)
		if err != nil {
			return fmt.Errorf("announcement route %q: %w", route.Match, err)
		}
		if jid == "" || seen[key] {
			continue
		}
		seen[key] = true
		id := "release-" + version + "-" + key
		items, err := d.Obj().Query(context.Background(), objstore.Query{Entity: store.Messages, Partition: jid, Where: []objstore.Cmp{{Attr: "verb", Op: objstore.Eq, Value: "release_announcement"}}, Project: []string{"verb"}})
		if err != nil {
			return err
		}
		exists := false
		for _, item := range items {
			_, storedID, ok := store.MsgCoord(item.Key.Sort)
			if ok && storedID == id {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		msg := core.Message{ID: id, ChatJID: jid, Content: content, Timestamp: time.Now().UTC(), FromMe: true, BotMsg: true, RoutedTo: core.ParseRouteTarget(route.Target).Folder, Verb: "release_announcement"}
		if err := d.PutOutbound(msg, core.MessageStatusPending); err != nil {
			return err
		}
	}
	return nil
}

func releaseAnnouncement(changelog string) (string, string, error) {
	version, bullet := "", ""
	for _, line := range strings.Split(changelog, "\n") {
		if strings.HasPrefix(line, "## [v") {
			if version != "" {
				break
			}
			version, _, _ = strings.Cut(strings.TrimPrefix(line, "## ["), "]")
		} else if version != "" && strings.HasPrefix(line, "> • ") {
			bullet = strings.TrimPrefix(line, "> • ")
			break
		}
	}
	if version == "" || bullet == "" {
		return "", "", fmt.Errorf("changelog has no complete release announcement")
	}
	return version, version + " — " + bullet + "\n\nhttps://github.com/kronael/arizuko/blob/main/CHANGELOG.md", nil
}

func announcementDestination(route core.Route) (string, string, error) {
	fields := make(map[string]string)
	for field := range strings.FieldsSeq(route.Match) {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" {
			return "", "", fmt.Errorf("invalid selector %q", field)
		}
		fields[key] = value
	}
	jid := fields["chat_jid"]
	if jid == "" {
		if fields["platform"] == "" || fields["room"] == "" {
			return "", "", fmt.Errorf("route has no concrete destination")
		}
		jid = fields["platform"] + ":" + fields["room"]
	}
	if strings.ContainsAny(jid, "*?[\\") {
		return "", "", nil
	}
	platform, room, ok := strings.Cut(jid, ":")
	if !ok || platform == "" || room == "" {
		return "", "", fmt.Errorf("invalid chat JID %q", jid)
	}
	if platform == "web" || platform == "slink" || platform == "hook" {
		return "", "", nil
	}
	key := jid
	switch platform {
	case "telegram":
	case "slack":
		parts, ok := chanlib.ParseSlackJID(jid)
		if !ok {
			return "", "", fmt.Errorf("invalid Slack JID %q", jid)
		}
		key = "slack:" + parts.Workspace
	case "discord":
		guild, channel, ok := strings.Cut(room, "/")
		if !ok || guild == "" || channel == "" || strings.Contains(channel, "/") {
			return "", "", fmt.Errorf("invalid Discord JID %q", jid)
		}
		if guild != "dm" {
			key = "discord:" + guild
		}
	default:
		return "", "", fmt.Errorf("unsupported platform %q", platform)
	}
	sender := fields["sender"]
	if strings.ContainsAny(sender, "*?[\\") {
		sender = ""
	}
	if !router.RouteMatchesIgnoreVerb(route, core.Message{ChatJID: jid, Sender: sender}) {
		return "", "", fmt.Errorf("selectors disagree for %q", jid)
	}
	if core.ParseRouteTarget(route.Target).Folder == "" {
		return "", "", fmt.Errorf("empty target folder")
	}
	return jid, key, nil
}
