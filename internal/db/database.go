package db

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	// PostgreSQL driver (primary path — single driver for all deployments)
	"gorm.io/driver/postgres"
)

// lockAwareLogger wraps a GORM logger so PostgreSQL connection errors are
// surfaced through slog even when GORM runs in Silent mode, where statement
// errors would otherwise be swallowed.
type lockAwareLogger struct {
	logger.Interface
}

// Trace intercepts statement execution errors before the wrapped logger
// applies its (possibly Silent) log level filter.
func (l lockAwareLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if err != nil {
		sql, rows := fc()
		slog.Error("PostgreSQL statement error", "error", err, "sql", sql, "rows", rows)
	}
	l.Interface.Trace(ctx, begin, fc, err)
}

// Error surfaces errors reported through the logger's Error path.
func (l lockAwareLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	for _, d := range data {
		if e, ok := d.(error); ok {
			slog.Error(msg, "error", e)
			return
		}
	}
	l.Interface.Error(ctx, msg, data...)
}

// execMigration runs a migration SQL statement. Duplicate column/index errors are
// expected during migrations and are ignored; every other failure is RECORDED so the
// runner can refuse to mark the migration applied (see migration_failures.go).
func execMigration(db *gorm.DB, sql, label string) {
	err := db.Exec(sql).Error
	if err == nil || isMigrationIgnorable(err) {
		return
	}
	// Schema migrations run BEFORE AutoMigrate, so an ALTER for a table that this
	// installation does not have yet is expected on a legacy upgrade: AutoMigrate creates
	// the table from the model, columns included. Skip those, but keep recording a
	// "no such table" for a table that DOES exist, which is the mis-ordered DDL the
	// classification was written for.
	if strings.Contains(err.Error(), "does not exist") {
		if table := migrationTargetTable(sql); table != "" && !db.Migrator().HasTable(table) {
			slog.Debug("Migration targets a table AutoMigrate will create", "label", label, "table", table)
			return
		}
	}
	noteMigrationFailure(label, err)
}

// migrationTargetTable extracts the table a migration statement refers to, so the
// "not created yet" case can be told apart from a genuine ordering bug.
func migrationTargetTable(sql string) string {
	up := strings.ToUpper(strings.TrimSpace(sql))
	for _, marker := range []string{"ALTER TABLE ", " ON ", " INTO ", " FROM "} {
		if i := strings.Index(up, marker); i >= 0 {
			rest := strings.TrimSpace(sql[i+len(marker):])
			if f := strings.Fields(rest); len(f) > 0 {
				name := strings.Trim(f[0], "`\"[];,")
				// "ON tasks(status)" -> "tasks": an index or column list must not leak into
				// the table name.
				if j := strings.IndexAny(name, "(;,"); j > 0 {
					name = name[:j]
				}
				if name != "" && !strings.HasPrefix(strings.ToUpper(name), "SELECT") {
					return name
				}
			}
		}
	}
	return ""
}

func isMigrationIgnorable(err error) bool {
	s := err.Error()
	// "does not exist" is deliberately NOT ignorable: swallowing it masked
	// genuinely mis-ordered DDL and let broken migrations "pass".
	for _, kw := range []string{"already exists", "duplicate column", "duplicate key"} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// withMigrationTx wraps a data-migration body in one transaction. gormigrate
// runs with UseTransaction:false, so a mid-loop failure previously persisted
// half the rows without recording the migration ID; the next startup replayed
// the step, hit duplicate-key errors on the already-inserted rows, and the
// server could never boot.
func withMigrationTx(tx *gorm.DB, fn func(*gorm.DB) error) error {
	t := tx.Begin()
	if t.Error != nil {
		return t.Error
	}
	if err := fn(t); err != nil {
		_ = t.Rollback()
		return err
	}
	return t.Commit().Error
}

// migrationRowExists gives data migrations rerun idempotency: rows imported
// by an earlier partially-committed attempt are skipped instead of crashing
// on primary-key conflicts. Uses GORM-escaped identifier to stay portable
// across MySQL backticks and Postgres double-quotes.
func migrationRowExists(tx *gorm.DB, table, column, value string) bool {
	var count int64
	tx.Table(table).Where(tx.Statement.Quote(column)+" = ?", value).Count(&count)
	return count > 0
}

// generateRandomPassword creates a random alphanumeric password of the given length.
func generateRandomPassword(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	charsetLen := len(charset)
	threshold := 256 - (256 % charsetLen)
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand.Read failed: %w", err)
	}
	for i, v := range b {
		for int(v) >= threshold {
			if _, err := rand.Read(b[i : i+1]); err != nil {
				return "", fmt.Errorf("crypto/rand.Read failed: %w", err)
			}
			v = b[i]
		}
		b[i] = charset[int(v)%charsetLen]
	}
	return string(b), nil
}

// LogGeneratedPassword controls whether the first-boot admin banner prints the
// generated password in plaintext. It is process-wide because it is read from
// config before the DB is opened, and only matters during that one first seed.
var LogGeneratedPassword = true

// SetLogGeneratedPassword overrides the first-boot banner behaviour. Call it
// before InitDB/InitDBWithDriver. Defaults to true.
func SetLogGeneratedPassword(v bool) { LogGeneratedPassword = v }

// InitDB initializes the database using PostgreSQL via gorm.io/driver/postgres.
func InitDB(dbPath string, logLevel slog.Level, defaultPassword ...string) (*gorm.DB, error) {
	return InitDBWithDriver("", "", dbPath, logLevel, 25, 5, 30*time.Minute, defaultPassword...)
}

// InitDBWithDriver initializes the database. Only "postgres" driver is
// supported; driver="postgres" with a non-empty DSN opens PostgreSQL, any
// other value falls back to PostgreSQL using the DSN from the fallback path
// interpretation. SQLite is no longer supported — the project ships a single
// database driver (PostgreSQL).
func InitDBWithDriver(driver, dsn, fallbackPath string, logLevel slog.Level, dbMaxOpenConns int, dbMaxIdleConns int, dbConnMaxLifetime time.Duration, defaultPassword ...string) (*gorm.DB, error) {
	gormConfig := &gorm.Config{
		Logger: lockAwareLogger{Interface: logger.Default.LogMode(logger.Silent)},
	}
	if logLevel == slog.LevelDebug {
		gormConfig.Logger = lockAwareLogger{Interface: logger.Default.LogMode(logger.Info)}
	}

	if driver != "" && driver != "postgres" {
		return nil, fmt.Errorf("unsupported database driver %q: only \"postgres\" is supported (sqlite was removed in v2.8)", driver)
	}

	var db *gorm.DB
	var err error

	if dsn != "" {
		db, err = gorm.Open(postgres.Open(dsn), gormConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to PostgreSQL: %w", err)
		}
		slog.Info("Database initialized (PostgreSQL)", "dsn", redactDSN(dsn))
	} else {
		// No DSN: construct a default local PostgreSQL DSN. The operator
		// must have PostgreSQL running (see docker-compose.yml --profile postgres).
		defaultDSN := "host=localhost user=forgec2 password=forgec2 dbname=forgec2 port=5432 sslmode=disable"
		db, err = gorm.Open(postgres.Open(defaultDSN), gormConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to PostgreSQL (default DSN %s): %w", defaultDSN, err)
		}
		slog.Info("Database initialized (PostgreSQL)", "dsn", "host=localhost user=forgec2 dbname=forgec2 port=5432 sslmode=disable")
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get underlying DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(dbMaxOpenConns)
	sqlDB.SetMaxIdleConns(dbMaxIdleConns)
	sqlDB.SetConnMaxLifetime(dbConnMaxLifetime)

	// Apply gormigrate schema migrations FIRST. They must run before AutoMigrate
	// so table renames (e.g. agents -> implants) operate on the pre-existing
	// schema; running AutoMigrate first would create the new table and silently
	// strand the old table's data.
	if err := runSchemaMigrations(db); err != nil {
		return nil, fmt.Errorf("schema migration failed: %w", err)
	}

	// Auto-migrate all models (creates any tables/columns still missing on
	// fresh installs and after historical migrations)
	if err := db.AutoMigrate(&Implant{}, &Task{}, &AuditLog{}, &Listener{}, &TokenEntry{}, &SocksSession{}, &CredentialEntry{}, &User{}, &BuildLog{}, &ScanResult{}, &NetworkHost{}, &CommandTemplate{}, &BOFFile{}, &ServerConfig{}, &WebhookConfig{}, &Plugin{}, &PluginReview{}, &PluginDependency{}, &PluginUpdateStatus{}, &AutomationRule{}, &AlertRule{}, &Alert{}, &SystemMetric{}, &GeneratedReport{}, &Campaign{}, &MeshPeer{}, &BloodHoundResult{}, &OpsecHistory{}, &OpsecRule{}, &CircuitBreakerConfig{}, &CircuitBreakerEvent{}, &CustomRole{}, &PhishingTemplate{}, &PhishingCampaign{}, &PhishingEvent{}, &AgentTag{}, &AgentTagAssignment{}, &AutoTagRule{}, &Notification{}, &AgentGroup{}, &AgentGroupAssignment{}, &Workflow{}, &WorkflowStep{}, &ExecutionLog{}, &ChatMessage{}, &StagerToken{}, &Redirector{}, &AgentLock{}, &CloudCred{}, &AIChatSession{}, &AIChatMessage{}, &AIProviderProfile{}, &AIChatRun{}, &AIChatRunEvent{}, &AIExecutionIntent{}, &AIAttachment{}, &AIKnowledgeCollection{}, &AIKnowledgeSource{}, &AIKnowledgeChunk{}, &ExtC2Channel{}, &AgentStatusEvent{}, &SIEMRule{}, &UserSession{}, &PasswordHistory{}, &APIKey{}, &Script{}, &RegSecret{}, &KillSwitch{}, &Tenant{}, &CredentialUsage{}, &CommandMacro{}, &MacroRun{}, &NotificationRoute{}, &SavedView{}, &OneShotTask{}, &Engagement{}, &PlaybookRun{}); err != nil {
		return nil, err
	}

	// Apply index migrations LAST, once every table/column they target is
	// guaranteed to exist on all upgrade paths.
	if err := runIndexMigrations(db); err != nil {
		return nil, fmt.Errorf("index migration failed: %w", err)
	}

	// Multi-tenant bootstrap: ensure a default tenant exists and backfill any
	// legacy (tenant_id = 0) rows so every asset is owned by a tenant. New
	// tenants are created on operator registration.
	if err := ensureDefaultTenant(db); err != nil {
		return nil, fmt.Errorf("default tenant bootstrap failed: %w", err)
	}

	// Backfill credential dedup keys for rows predating the column. Idempotent
	// by data state: it stops as soon as no empty key remains, so a fresh
	// DB costs one bounded probe.
	backfillCredentialDedupKeys(db)

	// Seed default admin user if none exist
	var userCount int64
	if err := db.Model(&User{}).Count(&userCount).Error; err != nil {
		return nil, fmt.Errorf("failed to check for existing users: %w", err)
	}
	if userCount == 0 {
		adminPass := ""
		if len(defaultPassword) > 0 && defaultPassword[0] != "" {
			adminPass = defaultPassword[0]
		} else {
			var genErr error
			adminPass, genErr = generateRandomPassword(12)
			if genErr != nil {
				slog.Error("Failed to generate random admin password", "err", genErr)
				return nil, genErr
			}
		}
		defaultAdminHash, err := bcrypt.GenerateFromPassword([]byte(adminPass), bcrypt.DefaultCost)
		if err != nil {
			slog.Error("Failed to hash default admin password", "err", err)
		} else {
			db.Create(&User{
				Username:            "admin",
				PasswordHash:        string(defaultAdminHash),
				Role:                "admin",
				IsActive:            true,
				ForcePasswordChange: true,
			})
			shown := adminPass
			if !LogGeneratedPassword {
				shown = "(redacted — set auth.log_generated_password: true to print it)"
			}
			slog.Warn("╔═════════════════════════════════════════════════════════╗")
			slog.Warn("║  DEFAULT ADMIN CREDENTIALS (CHANGE IMMEDIATELY!)       ║")
			slog.Warn(fmt.Sprintf("║  Username: admin  Password: %-24s  ║", shown))
			slog.Warn("║  This password is in your log file in plaintext.         ║")
			slog.Warn("║  Delete/rotate logs, or set                             ║")
			slog.Warn("║  auth.log_generated_password: false to redact.          ║")
			slog.Warn("╚══════════════════════════════════════════════════════════╝")
		}
	}

	return db, nil
}

// DefaultTenantName is the name of the bootstrap tenant all legacy assets are
// assigned to.
const DefaultTenantName = "default"

// ensureDefaultTenant creates the singleton "default" tenant and backfills any
// legacy (tenant_id = 0) rows so a fresh or upgraded deployment is fully
// tenant-scoped. Returns the default tenant ID.
func ensureDefaultTenant(db *gorm.DB) error {
	var t Tenant
	if err := db.Where("name = ?", "default").FirstOrCreate(&t, Tenant{Name: "default"}).Error; err != nil {
		return err
	}
	backfill := func(model interface{}, idCol string) {
		if err := db.Model(model).Where("tenant_id = ? OR tenant_id IS NULL", 0).Update("tenant_id", t.ID).Error; err != nil {
			slog.Warn("Tenant backfill partial", "model", idCol, "err", err)
		}
	}
	backfill(&User{}, "users")
	backfill(&Implant{}, "implants")
	backfill(&Task{}, "tasks")
	// Credential ownership follows its source implant when possible. Manual and
	// orphaned legacy rows fall back to the default tenant below.
	if err := db.Exec(`UPDATE credential_entries
		SET tenant_id = COALESCE((SELECT tenant_id FROM implants WHERE implants.id = credential_entries.agent_id), 0)
		WHERE tenant_id = 0 OR tenant_id IS NULL`).Error; err != nil {
		slog.Warn("Credential tenant backfill partial", "err", err)
	}
	backfill(&CredentialEntry{}, "credential_entries")
	slog.Info("Default tenant ensured", "tenant_id", t.ID)
	return nil
}

// backfillCredentialDedupKeys fills DedupKey on credential rows predating the
// column. Keys digest the plaintext identity, so pages select the encrypted
// columns and let AfterFind decrypt them; the Update keeps a dedup_key = ”
// guard so an interrupted backfill is safe to re-run.
func backfillCredentialDedupKeys(db *gorm.DB) {
	const pageSize = 500
	for {
		var rows []CredentialEntry
		if err := db.Select("id, agent_id, domain, username, password, hash, hash_type, source").
			Where("dedup_key = ''").
			Order("id ASC").Limit(pageSize).Find(&rows).Error; err != nil {
			slog.Warn("Credential dedup-key backfill failed", "err", err)
			return
		}
		if len(rows) == 0 {
			return
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			for i := range rows {
				key := ComputeCredDedupKey(rows[i].AgentID, rows[i].Domain, rows[i].Username, rows[i].Password, rows[i].Hash)
				if err := tx.Model(&CredentialEntry{}).
					Where("id = ? AND dedup_key = ''", rows[i].ID).
					Update("dedup_key", key).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			slog.Warn("Credential dedup-key backfill aborted, will retry on next start", "err", err)
			return
		}
	}
}

// MigrateOldRoles migrates "operator"/"viewer"/"guest" to "user"
func MigrateOldRoles(db *gorm.DB) {
	result := db.Model(&User{}).Where("role IN ?", []string{"operator", "viewer", "guest"}).Update("role", RoleUser)
	if result.RowsAffected > 0 {
		slog.Info("Migrated old roles to 'user'", "count", result.RowsAffected)
	}
}

// redactString masks a secret entirely; no plaintext fragments are ever
// allowed into logs or startup output. Used for secrets the operator has
// already seen elsewhere — the first-boot admin banner deliberately does NOT
// go through this, because it is the only place that credential is shown.
func redactString(s string) string {
	if s == "" {
		return ""
	}
	return strings.Repeat("*", len(s))
}

func redactDSN(dsn string) string {
	parts := strings.Split(dsn, " ")
	out := make([]string, len(parts))
	for i, p := range parts {
		lower := strings.ToLower(p)
		if strings.HasPrefix(lower, "password=") || strings.HasPrefix(lower, "pwd=") {
			out[i] = p[:strings.Index(p, "=")+1] + "****"
		} else {
			out[i] = p
		}
	}
	return strings.Join(out, " ")
}
