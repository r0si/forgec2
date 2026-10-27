package db

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// migrationFailures records DDL statements that failed for a reason that is NOT expected
// (a duplicate column or an already-existing index is expected; "no such table" is not).
//
// The point is the retry, not the log: gormigrate records a migration as applied as soon as
// its Migrate func returns nil, and execMigration used to swallow the error - so a failed
// CREATE INDEX was marked done and never retried, leaving the index permanently absent with
// nothing but a warning line at startup. These helpers let the runners notice and fail the
// run instead, which keeps the migration unapplied so the next boot tries again.
var (
	migrationFailMu     sync.Mutex
	migrationFailLabels []string
)

func resetMigrationFailures() {
	migrationFailMu.Lock()
	migrationFailLabels = nil
	migrationFailMu.Unlock()
}

func noteMigrationFailure(label string, err error) {
	slog.Error("Migration statement failed and will NOT be recorded as applied",
		"label", label, "err", err)
	migrationFailMu.Lock()
	migrationFailLabels = append(migrationFailLabels, label)
	migrationFailMu.Unlock()
}

// migrationFailureError returns an error naming the statements that failed during the last
// run, or nil when everything applied.
func migrationFailureError(scope string) error {
	migrationFailMu.Lock()
	defer migrationFailMu.Unlock()
	if len(migrationFailLabels) == 0 {
		return nil
	}
	labels := append([]string(nil), migrationFailLabels...)
	migrationFailLabels = nil
	return fmt.Errorf("%s: %d migration statement(s) failed and were not recorded: %s",
		scope, len(labels), strings.Join(labels, ", "))
}
