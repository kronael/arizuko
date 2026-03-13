package router

import (
	"regexp"
	"strings"

	"github.com/onvos/arizuko/core"
)

func EscapeXml(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
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
		b.WriteString(`" time="`)
		b.WriteString(m.Timestamp.Format("2006-01-02T15:04:05Z"))
		b.WriteString(`">`)
		b.WriteString(EscapeXml(m.Content))
		b.WriteString("</message>\n")
	}
	b.WriteString("</messages>")
	return b.String()
}

var (
	nonAlphanumRe   = regexp.MustCompile(`[^A-Za-z0-9]`)
	multiUnderscoreRe = regexp.MustCompile(`_+`)
)

// SpawnFolderName derives a valid group folder segment from a JID.
// e.g. "tg:-100123456" → "tg_100123456"
func SpawnFolderName(jid string) string {
	s := nonAlphanumRe.ReplaceAllString(jid, "_")
	s = multiUnderscoreRe.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}

var internalRe = regexp.MustCompile(`(?s)<internal>.*?</internal>`)

func FormatOutbound(raw string) string {
	return strings.TrimSpace(internalRe.ReplaceAllString(raw, ""))
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
	suffix := target[len(source):]
	return strings.HasPrefix(suffix, "/") && strings.IndexByte(suffix[1:], '/') == -1
}

// ResolveRoute evaluates flat routes sequentially (first match wins).
// Returns target folder or "".
func ResolveRoute(msg core.Message, routes []core.Route) string {
	for _, r := range routes {
		switch r.Type {
		case "command":
			t := strings.TrimSpace(msg.Content)
			if r.Match != "" && (t == r.Match || strings.HasPrefix(t, r.Match+" ")) {
				return r.Target
			}
		case "pattern":
			if r.Match == "" || len(r.Match) > 200 {
				continue
			}
			re, err := regexp.Compile(r.Match)
			if err != nil {
				continue
			}
			if re.MatchString(msg.Content) {
				return r.Target
			}
		case "keyword":
			if r.Match != "" && strings.Contains(strings.ToLower(msg.Content), strings.ToLower(r.Match)) {
				return r.Target
			}
		case "sender":
			if r.Match == "" || len(r.Match) > 200 {
				continue
			}
			name := msg.Name
			if name == "" {
				name = msg.Sender
			}
			re, err := regexp.Compile(r.Match)
			if err != nil {
				continue
			}
			if re.MatchString(name) {
				return r.Target
			}
		case "verb":
			// verb matching reserved for social channel messages
			continue
		case "trigger", "default":
			return r.Target
		}
	}
	return ""
}
