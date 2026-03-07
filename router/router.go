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

var internalRe = regexp.MustCompile(`(?s)<internal>.*?</internal>`)

func FormatOutbound(raw string) string {
	return strings.TrimSpace(internalRe.ReplaceAllString(raw, ""))
}

func IsAuthorizedRoutingTarget(source, target string) bool {
	srcRoot := strings.SplitN(source, "/", 2)[0]
	tgtRoot := strings.SplitN(target, "/", 2)[0]
	if srcRoot != tgtRoot {
		return false
	}
	suffix := target[len(source):]
	return strings.HasPrefix(suffix, "/") && strings.IndexByte(suffix[1:], '/') == -1
}

func ResolveRoutingTarget(msg core.Message, rules []core.RoutingRule) string {
	tiers := []string{"command", "pattern", "keyword", "sender", "default"}
	for _, tier := range tiers {
		for _, rule := range rules {
			if rule.Kind != tier {
				continue
			}
			switch rule.Kind {
			case "command":
				t := strings.TrimSpace(msg.Content)
				if t == rule.Match || strings.HasPrefix(t, rule.Match+" ") {
					return rule.Target
				}
			case "pattern":
				if len(rule.Match) > 200 {
					continue
				}
				re, err := regexp.Compile(rule.Match)
				if err != nil {
					continue
				}
				if re.MatchString(msg.Content) {
					return rule.Target
				}
			case "keyword":
				if strings.Contains(
					strings.ToLower(msg.Content),
					strings.ToLower(rule.Match),
				) {
					return rule.Target
				}
			case "sender":
				if len(rule.Match) > 200 {
					continue
				}
				name := msg.Name
				if name == "" {
					name = msg.Sender
				}
				re, err := regexp.Compile(rule.Match)
				if err != nil {
					continue
				}
				if re.MatchString(name) {
					return rule.Target
				}
			case "default":
				return rule.Target
			}
		}
	}
	return ""
}
