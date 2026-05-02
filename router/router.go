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
		// Reply pointer renders as a sibling header ABOVE the message.
		// Self-closing when no excerpt is available; carries excerpt as
		// body when the parent text is known. Reads naturally:
		//   <reply-to id="3314" sender="bot" time="..."/>
		//   <message id="3325" ...>body</message>
		if m.ReplyToID != "" {
			rSender := m.ReplyToSender
			if rSender == "" {
				rSender = "unknown"
			}
			b.WriteString(`<reply-to id="`)
			b.WriteString(escapeXml(m.ReplyToID))
			b.WriteString(`" sender="`)
			b.WriteString(escapeXml(rSender))
			b.WriteString(`"`)
			if m.ReplyToText != "" {
				b.WriteString(`>`)
				b.WriteString(escapeXml(m.ReplyToText))
				b.WriteString("</reply-to>\n")
			} else {
				b.WriteString("/>\n")
			}
		}
		b.WriteString(`<`)
		b.WriteString(tm.tag)
		if m.ID != "" {
			b.WriteString(` id="`)
			b.WriteString(escapeXml(m.ID))
			b.WriteString(`"`)
		}
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
		if tm.tag == "observed" && m.ChatJID != "" {
			b.WriteString(` source="`)
			b.WriteString(escapeXml(m.ChatJID))
			b.WriteString(`"`)
		}
		if m.Errored {
			b.WriteString(` errored="true"`)
		}
		b.WriteString(`>`)
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
	if strings.SplitN(source, "/", 2)[0] == "root" {
		return true
	}
	return path.Dir(target) == source
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

// RouteMatches reports whether every "key=glob" predicate in r.Match matches
// the corresponding field of msg. Empty match expression matches everything.
// Malformed tokens (no '=' or empty key) are skipped.
//
// Glob semantics:
//   - key=<exact>  value equals <exact>
//   - key=<glob>   value matches glob (path.Match: * doesn't cross /)
//   - key=*        value is present (non-empty); bare * silently rejects empty
//   - key=         value is absent (empty)
//   - omit key     unconstrained — no filter on this field
func RouteMatches(r core.Route, msg core.Message) bool {
	for _, f := range strings.Fields(r.Match) {
		k, pat, ok := strings.Cut(f, "=")
		if !ok || k == "" {
			continue
		}
		val := msgField(msg, k)
		if pat == "" {
			// "key=" — value must be empty/absent
			if val != "" {
				return false
			}
			continue
		}
		// Any non-empty pattern requires non-empty value (so bare * rejects empty).
		if val == "" {
			return false
		}
		m, err := path.Match(pat, val)
		if err != nil || !m {
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
