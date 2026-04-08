package router

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/onvos/arizuko/core"
)

func escapeXml(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

func ClockXml(tz string) string {
	loc := time.UTC
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	now := time.Now().In(loc)
	return fmt.Sprintf(`<clock time="%s" tz="%s"/>`,
		now.Format("2006-01-02T15:04:05Z07:00"), loc.String())
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

type taggedMsg struct {
	msg core.Message
	tag string
}

func FormatMessages(msgs []core.Message, observed ...[]core.Message) string {
	tagged := make([]taggedMsg, 0, len(msgs))
	for _, m := range msgs {
		tagged = append(tagged, taggedMsg{m, "message"})
	}
	if len(observed) > 0 {
		for _, m := range observed[0] {
			tagged = append(tagged, taggedMsg{m, "observed"})
		}
	}
	sort.Slice(tagged, func(i, j int) bool {
		return tagged[i].msg.Timestamp.Before(tagged[j].msg.Timestamp)
	})

	var b strings.Builder
	b.WriteString("<messages>\n")
	for _, tm := range tagged {
		m := tm.msg
		name := m.Name
		if name == "" {
			name = m.Sender
		}
		b.WriteString(`<`)
		b.WriteString(tm.tag)
		b.WriteString(` sender="`)
		b.WriteString(escapeXml(name))
		b.WriteString(`"`)
		if m.Sender != "" && m.Sender != name {
			b.WriteString(` sender_id="`)
			b.WriteString(escapeXml(m.Sender))
			b.WriteString(`"`)
		}
		b.WriteString(` time="`)
		b.WriteString(m.Timestamp.Format("2006-01-02T15:04:05Z"))
		b.WriteString(`" ago="`)
		b.WriteString(timeAgo(m.Timestamp))
		b.WriteString(`"`)
		if m.ChatJID != "" {
			b.WriteString(` chat_id="`)
			b.WriteString(escapeXml(m.ChatJID))
			b.WriteString(`"`)
		}
		if p := core.JidPlatform(m.ChatJID); p != "" {
			b.WriteString(` platform="`)
			b.WriteString(p)
			b.WriteString(`"`)
		}
		if m.Verb != "" && m.Verb != "message" {
			b.WriteString(` verb="`)
			b.WriteString(escapeXml(m.Verb))
			b.WriteString(`"`)
		}
		if m.Topic != "" {
			b.WriteString(` thread="`)
			b.WriteString(escapeXml(m.Topic))
			b.WriteString(`"`)
		}
		if m.ReplyToID != "" {
			b.WriteString(` reply_to="`)
			b.WriteString(escapeXml(m.ReplyToID))
			b.WriteString(`"`)
		}
		if tm.tag == "observed" && m.ChatJID != "" {
			b.WriteString(` source="`)
			b.WriteString(escapeXml(m.ChatJID))
			b.WriteString(`"`)
		}
		b.WriteString(`>`)
		if m.ReplyToText != "" {
			b.WriteString(`<reply_to sender="`)
			rSender := m.ReplyToSender
			if rSender == "" {
				rSender = "unknown"
			}
			b.WriteString(escapeXml(rSender))
			if m.ReplyToID != "" {
				b.WriteString(`" id="`)
				b.WriteString(escapeXml(m.ReplyToID))
			}
			b.WriteString(`">`)
			b.WriteString(escapeXml(m.ReplyToText))
			b.WriteString("</reply_to>")
		}
		b.WriteString(escapeXml(m.Content))
		b.WriteString("</")
		b.WriteString(tm.tag)
		b.WriteString(">\n")
	}
	b.WriteString("</messages>")
	return b.String()
}

var internalRe = regexp.MustCompile(`(?s)<internal>.*?</internal>`)
var statusRe = regexp.MustCompile(`(?s)<status>(.*?)</status>`)

func FormatOutbound(raw string) string {
	s := internalRe.ReplaceAllString(raw, "")
	s = StripThinkBlocks(s)
	s = statusRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func ExtractStatusBlocks(s string) (string, []string) {
	var statuses []string
	cleaned := statusRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := statusRe.FindStringSubmatch(m)
		if len(sub) > 1 {
			t := strings.TrimSpace(sub[1])
			if t != "" {
				statuses = append(statuses, t)
			}
		}
		return ""
	})
	return cleaned, statuses
}

func StripThinkBlocks(s string) string {
	var b strings.Builder
	depth, i := 0, 0
	for i < len(s) {
		if strings.HasPrefix(s[i:], "<think>") {
			depth++
			i += 7
		} else if strings.HasPrefix(s[i:], "</think>") && depth > 0 {
			depth--
			i += 8
		} else if depth > 0 {
			i++
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

var platformShort = map[string]string{
	"telegram": "tg", "whatsapp": "wa", "discord": "dc",
	"email": "em", "web": "web",
}

func senderToUserFileID(sender string) string {
	parts := strings.SplitN(sender, ":", 2)
	if len(parts) != 2 {
		return sender
	}
	short := platformShort[parts[0]]
	if short == "" {
		p := parts[0]
		if len(p) > 2 {
			p = p[:2]
		}
		short = p
	}
	return short + "-" + parts[1]
}

var nameRe = regexp.MustCompile(`(?m)^name:\s*(.+)$`)

func UserContextXml(sender, groupDir string) string {
	if sender == "" || sender == "system" {
		return ""
	}
	id := senderToUserFileID(sender)
	attrs := []string{`id="` + escapeXml(id) + `"`}

	usersDir := filepath.Join(groupDir, "users")
	userFile := filepath.Join(usersDir, id+".md")
	resolved, err := filepath.Abs(userFile)
	if err == nil && strings.HasPrefix(resolved, filepath.Clean(usersDir)+string(os.PathSeparator)) {
		if data, err := os.ReadFile(userFile); err == nil {
			content := string(data)
			if m := nameRe.FindStringSubmatch(content); len(m) > 1 {
				attrs = append(attrs, `name="`+escapeXml(strings.TrimSpace(m[1]))+`"`)
			}
			attrs = append(attrs, `memory="~/users/`+id+`.md"`)
		}
	}
	return "<user " + strings.Join(attrs, " ") + " />"
}

func IsAuthorizedRoutingTarget(source, target string) bool {
	srcRoot := strings.SplitN(source, "/", 2)[0]
	if srcRoot == "root" {
		return true
	}
	tgtRoot := strings.SplitN(target, "/", 2)[0]
	if srcRoot != tgtRoot {
		return false
	}
	if len(target) <= len(source) {
		return false
	}
	suffix := target[len(source):]
	return strings.HasPrefix(suffix, "/") && len(suffix) > 1 &&
		strings.IndexByte(suffix[1:], '/') == -1
}

func expandTarget(target string, msg core.Message) string {
	if !strings.Contains(target, "{") {
		return target
	}
	id := senderToUserFileID(msg.Sender)
	if id == "" {
		return ""
	}
	return strings.ReplaceAll(target, "{sender}", id)
}

// matchPredicate is one key=glob pair parsed from a route's match expression.
type matchPredicate struct {
	key     string
	pattern string
}

// parseMatch splits "key=glob key=glob ..." into predicates. Whitespace is
// the only separator. Malformed tokens (no '=' or empty key) are skipped.
func parseMatch(s string) []matchPredicate {
	if s == "" {
		return nil
	}
	fields := strings.Fields(s)
	out := make([]matchPredicate, 0, len(fields))
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		if !ok || k == "" {
			continue
		}
		out = append(out, matchPredicate{key: k, pattern: v})
	}
	return out
}

// msgField returns the message value for a match key. Unknown keys yield
// an empty string, which will not match any non-empty glob.
func msgField(msg core.Message, key string) string {
	switch key {
	case "platform":
		return core.JidPlatform(msg.ChatJID)
	case "room":
		return core.JidRoom(msg.ChatJID)
	case "chat_jid":
		return msg.ChatJID
	case "sender":
		return msg.Sender
	case "verb":
		return msg.Verb
	}
	return ""
}

// RouteMatches reports whether every predicate in r.Match matches the
// corresponding field of msg. Empty match expression matches everything.
func RouteMatches(r core.Route, msg core.Message) bool {
	for _, p := range parseMatch(r.Match) {
		v := msgField(msg, p.key)
		ok, err := path.Match(p.pattern, v)
		if err != nil || !ok {
			return false
		}
	}
	return true
}

func ResolveRoute(msg core.Message, routes []core.Route) string {
	for _, r := range routes {
		if !RouteMatches(r, msg) {
			continue
		}
		t := expandTarget(r.Target, msg)
		if t != "" {
			return t
		}
	}
	return ""
}
