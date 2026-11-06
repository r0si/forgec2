package server

import "gorm.io/gorm"

// Dialect-portable SQL date expressions.
//
// Several aggregate dashboard/stats endpoints group beacon/task history by
// day/hour. The v2.8 SQLite→PostgreSQL migration converted the schema and the
// maintenance paths but left the raw date functions in four read queries as
// SQLite-only SQL (strftime, julianday). On the only production driver (the
// PostgreSQL deployment) those functions do not exist, so the activity heatmap,
// the MITRE coverage heatmap and the queue-stall reports answered 500 at run
// time while their SQLite tests stayed green. These helpers return the correct
// expression per driver so the queries execute on both, keeping the SQLite form
// (and its exact UTC semantics) for the test harness.

// isPostgresDB reports whether the live connection is the PostgreSQL driver
// (the production default). Pair of isSQLiteDB, defined in backup.go.
func isPostgresDB(db *gorm.DB) bool {
	if db == nil || db.Dialector == nil {
		return false
	}
	return db.Dialector.Name() == "postgres"
}

// dayAgoHourBucketSelect builds the activity-heatmap projection: an integer
// "days ago" bucket (0 = today, counting up) and the UTC hour of the row, using
// whichever date functions the active driver provides. created_at is stored as
// UTC on both drivers, so hours are compared against a UTC wall clock.
//
// SQLite: julianday difference truncated to whole days; strftime('%H') for the
// hour. PostgreSQL: epoch-seconds / 86400 floored for the day; EXTRACT(HOUR)
// over the naive UTC timestamp for the hour (now() AT TIME ZONE 'UTC' pins the
// current instant to the same naive-UTC basis created_at is stored on).
func dayAgoHourBucketSelect(db *gorm.DB, col string) string {
	if isPostgresDB(db) {
		return "FLOOR(EXTRACT(EPOCH FROM ((now() AT TIME ZONE 'UTC') - " + col + ")) / 86400)::int AS day, " +
			"EXTRACT(HOUR FROM " + col + ")::int AS hour, COUNT(*) AS count"
	}
	return "CAST((julianday('now') - julianday(" + col + ")) AS INTEGER) as day, " +
		"strftime('%H', " + col + ") as hour, COUNT(*) as count"
}

// dayStringSelectExpr renders a 'YYYY-MM-DD' string bucket for a timestamp
// column, portable across the two drivers.
func dayStringSelectExpr(db *gorm.DB, col string) string {
	if isPostgresDB(db) {
		return "TO_CHAR(" + col + ", 'YYYY-MM-DD') as day"
	}
	return "strftime('%Y-%m-%d', " + col + ") as day"
}

// secondsSinceExpr renders "ref - col" as an integer count of seconds, using a
// caller-supplied reference expression (refSQL — normally the single "?"
// placeholder bound to a formatted UTC timestamp, so the age is measured
// against the same clock the Go layer already captured rather than the DB
// server's clock; the stored times carry a local offset while the reference is
// UTC, so passing the instant explicitly avoids a silent timezone skew). SQLite
// uses strftime('%s'); PostgreSQL uses EXTRACT(EPOCH FROM (ref - col)) with the
// reference cast to a timestamp on created_at's naive-UTC basis.
func secondsSinceExpr(db *gorm.DB, refSQL, col string) string {
	if isPostgresDB(db) {
		return "FLOOR(EXTRACT(EPOCH FROM ((" + refSQL + ")::timestamp - " + col + ")))::bigint"
	}
	return "CAST(strftime('%s', " + refSQL + ") - strftime('%s', " + col + ") AS INTEGER)"
}
