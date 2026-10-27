package db

import (
	"log/slog"

	"gorm.io/gorm"
)

// execPragma applies a connection pragma and reports a failure accurately.
//
// These are NOT migrations: nothing records them as applied, so routing them through
// execMigration made a failed pragma claim "will NOT be recorded as applied" - true but
// meaningless - and pushed a pragma label into the migration-failure counter where a later
// reader would mistake it for broken DDL. A pragma that fails still matters (foreign_keys off
// changes data integrity, journal_mode off changes crash safety), so it is logged at error
// level and named.
func execPragma(db *gorm.DB, sql, label string) {
	if err := db.Exec(sql).Error; err != nil {
		slog.Error("Database pragma could not be applied; the database is running with a "+
			"less safe or slower setting", "label", label, "sql", sql, "err", err)
	}
}
