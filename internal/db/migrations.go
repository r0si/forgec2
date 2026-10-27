package db

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// initialSchemaIndexes are the CREATE INDEX statements that were historically
// part of the initial-schema migration. They are applied by an index-phase
// migration that runs AFTER AutoMigrate, so databases arriving via the legacy
// agents->implants rename path (whose target columns only exist after
// AutoMigrate) still get their indexes created.
var initialSchemaIndexes = []struct{ sql, label string }{
	{"CREATE INDEX IF NOT EXISTS idx_implants_last_seen ON implants(last_seen)", "idx_implants_last_seen"},
	{"CREATE INDEX IF NOT EXISTS idx_implants_status ON implants(status)", "idx_implants_status"},
	{"CREATE INDEX IF NOT EXISTS idx_implants_listener_id ON implants(listener_id)", "idx_implants_listener_id"},
	{"CREATE INDEX IF NOT EXISTS idx_implants_hostname ON implants(hostname)", "idx_implants_hostname"},
	{"CREATE INDEX IF NOT EXISTS idx_implants_ip ON implants(ip)", "idx_implants_ip"},
	{"CREATE INDEX IF NOT EXISTS idx_tasks_agent_status_created ON tasks(agent_id, status, created_at)", "idx_tasks_agent_status_created"},
	{"CREATE INDEX IF NOT EXISTS idx_tasks_created_status ON tasks(created_at, status)", "idx_tasks_created_status"},
	{"CREATE INDEX IF NOT EXISTS idx_tasks_type ON tasks(type)", "idx_tasks_type"},
	{"CREATE INDEX IF NOT EXISTS idx_credential_entries_agent_id ON credential_entries(agent_id)", "idx_credential_entries_agent_id"},
	{"CREATE INDEX IF NOT EXISTS idx_credential_entries_source ON credential_entries(source)", "idx_credential_entries_source"},
	{"CREATE INDEX IF NOT EXISTS idx_credential_entries_created ON credential_entries(created_at)", "idx_credential_entries_created"},
	{"CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_logs(user)", "idx_audit_user"},
	{"CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs(action)", "idx_audit_action"},
	{"CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at)", "idx_audit_created"},
	{"CREATE INDEX IF NOT EXISTS idx_scan_agent_id ON scan_results(agent_id)", "idx_scan_agent_id"},
	{"CREATE INDEX IF NOT EXISTS idx_scan_created ON scan_results(created_at)", "idx_scan_created"},
	{"CREATE INDEX IF NOT EXISTS idx_implants_username ON implants(username)", "idx_implants_username"},
	{"CREATE INDEX IF NOT EXISTS idx_implants_os ON implants(os)", "idx_implants_os"},
	{"CREATE INDEX IF NOT EXISTS idx_implants_arch ON implants(arch)", "idx_implants_arch"},
	{"CREATE INDEX IF NOT EXISTS idx_implants_elevated ON implants(elevated)", "idx_implants_elevated"},
	{"CREATE INDEX IF NOT EXISTS idx_implants_created ON implants(created_at)", "idx_implants_created"},
	{"CREATE INDEX IF NOT EXISTS idx_implants_parent_id ON implants(parent_id)", "idx_implants_parent_id"},
	{"CREATE INDEX IF NOT EXISTS idx_users_username ON users(username)", "idx_users_username"},
	{"CREATE INDEX IF NOT EXISTS idx_users_role ON users(role)", "idx_users_role"},
	{"CREATE INDEX IF NOT EXISTS idx_users_active ON users(is_active)", "idx_users_active"},
	{"CREATE INDEX IF NOT EXISTS idx_listeners_enabled ON listeners(enabled)", "idx_listeners_enabled"},
	{"CREATE INDEX IF NOT EXISTS idx_listeners_scheme ON listeners(scheme)", "idx_listeners_scheme"},
	{"CREATE INDEX IF NOT EXISTS idx_token_entries_active ON token_entries(active)", "idx_token_entries_active"},
	{"CREATE INDEX IF NOT EXISTS idx_token_entries_domain ON token_entries(domain)", "idx_token_entries_domain"},
	{"CREATE INDEX IF NOT EXISTS idx_socks_sessions_status ON socks_sessions(status)", "idx_socks_sessions_status"},
	{"CREATE INDEX IF NOT EXISTS idx_credentials_type ON credential_entries(type)", "idx_credentials_type"},
	{"CREATE INDEX IF NOT EXISTS idx_credentials_confirmed ON credential_entries(confirmed)", "idx_credentials_confirmed"},
	{"CREATE INDEX IF NOT EXISTS idx_build_logs_user ON build_logs(user)", "idx_build_logs_user"},
	{"CREATE INDEX IF NOT EXISTS idx_build_logs_status ON build_logs(status)", "idx_build_logs_status"},
	{"CREATE INDEX IF NOT EXISTS idx_network_hosts_ip ON network_hosts(ip)", "idx_network_hosts_ip"},
	{"CREATE INDEX IF NOT EXISTS idx_command_templates_category ON command_templates(category)", "idx_command_templates_category"},
	{"CREATE INDEX IF NOT EXISTS idx_alerts_status ON alerts(status)", "idx_alerts_status"},
	{"CREATE INDEX IF NOT EXISTS idx_alerts_severity ON alerts(severity)", "idx_alerts_severity"},
	{"CREATE INDEX IF NOT EXISTS idx_alerts_type ON alerts(type)", "idx_alerts_type"},
	{"CREATE INDEX IF NOT EXISTS idx_alert_rules_enabled ON alert_rules(enabled)", "idx_alert_rules_enabled"},
	{"CREATE INDEX IF NOT EXISTS idx_system_metrics_created ON system_metrics(created_at)", "idx_system_metrics_created"},
	{"CREATE INDEX IF NOT EXISTS idx_automation_rules_enabled ON automation_rules(enabled)", "idx_automation_rules_enabled"},
	{"CREATE INDEX IF NOT EXISTS idx_automation_rules_event ON automation_rules(event_type)", "idx_automation_rules_event"},
	{"CREATE INDEX IF NOT EXISTS idx_tasks_agent_created ON tasks(agent_id, created_at DESC)", "idx_tasks_agent_created"},
	{"CREATE INDEX IF NOT EXISTS idx_credential_entries_agent_created ON credential_entries(agent_id, created_at)", "idx_credential_entries_agent_created"},
	{"CREATE INDEX IF NOT EXISTS idx_notifications_type_read_created ON notifications(type, read, created_at)", "idx_notifications_type_read_created"},
	{"CREATE INDEX IF NOT EXISTS idx_notifications_read_created ON notifications(read, created_at)", "idx_notifications_read_created"},
	{"CREATE INDEX IF NOT EXISTS idx_implants_status_last_seen ON implants(status, last_seen)", "idx_implants_status_last_seen"},
	{"CREATE INDEX IF NOT EXISTS idx_audit_logs_action_created ON audit_logs(action, created_at)", "idx_audit_logs_action_created"},
	{"CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status)", "idx_tasks_status"},
	{"CREATE INDEX IF NOT EXISTS idx_alerts_status_severity ON alerts(status, severity)", "idx_alerts_status_severity"},
	{"CREATE INDEX IF NOT EXISTS idx_alerts_rule_source ON alerts(rule_id, source, status)", "idx_alerts_rule_source"},
	{"CREATE INDEX IF NOT EXISTS idx_chat_messages_channel ON chat_messages(channel)", "idx_chat_messages_channel"},
	{"CREATE INDEX IF NOT EXISTS idx_opsec_history_agent_created ON opsec_history(agent_id, created_at)", "idx_opsec_history_agent_created"},
	{"CREATE INDEX IF NOT EXISTS idx_network_hosts_agent_ip ON network_hosts(agent_id, ip)", "idx_network_hosts_agent_ip"},
	{"CREATE INDEX IF NOT EXISTS idx_phishing_events_type_created ON phishing_events(event_type, created_at)", "idx_phishing_events_type_created"},
	{"CREATE INDEX IF NOT EXISTS idx_credential_domain_created ON credential_entries(domain, created_at)", "idx_credential_domain_created"},
	{"CREATE INDEX IF NOT EXISTS idx_audit_user_created ON audit_logs(user, created_at)", "idx_audit_user_created"},
}

// schemaMigrations mutate tables/columns/data and run BEFORE AutoMigrate.
var schemaMigrations = []*gormigrate.Migration{
	{
		ID: "2024-01-01-initial-schema",
		Migrate: func(tx *gorm.DB) error {
			m := func(sql, label string) {
				execMigration(tx, sql, label)
			}

			m("ALTER TABLE implants ADD COLUMN pid INTEGER DEFAULT 0", "add_pid")
			m("ALTER TABLE implants ADD COLUMN public_ip TEXT DEFAULT ''", "add_public_ip")
			m("ALTER TABLE implants ADD COLUMN country TEXT DEFAULT ''", "add_country")
			m("ALTER TABLE implants ADD COLUMN city TEXT DEFAULT ''", "add_city")
			m("ALTER TABLE implants ADD COLUMN latitude REAL DEFAULT 0", "add_latitude")
			m("ALTER TABLE implants ADD COLUMN longitude REAL DEFAULT 0", "add_longitude")
			m("ALTER TABLE implants ADD COLUMN active_window TEXT DEFAULT ''", "add_active_window")
			m("ALTER TABLE implants ADD COLUMN trusted INTEGER DEFAULT 0", "add_trusted")
			m("ALTER TABLE listeners ADD COLUMN tags VARCHAR(500) DEFAULT ''", "add_listeners_tags")
			m("ALTER TABLE listeners ADD COLUMN color VARCHAR(7) DEFAULT ''", "add_listeners_color")
			m("ALTER TABLE listeners ADD COLUMN status VARCHAR(20) DEFAULT 'running'", "add_listeners_status")

			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// Irreversible: columns and indexes cannot be dropped safely
			// without risking data loss. A full DB restore is required.
			return nil
		},
	},
	{
		ID: "2025-07-20-rename-agents-table",
		Migrate: func(tx *gorm.DB) error {
			m := func(sql, label string) {
				execMigration(tx, sql, label)
			}
			if !tx.Migrator().HasTable("agents") {
				return nil
			}
			if !tx.Migrator().HasTable("implants") {
				m("ALTER TABLE agents RENAME TO implants", "rename_agents_to_implants")
				return nil
			}
			// Older server versions ran AutoMigrate before this migration,
			// creating an empty `implants` table and stranding the legacy data
			// in `agents`. Heal that state only when `implants` holds no rows.
			var implantCount int64
			if err := tx.Table("implants").Count(&implantCount).Error; err != nil {
				return err
			}
			if implantCount == 0 {
				m("DROP TABLE implants", "drop_empty_implants")
				m("ALTER TABLE agents RENAME TO implants", "rename_agents_to_implants")
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			m := func(sql, label string) {
				execMigration(tx, sql, label)
			}
			m("ALTER TABLE implants RENAME TO agents", "rename_implants_to_agents")
			return nil
		},
	},
	{
		ID: "2025-07-20-cleanup-force-logout",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Model(&User{}).Where("force_logout_at IS NOT NULL").Update("force_logout_at", nil).Error; err != nil {
				return err
			}
			MigrateOldRoles(tx)
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// Irreversible: old role values are lost after migration.
			return nil
		},
	},
	{
		ID: "2025-07-25-add-agent-status-events",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&AgentStatusEvent{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("agent_status_events")
		},
	},
	{
		ID: "2025-07-25-add-user-sessions",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&UserSession{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("user_sessions")
		},
	},
	{
		ID: "2026-08-05-add-reg-secrets",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&RegSecret{}); err != nil {
				return err
			}
			return tx.AutoMigrate(&Implant{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("reg_secrets")
		},
	},
	{
		ID: "2026-07-26-add-task-acknowledged-at",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "ALTER TABLE tasks ADD COLUMN acknowledged_at DATETIME", "add_tasks_acknowledged_at")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil
		},
	},
	{
		ID: "2026-07-26-add-implant-protocol-version",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "ALTER TABLE implants ADD COLUMN protocol_version INTEGER DEFAULT 0", "add_implants_protocol_version")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "ALTER TABLE implants DROP COLUMN protocol_version", "drop_implants_protocol_version")
			return nil
		},
	},
	{
		ID: "2026-07-27-add-implant-env-fields",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "ALTER TABLE implants ADD COLUMN env_threat_score INTEGER DEFAULT 0", "add_implants_env_threat_score")
			execMigration(tx, "ALTER TABLE implants ADD COLUMN env_honeypot BOOLEAN DEFAULT FALSE", "add_implants_env_honeypot")
			execMigration(tx, "ALTER TABLE implants ADD COLUMN env_class VARCHAR(32) DEFAULT ''", "add_implants_env_class")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "ALTER TABLE implants DROP COLUMN env_threat_score", "drop_implants_env_threat_score")
			execMigration(tx, "ALTER TABLE implants DROP COLUMN env_honeypot", "drop_implants_env_honeypot")
			execMigration(tx, "ALTER TABLE implants DROP COLUMN env_class", "drop_implants_env_class")
			return nil
		},
	},
	{
		ID: "2026-07-28-add-audit-log-hash-chain",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "ALTER TABLE audit_logs ADD COLUMN prev_hash VARCHAR(64) DEFAULT ''", "add_audit_logs_prev_hash")
			execMigration(tx, "ALTER TABLE audit_logs ADD COLUMN entry_hash VARCHAR(64) DEFAULT ''", "add_audit_logs_entry_hash")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "ALTER TABLE audit_logs DROP COLUMN prev_hash", "drop_audit_logs_prev_hash")
			execMigration(tx, "ALTER TABLE audit_logs DROP COLUMN entry_hash", "drop_audit_logs_entry_hash")
			return nil
		},
	},
	{
		ID: "2026-08-02-dedupe-listener-names",
		Migrate: func(tx *gorm.DB) error {
			// Listener.Name is promoted to a unique index (via AutoMigrate).
			// Disambiguate any pre-existing duplicates (keep the oldest row's
			// name, suffix the rest) so the unique index creation succeeds.
			// Legacy databases may lack the table entirely at this stage.
			if !tx.Migrator().HasTable("listeners") {
				return nil
			}
			rows := []struct {
				Name string
				Min  uint
			}{}
			if err := tx.Table("listeners").Select("name, MIN(id) AS min").Group("name").Having("COUNT(*) > 1").Scan(&rows).Error; err != nil {
				return err
			}
			for _, r := range rows {
				var dupIDs []uint
				if err := tx.Table("listeners").Where("name = ? AND id != ?", r.Name, r.Min).Order("id").Pluck("id", &dupIDs).Error; err != nil {
					return err
				}
				for i, id := range dupIDs {
					suffix := fmt.Sprintf(" (%d)", i+2)
					newname := r.Name + suffix
					if len(newname) > 128 {
						newname = r.Name[:128-len(suffix)] + suffix
					}
					if err := tx.Table("listeners").Where("id = ?", id).Update("name", newname).Error; err != nil {
						return err
					}
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// Irreversible: original duplicate names are not tracked.
			return nil
		},
	},
	{
		ID: "2026-08-02-add-beacon-v2-columns",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "ALTER TABLE implants ADD COLUMN identity_pub VARCHAR(64) DEFAULT ''", "add_implants_identity_pub")
			execMigration(tx, "ALTER TABLE implants ADD COLUMN registered BOOLEAN DEFAULT FALSE", "add_implants_registered")
			execMigration(tx, "ALTER TABLE implants ADD COLUMN last_seq INTEGER DEFAULT 0", "add_implants_last_seq")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "ALTER TABLE implants DROP COLUMN identity_pub", "drop_implants_identity_pub")
			execMigration(tx, "ALTER TABLE implants DROP COLUMN registered", "drop_implants_registered")
			execMigration(tx, "ALTER TABLE implants DROP COLUMN last_seq", "drop_implants_last_seq")
			return nil
		},
	},
	{
		// The legacy scheduled_tasks table was pure CRUD: its schedule string
		// was never parsed and no loop ever dispatched rows. Scheduler
		// functionality merges into automation_rules as event_type="schedule"
		// rules; migrate any existing rows so no data is lost.
		ID: "2026-08-11-merge-scheduler-into-automation",
		Migrate: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable("scheduled_tasks") {
				return nil
			}
			if !tx.Migrator().HasTable("automation_rules") {
				execMigration(tx, "DROP TABLE scheduled_tasks", "drop_legacy_scheduled_tasks")
				return nil
			}
			return withMigrationTx(tx, func(tx *gorm.DB) error {
				m := func(sql, label string) {
					execMigration(tx, sql, label)
				}
				m("ALTER TABLE automation_rules ADD COLUMN schedule VARCHAR(255) DEFAULT ''", "add_automation_rules_schedule")
				m("ALTER TABLE automation_rules ADD COLUMN agent_id VARCHAR(36) DEFAULT ''", "add_automation_rules_agent_id")
				m("ALTER TABLE automation_rules ADD COLUMN task_type VARCHAR(50) DEFAULT ''", "add_automation_rules_task_type")
				m("ALTER TABLE automation_rules ADD COLUMN command TEXT", "add_automation_rules_command")
				m("ALTER TABLE automation_rules ADD COLUMN params TEXT", "add_automation_rules_params")
				m("ALTER TABLE automation_rules ADD COLUMN last_run DATETIME", "add_automation_rules_last_run")
				m("ALTER TABLE automation_rules ADD COLUMN next_run DATETIME", "add_automation_rules_next_run")
				m("ALTER TABLE automation_rules ADD COLUMN run_count INTEGER DEFAULT 0", "add_automation_rules_run_count")
				m("ALTER TABLE automation_rules ADD COLUMN created_by VARCHAR(100) DEFAULT ''", "add_automation_rules_created_by")

				type legacyTask struct {
					ID        string
					Name      string
					Enabled   bool
					AgentID   string
					TaskType  string
					Command   string
					Params    string
					Schedule  string
					LastRun   *time.Time
					NextRun   *time.Time
					RunCount  int
					CreatedBy string
					CreatedAt time.Time
					UpdatedAt time.Time
				}
				var tasks []legacyTask
				if err := tx.Table("scheduled_tasks").Find(&tasks).Error; err != nil {
					return err
				}
				for _, t := range tasks {
					// Rerun idempotency: skip rows an earlier partially-committed
					// attempt already imported.
					if migrationRowExists(tx, "automation_rules", "id", t.ID) {
						continue
					}
					actions, err := json.Marshal([]map[string]interface{}{{
						"type": "create_task",
						"params": map[string]string{
							"agent_id": t.AgentID,
							"type":     t.TaskType,
							"command":  t.Command,
						},
					}})
					if err != nil {
						return err
					}
					row := map[string]interface{}{
						"id":         t.ID,
						"name":       t.Name,
						"enabled":    t.Enabled,
						"event_type": "schedule",
						"conditions": "[]",
						"actions":    string(actions),
						"schedule":   t.Schedule,
						"agent_id":   t.AgentID,
						"task_type":  t.TaskType,
						"command":    t.Command,
						"params":     t.Params,
						"run_count":  t.RunCount,
						"created_by": t.CreatedBy,
						"created_at": t.CreatedAt,
						"updated_at": t.UpdatedAt,
					}
					if t.LastRun != nil {
						row["last_run"] = *t.LastRun
					}
					if t.NextRun != nil {
						row["next_run"] = *t.NextRun
					}
					if err := tx.Table("automation_rules").Create(row).Error; err != nil {
						return err
					}
				}
				m("DROP TABLE scheduled_tasks", "drop_legacy_scheduled_tasks")
				return nil
			})
		},
		Rollback: func(tx *gorm.DB) error {
			// Irreversible: rows were merged into automation_rules.
			return nil
		},
	},
	{
		// Workflow execution history collapses into a single execution_logs
		// table (one row per step, grouped by execution_id). Migrate existing
		// rows so no history is lost, then drop the two legacy tables.
		ID: "2026-08-11-merge-workflow-history-into-execution-logs",
		Migrate: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable("workflow_executions") {
				return nil
			}
			return withMigrationTx(tx, func(tx *gorm.DB) error {
				m := func(sql, label string) {
					execMigration(tx, sql, label)
				}
				m(`CREATE TABLE IF NOT EXISTS execution_logs (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					execution_id VARCHAR(36) NOT NULL,
					workflow_id VARCHAR(36) NOT NULL,
					workflow_name VARCHAR(200) DEFAULT '',
					agent_id VARCHAR(100) DEFAULT '',
					agent_host VARCHAR(255) DEFAULT '',
					step_order INTEGER DEFAULT 0,
					task_type VARCHAR(50) DEFAULT '',
					command TEXT,
					task_id INTEGER DEFAULT 0,
					status VARCHAR(20) DEFAULT '',
					result TEXT,
					branch_action VARCHAR(50) DEFAULT '',
					branch_target VARCHAR(255) DEFAULT '',
					error_msg TEXT,
					started_at DATETIME,
					completed_at DATETIME,
					created_at DATETIME
				)`, "create_execution_logs")

				type legacyExec struct {
					ID           uint
					WorkflowID   string
					WorkflowName string
					Status       string
					ErrorMsg     string
					StartedAt    time.Time
					CompletedAt  *time.Time
				}
				type legacyStep struct {
					ExecutionID  uint
					StepOrder    int
					TaskType     string
					Command      string
					TaskID       uint
					AgentID      string
					Status       string
					Result       string
					BranchAction string
					BranchTarget string
					StartedAt    time.Time
					CompletedAt  *time.Time
				}
				var execs []legacyExec
				if err := tx.Table("workflow_executions").Order("id").Find(&execs).Error; err != nil {
					return err
				}
				for _, e := range execs {
					execID := fmt.Sprintf("wf-%d", e.ID)
					// Rerun idempotency: skip executions an earlier partially-
					// committed attempt already imported.
					if migrationRowExists(tx, "execution_logs", "execution_id", execID) {
						continue
					}
					var steps []legacyStep
					hasStepLogs := tx.Migrator().HasTable("workflow_step_logs")
					if err := tx.Table("workflow_step_logs").Where("execution_id = ?", e.ID).Order("step_order").Find(&steps).Error; err != nil && hasStepLogs {
						return err
					}
					if len(steps) == 0 {
						row := map[string]interface{}{
							"execution_id":  execID,
							"workflow_id":   e.WorkflowID,
							"workflow_name": e.WorkflowName,
							"status":        e.Status,
							"error_msg":     e.ErrorMsg,
							"started_at":    e.StartedAt,
							"completed_at":  e.CompletedAt,
							"created_at":    e.StartedAt,
						}
						if err := tx.Table("execution_logs").Create(row).Error; err != nil {
							return err
						}
						continue
					}
					for _, s := range steps {
						row := map[string]interface{}{
							"execution_id":  execID,
							"workflow_id":   e.WorkflowID,
							"workflow_name": e.WorkflowName,
							"agent_id":      s.AgentID,
							"step_order":    s.StepOrder,
							"task_type":     s.TaskType,
							"command":       s.Command,
							"task_id":       s.TaskID,
							"status":        s.Status,
							"result":        s.Result,
							"branch_action": s.BranchAction,
							"branch_target": s.BranchTarget,
							"started_at":    s.StartedAt,
							"completed_at":  s.CompletedAt,
							"created_at":    s.StartedAt,
						}
						if e.ErrorMsg != "" {
							row["error_msg"] = e.ErrorMsg
						}
						if err := tx.Table("execution_logs").Create(row).Error; err != nil {
							return err
						}
					}
				}
				m("DROP TABLE workflow_step_logs", "drop_workflow_step_logs")
				m("DROP TABLE workflow_executions", "drop_workflow_executions")
				return nil
			})
		},
		Rollback: func(tx *gorm.DB) error {
			// Irreversible: step logs were flattened into execution_logs.
			return nil
		},
	},
	{
		// Persist the per-agent traffic auto-adapt toggle. Fresh databases get
		// the column from AutoMigrate; legacy ones get it here.
		ID: "2026-08-19-implant-auto-adapt",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "ALTER TABLE implants ADD COLUMN auto_adapt BOOLEAN DEFAULT FALSE", "add_implants_auto_adapt")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "ALTER TABLE implants DROP COLUMN auto_adapt", "drop_implants_auto_adapt")
			return nil
		},
	},
	{
		// Pre-tag servers let GORM derive pid/p2p columns as p_id/p2_p_mode/
		// p2_p_listen_addr. The explicit column tags now use pid/p2p_mode/
		// p2p_listen_addr; copy any stranded legacy values forward so old
		// agents keep their pid/p2p state. Fresh installs skip migrations
		// via InitSchema; guard HasTable for safety.
		ID: "2026-09-11-implant-canonical-pid-p2p-columns",
		Migrate: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable("implants") {
				return nil
			}
			m := func(sql, label string) {
				execMigration(tx, sql, label)
			}
			m("ALTER TABLE implants ADD COLUMN pid INTEGER DEFAULT 0", "add_implants_pid")
			m("ALTER TABLE implants ADD COLUMN p2p_mode TEXT DEFAULT ''", "add_implants_p2p_mode")
			m("ALTER TABLE implants ADD COLUMN p2p_listen_addr TEXT DEFAULT ''", "add_implants_p2p_listen_addr")
			if tx.Migrator().HasColumn("implants", "p_id") {
				if err := tx.Exec("UPDATE implants SET pid = p_id WHERE p_id IS NOT NULL AND p_id != 0 AND (pid IS NULL OR pid = 0)").Error; err != nil {
					return err
				}
			}
			if tx.Migrator().HasColumn("implants", "p2_p_mode") {
				if err := tx.Exec("UPDATE implants SET p2p_mode = p2_p_mode WHERE p2_p_mode IS NOT NULL AND p2_p_mode != '' AND (p2p_mode IS NULL OR p2p_mode = '')").Error; err != nil {
					return err
				}
			}
			if tx.Migrator().HasColumn("implants", "p2_p_listen_addr") {
				if err := tx.Exec("UPDATE implants SET p2p_listen_addr = p2_p_listen_addr WHERE p2_p_listen_addr IS NOT NULL AND p2_p_listen_addr != '' AND (p2p_listen_addr IS NULL OR p2p_listen_addr = '')").Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// Irreversible: legacy columns are left in place, values merged.
			return nil
		},
	},
	// Drop the write-only role_permissions table.
	//
	// It was seeded on every boot but never read: RoleHasPermission and
	// RoleHasPermissionDB resolve from the in-memory RolePermissionsMap plus
	// custom_roles, so the rows had no effect on any access decision. The two
	// older "remap scheduler/workflow perms" migrations that only rewrote
	// strings in this table are gone with it.
	//
	// Verified behaviour on both paths: on a fresh database gormigrate's
	// InitSchema marks every migration applied without running bodies (schema
	// comes from AutoMigrate, which no longer creates this table), so this is
	// a quiet no-op; on an existing database that already has a migrations
	// table, only this missing ID runs and the DROP fires.
	{
		ID: "2026-09-27-drop-role-permissions",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "DROP TABLE IF EXISTS role_permissions", "drop_role_permissions")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// Recreating the table restores the shape but not the seed data;
			// the table is unused either way, so there is nothing to restore.
			execMigration(tx, `CREATE TABLE IF NOT EXISTS role_permissions (
				id integer PRIMARY KEY AUTOINCREMENT,
				role varchar(32),
				permission varchar(64),
				created_at datetime
			)`, "recreate_role_permissions")
			return nil
		},
	},
	// Stamp macro runs with their macro's tenant.
	//
	// MacroRun rows were written without a tenant, so a scoped operator
	// listing runs saw cross-tenant rows. The column is added, then
	// backfilled from the parent command_macros row (JOIN on macro_id);
	// runs whose macro was deleted keep tenant 0, which tenantScope
	// already treats as legacy/unscoped. New runs inherit the tenant in
	// startMacroRun, and the handlers scope every read/write by it.
	{
		ID: "2026-10-10-macro-runs-tenant-id",
		Migrate: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable("macro_runs") {
				return nil
			}
			execMigration(tx, "ALTER TABLE macro_runs ADD COLUMN tenant_id INTEGER DEFAULT 0", "add_macro_runs_tenant_id")
			if tx.Migrator().HasTable("command_macros") {
				// Inherit from the parent macro; the subquery yields NULL
				// when the macro was deleted, and the UPDATE ... = NULL
				// form would leave those rows NULL (invisible to both
				// scoped and legacy reads), so normalize afterwards.
				if err := tx.Exec("UPDATE macro_runs SET tenant_id = (SELECT tenant_id FROM command_macros WHERE command_macros.id = macro_runs.macro_id) WHERE tenant_id IS NULL OR tenant_id = 0").Error; err != nil {
					return err
				}
				if err := tx.Exec("UPDATE macro_runs SET tenant_id = 0 WHERE tenant_id IS NULL").Error; err != nil {
					return err
				}
			}
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_macro_runs_tenant_id ON macro_runs(tenant_id)", "add_macro_runs_tenant_id_index")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_macro_runs_tenant_id", "drop_macro_runs_tenant_id_index")
			if tx.Migrator().HasColumn("macro_runs", "tenant_id") {
				execMigration(tx, "ALTER TABLE macro_runs DROP COLUMN tenant_id", "drop_macro_runs_tenant_id")
			}
			return nil
		},
	},
	// Stamp campaigns with the tenant that created them.
	//
	// Campaign rows predate tenant stamping, so a scoped operator listing
	// campaigns saw every tenant's campaigns and could seed kill chains into
	// another tenant's agents. Existing rows have no parent to inherit from,
	// so they keep tenant 0 — the legacy/unscoped marker that tenantScope
	// treats as "visible to unscoped operators only", exactly like agents and
	// macros. New campaigns stamp the creator's tenant and every handler
	// scopes reads/writes by it.
	{
		ID: "2026-10-13-campaigns-tenant-id",
		Migrate: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable("campaigns") {
				return nil
			}
			execMigration(tx, "ALTER TABLE campaigns ADD COLUMN tenant_id INTEGER DEFAULT 0", "add_campaigns_tenant_id")
			if err := tx.Exec("UPDATE campaigns SET tenant_id = 0 WHERE tenant_id IS NULL").Error; err != nil {
				return err
			}
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_campaigns_tenant_id ON campaigns(tenant_id)", "add_campaigns_tenant_id_index")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_campaigns_tenant_id", "drop_campaigns_tenant_id_index")
			if tx.Migrator().HasColumn("campaigns", "tenant_id") {
				execMigration(tx, "ALTER TABLE campaigns DROP COLUMN tenant_id", "drop_campaigns_tenant_id")
			}
			return nil
		},
	},
	// Stamp alert rules and alerts with their tenant.
	//
	// Alert rows carry the alerting host's hostname and every tenant read the
	// whole table, so one operator could enumerate another tenant's asset
	// names; the rules behind them were equally global and could be edited or
	// deleted by any operator. Rules keep tenant 0 (legacy/unscoped — the
	// marker tenantScope already treats as visible to unscoped operators only)
	// because they predate stamping and have no parent to inherit from.
	//
	// Alerts can inherit, so they are backfilled from the source implant where
	// one is still present; alerts whose agent was deleted, and server-wide
	// ("system") alerts with no agent at all, keep tenant 0.
	{
		ID: "2026-10-14-alerts-tenant-id",
		Migrate: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable("alert_rules") {
				return nil
			}
			execMigration(tx, "ALTER TABLE alert_rules ADD COLUMN tenant_id INTEGER DEFAULT 0", "add_alert_rules_tenant_id")
			if err := tx.Exec("UPDATE alert_rules SET tenant_id = 0 WHERE tenant_id IS NULL").Error; err != nil {
				return err
			}
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_alert_rules_tenant_id ON alert_rules(tenant_id)", "add_alert_rules_tenant_id_index")

			if !tx.Migrator().HasTable("alerts") {
				return nil
			}
			execMigration(tx, "ALTER TABLE alerts ADD COLUMN tenant_id INTEGER DEFAULT 0", "add_alerts_tenant_id")
			if tx.Migrator().HasTable("implants") {
				// Inherit from the alerting agent. The correlated subquery
				// yields NULL when the implant is gone, so restrict the UPDATE
				// to alerts whose source really is a live implant — leaving the
				// rest for the NULL normalization below rather than stamping
				// them NULL.
				if err := tx.Exec(`UPDATE alerts SET tenant_id = (
						SELECT i.tenant_id FROM implants i WHERE i.id = alerts.source
					)
					WHERE EXISTS (SELECT 1 FROM implants i WHERE i.id = alerts.source)`).Error; err != nil {
					return err
				}
			}
			if err := tx.Exec("UPDATE alerts SET tenant_id = 0 WHERE tenant_id IS NULL").Error; err != nil {
				return err
			}
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_alerts_tenant_id ON alerts(tenant_id)", "add_alerts_tenant_id_index")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_alerts_tenant_id", "drop_alerts_tenant_id_index")
			if tx.Migrator().HasTable("alerts") && tx.Migrator().HasColumn("alerts", "tenant_id") {
				execMigration(tx, "ALTER TABLE alerts DROP COLUMN tenant_id", "drop_alerts_tenant_id")
			}
			execMigration(tx, "DROP INDEX IF EXISTS idx_alert_rules_tenant_id", "drop_alert_rules_tenant_id_index")
			if tx.Migrator().HasTable("alert_rules") && tx.Migrator().HasColumn("alert_rules", "tenant_id") {
				execMigration(tx, "ALTER TABLE alert_rules DROP COLUMN tenant_id", "drop_alert_rules_tenant_id")
			}
			return nil
		},
	},
	// Stamp redirectors with the tenant that registered them.
	//
	// A redirector row holds the SSH credentials and target host of the
	// operator's redirector infrastructure, and both the listing and the
	// SSH deploy path read it globally: one tenant could enumerate and edit
	// another tenant's redirectors, and a deploy probe flipped every matching
	// host to "active" across all tenants. Existing rows predate stamping and
	// have no parent to inherit from, so they keep tenant 0 — legacy/unscoped,
	// which tenantScope treats as visible to unscoped operators only.
	{
		ID: "2026-10-15-redirectors-tenant-id",
		Migrate: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable("redirectors") {
				return nil
			}
			execMigration(tx, "ALTER TABLE redirectors ADD COLUMN tenant_id INTEGER DEFAULT 0", "add_redirectors_tenant_id")
			if err := tx.Exec("UPDATE redirectors SET tenant_id = 0 WHERE tenant_id IS NULL").Error; err != nil {
				return err
			}
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_redirectors_tenant_id ON redirectors(tenant_id)", "add_redirectors_tenant_id_index")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_redirectors_tenant_id", "drop_redirectors_tenant_id_index")
			if tx.Migrator().HasColumn("redirectors", "tenant_id") {
				execMigration(tx, "ALTER TABLE redirectors DROP COLUMN tenant_id", "drop_redirectors_tenant_id")
			}
			return nil
		},
	},
}

// indexMigrations create/drop indexes and run AFTER AutoMigrate, so their
// target tables/columns are guaranteed to exist on every upgrade path.
var indexMigrations = []*gormigrate.Migration{
	{
		ID: "2024-01-01-initial-schema-indexes",
		Migrate: func(tx *gorm.DB) error {
			for _, idx := range initialSchemaIndexes {
				execMigration(tx, idx.sql, idx.label)
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return nil
		},
	},
	{
		ID: "2025-07-24-add-composite-indexes",
		Migrate: func(tx *gorm.DB) error {
			m := func(sql, label string) {
				execMigration(tx, sql, label)
			}
			m("CREATE INDEX IF NOT EXISTS idx_tasks_type_status ON tasks(type, status)", "idx_tasks_type_status")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			m := func(sql, label string) {
				execMigration(tx, sql, label)
			}
			m("DROP INDEX IF EXISTS idx_tasks_type_status", "drop_idx_tasks_type_status")
			return nil
		},
	},
	{
		ID: "2025-07-24-add-hot-query-indexes",
		Migrate: func(tx *gorm.DB) error {
			m := func(sql, label string) {
				execMigration(tx, sql, label)
			}
			m("CREATE INDEX IF NOT EXISTS idx_listeners_type ON listeners(type)", "idx_listeners_type")
			m("CREATE INDEX IF NOT EXISTS idx_listeners_type_enabled ON listeners(type, enabled)", "idx_listeners_type_enabled")
			m("CREATE INDEX IF NOT EXISTS idx_plugins_type ON plugins(type)", "idx_plugins_type")
			m("CREATE INDEX IF NOT EXISTS idx_plugins_category ON plugins(category)", "idx_plugins_category")
			m("CREATE INDEX IF NOT EXISTS idx_credential_entries_domain ON credential_entries(domain)", "idx_credential_entries_domain")
			m("CREATE INDEX IF NOT EXISTS idx_webhook_configs_event_type_enabled ON webhook_configs(event_type, enabled)", "idx_webhook_configs_event_type_enabled")
			m("CREATE INDEX IF NOT EXISTS idx_alert_rules_type_enabled ON alert_rules(type, enabled)", "idx_alert_rules_type_enabled")
			m("CREATE INDEX IF NOT EXISTS idx_phishing_events_event_type ON phishing_events(event_type)", "idx_phishing_events_event_type")
			m("CREATE INDEX IF NOT EXISTS idx_templates_category ON command_templates(category)", "idx_templates_category")
			m("CREATE INDEX IF NOT EXISTS idx_notifications_type_read ON notifications(type, read)", "idx_notifications_type_read")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			m := func(sql, label string) {
				execMigration(tx, sql, label)
			}
			m("DROP INDEX IF EXISTS idx_listeners_type", "drop_idx_listeners_type")
			m("DROP INDEX IF EXISTS idx_listeners_type_enabled", "drop_idx_listeners_type_enabled")
			m("DROP INDEX IF EXISTS idx_plugins_type", "drop_idx_plugins_type")
			m("DROP INDEX IF EXISTS idx_plugins_category", "drop_idx_plugins_category")
			m("DROP INDEX IF EXISTS idx_credential_entries_domain", "drop_idx_credential_entries_domain")
			m("DROP INDEX IF EXISTS idx_webhook_configs_event_type_enabled", "drop_idx_webhook_configs_event_type_enabled")
			m("DROP INDEX IF EXISTS idx_alert_rules_type_enabled", "drop_idx_alert_rules_type_enabled")
			m("DROP INDEX IF EXISTS idx_phishing_events_event_type", "drop_idx_phishing_events_event_type")
			m("DROP INDEX IF EXISTS idx_templates_category", "drop_idx_templates_category")
			m("DROP INDEX IF EXISTS idx_notifications_type_read", "drop_idx_notifications_type_read")
			return nil
		},
	},
	{
		ID: "2025-07-24-add-nocase-indexes",
		Migrate: func(tx *gorm.DB) error {
			m := func(sql, label string) {
				execMigration(tx, sql, label)
			}
			m("CREATE INDEX IF NOT EXISTS idx_tasks_agent_id_nocase ON tasks(agent_id COLLATE NOCASE)", "idx_tasks_agent_id_nocase")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			m := func(sql, label string) {
				execMigration(tx, sql, label)
			}
			m("DROP INDEX IF EXISTS idx_tasks_agent_id_nocase", "drop_idx_tasks_agent_id_nocase")
			return nil
		},
	},
	{
		ID: "2025-07-24-add-missing-indexes",
		Migrate: func(tx *gorm.DB) error {
			m := func(sql, label string) {
				execMigration(tx, sql, label)
			}
			m("CREATE INDEX IF NOT EXISTS idx_scan_results_task_id ON scan_results(task_id)", "idx_scan_results_task_id")
			m("CREATE INDEX IF NOT EXISTS idx_credential_entries_task_id ON credential_entries(task_id)", "idx_credential_entries_task_id")
			m("CREATE INDEX IF NOT EXISTS idx_implants_domain ON implants(domain)", "idx_implants_domain")
			m("CREATE INDEX IF NOT EXISTS idx_bloodhound_results_task_id ON bloodhound_results(task_id)", "idx_bloodhound_results_task_id")
			m("CREATE INDEX IF NOT EXISTS idx_build_logs_listener_id ON build_logs(listener_id)", "idx_build_logs_listener_id")
			m("CREATE INDEX IF NOT EXISTS idx_tasks_agent_type_status ON tasks(agent_id, type, status)", "idx_tasks_agent_type_status")
			m("CREATE INDEX IF NOT EXISTS idx_tasks_agent_status ON tasks(agent_id, status)", "idx_tasks_agent_status")
			m("CREATE INDEX IF NOT EXISTS idx_credential_entries_agent_domain ON credential_entries(agent_id, domain)", "idx_credential_entries_agent_domain")
			m("CREATE INDEX IF NOT EXISTS idx_scan_results_task_port ON scan_results(task_id, port)", "idx_scan_results_task_port")
			m("CREATE INDEX IF NOT EXISTS idx_scan_results_agent_created ON scan_results(agent_id, created_at)", "idx_scan_results_agent_created")
			m("CREATE INDEX IF NOT EXISTS idx_implants_listener_status ON implants(listener_id, last_seen)", "idx_implants_listener_status")
			m("CREATE INDEX IF NOT EXISTS idx_token_entries_agent_active ON token_entries(agent_id, active)", "idx_token_entries_agent_active")
			m("DROP INDEX IF EXISTS idx_templates_category", "drop_dup_templates_category")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			m := func(sql, label string) {
				execMigration(tx, sql, label)
			}
			m("CREATE INDEX IF NOT EXISTS idx_templates_category ON command_templates(category)", "restore_idx_templates_category")
			m("DROP INDEX IF EXISTS idx_scan_results_task_id", "drop_idx_scan_results_task_id")
			m("DROP INDEX IF EXISTS idx_credential_entries_task_id", "drop_idx_credential_entries_task_id")
			m("DROP INDEX IF EXISTS idx_implants_domain", "drop_idx_implants_domain")
			m("DROP INDEX IF EXISTS idx_bloodhound_results_task_id", "drop_idx_bloodhound_results_task_id")
			m("DROP INDEX IF EXISTS idx_build_logs_listener_id", "drop_idx_build_logs_listener_id")
			m("DROP INDEX IF EXISTS idx_tasks_agent_type_status", "drop_idx_tasks_agent_type_status")
			m("DROP INDEX IF EXISTS idx_tasks_agent_status", "drop_idx_tasks_agent_status")
			m("DROP INDEX IF EXISTS idx_credential_entries_agent_domain", "drop_idx_credential_entries_agent_domain")
			m("DROP INDEX IF EXISTS idx_scan_results_task_port", "drop_idx_scan_results_task_port")
			m("DROP INDEX IF EXISTS idx_scan_results_agent_created", "drop_idx_scan_results_agent_created")
			m("DROP INDEX IF EXISTS idx_implants_listener_status", "drop_idx_implants_listener_status")
			m("DROP INDEX IF EXISTS idx_token_entries_agent_active", "drop_idx_token_entries_agent_active")
			return nil
		},
	},
	{
		ID: "2025-07-25-add-tasks-created-status-index",
		Migrate: func(tx *gorm.DB) error {
			m := func(sql, label string) {
				execMigration(tx, sql, label)
			}
			m("CREATE INDEX IF NOT EXISTS idx_tasks_agent_created_status ON tasks(agent_id, created_at DESC, status)", "idx_tasks_agent_created_status")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			m := func(sql, label string) {
				execMigration(tx, sql, label)
			}
			m("DROP INDEX IF EXISTS idx_tasks_agent_created_status", "drop_idx_tasks_agent_created_status")
			return nil
		},
	},
	{
		ID: "2025-07-26-add-missing-indexes",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_credential_entries_username ON credential_entries(username)", "idx_credential_entries_username")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_credential_entries_hash ON credential_entries(hash)", "idx_credential_entries_hash")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_tasks_type_created ON tasks(type, created_at)", "idx_tasks_type_created")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_implants_domain ON implants(domain)", "idx_implants_domain")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_implants_parent_agent_id ON implants(parent_agent_id)", "idx_implants_parent_agent_id")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_webhook_configs_url ON webhook_configs(url)", "idx_webhook_configs_url")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_credential_entries_username", "idx_credential_entries_username")
			execMigration(tx, "DROP INDEX IF EXISTS idx_credential_entries_hash", "idx_credential_entries_hash")
			execMigration(tx, "DROP INDEX IF EXISTS idx_tasks_type_created", "idx_tasks_type_created")
			execMigration(tx, "DROP INDEX IF EXISTS idx_implants_domain", "idx_implants_domain")
			execMigration(tx, "DROP INDEX IF EXISTS idx_implants_parent_agent_id", "idx_implants_parent_agent_id")
			execMigration(tx, "DROP INDEX IF EXISTS idx_webhook_configs_url", "idx_webhook_configs_url")
			return nil
		},
	},
	{
		ID: "2026-07-26-add-task-claim-indexes",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_tasks_agent_status_priority_created ON tasks(agent_id, status, priority DESC, created_at ASC)", "idx_tasks_agent_status_priority_created")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_tasks_status_claimed_at ON tasks(status, claimed_at)", "idx_tasks_status_claimed_at")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_tasks_agent_status_priority_created", "drop_idx_tasks_agent_status_priority_created")
			execMigration(tx, "DROP INDEX IF EXISTS idx_tasks_status_claimed_at", "drop_idx_tasks_status_claimed_at")
			return nil
		},
	},
	{
		ID: "2026-07-26-add-task-acknowledged-at-index",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_tasks_status_claimed_acknowledged ON tasks(status, claimed_at, acknowledged_at)", "idx_tasks_status_claimed_acknowledged")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_tasks_status_claimed_acknowledged", "drop_idx_tasks_status_claimed_acknowledged")
			return nil
		},
	},
	{
		ID: "2026-07-27-add-hot-path-composite-indexes",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_implants_status_lastseen ON implants(status, last_seen)", "implant status+lastseen index")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_auditlogs_action_created ON audit_logs(action, created_at)", "auditlog action+created index")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_auditlogs_agent_created ON audit_logs(agent_id, created_at)", "auditlog agent+created index")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_tasks_agent_type ON tasks(agent_id, type)", "task agent+type index")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_tasks_status_created ON tasks(status, created_at)", "task status+created index")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_implants_status_lastseen", "drop implant status+lastseen index")
			execMigration(tx, "DROP INDEX IF EXISTS idx_auditlogs_action_created", "drop auditlog action+created index")
			execMigration(tx, "DROP INDEX IF EXISTS idx_auditlogs_agent_created", "drop auditlog agent+created index")
			execMigration(tx, "DROP INDEX IF EXISTS idx_tasks_agent_type", "drop task agent+type index")
			execMigration(tx, "DROP INDEX IF EXISTS idx_tasks_status_created", "drop task status+created index")
			return nil
		},
	},
	{
		ID: "2026-07-31-add-join-table-implant-indexes",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_agent_tag_assignments_implant ON agent_tag_assignments(implant_id)", "idx_agent_tag_assignments_implant")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_agent_group_assignments_implant ON agent_group_assignments(implant_id)", "idx_agent_group_assignments_implant")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_agent_tag_assignments_implant", "drop_idx_agent_tag_assignments_implant")
			execMigration(tx, "DROP INDEX IF EXISTS idx_agent_group_assignments_implant", "drop_idx_agent_group_assignments_implant")
			return nil
		},
	},
	{
		ID: "2026-08-02-add-data-integrity-indexes",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_agent_status_events_agent_time ON agent_status_events(agent_id, timestamp)", "idx_agent_status_events_agent_time")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_credential_entries_expires ON credential_entries(expires_at)", "idx_credential_entries_expires")
			execMigration(tx, "CREATE UNIQUE INDEX IF NOT EXISTS idx_listeners_name_unique ON listeners(name)", "idx_listeners_name_unique")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_agent_status_events_agent_time", "drop_idx_agent_status_events_agent_time")
			execMigration(tx, "DROP INDEX IF EXISTS idx_credential_entries_expires", "drop_idx_credential_entries_expires")
			execMigration(tx, "DROP INDEX IF EXISTS idx_listeners_name_unique", "drop_idx_listeners_name_unique")
			return nil
		},
	},
	{
		ID: "2026-08-06-add-system-metrics-created-at-index",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_system_metrics_created_at ON system_metrics(created_at)", "idx_system_metrics_created_at")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_system_metrics_created_at", "drop_idx_system_metrics_created_at")
			return nil
		},
	},
	{
		ID: "2026-09-03-add-task-stale-sweep-index",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_tasks_status_claimed ON tasks(status, claimed_at)", "idx_tasks_status_claimed")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_tasks_status_claimed", "drop_idx_tasks_status_claimed")
			return nil
		},
	},
	{
		ID: "2026-09-16-add-task-idempotency-unique-index",
		Migrate: func(tx *gorm.DB) error {
			// Collapse pre-existing live duplicates so the UNIQUE index can
			// be created: keep the newest row per (agent, key), cancel the
			// rest with an auditable reason. Terminal tasks never block keys.
			live := []string{"pending", "pending_approval", "running"}
			var dups []struct {
				AgentID        string
				IdempotencyKey string
			}
			if err := tx.Model(&Task{}).
				Select("agent_id, idempotency_key").
				Where("idempotency_key <> '' AND status IN ?", live).
				Group("agent_id, idempotency_key").Having("COUNT(*) > 1").
				Scan(&dups).Error; err != nil {
				return err
			}
			for _, d := range dups {
				var ids []uint
				if err := tx.Model(&Task{}).
					Where("agent_id = ? AND idempotency_key = ? AND status IN ?", d.AgentID, d.IdempotencyKey, live).
					Order("id DESC").Pluck("id", &ids).Error; err != nil {
					return err
				}
				if len(ids) > 1 {
					if err := tx.Model(&Task{}).Where("id IN ?", ids[1:]).
						Updates(map[string]interface{}{
							"status": "cancelled",
							"error":  "superseded by duplicate idempotency key (migration dedup)",
						}).Error; err != nil {
						return err
					}
				}
			}
			execMigration(tx, "CREATE UNIQUE INDEX IF NOT EXISTS uidx_tasks_idem_live ON tasks(agent_id, idempotency_key) WHERE status IN ('pending','pending_approval','running') AND idempotency_key <> ''", "uidx_tasks_idem_live")
			if !tx.Migrator().HasIndex(&Task{}, "uidx_tasks_idem_live") {
				return fmt.Errorf("partial unique index uidx_tasks_idem_live was not created")
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS uidx_tasks_idem_live", "drop_uidx_tasks_idem_live")
			return nil
		},
	},
	{
		ID: "2026-09-17-add-task-sweep-cover-indexes",
		Migrate: func(tx *gorm.DB) error {
			// Cover the three sweep predicates that previously fell back to
			// full status='running' scans: acked-result (status,
			// acknowledged_at), requeue/exhausted/sent (status, claimed_at,
			// acknowledged_at, delivery_attempts), approvals (status,
			// approval_expires_at).
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_tasks_status_acknowledged ON tasks(status, acknowledged_at)", "idx_tasks_status_acknowledged")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_tasks_sweep_running ON tasks(status, claimed_at, acknowledged_at, delivery_attempts)", "idx_tasks_sweep_running")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_tasks_approval_expiry ON tasks(status, approval_expires_at)", "idx_tasks_approval_expiry")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_tasks_status_acknowledged", "drop_idx_tasks_status_acknowledged")
			execMigration(tx, "DROP INDEX IF EXISTS idx_tasks_sweep_running", "drop_idx_tasks_sweep_running")
			execMigration(tx, "DROP INDEX IF EXISTS idx_tasks_approval_expiry", "drop_idx_tasks_approval_expiry")
			return nil
		},
	},
	{
		ID: "2026-09-28-add-beacon-dispatch-cover-indexes",
		Migrate: func(tx *gorm.DB) error {
			// Cover the per-beacon claim predicates that previously scanned:
			// the re-read of just-claimed rows and the lost-response refill
			// both filter agent_id + status + claimed_by, and the operator
			// claimed-by filter had no index at all. The parent walk for
			// chain assignment queries parent_agent_id per depth level.
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_tasks_agent_status_claimed ON tasks(agent_id, status, claimed_by)", "idx_tasks_agent_status_claimed")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_tasks_op_claimed ON tasks(operator_claimed_by)", "idx_tasks_op_claimed")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_implants_parent_agent ON implants(parent_agent_id)", "idx_implants_parent_agent")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_tasks_agent_status_claimed", "drop_idx_tasks_agent_status_claimed")
			execMigration(tx, "DROP INDEX IF EXISTS idx_tasks_op_claimed", "drop_idx_tasks_op_claimed")
			execMigration(tx, "DROP INDEX IF EXISTS idx_implants_parent_agent", "drop_idx_implants_parent_agent")
			return nil
		},
	},
	// Unique (type, channel_id) on external C2 channels. Configure used to
	// Create unconditionally, so reconfiguring the same channel accumulated
	// rows (and orphaned the previous poller); the upsert path in
	// extc2_handlers requires this constraint. Legacy duplicates collapse to
	// the lowest id first, or the unique index cannot be created.
	{
		ID: "2026-10-10-extc2-channel-unique",
		Migrate: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable("extc2_channels") {
				return nil
			}
			if err := tx.Exec("DELETE FROM extc2_channels WHERE id NOT IN (SELECT MIN(id) FROM extc2_channels GROUP BY type, channel_id)").Error; err != nil {
				return err
			}
			execMigration(tx, "CREATE UNIQUE INDEX IF NOT EXISTS idx_extc2_type_channel ON extc2_channels(type, channel_id)", "idx_extc2_type_channel")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_extc2_type_channel", "drop_idx_extc2_type_channel")
			return nil
		},
	},
	// ── Hot-path indexes for read shapes the earlier migrations missed ──
	// Every index below was picked by EXPLAIN QUERY PLAN against the real
	// handler query, not by guessing at columns. Each one replaced either a
	// full scan plus a temp b-tree sort, or a non-covering search that still
	// fetched every matching row.
	//
	// Deliberately NOT included (measured, plan unchanged):
	//   - alert_rules(created_at): handleGetAlertRules already seeks
	//     idx_alert_rules_tenant_id; a second index changed nothing.
	//   - credential_usages(credential_id, created_at): the vault page loads
	//     the model (SELECT *), so the composite never became covering. The
	//     real cost there is the unbounded row count, not the sort.
	//   - network_hosts(agent_id, last_seen): the topology view filters with
	//     `agent_id IN (SELECT id FROM implants WHERE tenant_id = ?)` and
	//     SQLite still sorts. The composite only removed the b-tree for a
	//     literal `agent_id = ?`, a shape no handler emits.
	{
		ID: "2026-10-15-hot-path-read-indexes",
		Migrate: func(tx *gorm.DB) error {
			// metric_alerts evaluates the failure rate every
			// defaultAlertEvalInterval (30s) as
			// `status IN ('completed','failed') AND updated_at > ?`.
			// idx_tasks_status alone located the candidate rows and then read
			// updated_at off every one of them, so the cost grew with all of
			// history rather than with the window.
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_tasks_status_updated ON tasks(status, updated_at)", "idx_tasks_status_updated")
			// The AI task tool's creator filter (`created_by = 'ai'`) plus
			// `ORDER BY created_at DESC`. tasks has 24 indexes and none led
			// with created_by, so this scanned idx_tasks_created_at instead.
			// The `NOT IN ('ai','automation','system')` branch cannot seek this
			// index and keeps its previous plan.
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_tasks_created_by_created ON tasks(created_by, created_at)", "idx_tasks_created_by_created")
			// handleGetAlerts is tenant-scoped and orders by created_at DESC; the
			// tenant index alone forced a temp b-tree sort per request.
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_alerts_tenant_created ON alerts(tenant_id, created_at DESC)", "idx_alerts_tenant_created")
			// The notification inbox's `severity = ?` filter had no index, and
			// with `read` unset neither (read, created_at) nor (type, read)
			// applies, so a filtered inbox scanned the whole table.
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_notifications_severity_created ON notifications(severity, created_at DESC)", "idx_notifications_severity_created")
			// Token lists (per-agent page and global list) order by created_at
			// DESC; idx_token_entries_agent_active sorts afterwards.
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_token_entries_agent_created ON token_entries(agent_id, created_at DESC)", "idx_token_entries_agent_created")
			// Build log filters: platform (ops list) and status+window
			// (effectiveness report).
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_build_logs_platform_created ON build_logs(platform, created_at DESC)", "idx_build_logs_platform_created")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_build_logs_status_created ON build_logs(status, created_at DESC)", "idx_build_logs_status_created")
			// Chat history is one ascending window per channel.
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_chat_messages_channel_created ON chat_messages(channel, created_at)", "idx_chat_messages_channel_created")
			// Session and API-key lists: both are per-user and order by
			// created_at DESC; only user_id was indexed.
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_user_sessions_user_created ON user_sessions(user_id, created_at DESC)", "idx_user_sessions_user_created")
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_api_keys_user_created ON api_keys(user_id, created_at DESC)", "idx_api_keys_user_created")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			for _, idx := range []string{
				"idx_tasks_status_updated",
				"idx_tasks_created_by_created",
				"idx_alerts_tenant_created",
				"idx_notifications_severity_created",
				"idx_token_entries_agent_created",
				"idx_build_logs_platform_created",
				"idx_build_logs_status_created",
				"idx_chat_messages_channel_created",
				"idx_user_sessions_user_created",
				"idx_api_keys_user_created",
			} {
				execMigration(tx, "DROP INDEX IF EXISTS "+idx, "drop_"+idx)
			}
			return nil
		},
	},
	// Case-insensitive agent-id lookup. Replaces a migration that asked for
	// implants(agent_id COLLATE NOCASE): the implant's primary key column is
	// `id`, not `agent_id`, so that index never existed and every boot logged a
	// "no such column: agent_id" warning. The intent is real — beacon dispatch
	// lower-cases the agent id before looking it up — so this lands the index
	// that actually serves it.
	//
	// A new ID rather than an edit to the old one: existing databases already
	// recorded 2025-07-24-add-nocase-indexes in the migration ledger, so
	// changing that migration's SQL would never re-run it on upgrade.
	{
		ID: "2026-10-16-implant-id-nocase-index",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_implants_id_nocase ON implants(id COLLATE NOCASE)", "idx_implants_id_nocase")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_implants_id_nocase", "drop_idx_implants_id_nocase")
			return nil
		},
	},
	// The listener detail page lists the agents registered through one
	// listener, newest first: handlers_listeners.go:77,114 emit
	// `WHERE listener_id = ? ORDER BY last_seen DESC LIMIT 5000`, and the
	// active-agent counters reuse the predicate with a last_seen window
	// (handlers_listeners.go:82,121,572; handlers_opsec.go:158;
	// ai_tools_exec.go:1286). idx_implants_listener_id finds the rows but
	// nothing supplies the order, so every request sorted the listener's whole
	// fleet in a temp b-tree before the LIMIT could cut it down, and the
	// counters paid a rowid lookup per match just to read last_seen. The
	// composite serves both shapes: equality seek plus an ordered,
	// index-only range. A new migration ID rather than an edit to
	// 2026-10-15-hot-path-read-indexes, which databases have already applied.
	{
		ID: "2026-10-16-implant-listener-read-index",
		Migrate: func(tx *gorm.DB) error {
			execMigration(tx, "CREATE INDEX IF NOT EXISTS idx_implants_listener_seen ON implants(listener_id, last_seen)", "idx_implants_listener_seen")
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			execMigration(tx, "DROP INDEX IF EXISTS idx_implants_listener_seen", "drop_idx_implants_listener_seen")
			return nil
		},
	},
}

// Migrations is the combined migration history, kept for tooling and tests.
var Migrations = append(append([]*gormigrate.Migration{}, schemaMigrations...), indexMigrations...)

func runSchemaMigrations(db *gorm.DB) error {
	resetMigrationFailures()
	m := gormigrate.New(db, gormigrate.DefaultOptions, schemaMigrations)
	m.InitSchema(func(tx *gorm.DB) error {
		return nil
	})
	if err := m.Migrate(); err != nil {
		return err
	}
	// A statement that failed for a non-ignorable reason must keep its migration UNapplied,
	// or gormigrate records it as done and never retries.
	return migrationFailureError("schema migrations")
}

func runIndexMigrations(db *gorm.DB) error {
	resetMigrationFailures()
	m := gormigrate.New(db, gormigrate.DefaultOptions, indexMigrations)
	m.InitSchema(func(tx *gorm.DB) error {
		return nil
	})
	if err := m.Migrate(); err != nil {
		return err
	}
	// A CREATE INDEX that failed must keep the migration unapplied too: otherwise the index
	// is absent forever and the only trace is a warning line at startup.
	return migrationFailureError("index migrations")
}
