package core

import (
	"os"
	"strings"
	"testing"
)

func TestRouteModesAreDocumented(t *testing.T) {
	doc, err := os.ReadFile("../ROUTING.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"observe", "engaged", "announce", "issues"} {
		if !strings.Contains(string(doc), "`?"+mode+"`") {
			t.Errorf("route mode %q is absent from ROUTING.md", mode)
		}
	}
}
