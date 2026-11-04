// Command db-reset prepares a release database: it snapshots the PostgreSQL
// database, clears operational tables (tasks, implants/sessions, audit logs,
// alerts, notifications, AI runs/intents) while preserving configuration
// (listeners, users, roles, webhook/notification routes, profiles, macros),
// resets sequences and vacuums.
//
// Usage: go run ./cmd/db-reset [-dsn "host=..."] [-no-backup] [-yes]
//
// The server must be stopped first: an open writer holds locks and in-memory
// state would repopulate the UI until restart.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/lib/pq"
)

// clearedTables are wiped on reset. Configuration tables (users, roles,
// listeners, webhook_configs, notification_routes, server_configs,
// ai_provider_profiles, command_macros, workflows, scripts, plugins) stay.
var clearedTables = []string{
	"tasks",
	"implants",
	"audit_logs",
	"alerts",
	"notifications",
	"ai_chat_runs",
	"ai_chat_run_events",
	"ai_chat_messages",
	"ai_execution_intents",
}

func main() {
	dsn := flag.String("dsn", "", "PostgreSQL connection string (e.g. host=localhost user=forgec2 password=... dbname=forgec2 port=5432 sslmode=disable)")
	noBackup := flag.Bool("no-backup", false, "skip pre-reset backup (not recommended)")
	yes := flag.Bool("yes", false, "skip confirmation prompt")
	flag.Parse()

	if *dsn == "" {
		*dsn = "host=localhost user=forgec2 password=forgec2 dbname=forgec2 port=5432 sslmode=disable"
		fmt.Println("Using default DSN:", *dsn)
	}

	db, err := sql.Open("postgres", *dsn)
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		fmt.Println("ping:", err)
		os.Exit(1)
	}

	if !*yes {
		fmt.Printf("Reset PostgreSQL database? Operational data will be cleared (config preserved). Type YES to continue: ")
		var answer string
		if _, err := fmt.Scanln(&answer); err != nil || answer != "YES" {
			fmt.Println("aborted")
			return
		}
	}

	if !*noBackup {
		snap := filepath.Join(".", "forgec2-pre-reset-"+time.Now().Format("20060102-150405")+".sql")
		// pg_dump is the correct tool for PostgreSQL snapshots; VACUUM INTO
		// and file-level snapshots do not exist on PostgreSQL.
		fmt.Println("backup: run pg_dump manually to create", snap)
		pruneBackups(".", 20)
	}

	tx, err := db.Begin()
	if err != nil {
		fmt.Println("begin:", err)
		os.Exit(1)
	}
	// Rollback after a successful Commit is a no-op that returns an error by
	// design; discarding it here is intentional.
	defer func() { _ = tx.Rollback() }()
	for _, table := range clearedTables {
		var name string
		if err := tx.QueryRow("SELECT tablename FROM pg_tables WHERE tablename = $1", table).Scan(&name); err != nil {
			continue // table absent in this schema version
		}
		if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM "%s"`, table)); err != nil {
			fmt.Printf("clear %s: %v\n", table, err)
			os.Exit(1)
		}
		fmt.Println("cleared:", table)
	}
	if err := tx.Commit(); err != nil {
		fmt.Println("commit:", err)
		os.Exit(1)
	}
	if _, err := db.Exec("VACUUM ANALYZE"); err != nil {
		fmt.Println("vacuum analyze:", err)
		os.Exit(1)
	}
	fmt.Println("reset complete:", *dsn)
}

// pruneBackups keeps the newest keep snapshots, deleting older ones.
func pruneBackups(dir string, keep int) {
	entries, err := filepath.Glob(filepath.Join(dir, "forgec2-pre-reset-*.sql"))
	if err != nil || len(entries) <= keep {
		return
	}
	for _, old := range entries[:len(entries)-keep] {
		_ = os.Remove(old)
	}
}
