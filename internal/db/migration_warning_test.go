package db

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"
)

// migrationLogCapture collects slog records emitted while migrations run.
type migrationLogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *migrationLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *migrationLogCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r.Clone())
	return nil
}

func (c *migrationLogCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *migrationLogCapture) WithGroup(string) slog.Handler      { return c }

// migrationWarnings returns the label/err text of every "Migration warning"
// record captured. It filters on the message rather than the level: startup
// also logs WARN-level records (the first-boot admin banner), and those are not
// schema problems.
func (c *migrationLogCapture) migrationWarnings() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, r := range c.records {
		if r.Message != "Migration warning" {
			continue
		}
		var sb strings.Builder
		sb.WriteString(r.Message)
		r.Attrs(func(a slog.Attr) bool {
			sb.WriteString(" | ")
			sb.WriteString(a.Key)
			sb.WriteString("=")
			sb.WriteString(a.Value.String())
			return true
		})
		out = append(out, sb.String())
	}
	return out
}

// runMigrationsCaptured opens a throwaway PostgreSQL database, runs the full
// migration chain, and returns every warning it produced.
func runMigrationsCaptured(t *testing.T) []string {
	t.Helper()
	cap := &migrationLogCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(cap))
	t.Cleanup(func() { slog.SetDefault(prev) })

	database, err := InitDBWithDriver("postgres", testPostgresDSN, "", slog.LevelError, 1, 1, 0)
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	if sqlDB, err := database.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	return cap.migrationWarnings()
}

// TestFreshMigrationIsWarningFree is the class-level guard against index
// migrations that can never succeed.
//
// Four such statements had accumulated in the tree: alert_rules(event_type)
// (the column is `type`), chat_messages(session_id) (no such column),
// implants(agent_id COLLATE NOCASE) (the primary key column is `id`), and
// tasks(result(255)) (SQLite has no prefix indexes). Each one failed silently on
// every boot — execMigration logs a WARN and continues — so the migrations
// looked like they ran while creating nothing, and the warnings were buried in
// startup noise nobody reads.
//
// Naming the four is not the point; the point is that ANY future statement
// naming a column or table that does not exist now fails this test.
func TestFreshMigrationIsWarningFree(t *testing.T) {
	warnings := runMigrationsCaptured(t)
	for _, w := range warnings {
		t.Errorf("migration warning: %s", w)
	}
}

// TestNoSuchColumnIndexMigrationsAreGone pins the specific removals so a future
// copy of one of these statements is caught by name as well as by effect.
func TestNoSuchColumnIndexMigrationsAreGone(t *testing.T) {
	dead := []string{
		"idx_alert_rules_event",
		"idx_chat_messages_session_id",
		"idx_implants_agent_id_nocase",
		"idx_tasks_result",
	}
	database := setupTestDB(t)
	for _, mig := range indexMigrations {
		if err := mig.Migrate(database); err != nil {
			t.Fatalf("migration %s: %v", mig.ID, err)
		}
	}
	for _, name := range dead {
		for _, table := range []string{"alert_rules", "chat_messages", "implants", "tasks"} {
			if tableHasIndex(t, database, table, name) {
				t.Errorf("index %s on %s still exists; it was dead (wrong column or unsupported syntax)", name, table)
			}
		}
	}
	// The replacement must actually exist.
	if !tableHasIndex(t, database, "implants", "idx_implants_id_nocase") {
		t.Error("idx_implants_id_nocase missing: beacon dispatch lower-cases the agent id before lookup")
	}
}

func tableHasIndex(t *testing.T, database *gorm.DB, table, want string) bool {
	t.Helper()
	rows, err := database.Raw("PRAGMA index_list('" + table + "')").Rows()
	if err != nil {
		t.Fatalf("index_list(%s): %v", table, err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index row: %v", err)
		}
		if name == want {
			found = true
		}
	}
	return found
}
