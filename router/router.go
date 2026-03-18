package router

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/onvos/arizuko/core"
)

func EscapeXml(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// ClockXml returns a <clock> tag with current time and timezone.
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

// TimeAgo returns a human-friendly duration since t (e.g. "3m", "2h", "1d").
func TimeAgo(t time.Time) string {
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

func FormatMessages(msgs []core.Message) string {
	var b strings.Builder
	b.WriteString("<messages>\n")
	for _, m := range msgs {
		name := m.Name
		if name == "" {
			name = m.Sender
		}
		b.WriteString(`<message sender="`)
		b.WriteString(EscapeXml(name))
		b.WriteString(`"`)
		if m.Sender != "" && m.Sender != name {
			b.WriteString(` sender_id="`)
			b.WriteString(EscapeXml(m.Sender))
			b.WriteString(`"`)
		}
		b.WriteString(` time="`)
		b.WriteString(m.Timestamp.Format("2006-01-02T15:04:05Z"))
		b.WriteString(`" ago="`)
		b.WriteString(TimeAgo(m.Timestamp))
		b.WriteString(`"`)
		if m.ReplyToID != "" {
			b.WriteString(` reply_to="`)
			b.WriteString(EscapeXml(m.ReplyToID))
			b.WriteString(`"`)
		}
		b.WriteString(`>`)
		if m.ReplyToText != "" {
			b.WriteString(`<reply_to sender="`)
			rSender := m.ReplyToSender
			if rSender == "" {
				rSender = "unknown"
			}
			b.WriteString(EscapeXml(rSender))
			if m.ReplyToID != "" {
				b.WriteString(`" id="`)
				b.WriteString(EscapeXml(m.ReplyToID))
			}
			b.WriteString(`">`)
			b.WriteString(EscapeXml(m.ReplyToText))
			b.WriteString("</reply_to>")
		}
		b.WriteString(EscapeXml(m.Content))
		b.WriteString("</message>\n")
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

// ExtractStatusBlocks removes <status> blocks and returns them separately.
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

// StripThinkBlocks removes <think>...</think> blocks including nested ones.
func StripThinkBlocks(s string) string {
	var b strings.Builder
	depth := 0
	i := 0
	for i < len(s) {
		if strings.HasPrefix(s[i:], "<think>") {
			depth++
			i += 7
		} else if strings.HasPrefix(s[i:], "</think>") && depth > 0 {
			depth--
			i += 8
		} else if depth == 0 {
			b.WriteByte(s[i])
			i++
		} else {
			i++
		}
	}
	return b.String()
}

var platformShort = map[string]string{
	"telegram": "tg", "whatsapp": "wa", "discord": "dc",
	"email": "em", "web": "web",
}

// SenderToUserFileID converts "platform:id" to short file ID (e.g. "tg-123").
func SenderToUserFileID(sender string) string {
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

// UserContextXml returns a <user> tag for the sender, or "" if no sender.
// Reads user file from groupDir/users/<id>.md for name lookup.
func UserContextXml(sender, groupDir string) string {
	if sender == "" || sender == "system" {
		return ""
	}
	id := SenderToUserFileID(sender)
	attrs := []string{`id="` + EscapeXml(id) + `"`}

	usersDir := filepath.Join(groupDir, "users")
	userFile := filepath.Join(usersDir, id+".md")
	resolved, err := filepath.Abs(userFile)
	if err == nil && strings.HasPrefix(resolved, filepath.Clean(usersDir)+string(os.PathSeparator)) {
		if data, err := os.ReadFile(userFile); err == nil {
			content := string(data)
			if m := nameRe.FindStringSubmatch(content); len(m) > 1 {
				attrs = append(attrs, `name="`+EscapeXml(strings.TrimSpace(m[1]))+`"`)
			}
			attrs = append(attrs, `memory="~/users/`+id+`.md"`)
		}
	}
	return "<user " + strings.Join(attrs, " ") + " />"
}

// IsAuthorizedRoutingTarget returns true if source may delegate to target.
// Root world can delegate to any folder; otherwise same world + descendant.
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

// ExpandTarget performs RFC 6570 Level 1 template expansion on a route target.
// Only {sender} is supported — expands to SenderToUserFileID(msg.Sender).
func ExpandTarget(target string, msg core.Message) string {
	if !strings.Contains(target, "{") {
		return target
	}
	id := SenderToUserFileID(msg.Sender)
	if id == "" || id == "-" || id == "-unknown" {
		return ""
	}
	return strings.ReplaceAll(target, "{sender}", id)
}

func routeMatches(r core.Route, msg core.Message) bool {
	switch r.Type {
	case "command":
		t := strings.TrimSpace(msg.Content)
		return r.Match != "" && (t == r.Match || strings.HasPrefix(t, r.Match+" "))
	case "pattern":
		if r.Match == "" || len(r.Match) > 200 {
			return false
		}
		re, err := regexp.Compile(r.Match)
		if err != nil {
			return false
		}
		return re.MatchString(msg.Content)
	case "keyword":
		return r.Match != "" && strings.Contains(
			strings.ToLower(msg.Content), strings.ToLower(r.Match))
	case "sender":
		if r.Match == "" || len(r.Match) > 200 {
			return false
		}
		name := msg.Name
		if name == "" {
			name = msg.Sender
		}
		re, err := regexp.Compile(r.Match)
		if err != nil {
			return false
		}
		return re.MatchString(name)
	case "trigger", "default":
		return true
	}
	return false
}

// ResolveRoute evaluates flat routes sequentially (first match wins).
// Template targets are expanded per-message. Returns target folder or "".
func ResolveRoute(msg core.Message, routes []core.Route) string {
	for _, r := range routes {
		if !routeMatches(r, msg) {
			continue
		}
		t := ExpandTarget(r.Target, msg)
		if t != "" {
			return t
		}
	}
	return ""
}
