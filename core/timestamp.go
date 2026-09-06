package core

import "time"

// TSLayout is the layout for every time stored in, or compared against, a TEXT
// timestamp column. It is fixed width in UTC — nine fraction digits, always —
// so lexicographic order over the column IS chronological order. Readers keep
// parsing with time.RFC3339Nano, which reads a fixed-width nine-digit fraction.
//
// time.RFC3339Nano cannot hold that guarantee as a WRITE layout: it strips
// trailing zeros, so a whole second renders `2026-08-26T19:06:41Z` and a
// fraction of the same second renders `2026-08-26T19:06:41.882458214Z`. `.` is
// 0x2E and `Z` is 0x5A, so the later instant sorts FIRST. Columns read as
// cursors (messages.timestamp against chats.agent_cursor, and
// sessions.observed_cursor) then skip such a row permanently, because a cursor
// only moves forward. Every chat `/approve` wrote its re-issue trigger into that
// blind spot, so no held call was ever re-issued (BUGS L97).
const TSLayout = "2006-01-02T15:04:05.000000000Z07:00"

// TS renders t for a TEXT timestamp column. It forces UTC: an offset both
// changes the width and puts zone digits where the fraction belongs, which
// costs the ordering the layout buys.
func TS(t time.Time) string { return t.UTC().Format(TSLayout) }

// NowTS is TS(time.Now()).
func NowTS() string { return TS(time.Now()) }
