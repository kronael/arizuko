// Package expand renders named placeholders.
package expand

import (
	"fmt"
	"strings"
)

func Expand(s string, vars map[string]string) (string, error) {
	var out strings.Builder
	for len(s) > 0 {
		i := strings.IndexAny(s, "{}")
		if i < 0 {
			out.WriteString(s)
			break
		}
		out.WriteString(s[:i])
		if s[i] != '{' {
			return "", fmt.Errorf("unexpected closing brace")
		}
		j := strings.IndexByte(s[i+1:], '}')
		if j < 0 {
			return "", fmt.Errorf("unclosed placeholder")
		}
		name := s[i+1 : i+1+j]
		value, ok := vars[name]
		if !ok || strings.ContainsAny(name, "{}") {
			return "", fmt.Errorf("unknown placeholder %q", name)
		}
		out.WriteString(value)
		s = s[i+j+2:]
	}
	return out.String(), nil
}
