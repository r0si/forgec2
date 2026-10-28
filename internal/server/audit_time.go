package server

import "time"

// comparableLocalTime puts an instant into the zone the timestamps are STORED in.
//
// SQLite keeps these columns as text carrying the writer's offset
// ("2026-10-28T13:31:55+08:00"), and a bound time is rendered the same way, so a comparison only
// works when both sides carry the same offset. Binding an absolute UTC value (from an RFC3339 query
// parameter, say) compared "13:31+08:00" against "04:01+00:00" and returned every row: the filter
// looked applied and did nothing. Duration-based filters are unaffected because they are derived from
// time.Now(), which is already local.
func comparableLocalTime(t time.Time) time.Time {
	return t.In(time.Local)
}
