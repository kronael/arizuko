package core

import (
	"fmt"

	"github.com/kronael/arizuko/expand"
)

// ValidateRouteTarget checks the URI and its two template vocabularies.
func ValidateRouteTarget(target string) error {
	rt := ParseRouteTarget(target)
	if rt.Folder == "" {
		return fmt.Errorf("invalid route target %q", target)
	}
	switch rt.Mode {
	case "", "observe", "engaged", "announce", "issues":
	default:
		return fmt.Errorf("invalid route mode %q", rt.Mode)
	}
	if _, err := expand.Expand(rt.Folder, map[string]string{"sender": ""}); err != nil {
		return err
	}
	_, err := expand.Expand(rt.Topic, map[string]string{"jid": "", "thread": ""})
	return err
}
