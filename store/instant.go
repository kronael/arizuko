package store

import (
	"fmt"
	"time"

	"github.com/kronael/arizuko/core"
)

// sqliteDatetime is the layout `datetime('now')` writes: UTC, a space where
// RFC3339 has a T, and no zone.
const sqliteDatetime = "2006-01-02 15:04:05"

// instantLayouts is every shape a stored instant is found in, MEASURED on all
// three live instances 2026-09-09: 20-byte RFC3339 in every column; 19-byte
// datetime('now') in acl, acl_membership, groups, network_rules and
// web_routes; 28-30 byte RFC3339Nano/core.TS in groups, route_tokens, acl and
// web_routes. Four widths in one column: 19 sorts before 20 before 28, so
// `granted_at <` ordered by width first (BUGS L97).
var instantLayouts = []string{core.TSLayout, time.RFC3339Nano, time.RFC3339, sqliteDatetime}

// normaliseInstant re-renders one stored instant at core.TS's width. Anything
// none of the layouts reads is a LOUD fault: an instant nobody can read is one
// a comparison answers by byte order.
func normaliseInstant(entity, attr, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	for _, layout := range instantLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return core.TS(t), nil
		}
	}
	return "", fmt.Errorf("%s.%s is %q, which is not an instant this reads; "+
		"a stored instant is compared as TEXT and a value of another shape answers by "+
		"byte order rather than by time", entity, attr, raw)
}
