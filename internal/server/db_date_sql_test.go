package server

import (
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/testutil"
	"gorm.io/gorm"
)

// The v2.8 SQLite→PostgreSQL migration left four read queries using SQLite-only
// date functions (strftime/julianday) that crash the activity heatmap, the MITRE
// coverage map and the queue-stall reports at run time on the only production
// driver, while their SQLite tests stayed green. These tests pin the
// dialect-aware helpers: SQLite output must be unchanged (back-compat with the
// tests and any legacy SQLite deployment) and PostgreSQL output must use only
// PostgreSQL functions, never strftime/julianday.

func fakePostgresGorm(t *testing.T) *gorm.DB {
	t.Helper()
	return newFakePostgresDB(t) // reports Dialector.Name()=="postgres", no live server
}

func sqliteGorm(t *testing.T) *gorm.DB {
	t.Helper()
	return testutil.SetupTestDB(t)
}

func TestIsPostgresDB(t *testing.T) {
	if isPostgresDB(nil) {
		t.Fatal("nil must not be postgres")
	}
	if isPostgresDB(sqliteGorm(t)) {
		t.Fatal("sqlite handle reported as postgres")
	}
	if !isPostgresDB(fakePostgresGorm(t)) {
		t.Fatal("postgres handle not recognized")
	}
}

// assertNoSQLiteFuncs fails if a PostgreSQL-branch expression contains a
// SQLite-only date function; that is the exact defect this change fixes.
func assertNoSQLiteFuncs(t *testing.T, label, sql string) {
	t.Helper()
	for _, fn := range []string{"julianday", "strftime", "datetime("} {
		if strings.Contains(strings.ToLower(sql), fn) {
			t.Errorf("%s (postgres branch) still contains SQLite function %q: %s", label, fn, sql)
		}
	}
}

func TestDayAgoHourBucketSelectDialect(t *testing.T) {
	pg := dayAgoHourBucketSelect(fakePostgresGorm(t), "created_at")
	assertNoSQLiteFuncs(t, "dayAgoHourBucketSelect", pg)
	if !strings.Contains(pg, "EXTRACT(EPOCH") || !strings.Contains(pg, "EXTRACT(HOUR") {
		t.Errorf("postgres day/hour select missing EXTRACT forms: %s", pg)
	}
	if !strings.Contains(pg, "AT TIME ZONE 'UTC'") {
		t.Errorf("postgres day bucket must pin now() to UTC (created_at is naive UTC): %s", pg)
	}

	// SQLite branch must be byte-identical to the original expression, so the
	// SQLite-backed tests keep exercising the same SQL they always did.
	sl := dayAgoHourBucketSelect(sqliteGorm(t), "created_at")
	if !strings.Contains(sl, "julianday") || !strings.Contains(sl, "strftime('%H'") {
		t.Errorf("sqlite day/hour select drifted from the historical expression: %s", sl)
	}
}

func TestDayStringSelectExprDialect(t *testing.T) {
	pg := dayStringSelectExpr(fakePostgresGorm(t), "created_at")
	assertNoSQLiteFuncs(t, "dayStringSelectExpr", pg)
	if !strings.Contains(pg, "TO_CHAR(created_at, 'YYYY-MM-DD')") {
		t.Errorf("postgres day string missing TO_CHAR form: %s", pg)
	}
	sl := dayStringSelectExpr(sqliteGorm(t), "created_at")
	if !strings.Contains(sl, "strftime('%Y-%m-%d', created_at)") {
		t.Errorf("sqlite day string drifted: %s", sl)
	}
}

func TestSecondsSinceExprDialect(t *testing.T) {
	pg := secondsSinceExpr(fakePostgresGorm(t), "?", "created_at")
	assertNoSQLiteFuncs(t, "secondsSinceExpr", pg)
	if !strings.Contains(pg, "EXTRACT(EPOCH") {
		t.Errorf("postgres seconds-since missing EXTRACT(EPOCH: %s", pg)
	}
	// The reference placeholder must survive, and the param cast to timestamp
	// so it is measured against created_at's naive-UTC basis.
	if !strings.Contains(pg, "(?)::timestamp") {
		t.Errorf("postgres seconds-since must cast the reference to timestamp: %s", pg)
	}
	if !strings.Contains(pg, "created_at") {
		t.Errorf("postgres seconds-since must reference the column: %s", pg)
	}

	sl := secondsSinceExpr(sqliteGorm(t), "?", "created_at")
	if !strings.Contains(sl, "strftime('%s', ?)") || !strings.Contains(sl, "strftime('%s', created_at)") {
		t.Errorf("sqlite seconds-since drifted from the historical expression: %s", sl)
	}
}
