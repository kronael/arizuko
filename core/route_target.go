package core

import (
	"fmt"
	"net/url"

	"github.com/kronael/arizuko/expand"
)

// ValidateRouteTarget checks the URI and its two template vocabularies.
func ValidateRouteTarget(target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	if u.IsAbs() || u.Host != "" || u.Path == "" || u.ForceQuery {
		return fmt.Errorf("invalid route target %q", target)
	}
	switch u.RawQuery {
	case "", "observe", "engaged", "announce", "issues":
	default:
		return fmt.Errorf("invalid route mode %q", u.RawQuery)
	}
	if _, err := expand.Expand(u.Path, map[string]string{"sender": ""}); err != nil {
		return err
	}
	_, err = expand.Expand(u.Fragment, map[string]string{"jid": "", "thread": ""})
	return err
}
