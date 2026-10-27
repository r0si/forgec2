package db

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

const (
	PermAgentsRead         = "agents.read"
	PermAgentsWrite        = "agents.write"
	PermAgentsDelete       = "agents.delete"
	PermListenersRead      = "listeners.read"
	PermListenersWrite     = "listeners.write"
	PermListenersDelete    = "listeners.delete"
	PermTasksRead          = "tasks.read"
	PermTasksWrite         = "tasks.write"
	PermTasksDelete        = "tasks.delete"
	PermCredsRead          = "credentials.read"
	PermCredsWrite         = "credentials.write"
	PermCredsDelete        = "credentials.delete"
	PermFilesRead          = "files.read"
	PermFilesWrite         = "files.write"
	PermUsersRead          = "users.read"
	PermUsersWrite         = "users.write"
	PermUsersDelete        = "users.delete"
	PermSettingsRead       = "settings.read"
	PermSettingsWrite      = "settings.write"
	PermAuditRead          = "audit.read"
	PermGroupsRead         = "groups.read"
	PermGroupsWrite        = "groups.write"
	PermPluginsRead        = "plugins.read"
	PermPluginsWrite       = "plugins.write"
	PermPluginsExecute     = "plugins.execute"
	PermPluginsDelete      = "plugins.delete"
	PermRolesRead          = "roles.read"
	PermRolesWrite         = "roles.write"
	PermCampaignsRead      = "campaigns.read"
	PermCampaignsWrite     = "campaigns.write"
	PermOpsecRead          = "opsec.read"
	PermOpsecWrite         = "opsec.write"
	PermIntelRead          = "intel.read"
	PermIntelWrite         = "intel.write"
	PermAutomationRead     = "automation.read"
	PermAutomationWrite    = "automation.write"
	PermNotificationsRead  = "notifications.read"
	PermNotificationsWrite = "notifications.write"
	PermAIUse              = "ai.use"
	PermAIConfigure        = "ai.configure"
	PermAIKnowledgeManage  = "ai.knowledge.manage"
	// PermBulkExport gates bulk secret exports (credential/task/handover/
	// report bundles) for API-key callers. The scope exists so automation
	// keys must opt in; interactive session callers pass.
	PermBulkExport = "bulk_export"
)

var RolePermissionsMap = map[string][]string{
	RoleAdmin: {
		PermAgentsRead, PermAgentsWrite, PermAgentsDelete,
		PermListenersRead, PermListenersWrite, PermListenersDelete,
		PermTasksRead, PermTasksWrite, PermTasksDelete,
		PermCredsRead, PermCredsWrite, PermCredsDelete,
		PermFilesRead, PermFilesWrite,
		PermUsersRead, PermUsersWrite, PermUsersDelete,
		PermSettingsRead, PermSettingsWrite,
		PermAuditRead,
		PermGroupsRead, PermGroupsWrite,
		PermPluginsRead, PermPluginsWrite, PermPluginsExecute, PermPluginsDelete,
		PermRolesRead, PermRolesWrite,
		PermCampaignsRead, PermCampaignsWrite,
		PermOpsecRead, PermOpsecWrite,
		PermIntelRead, PermIntelWrite,
		PermAutomationRead, PermAutomationWrite,
		PermNotificationsRead, PermNotificationsWrite,
		PermAIUse, PermAIConfigure, PermAIKnowledgeManage,
	},
	RoleUser: {
		PermAgentsRead, PermAgentsWrite,
		PermListenersRead, PermListenersWrite,
		PermTasksRead, PermTasksWrite,
		PermCredsRead, PermCredsWrite,
		PermFilesRead, PermFilesWrite,
		PermUsersRead,
		PermSettingsRead,
		PermAuditRead,
		PermGroupsRead, PermGroupsWrite,
		PermRolesRead,
		PermCampaignsRead,
		PermOpsecRead,
		PermIntelRead,
		PermAutomationRead,
		PermNotificationsRead,
		PermAIUse,
	},
}

// TaskStats holds per-agent task status counts (non-persisted, computed at query time)
type TaskStats struct {
	Pending   int `json:"pending"`
	Running   int `json:"running"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

// Implant represents a connected implant (agent)
type Implant struct {
	ID        string    `gorm:"primaryKey" json:"id"`
	TenantID  uint      `gorm:"index;index:idx_implants_tenant_seen,priority:1" json:"tenant_id"` // multi-tenant isolation (0 = legacy/unscoped)
	Hostname  string    `json:"hostname"`
	Username  string    `json:"username"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
	IP        string    `json:"ip"`
	PublicIP  string    `json:"public_ip"` // public IP from beacon connection
	Country   string    `json:"country"`   // GeoIP country
	City      string    `json:"city"`      // GeoIP city
	Latitude  float64   `json:"latitude"`  // GeoIP latitude
	Longitude float64   `json:"longitude"` // GeoIP longitude
	LastSeen  time.Time `gorm:"index;index:idx_implants_status_seen,priority:2;index:idx_implants_tenant_seen,priority:2" json:"last_seen"`
	Status    string    `gorm:"index;index:idx_implants_status_seen,priority:1" json:"status"` // online, offline
	Trusted   bool      `gorm:"default:false" json:"trusted"`                                  // operator-approved agent
	// Force-offline: server-side service denial. A blocked implant's check-ins
	// are refused (well-formed but taskless replies), its status pinned
	// offline, and every attempt audited. Distinct from soft-delete:
	// tombstoned agents are restored when they beacon again, blocked ones stay
	// locked out until explicitly unblocked.
	Blocked       bool   `gorm:"default:false;index" json:"blocked"`
	BlockedReason string `gorm:"size:255;default:''" json:"blocked_reason,omitempty"`
	Notes         string `json:"notes"`
	Tags          string `json:"tags"` // comma separated
	ListenerID    uint   `json:"listener_id"`
	// Multi-hop Proxy Chain (ParentAgentID is the next-hop toward C2, distinct from P2P parent_id)
	ParentAgentID string `gorm:"size:36;default:''" json:"parent_agent_id,omitempty"`
	// P2P Beacon Chaining
	ParentID      string `gorm:"index" json:"parent_id"`                        // UUID of parent agent (empty if direct)
	P2PMode       string `gorm:"column:p2p_mode" json:"p2p_mode"`               // "", "smb", "tcp" - how child connects
	P2PListenAddr string `gorm:"column:p2p_listen_addr" json:"p2p_listen_addr"` // smb pipe name or tcp addr for children
	// P2P Gossip Mesh
	PeerCount int    `gorm:"default:0" json:"peer_count"`
	BestRoute string `gorm:"default:''" json:"best_route"`
	// Agent metadata (reported every beacon)
	Version         string `json:"version"`               // agent build version
	ProtocolVersion uint   `json:"protocol_version"`      // wire protocol version
	PID             int    `gorm:"column:pid" json:"pid"` // agent process ID
	ProcessName     string `json:"process_name"`          // e.g. forgec2.exe
	Integrity       string `json:"integrity"`             // Low / Medium / High / System
	Elevated        bool   `json:"elevated"`              // running as admin/root
	Domain          string `json:"domain"`                // AD domain or workgroup
	// Per-agent sleep config (server-side tracking)
	CurrentInterval int    `json:"current_interval"`                // current sleep interval (seconds)
	CurrentJitter   int    `json:"current_jitter"`                  // current jitter percentage
	AutoAdapt       bool   `gorm:"default:false" json:"auto_adapt"` // operator-enabled traffic auto-adaptation
	ActiveWindow    string `json:"active_window"`                   // foreground window title (reported each beacon)
	// Working hours (server-side tracking)
	WorkingHoursStart string `gorm:"size:5" json:"working_hours_start"` // HH:MM
	WorkingHoursEnd   string `gorm:"size:5" json:"working_hours_end"`   // HH:MM
	WorkingHoursTZ    string `gorm:"size:50" json:"working_hours_tz"`   // IANA timezone
	// Per-agent kill date
	KillDate *time.Time `json:"kill_date,omitempty"` // agent self-destructs after this time
	// Agent self-assessed environment threat data
	EnvThreatScore int    `json:"env_threat_score" gorm:"default:0"`   // 0-100 agent-reported threat level
	EnvHoneypot    bool   `json:"env_honeypot" gorm:"default:false"`   // agent detected honeypot env
	EnvClass       string `json:"env_class" gorm:"size:32;default:''"` // environment classification
	// Protocol v2 identity & replay protection
	IdentityPub string    `gorm:"size:64;default:''" json:"identity_pub,omitempty"`    // base64 X25519 identity public key (registered)
	Registered  bool      `gorm:"default:false" json:"registered"`                     // v2 registration handshake completed
	LastSeq     uint64    `gorm:"default:0" json:"last_seq,omitempty"`                 // last accepted beacon seq (replay window)
	SecretID    string    `gorm:"size:64;default:'';index" json:"secret_id,omitempty"` // v3 per-implant registration secret id (bound at registration)
	CreatedAt   time.Time `gorm:"index" json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Soft delete: operators can remove an agent from the UI without losing its
	// intel. Normal queries exclude deleted rows; reconciliation paths use
	// Unscoped so a removed agent that beacons again is restored, not duplicated.
	DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}

// RegSecret is a v3 per-implant registration secret generated at build time.
// Only the secret_id is public (compiled into the implant); the sealed secret
// lives here at rest and is unsealed server-side to verify beacon frames.
type RegSecret struct {
	ID        string `gorm:"primaryKey" json:"id"`                         // public secret id (compiled into the implant)
	SecretEnc string `gorm:"type:text" json:"-"`                           // AES-256-GCM sealed registration secret
	AgentID   string `gorm:"size:36;default:''" json:"agent_id,omitempty"` // bound after first registration
	Bound     bool   `gorm:"default:false" json:"bound"`
	// TenantID is the tenant of the operator who minted the secret, so an
	// implant built by one team binds to that team when it first phones home
	// instead of falling into the bootstrap default tenant (0 = legacy
	// unscoped row, minted by the CLI without -tenant or before this column;
	// such an agent still lands in the default tenant).
	TenantID  uint      `gorm:"index" json:"tenant_id"`
	CreatedAt time.Time `json:"created_at"`
}

// KillSwitch is the fleet-wide kill-switch broadcast state (singleton row,
// ID=1). When Armed, every beacon response — including fresh registrations
// and implants returning from downtime — carries a per-implant authenticated
// token, so a compromised or abandoned server can order the entire fleet to
// self-destruct even without reaching each agent individually.
type KillSwitch struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Armed       bool       `gorm:"default:false" json:"armed"`
	Token       string     `gorm:"size:64" json:"-"` // regenerated on every arm (replay protection)
	TriggeredAt *time.Time `json:"triggered_at,omitempty"`
	TriggeredBy string     `gorm:"size:64" json:"triggered_by,omitempty"`
	DisarmedAt  *time.Time `json:"disarmed_at,omitempty"`
	DisarmedBy  string     `gorm:"size:64" json:"disarmed_by,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Task represents a command/task sent to an agent
type Task struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// The composite index matters for the operator task list: with only the plain
	// tenant_id index, "WHERE tenant_id = ? ORDER BY created_at DESC LIMIT 50" read
	// every row of the tenant and sorted it in a TEMP B-TREE on every page load.
	TenantID uint   `gorm:"index;index:idx_tasks_tenant_created,priority:1" json:"tenant_id"` // multi-tenant isolation (0 = legacy/unscoped)
	AgentID  string `gorm:"index;index:idx_tasks_agent_status,priority:1;index:idx_tasks_agent_created,priority:1" json:"agent_id"`
	Type     string `json:"type"`
	Command  string `json:"command"`
	Shell    string `json:"shell"`
	Path     string `json:"path,omitempty"`
	Data     string `json:"data,omitempty"`
	Offset   int64  `json:"offset,omitempty"`
	Size     int64  `json:"size,omitempty"`
	// PrevMAC/MAC carry the chunked file-transfer HMAC integrity chain on
	// push-upload tasks (hex); the agent verifies Data against them.
	PrevMAC string `json:"prev_mac,omitempty"`
	MAC     string `json:"mac,omitempty"`
	// TaskKey is the operator-issued per-task AES-256-GCM key (base64) used to
	// seal this task's result independently of the session key (P2). Empty when
	// the task uses the normal channel encryption.
	TaskKey  string `gorm:"column:task_key" json:"task_key,omitempty"`
	Status   string `gorm:"index;index:idx_tasks_agent_status,priority:2;index:idx_tasks_status_created,priority:1" json:"status"`
	Priority int    `gorm:"default:1;index:idx_tasks_agent_status,priority:3" json:"priority"` // 0=low, 1=normal, 2=high, 3=urgent
	Result   string `json:"result"`
	Error    string `json:"error"`
	// File transfer progress tracking (optimization)
	Progress    int    `json:"progress,omitempty"`    // 0-100 percentage
	TotalBytes  int64  `json:"total_bytes,omitempty"` // total file size
	Transferred int64  `json:"transferred,omitempty"` // bytes transferred so far
	CreatedBy   string `json:"created_by"`            // operator username who created the task
	// Two-man approval tracking: ApprovedBy is the operator that approved a
	// pending_approval task. Enforced to differ from CreatedBy for dangerous
	// task types when security.require_approval_for_dangerous is enabled.
	ApprovedBy string     `json:"approved_by,omitempty"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`
	// Operator collaboration claim (who is working this task in the UI).
	// Separate from ClaimedBy/ClaimedAt, which are the AGENT dispatch claim
	// used by the beacon pipeline — sharing one column corrupted both.
	OperatorClaimedBy string     `json:"operator_claimed_by,omitempty"`
	OperatorClaimedAt *time.Time `json:"operator_claimed_at,omitempty"`
	// ApprovalExpiresAt bounds pending_approval lifetime (auto-reject after
	// ApprovalExpiryDuration). Set at creation for approval-gated tasks.
	ApprovalExpiresAt *time.Time `gorm:"index" json:"approval_expires_at,omitempty"`
	ClaimedBy         string     `gorm:"size:255" json:"claimed_by"`
	ClaimedAt         time.Time  `json:"claimed_at"`
	AcknowledgedAt    *time.Time `gorm:"index" json:"acknowledged_at,omitempty"`
	// DeliveryAttempts counts how many times a stale running task has been
	// requeued without ever being acknowledged; at 3 it is failed outright.
	DeliveryAttempts int `gorm:"default:0" json:"delivery_attempts"`
	// Task callbacks: optional URL to POST results to when task completes
	CallbackURL    string `gorm:"size:1024" json:"callback_url,omitempty"`
	CallbackMethod string `gorm:"size:10;default:'POST'" json:"callback_method,omitempty"`
	CallbackSent   bool   `gorm:"default:false" json:"callback_sent"`
	// LastResultID is the agent-side id of the most recently applied result.
	// It gives durable idempotency for results re-resent after a dropped frame
	// (survives server restarts, unlike the in-memory dedup cache).
	LastResultID string `gorm:"size:64;index" json:"last_result_id,omitempty"`
	// IdempotencyKey is an operator-supplied dedup key: creating a task with
	// a key that already has a live (pending/pending_approval/running) task
	// on the same agent returns the existing task instead of a duplicate.
	// Empty = no dedup. Terminal tasks never block reuse of a key.
	IdempotencyKey string    `gorm:"size:64;index" json:"idempotency_key,omitempty"`
	CreatedAt      time.Time `gorm:"index;index:idx_tasks_agent_created,priority:2;index:idx_tasks_status_created,priority:2;index:idx_tasks_tenant_created,priority:2" json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Agent          Implant   `gorm:"foreignKey:AgentID" json:"-"`
}

// SensitiveTaskTypes are task types whose Command/Data fields carry secrets
// or code payloads: sprayed domain passwords, token_make/lateral-movement
// credentials, shellcode/assembly blobs. Their Command/Data are encrypted at
// rest by the Task BeforeCreate/BeforeUpdate hooks and transparently decrypted
// by AfterFind, mirroring the Result/Error treatment. Operator-facing types
// that stay searchable in the database (shell, ps, upload, download_url) are
// intentionally NOT listed here; they are only wire-encrypted at dispatch.
var SensitiveTaskTypes = map[string]bool{
	"password_spray": true, "cred_check": true, "token_make": true, "lateral": true,
	"mimikatz": true, "creds": true, "dcsync": true,
	"execute_assembly": true, "bof": true, "peloader": true,
	"inject": true, "spawn": true, "shinject": true, "shspawn": true,
	"powerpick": true, "reg_set": true, "clipboard_set": true,
	"wechat_history": true,
	// screen_webrtc_start carries the one-shot signaling token and TURN
	// credentials in Data; encrypt it at rest and on the wire.
	"screen_webrtc_start": true,
}

// SensitiveShellTypes are task types whose Shell field carries a secret
// rather than an interpreter/path/arguments: token_make rides the plaintext
// password in Shell (Command=DOMAIN\\user, Shell=password, Path=logon type).
// These Shell values are encrypted/decrypted alongside Command/Data.
var SensitiveShellTypes = map[string]bool{
	"token_make": true,
}

// AfterFind transparently decrypts task Result/Error (AES-256-GCM, FC2ENC:)
// on every load so callers always see plaintext. DecryptLoot is a no-op for
// legacy plaintext values, preserving backward compatibility with existing
// task rows. This keeps command output (which frequently contains credentials,
// tokens, and file contents) encrypted at rest in the database (H3).
func (t *Task) AfterFind(_ *gorm.DB) error {
	if t.Result != "" {
		if dec, err := crypto.DecryptLoot(t.Result); err == nil {
			t.Result = dec
		}
	}
	if t.Error != "" {
		if dec, err := crypto.DecryptLoot(t.Error); err == nil {
			t.Error = dec
		}
	}
	if SensitiveTaskTypes[t.Type] {
		if t.Command != "" {
			if dec, err := crypto.DecryptLoot(t.Command); err == nil {
				t.Command = dec
			}
		}
		if t.Data != "" {
			if dec, err := crypto.DecryptLoot(t.Data); err == nil {
				t.Data = dec
			}
		}
	}
	if SensitiveShellTypes[t.Type] {
		if t.Shell != "" {
			if dec, err := crypto.DecryptLoot(t.Shell); err == nil {
				t.Shell = dec
			}
		}
	}
	return nil
}

// reencryptField normalizes one sensitive field to active-key ciphertext:
// plaintext is encrypted (failure aborts the write — fail-closed), readable
// ciphertext is re-encrypted (normalizes previous-key rows on every write),
// and unreadable ciphertext is kept as-is (still safe at rest; failing the
// write would wedge status updates on rows the vault can no longer read).
func reencryptField(val *string) error {
	if *val == "" {
		return nil
	}
	if strings.HasPrefix(*val, "FC2ENC:") {
		if dec, err := crypto.DecryptLoot(*val); err == nil {
			*val = dec
			return encryptField(val)
		}
		return nil
	}
	return encryptField(val)
}

// encryptSensitiveFields is the BeforeCreate/BeforeUpdate counterpart of the
// AfterFind decryption. Decrypt-then-encrypt keeps the hook idempotent: an
// already-encrypted in-memory value normalizes back to ciphertext, and a
// freshly built plaintext task is encrypted exactly once.
// Fail-closed: any encryption failure is returned so the write aborts instead
// of persisting secrets as plaintext.
func (t *Task) encryptSensitiveFields() error {
	if SensitiveTaskTypes[t.Type] {
		if err := reencryptField(&t.Command); err != nil {
			return err
		}
		if err := reencryptField(&t.Data); err != nil {
			return err
		}
	}
	if SensitiveShellTypes[t.Type] {
		if err := reencryptField(&t.Shell); err != nil {
			return err
		}
	}
	return nil
}

func (t *Task) BeforeCreate(_ *gorm.DB) error {
	return t.encryptSensitiveFields()
}

func (t *Task) BeforeUpdate(_ *gorm.DB) error {
	return t.encryptSensitiveFields()
}

// EncryptTaskFields encrypts Result/Error for storage. It is applied at the
// explicit DB write sites (beacon results, extc2 results) so the ciphertext
// persisted differs from the in-memory plaintext used for broadcasts,
// callbacks, and credential parsing.
// Fail-closed: returns an error instead of leaving plaintext; callers must
// drop the output (never persist it) on error.
func (t *Task) EncryptTaskFields() error {
	if t.Result != "" {
		enc, err := crypto.EncryptLoot(t.Result)
		if err != nil {
			return err
		}
		t.Result = enc
	}
	if t.Error != "" {
		enc, err := crypto.EncryptLoot(t.Error)
		if err != nil {
			return err
		}
		t.Error = enc
	}
	return nil
}

// AuditLog represents a security audit log entry
type AuditLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	User      string    `json:"user"`                      // username or "system"
	Action    string    `gorm:"index" json:"action"`       // action type: login, logout, command, delete, etc.
	Resource  string    `json:"resource"`                  // affected resource
	AgentID   string    `gorm:"index" json:"agent_id"`     // related agent ID if applicable
	TenantID  uint      `gorm:"index" json:"tenant_id"`    // owning tenant; 0 = legacy/unscoped (visible to all)
	IP        string    `json:"ip"`                        // client IP address
	Success   bool      `json:"success"`                   // whether the action succeeded
	Error     string    `json:"error"`                     // error message if failed
	Details   string    `json:"details"`                   // additional details
	PrevHash  string    `gorm:"size:64" json:"prev_hash"`  // SHA-256 of the previous audit entry (append-only hash chain)
	EntryHash string    `gorm:"size:64" json:"entry_hash"` // SHA-256 hash of this entry (covers all fields)
	CreatedAt time.Time `gorm:"index" json:"created_at"`
}

// Listener represents a C2 listener profile for agents to connect to.
// Supports multiple listeners (different hosts/ports/protocols) like in Cobalt Strike.
//
// Recommended: use "Scheme" for the full wire protocol.
// "Type" is kept for backward compatibility and derived from Scheme.
type Listener struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `gorm:"uniqueIndex;size:128" json:"name"`
	Scheme   string `json:"scheme"` // "http", "https", "tcp", "tls"  (preferred)
	Type     string `json:"type"`   // "http", "tcp", "dns", "icmp" (derived, kept for compat)
	Host     string `json:"host"`   // IP or domain
	Port     int    `json:"port"`
	Protocol string `json:"protocol"` // deprecated alias for Scheme, kept for compat
	Notes    string `json:"notes"`
	Enabled  bool   `json:"enabled"`
	Tags     string `gorm:"size:500;default:''" json:"tags"`
	Color    string `gorm:"size:7;default:''" json:"color"`
	Status   string `gorm:"size:20;default:'running'" json:"status"`

	// DNS-specific: the DNS zone domain (e.g. "c2.example.com") and UDP listen address
	DNSDomain     string `gorm:"size:255" json:"dns_domain"`
	DNSListenAddr string `gorm:"size:255" json:"dns_listen_addr"`

	// ICMP-specific: the bind address (e.g. "0.0.0.0")
	ICMPAddr string `gorm:"size:255" json:"icmp_addr"`

	// Per-listener TLS posture for https/tls listeners. Empty/false values
	// inherit the server-wide beacon settings (server.client_ca_file /
	// server.require_client_cert); the knobs can only strengthen that posture
	// (see listener_tls.go), never relax it.
	ClientCAFile      string `gorm:"size:255" json:"client_ca_file"`
	RequireClientCert bool   `json:"require_client_cert"`
	MinTLSVersion     string `gorm:"size:16" json:"min_tls_version"`

	TenantID uint `gorm:"index" json:"tenant_id"`

	CreatedAt time.Time `gorm:"index" json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// BeforeCreate hook for UUID
func (i *Implant) BeforeCreate(tx *gorm.DB) (err error) {
	if i.ID == "" {
		i.ID = uuid.New().String()
	}
	return nil
}

// encryptField encrypts a single field value using loot encryption.
//
// Failure contract (fail-closed): when encryption is unavailable (key unset /
// misconfigured) an error is returned and the write MUST be aborted by the
// caller — storing the secret in plaintext silently is worse than failing the
// write loudly. Operators notice via the returned error, not log archaeology.
func encryptField(val *string) error {
	if *val == "" {
		return nil
	}
	enc, err := crypto.EncryptLoot(*val)
	if err != nil {
		return fmt.Errorf("vault encryption unavailable, refusing plaintext store: %w", err)
	}
	*val = enc
	return nil
}

// decryptField decrypts a single field value using loot encryption.
//
// Failure contract: values that fail decryption are passed through as-is.
// They are either legacy plaintext rows or rows written under a previous
// loot_key — either way returning an error would make AfterFind fail and
// 404/500 every credential read after a key rotation.
func decryptField(val *string) error {
	if *val == "" {
		return nil
	}
	dec, err := crypto.DecryptLoot(*val)
	if err != nil {
		return nil // passthrough legacy/unencrypted value
	}
	*val = dec
	return nil
}

// BeforeCreate encrypts sensitive fields before inserting into the database.
func (c *CredentialEntry) BeforeCreate(tx *gorm.DB) error {
	if c.TenantID == 0 && c.AgentID != "" {
		var agent Implant
		if err := tx.Select("tenant_id").Where("id = ?", c.AgentID).First(&agent).Error; err == nil {
			c.TenantID = agent.TenantID
		}
	}
	if c.TenantID == 0 {
		var tenant Tenant
		if err := tx.Select("id").Where("name = ?", DefaultTenantName).First(&tenant).Error; err == nil {
			c.TenantID = tenant.ID
		}
	}
	// Default 30-day review horizon for harvested loot (operator can extend
	// per entry in the edit UI). Expired entries surface as "stale" in the
	// lifecycle state machine instead of silently living forever.
	if c.ExpiresAt.IsZero() {
		c.ExpiresAt = time.Now().AddDate(0, 0, 30)
	}
	// The dedup key must digest the plaintext fields, so it is computed
	// before they are encrypted below.
	if c.DedupKey == "" {
		c.DedupKey = ComputeCredDedupKey(c.AgentID, c.Domain, c.Username, c.Password, c.Hash)
	}
	if err := encryptField(&c.Password); err != nil {
		return err
	}
	return encryptField(&c.Hash)
}

// AfterFind decrypts sensitive fields after loading from the database.
func (c *CredentialEntry) AfterFind(tx *gorm.DB) error {
	if err := decryptField(&c.Password); err != nil {
		return err
	}
	return decryptField(&c.Hash)
}

// Webhook headers routinely carry Authorization bearer tokens: encrypt at
// rest like credential secrets. Empty stays empty; legacy plaintext rows
// pass through decryptField untouched, so no backfill migration is needed.
func (w *WebhookConfig) BeforeCreate(tx *gorm.DB) error {
	return encryptField(&w.Headers)
}

// BeforeUpdate encrypts webhook headers before updating the database.
func (w *WebhookConfig) BeforeUpdate(tx *gorm.DB) error {
	return encryptField(&w.Headers)
}

// AfterFind decrypts webhook headers after loading from the database.
func (w *WebhookConfig) AfterFind(tx *gorm.DB) error {
	return decryptField(&w.Headers)
}

// BeforeUpdate encrypts sensitive fields before updating the database.
func (c *CredentialEntry) BeforeUpdate(tx *gorm.DB) error {
	if err := encryptField(&c.Password); err != nil {
		return err
	}
	return encryptField(&c.Hash)
}

func (cc *CloudCred) BeforeCreate(tx *gorm.DB) error {
	if err := encryptField(&cc.Key); err != nil {
		return err
	}
	if err := encryptField(&cc.Value); err != nil {
		return err
	}
	return encryptField(&cc.Extra)
}

func (cc *CloudCred) AfterFind(tx *gorm.DB) error {
	if err := decryptField(&cc.Key); err != nil {
		return err
	}
	if err := decryptField(&cc.Value); err != nil {
		return err
	}
	return decryptField(&cc.Extra)
}

func (cc *CloudCred) BeforeUpdate(tx *gorm.DB) error {
	if err := encryptField(&cc.Key); err != nil {
		return err
	}
	if err := encryptField(&cc.Value); err != nil {
		return err
	}
	return encryptField(&cc.Extra)
}

func (e *ExtC2Channel) BeforeCreate(tx *gorm.DB) error {
	enc, err := crypto.EncryptExtC2(e.BotToken)
	if err != nil {
		return encryptField(&e.BotToken)
	}
	e.BotToken = enc
	return nil
}

func (e *ExtC2Channel) AfterFind(tx *gorm.DB) error {
	dec, err := crypto.DecryptExtC2(e.BotToken)
	if err == nil && dec != e.BotToken {
		e.BotToken = dec
		return nil
	}
	return decryptField(&e.BotToken)
}

func (e *ExtC2Channel) BeforeUpdate(tx *gorm.DB) error {
	enc, err := crypto.EncryptExtC2(e.BotToken)
	if err != nil {
		return encryptField(&e.BotToken)
	}
	e.BotToken = enc
	return nil
}

func (r *Redirector) BeforeCreate(tx *gorm.DB) error {
	if err := encryptField(&r.SSHKey); err != nil {
		return err
	}
	return encryptField(&r.SSHPassword)
}

func (r *Redirector) AfterFind(tx *gorm.DB) error {
	if err := decryptField(&r.SSHKey); err != nil {
		return err
	}
	return decryptField(&r.SSHPassword)
}

func (r *Redirector) BeforeUpdate(tx *gorm.DB) error {
	if err := encryptField(&r.SSHKey); err != nil {
		return err
	}
	return encryptField(&r.SSHPassword)
}

// TokenEntry records a stolen/created Windows token for an agent.
// It is the "Token Vault" -- persisted for replay across sessions.
type TokenEntry struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	AgentID     string    `gorm:"index" json:"agent_id"`
	PID         uint32    `json:"pid"`          // source process PID (0 = make_token)
	ProcessName string    `json:"process_name"` // e.g. lsass.exe, winlogon.exe
	Domain      string    `json:"domain"`       // domain / workgroup
	Username    string    `json:"username"`     // impersonated user
	LogonType   string    `json:"logon_type"`   // e.g. Interactive, Network
	Integrity   string    `json:"integrity"`    // Low / Medium / High / System
	TokenType   string    `json:"token_type"`   // Primary / Impersonation
	Source      string    `json:"source"`       // steal | make_token | duplicate
	Active      bool      `json:"active"`       // currently impersonated on this agent
	Notes       string    `json:"notes"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SocksSession tracks a C2-relayed SOCKS5 proxy tunnel.
// The C2 server opens a local TCP listener; SOCKS5 connections from the operator
// are tunnelled through the Beacon channel to the Agent which dials the target.
type SocksSession struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	AgentID    string    `gorm:"index" json:"agent_id"`
	ListenPort int       `json:"listen_port"` // server-side local port
	Status     string    `json:"status"`      // active | stopped
	BytesIn    int64     `json:"bytes_in"`    // operator 锟?agent
	BytesOut   int64     `json:"bytes_out"`   // agent 锟?operator
	ConnCount  int       `json:"conn_count"`  // total connections served
	Notes      string    `json:"notes"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// User represents an authenticated operator
// ForgeC2 multi-user support with role-based access control
// Roles: "admin" (full control), "user" (standard operator)
type User struct {
	ID                  uint      `gorm:"primaryKey" json:"id"`
	Username            string    `gorm:"uniqueIndex;size:64" json:"username"`
	PasswordHash        string    `json:"-"`
	Role                string    `json:"role"`                   // "admin" or "user"
	TenantID            uint      `gorm:"index" json:"tenant_id"` // multi-tenant isolation (0 = legacy/unscoped)
	IsActive            bool      `json:"is_active"`
	ForcePasswordChange bool      `gorm:"default:false" json:"force_password_change"`
	LastLogin           time.Time `json:"last_login"`
	LastIP              string    `json:"last_ip"`
	LastActivity        time.Time `json:"last_activity"`   // last page request or API call
	ForceLogoutAt       time.Time `json:"force_logout_at"` // set by admin to invalidate all sessions
	LoginAttempts       int       `json:"-"`
	CreatedAt           time.Time `gorm:"index" json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Tenant is a self-contained operator organization. Every operator (User) and
// the implants/tasks they own are scoped to a Tenant so that multiple
// independent teams can share one ForgeC2 deployment without seeing each
// other's assets (P2: multi-tenant isolation).
type Tenant struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"uniqueIndex;size:128" json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// CredentialEntry stores a parsed credential harvested from an agent.
// Auto-populated when "creds" task results arrive, or manually added.
type CredentialEntry struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	TenantID uint   `gorm:"index" json:"tenant_id"`
	AgentID  string `gorm:"index" json:"agent_id"`
	Domain   string `json:"domain"`
	Username string `json:"username"`
	Password string `json:"password"`
	Hash     string `json:"hash"`   // NTLM / SHA etc.
	Source   string `json:"source"` // lsass, sam, mimikatz, manual
	Type     string `json:"type"`   // cleartext, ntlm, aes, kerberos
	Notes    string `json:"notes"`
	Tags     string `json:"tags"` // comma separated tags
	// Lifecycle: VerifyStatus "" (never checked) / "pending" / "valid" / "invalid".
	VerifyStatus   string     `gorm:"size:16;default:'';index" json:"verify_status"`
	LastVerifiedAt *time.Time `json:"last_verified_at,omitempty"`
	ExpiresAt      time.Time  `gorm:"index" json:"expires_at"`
	Confirmed      bool       `json:"confirmed"` // whether credential has been verified
	TaskID         uint       `json:"task_id"`   // originating task (0 = manual)
	// DedupKey is a stable digest of the plaintext credential identity
	// (agent|domain|username|password|hash), computed before encryption at
	// insert. AES-GCM ciphertext is randomized per write and cannot be
	// compared across rows, so dedup scans read this bounded column
	// instead of pulling and decrypting whole rows. Legacy rows are
	// backfilled at startup (backfillCredentialDedupKeys in database.go).
	DedupKey  string    `gorm:"size:64;index" json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ComputeCredDedupKey digests the plaintext credential identity so vault
// dedup can compare a stable column instead of stored (encrypted) values.
// Fields are NUL-joined so no field content can mimic a boundary.
func ComputeCredDedupKey(agentID, domain, username, password, hash string) string {
	sum := sha256.Sum256([]byte(agentID + "\x00" + domain + "\x00" + username + "\x00" + password + "\x00" + hash))
	return hex.EncodeToString(sum[:])
}

// TableName overrides
func (Tenant) TableName() string          { return "tenants" }
func (Implant) TableName() string         { return "implants" }
func (Task) TableName() string            { return "tasks" }
func (AuditLog) TableName() string        { return "audit_logs" }
func (Listener) TableName() string        { return "listeners" }
func (TokenEntry) TableName() string      { return "token_entries" }
func (SocksSession) TableName() string    { return "socks_sessions" }
func (CredentialEntry) TableName() string { return "credential_entries" }

// CredentialUsage is an append-only ledger recording every interaction with a
// vault credential. Lifecycle status (fresh / verified / used / stale) is
// computed at read time from the latest entries, never stored.
type CredentialUsage struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	CredentialID uint      `gorm:"index;not null" json:"credential_id"`
	TaskID       uint      `json:"task_id"` // 0 = manual
	AgentID      string    `json:"agent_id"`
	Action       string    `json:"action"` // spray, lateral, verify, manual
	Result       string    `json:"result"` // ok, fail, locked, unknown
	Detail       string    `json:"detail"`
	Operator     string    `json:"operator"`
	CreatedAt    time.Time `gorm:"index" json:"created_at"`
}

func (CredentialUsage) TableName() string { return "credential_usages" }

// CredentialLifecycle computes the lifecycle status of a vault entry from its
// latest usage record and confirmation state.  The result is never stored;
// callers compute it on read.
//
// Statuses:
//   - fresh    — no usage records, not confirmed
//   - verified — last action was a successful verify or cred_check
//   - used     — last action was a successful spray/lateral
//   - stale    — expired or last action was a failure
func CredentialLifecycle(entry CredentialEntry, latestUsage *CredentialUsage, now time.Time) string {
	if !entry.ExpiresAt.IsZero() && entry.ExpiresAt.Before(now) {
		return "stale"
	}
	if latestUsage == nil {
		if entry.Confirmed {
			return "verified"
		}
		return "fresh"
	}
	if latestUsage.Result == "fail" || latestUsage.Result == "locked" {
		return "stale"
	}
	if latestUsage.Action == "verify" && latestUsage.Result == "ok" {
		return "verified"
	}
	if (latestUsage.Action == "spray" || latestUsage.Action == "lateral") && latestUsage.Result == "ok" {
		return "used"
	}
	if entry.Confirmed {
		return "verified"
	}
	return "fresh"
}

// BuildLog records a build attempt
type BuildLog struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Platform    string    `json:"platform"` // windows, linux
	Format      string    `json:"format"`   // exe, ps1, linux, stager
	C2URL       string    `json:"c2_url"`
	ListenerID  uint      `json:"listener_id"`
	Filename    string    `json:"filename"`
	User        string    `json:"user"`   // operator username
	Status      string    `json:"status"` // success, failed
	Error       string    `json:"error"`
	OutputPath  string    `json:"output_path"`
	ProfileName string    `json:"profile_name,omitempty"`
	ProfileHash string    `json:"profile_hash,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func (BuildLog) TableName() string { return "build_logs" }
func (User) TableName() string     { return "users" }

// ScanResult stores port/service scan results from agents
type ScanResult struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	AgentID   string    `gorm:"index" json:"agent_id"`
	TaskID    uint      `gorm:"index" json:"task_id"`
	TargetIP  string    `json:"target_ip"`
	Port      int       `json:"port"`
	Protocol  string    `json:"protocol"` // tcp, udp
	State     string    `json:"state"`    // open, closed, filtered
	Service   string    `json:"service"`  // http, ssh, smb, etc
	Version   string    `json:"version"`
	Banner    string    `json:"banner"`
	CreatedAt time.Time `json:"created_at"`
}

// NetworkHost stores discovered network hosts
type NetworkHost struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	AgentID   string    `gorm:"index" json:"agent_id"`
	IP        string    `json:"ip"`
	Hostname  string    `json:"hostname"`
	OS        string    `json:"os"`
	Services  string    `json:"services"` // JSON array of open ports/services
	LastSeen  time.Time `json:"last_seen"`
	CreatedAt time.Time `json:"created_at"`
}

// CommandTemplate stores reusable command templates
type CommandTemplate struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `json:"name"`
	Category    string    `json:"category"` // recon, privesc, lateral, exfil
	Command     string    `json:"command"`
	Description string    `json:"description"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// BOFFile stores uploaded BOF (.o) files for reuse across agents
type BOFFile struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"uniqueIndex;size:256" json:"name"`
	Data        []byte    `json:"-"`
	Size        int64     `json:"size"`
	Description string    `json:"description"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (BOFFile) TableName() string { return "bof_files" }

// BOFLibrary (bof_library) was removed. No handler ever read or wrote it; the
// live BOF store is BOFFile, which backs the whole BOF page and automation.

// ServerConfig stores key-value config for automation, events, etc.
type ServerConfig struct {
	Key       string    `gorm:"primaryKey;size:255" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (ServerConfig) TableName() string { return "server_configs" }

// WebhookConfig stores webhook endpoint configuration
type WebhookConfig struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Name      string `gorm:"size:255;not null" json:"name"`
	URL       string `gorm:"size:1024;not null" json:"url"`
	EventType string `gorm:"size:255;not null" json:"event_type"`
	Method    string `gorm:"size:16;default:'POST'" json:"method"`
	Headers   string `gorm:"type:text" json:"headers"`
	Enabled   bool   `gorm:"default:true" json:"enabled"`
	// TenantID contains webhook delivery: a webhook must never receive
	// another tenant's event data (payloads carry hostnames and findings).
	TenantID uint `gorm:"index" json:"tenant_id"`
	// EventCount/LastTrigger track real deliveries (incremented only after a
	// webhook push returns 2xx). Formerly fabricated as constant 0/"".
	EventCount  int64      `gorm:"default:0" json:"event_count"`
	LastTrigger *time.Time `json:"last_trigger"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (WebhookConfig) TableName() string { return "webhook_configs" }

// Plugin stores registered plugin metadata
type Plugin struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Name            string    `gorm:"size:255;not null;uniqueIndex" json:"name"`
	Version         string    `gorm:"size:64" json:"version"`
	Description     string    `gorm:"size:1024" json:"description"`
	Author          string    `gorm:"size:255" json:"author"`
	Type            string    `gorm:"size:64" json:"type"` // "hook", "command", "report"
	Enabled         bool      `gorm:"default:true" json:"enabled"`
	Config          string    `gorm:"type:text" json:"config"`
	Category        string    `gorm:"size:64" json:"category"` // "recon", "privesc", "lateral", "report", "automation", "utility"
	Homepage        string    `gorm:"size:512" json:"homepage"`
	License         string    `gorm:"size:64" json:"license"`
	Dependencies    string    `gorm:"type:text" json:"dependencies"` // JSON array of dependency names
	Tags            string    `gorm:"size:512" json:"tags"`          // comma separated
	RatingOverall   float64   `gorm:"-" json:"rating_overall"`
	RatingCount     int       `gorm:"-" json:"rating_count"`
	UpdateAvailable bool      `gorm:"-" json:"update_available"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (Plugin) TableName() string { return "plugins" }

// PluginReview stores plugin ratings and comments
type PluginReview struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	PluginID  uint      `gorm:"index" json:"plugin_id"`
	UserID    uint      `json:"user_id"`
	Username  string    `gorm:"size:64" json:"username"`
	Rating    int       `gorm:"not null" json:"rating"` // 1-5
	Comment   string    `gorm:"type:text" json:"comment"`
	CreatedAt time.Time `json:"created_at"`
}

func (PluginReview) TableName() string { return "plugin_reviews" }

// PluginDependency stores plugin dependency relationships
type PluginDependency struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	PluginID        uint      `gorm:"index" json:"plugin_id"`
	DependencyID    uint      `gorm:"index" json:"dependency_id"`
	Dependency      Plugin    `gorm:"foreignKey:DependencyID" json:"-"`
	RequiredVersion string    `gorm:"size:64" json:"required_version"`
	Optional        bool      `gorm:"default:false" json:"optional"`
	CreatedAt       time.Time `json:"created_at"`
}

func (PluginDependency) TableName() string { return "plugin_dependencies" }

// PluginUpdateStatus tracks plugin update information
type PluginUpdateStatus struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	PluginID        uint      `gorm:"uniqueIndex" json:"plugin_id"`
	LatestVersion   string    `gorm:"size:64" json:"latest_version"`
	UpdateAvailable bool      `json:"update_available"`
	UpdateURL       string    `gorm:"size:512" json:"update_url"`
	ReleaseNotes    string    `gorm:"type:text" json:"release_notes"`
	LastCheckedAt   time.Time `json:"last_checked_at"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (PluginUpdateStatus) TableName() string { return "plugin_update_status" }

// CommandMacro stores a recorded sequence of shell commands that can be
// replayed against one or more agents. Steps is a JSON array of
// [{"command":"whoami","delay_ms":500,"wait":true,"timeout_s":60}, ...].
type CommandMacro struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"uniqueIndex;size:128" json:"name"`
	Description string    `json:"description"`
	Steps       string    `gorm:"type:text" json:"steps"`
	CreatedBy   string    `json:"created_by"`
	TenantID    uint      `gorm:"index" json:"tenant_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (CommandMacro) TableName() string { return "command_macros" }

// MacroRun tracks one execution of a macro against a single agent so the UI
// can show live progress and a per-step result log.
type MacroRun struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	MacroID     uint   `json:"macro_id"`
	MacroName   string `gorm:"size:128" json:"macro_name"`
	AgentID     string `gorm:"index;size:64" json:"agent_id"`
	Status      string `gorm:"size:16" json:"status"` // running, completed, failed, stopped
	CurrentStep int    `json:"current_step"`
	TotalSteps  int    `json:"total_steps"`
	Log         string `gorm:"type:text" json:"log"` // JSON array of step entries
	CreatedBy   string `json:"created_by"`
	// TenantID mirrors CommandMacro.TenantID so macro-run reads stay
	// tenant-scoped. Runs inherit it from the parent macro.
	TenantID   uint       `gorm:"index" json:"tenant_id"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

func (MacroRun) TableName() string { return "macro_runs" }

// NotificationRoute delivers matching-severity notifications to an external
// channel (Discord, Telegram, or a generic webhook).
type NotificationRoute struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:128" json:"name"`
	Channel     string    `gorm:"size:32" json:"channel"`                     // discord, telegram, webhook
	Target      string    `gorm:"size:512" json:"target"`                     // discord/generic: webhook URL; telegram: chat id
	Secret      string    `gorm:"size:512" json:"secret"`                     // telegram bot token
	MinSeverity string    `gorm:"size:16;default:'info'" json:"min_severity"` // info, warning, critical
	Enabled     bool      `gorm:"default:true" json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (NotificationRoute) TableName() string { return "notification_routes" }

// SavedView stores a per-user, per-page snapshot of list filters/sorting so
// operators can switch between named views (e.g. "online windows boxes")
// without re-applying filters by hand.
type SavedView struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index;not null" json:"user_id"`
	Page      string    `gorm:"size:64;index" json:"page"` // agents, tasks, ...
	Name      string    `gorm:"size:128" json:"name"`
	State     string    `gorm:"type:text" json:"state"` // JSON object of filter values
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (SavedView) TableName() string { return "saved_views" }

// OneShotTask is a single future-dated command dispatch ("at 02:00 run X on
// agent Y"). Unlike AutomationRule this is one-shot and operator-facing.
type OneShotTask struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	AgentID    string     `gorm:"index;size:36" json:"agent_id"`
	Type       string     `gorm:"size:50;default:'shell'" json:"type"`
	Command    string     `gorm:"type:text" json:"command"`
	RunAt      time.Time  `gorm:"index" json:"run_at"`
	Status     string     `gorm:"size:16;default:'pending'" json:"status"` // pending, dispatching, done, cancelled, error
	TaskID     uint       `json:"task_id"`
	CreatedBy  string     `gorm:"size:100" json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func (OneShotTask) TableName() string { return "one_shot_tasks" }

func (ScanResult) TableName() string      { return "scan_results" }
func (NetworkHost) TableName() string     { return "network_hosts" }
func (CommandTemplate) TableName() string { return "command_templates" }

type AutomationRule struct {
	ID         string    `gorm:"primaryKey;size:255" json:"id"`
	Name       string    `gorm:"size:255;not null" json:"name"`
	Enabled    bool      `gorm:"default:true" json:"enabled"`
	EventType  string    `gorm:"size:255;not null" json:"event_type"`
	Conditions string    `gorm:"type:text" json:"conditions"`
	Actions    string    `gorm:"type:text" json:"actions"`
	Schedule   string    `gorm:"size:255;default:''" json:"schedule"` // cron / interval schedule for event_type="schedule"
	AgentID    string    `gorm:"size:36;default:''" json:"agent_id"`  // target agent for scheduled dispatch
	TaskType   string    `gorm:"size:50;default:''" json:"task_type"`
	Command    string    `gorm:"type:text" json:"command"`
	Params     string    `gorm:"type:text" json:"params"`
	LastRun    time.Time `json:"last_run"`
	NextRun    time.Time `json:"next_run"`
	RunCount   int       `gorm:"default:0" json:"run_count"`
	// Event-trigger throttle: minimum seconds between two firings of this
	// rule. Persisted so a restart does not reset the cooldown window.
	CooldownSeconds int       `gorm:"default:0" json:"cooldown_seconds"`
	LastTriggered   time.Time `json:"last_triggered"`
	CreatedBy       string    `gorm:"size:100" json:"created_by"`
	TenantID        uint      `gorm:"index" json:"tenant_id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (AutomationRule) TableName() string { return "automation_rules" }

type AlertRule struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// TenantID scopes a rule to the tenant that created it. Alerts carry the
	// source agent's hostname, so an unscoped rule listing disclosed another
	// tenant's asset names. 0 = legacy/unscoped, the convention tenantScope
	// already uses.
	TenantID    uint      `gorm:"index" json:"-"`
	Name        string    `gorm:"size:255;not null" json:"name"`
	Type        string    `gorm:"size:100;not null" json:"type"` // agent_offline, cpu_high, memory_high, disk_high, credential_found, agent_online
	Threshold   float64   `json:"threshold"`                     // Threshold (percentage or seconds)
	Enabled     bool      `gorm:"default:true" json:"enabled"`
	Description string    `gorm:"type:text" json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (AlertRule) TableName() string { return "alert_rules" }

type Alert struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// TenantID mirrors the alerting agent's tenant so one tenant's alert feed
	// never shows another's hostnames or severities. Stamped by the collector
	// at creation time; 0 = legacy/unscoped.
	TenantID   uint      `gorm:"index" json:"-"`
	RuleID     uint      `json:"rule_id"`
	Rule       AlertRule `gorm:"foreignKey:RuleID" json:"rule"`
	Type       string    `gorm:"size:100;not null" json:"type"`
	Severity   string    `gorm:"size:20;default:'warning'" json:"severity"` // critical, warning, info
	Title      string    `gorm:"size:255;not null" json:"title"`
	Message    string    `gorm:"type:text" json:"message"`
	Source     string    `json:"source"`                                 // agent_id, system, etc.
	SourceName string    `json:"source_name"`                            // hostname, etc.
	Status     string    `gorm:"size:20;default:'active'" json:"status"` // active, acknowledged, resolved
	Details    string    `gorm:"type:text" json:"details"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (Alert) TableName() string { return "alerts" }

type SystemMetric struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	CPULoad     float64   `json:"cpu_load"`
	MemoryUsed  float64   `json:"memory_used"`
	MemoryTotal float64   `json:"memory_total"`
	DiskUsed    float64   `json:"disk_used"`
	DiskTotal   float64   `json:"disk_total"`
	Hostname    string    `json:"hostname"`
	CreatedAt   time.Time `json:"created_at"`
}

func (SystemMetric) TableName() string { return "system_metrics" }

type GeneratedReport struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:255" json:"name"`
	Template  string    `gorm:"size:50" json:"template"`
	Format    string    `gorm:"size:10;default:html" json:"format"`
	Content   string    `gorm:"type:text" json:"content"`
	Sections  string    `gorm:"type:text" json:"sections"`
	CreatedAt time.Time `json:"created_at"`
}

func (GeneratedReport) TableName() string { return "generated_reports" }

// RolePermission (role_permissions) was removed. It was write-only: seeded at
// boot and never read, because every permission check resolves from the
// in-memory RolePermissionsMap plus the custom_roles table. The table is
// dropped by a migration for existing deployments.

func GetPermissionsForRole(role string) []string {
	if perms, ok := RolePermissionsMap[role]; ok {
		return perms
	}
	return []string{}
}

func RoleHasPermission(role, permission string) bool {
	// Check built-in roles first
	if perms, ok := RolePermissionsMap[role]; ok {
		for _, p := range perms {
			if p == permission {
				return true
			}
		}
	}
	return false
}

// RoleHasPermissionDB checks both built-in and custom DB-backed roles.
// Requires a database handle to query custom_roles table.
func RoleHasPermissionDB(database interface {
	Where(interface{}, ...interface{}) *gorm.DB
}, role, permission string) bool {
	if RoleHasPermission(role, permission) {
		return true
	}
	var customRole CustomRole
	if database.Where("name = ?", role).First(&customRole).Error != nil {
		return false
	}
	perms := parsePermissionJSON(customRole.Permissions)
	for _, p := range perms {
		if p == permission {
			return true
		}
	}
	return false
}

func parsePermissionJSON(raw string) []string {
	if raw == "" {
		return nil
	}
	var perms []string
	if err := json.Unmarshal([]byte(raw), &perms); err != nil {
		return nil
	}
	return perms
}

func GetAllRoles() []string {
	return []string{RoleAdmin, RoleUser}
}

func GetAllPermissions() []string {
	return []string{
		PermAgentsRead, PermAgentsWrite, PermAgentsDelete,
		PermListenersRead, PermListenersWrite, PermListenersDelete,
		PermTasksRead, PermTasksWrite, PermTasksDelete,
		PermCredsRead, PermCredsWrite, PermCredsDelete,
		PermFilesRead, PermFilesWrite,
		PermUsersRead, PermUsersWrite, PermUsersDelete,
		PermSettingsRead, PermSettingsWrite,
		PermAuditRead,
		PermGroupsRead, PermGroupsWrite,
		PermPluginsRead, PermPluginsWrite, PermPluginsExecute, PermPluginsDelete,
		PermRolesRead, PermRolesWrite,
		PermCampaignsRead, PermCampaignsWrite,
		PermOpsecRead, PermOpsecWrite,
		PermIntelRead, PermIntelWrite,
		PermAutomationRead, PermAutomationWrite,
		PermNotificationsRead, PermNotificationsWrite,
		PermAIUse, PermAIConfigure, PermAIKnowledgeManage,
	}
}

type MeshPeer struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	AgentID   string    `gorm:"index;size:255" json:"agent_id"`
	PeerID    string    `gorm:"size:255" json:"peer_id"`
	PeerAddr  string    `gorm:"size:255" json:"peer_addr"`
	Latency   int       `json:"latency"` // milliseconds
	LastSeen  time.Time `json:"last_seen"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (MeshPeer) TableName() string { return "mesh_peers" }

type BloodHoundResult struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	AgentID          string    `gorm:"index" json:"agent_id"`
	TaskID           uint      `json:"task_id"`
	CollectionMethod string    `json:"collection_method"`
	FilePath         string    `json:"file_path"` // path to saved ZIP on server
	JSONPath         string    `json:"json_path"` // path to parsed JSON output (optional)
	Summary          string    `json:"summary"`
	UserCount        int       `json:"user_count"`
	ComputerCount    int       `json:"computer_count"`
	GroupCount       int       `json:"group_count"`
	SessionCount     int       `json:"session_count"`
	DomainAdminCount int       `json:"domain_admin_count"`
	SPNCount         int       `json:"spn_count"`
	CreatedAt        time.Time `gorm:"index" json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (BloodHoundResult) TableName() string { return "bloodhound_results" }

// BloodHoundFile (bloodhound_files) was removed. No handler ever read or
// wrote it; BloodHoundResult is the live model and keeps the uploaded ZIPs on
// the filesystem under data/bloodhound/.

type Campaign struct {
	ID          string `gorm:"primaryKey;size:36" json:"id"`
	Name        string `gorm:"size:256;not null" json:"name"`
	Description string `gorm:"size:1024" json:"description"`
	Status      string `gorm:"size:32;default:active" json:"status"` // active, completed, archived
	// TenantID scopes a campaign to the operator that created it. Without it a
	// campaign row (and every kill-chain task it seeds) is visible to all
	// tenants. 0 = legacy/unscoped, the convention tenantScope already uses.
	TenantID  uint      `gorm:"index" json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Agents    []Implant `gorm:"many2many:campaign_agents;foreignKey:ID;joinForeignKey:campaign_id;References:ID;joinReferences:agent_id" json:"agents,omitempty"`
}

func (Campaign) TableName() string { return "campaigns" }

// CampaignAgent's explicit struct was removed. The campaign_agents table stays
// and is still live: it is maintained implicitly through
// Campaign.Agents []Implant many2many (Preload and Association("Agents")), which
// GORM handles without a named join model. Naming the join struct here had no
// reader or writer.

type OpsecHistory struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	AgentID   string    `gorm:"index" json:"agent_id"`
	TaskType  string    `json:"task_type"`
	RuleName  string    `json:"rule_name"`
	Allowed   bool      `json:"allowed"`
	Message   string    `json:"message"`
	RiskLevel int       `json:"risk_level"`
	Username  string    `json:"username"`
	Hostname  string    `json:"hostname"`
	CreatedAt time.Time `json:"created_at"`
}

func (OpsecHistory) TableName() string { return "opsec_history" }

type OpsecRule struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	Name          string    `gorm:"uniqueIndex;size:128" json:"name"`
	Description   string    `gorm:"size:512" json:"description"`
	RiskLevel     int       `json:"risk_level"`                  // 1=Low, 2=Medium, 3=High, 4=Critical
	DefaultAction int       `json:"default_action"`              // 0=Block, 1=Warn, 2=Bypass
	CheckType     string    `gorm:"size:64" json:"check_type"`   // maps to compiled Go check function
	TaskTypes     string    `gorm:"type:text" json:"task_types"` // comma-separated task type filter
	Conditions    string    `gorm:"type:text" json:"conditions"` // JSON condition rules
	Enabled       bool      `gorm:"default:true" json:"enabled"`
	CreatedBy     string    `gorm:"size:64" json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (OpsecRule) TableName() string { return "opsec_rules" }

type CircuitBreakerConfig struct {
	ID                 uint `gorm:"primaryKey" json:"id"`
	FailureThreshold   int  `gorm:"default:3" json:"failure_threshold"`
	CooldownSeconds    int  `gorm:"default:300" json:"cooldown_seconds"`
	HalfOpenMaxReqs    int  `gorm:"default:3" json:"half_open_max_reqs"`
	HealthCheckSeconds int  `gorm:"default:60" json:"health_check_seconds"`
}

func (CircuitBreakerConfig) TableName() string { return "circuit_breaker_configs" }

type CircuitBreakerEvent struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	ListenerID string    `gorm:"index" json:"listener_id"`
	OldState   string    `json:"old_state"`
	NewState   string    `json:"new_state"`
	Reason     string    `json:"reason"`
	CreatedAt  time.Time `json:"created_at"`
}

func (CircuitBreakerEvent) TableName() string { return "circuit_breaker_events" }

type CustomRole struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"uniqueIndex;size:32" json:"name"`
	Description string    `gorm:"size:256" json:"description"`
	Permissions string    `gorm:"type:text" json:"-"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (CustomRole) TableName() string { return "custom_roles" }

type PhishingTemplate struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:255;not null" json:"name"`
	Subject   string    `gorm:"size:512" json:"subject"`
	Body      string    `gorm:"type:text" json:"body"`
	FromName  string    `gorm:"size:255" json:"from_name"`
	FromEmail string    `gorm:"size:255" json:"from_email"`
	Type      string    `gorm:"size:50;default:html" json:"type"`
	CreatedBy string    `gorm:"size:255" json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (PhishingTemplate) TableName() string { return "phishing_templates" }

type PhishingCampaign struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:255;not null" json:"name"`
	TemplateID  uint      `json:"template_id"`
	TargetList  string    `gorm:"type:text" json:"target_list"`
	SMTPHost    string    `gorm:"size:255" json:"smtp_host"`
	SMTPPort    int       `json:"smtp_port"`
	SMTPUser    string    `gorm:"size:255" json:"smtp_user"`
	SMTPPass    string    `gorm:"size:255" json:"smtp_pass"`
	Status      string    `gorm:"size:50;default:draft" json:"status"`
	SentCount   int       `json:"sent_count"`
	FailedCount int       `json:"failed_count"`
	OpenCount   int       `json:"open_count"`
	CredCount   int       `json:"cred_count"`
	CreatedBy   string    `gorm:"size:255" json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (PhishingCampaign) TableName() string { return "phishing_campaigns" }

func (c *PhishingCampaign) BeforeCreate(tx *gorm.DB) error {
	return encryptField(&c.SMTPPass)
}

func (c *PhishingCampaign) AfterFind(tx *gorm.DB) error {
	return decryptField(&c.SMTPPass)
}

func (c *PhishingCampaign) BeforeUpdate(tx *gorm.DB) error {
	return encryptField(&c.SMTPPass)
}

type PhishingEvent struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	CampaignID uint      `gorm:"index" json:"campaign_id"`
	Token      string    `gorm:"size:255;index" json:"token"`
	Email      string    `json:"email"`
	EventType  string    `gorm:"size:50" json:"event_type"`
	Payload    string    `gorm:"type:text" json:"payload"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
}

func (PhishingEvent) TableName() string { return "phishing_events" }

func (e *PhishingEvent) BeforeCreate(tx *gorm.DB) error {
	return encryptField(&e.Payload)
}

func (e *PhishingEvent) AfterFind(tx *gorm.DB) error {
	return decryptField(&e.Payload)
}

func (e *PhishingEvent) BeforeUpdate(tx *gorm.DB) error {
	return encryptField(&e.Payload)
}

type AgentTag struct {
	ID        string     `gorm:"primaryKey;size:36" json:"id"`
	Name      string     `gorm:"uniqueIndex;size:100;not null" json:"name"`
	Color     string     `gorm:"size:7;default:#3498db" json:"color"` // hex color
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	Agents    []*Implant `gorm:"many2many:agent_tag_assignments;" json:"agents,omitempty"`
}

type AgentTagAssignment struct {
	AgentTagID string    `gorm:"primaryKey;size:36" json:"tag_id"`
	ImplantID  string    `gorm:"primaryKey;size:36" json:"agent_id"`
	CreatedAt  time.Time `json:"created_at"`
}

func (AgentTag) TableName() string           { return "agent_tags" }
func (AgentTagAssignment) TableName() string { return "agent_tag_assignments" }

// AutoTagRule defines a rule to automatically apply tags to agents.
type AutoTagRule struct {
	ID        string    `gorm:"primaryKey;size:36" json:"id"`
	Name      string    `gorm:"size:100;not null" json:"name"`
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Condition string    `gorm:"type:text;not null" json:"condition"`
	TagID     string    `gorm:"size:36;not null" json:"tag_id"`
	Tag       *AgentTag `gorm:"foreignKey:TagID" json:"tag,omitempty"`
	Priority  int       `gorm:"default:0" json:"priority"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Notification struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Type      string    `gorm:"size:50;not null;index" json:"type"`
	Title     string    `gorm:"size:255" json:"title"`
	Message   string    `gorm:"type:text" json:"message"`
	AgentID   string    `gorm:"size:36;index" json:"agent_id"`
	TaskID    uint      `json:"task_id,omitempty"`
	Severity  string    `gorm:"size:20;default:'info'" json:"severity"`
	Read      bool      `gorm:"default:false;index" json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

func (Notification) TableName() string { return "notifications" }

// AgentGroup — hierarchical agent grouping (parent_id for nesting)
type AgentGroup struct {
	ID          string        `gorm:"primaryKey;size:36" json:"id"`
	Name        string        `gorm:"size:200;not null" json:"name"`
	Description string        `gorm:"size:500" json:"description"`
	Color       string        `gorm:"size:7;default:#2ecc71" json:"color"`
	ParentID    *string       `gorm:"size:36;index" json:"parent_id"`
	Parent      *AgentGroup   `gorm:"foreignKey:ParentID" json:"parent,omitempty"`
	Children    []*AgentGroup `gorm:"foreignKey:ParentID" json:"children,omitempty"`
	Agents      []*Implant    `gorm:"many2many:agent_group_assignments;" json:"agents,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

func (AgentGroup) TableName() string { return "agent_groups" }

type AgentGroupAssignment struct {
	AgentGroupID string    `gorm:"primaryKey;size:36" json:"group_id"`
	ImplantID    string    `gorm:"primaryKey;size:36" json:"agent_id"`
	CreatedAt    time.Time `json:"created_at"`
}

func (AgentGroupAssignment) TableName() string { return "agent_group_assignments" }

// Workflow — multi-step batch command pipeline
type Workflow struct {
	ID          string         `gorm:"primaryKey;size:36" json:"id"`
	Name        string         `gorm:"size:200;not null" json:"name"`
	Description string         `gorm:"size:1000" json:"description"`
	Enabled     bool           `gorm:"default:true" json:"enabled"`
	ScopeType   string         `gorm:"size:20;default:'all'" json:"scope_type"` // "all", "tags", "groups", "agents"
	ScopeIDs    string         `gorm:"type:text" json:"scope_ids"`              // JSON array of IDs
	Steps       []WorkflowStep `gorm:"foreignKey:WorkflowID" json:"steps,omitempty"`
	CreatedBy   string         `gorm:"size:100" json:"created_by"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

func (Workflow) TableName() string { return "workflows" }

type WorkflowStep struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	WorkflowID    string    `gorm:"size:36;index;not null" json:"workflow_id"`
	StepOrder     int       `gorm:"not null" json:"step_order"`
	TaskType      string    `gorm:"size:50;not null" json:"task_type"`
	Command       string    `gorm:"type:text" json:"command"`
	Shell         string    `gorm:"size:20;default:'cmd'" json:"shell"`
	TimeoutSec    int       `gorm:"default:60" json:"timeout_sec"`
	RepeatCount   int       `gorm:"default:0" json:"repeat_count"`
	RepeatDelay   int       `gorm:"default:0" json:"repeat_delay"`
	StopOnFailure bool      `gorm:"default:true" json:"stop_on_failure"`
	ConditionExpr string    `gorm:"type:text" json:"condition_expr"` // JSON condition for previous step output
	Condition     string    `gorm:"type:text" json:"condition"`      // condition expression e.g. "contains('success')"
	OnSuccess     string    `gorm:"size:255" json:"on_success"`      // "continue", or step ID to jump to
	OnFailure     string    `gorm:"size:255" json:"on_failure"`      // "abort", "continue", or step ID to jump to
	CreatedAt     time.Time `json:"created_at"`
}

func (WorkflowStep) TableName() string { return "workflow_steps" }

// ExecutionLog records a single step execution within a workflow run.
// Rows are grouped by ExecutionID (one row per step), replacing the former
// workflow_executions + workflow_step_logs pair with a single simple table.
type ExecutionLog struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	ExecutionID  string     `gorm:"size:36;index;not null" json:"execution_id"`
	WorkflowID   string     `gorm:"size:36;index;not null" json:"workflow_id"`
	WorkflowName string     `gorm:"size:200" json:"workflow_name"`
	AgentID      string     `gorm:"size:100;index" json:"agent_id"`
	AgentHost    string     `gorm:"size:255" json:"agent_host,omitempty"`
	StepOrder    int        `json:"step_order"`
	TaskType     string     `gorm:"size:50" json:"task_type"`
	Command      string     `gorm:"type:text" json:"command"`
	TaskID       uint       `json:"task_id"`
	Status       string     `gorm:"size:20" json:"status"` // running/completed/aborted/failed
	Result       string     `gorm:"type:text" json:"result"`
	BranchAction string     `gorm:"size:50" json:"branch_action,omitempty"`
	BranchTarget string     `gorm:"size:255" json:"branch_target,omitempty"`
	ErrorMsg     string     `gorm:"type:text" json:"error_msg,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	CreatedAt    time.Time  `gorm:"index" json:"created_at"`
}

func (ExecutionLog) TableName() string { return "execution_logs" }

// ChatMessage — multi-operator chat message
type ChatMessage struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Username  string    `gorm:"size:100;not null" json:"username"`
	Message   string    `gorm:"type:text;not null" json:"message"`
	Channel   string    `gorm:"size:50;default:'general'" json:"channel"`
	CreatedAt time.Time `json:"created_at"`
}

func (ChatMessage) TableName() string { return "chat_messages" }

// AIChatSession — a persisted AI assistant conversation
type AIChatSession struct {
	ID              uint   `gorm:"primaryKey" json:"id"`
	TenantID        uint   `gorm:"index" json:"tenant_id"`
	OwnerID         uint   `gorm:"index" json:"owner_id"`
	Owner           string `gorm:"size:100;index" json:"owner"`
	Title           string `gorm:"size:255;not null;default:'New Chat'" json:"title"`
	ProfileID       *uint  `gorm:"index" json:"profile_id,omitempty"`
	ParentSessionID *uint  `gorm:"index" json:"parent_session_id,omitempty"`
	ForkMessageID   *uint  `gorm:"index" json:"fork_message_id,omitempty"`
	ContextAgentID  string `gorm:"size:64;index" json:"context_agent_id,omitempty"`
	WritePolicy     string `gorm:"size:32;not null;default:'approval'" json:"write_policy"`
	Draft           string `gorm:"type:text" json:"draft,omitempty"`
	// Summary is a rolling LLM-compressed digest of older turns so long
	// engagements survive trimConversationHistory truncation. SummaryUpToID
	// watermarks the newest AIChatMessage ID folded into Summary.
	Summary       string    `gorm:"type:text" json:"-"`
	SummaryUpToID uint      `gorm:"default:0" json:"-"`
	Pinned        bool      `gorm:"index;default:false" json:"pinned"`
	Archived      bool      `gorm:"index;default:false" json:"archived"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (AIChatSession) TableName() string { return "ai_chat_sessions" }

// AIChatMessage — a single message within an AI conversation
type AIChatMessage struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	SessionID uint      `gorm:"index;not null" json:"session_id"`
	RunID     string    `gorm:"size:36;index" json:"run_id,omitempty"`
	ClientID  string    `gorm:"size:64;index" json:"client_id,omitempty"`
	Role      string    `gorm:"size:20;not null" json:"role"` // user | assistant | tool
	Content   string    `gorm:"type:text;not null" json:"content"`
	ToolName  string    `gorm:"size:100" json:"tool_name,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (AIChatMessage) TableName() string { return "ai_chat_messages" }

// AIProviderProfile is an administrator-managed provider configuration. APIKey
// is encrypted at rest and is never included in public handler responses.
type AIProviderProfile struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	TenantID          uint       `gorm:"index" json:"tenant_id"`
	Name              string     `gorm:"size:120;not null" json:"name"`
	Provider          string     `gorm:"size:32;not null" json:"provider"`
	Model             string     `gorm:"size:200;not null" json:"model"`
	Endpoint          string     `gorm:"size:2048" json:"endpoint,omitempty"`
	APIKey            string     `gorm:"type:text" json:"-"`
	ContextLimit      int        `gorm:"default:48000" json:"context_limit"`
	OutputLimit       int        `gorm:"default:4096" json:"output_limit"`
	SupportsReasoning bool       `gorm:"default:false" json:"supports_reasoning"`
	SupportsTools     bool       `gorm:"default:true" json:"supports_tools"`
	Enabled           bool       `gorm:"index;default:true" json:"enabled"`
	IsDefault         bool       `gorm:"index;default:false" json:"is_default"`
	LastHealthStatus  string     `gorm:"size:32" json:"last_health_status,omitempty"`
	LastHealthError   string     `gorm:"size:500" json:"last_health_error,omitempty"`
	LastHealthLatency int64      `json:"last_health_latency_ms,omitempty"`
	LastCheckedAt     *time.Time `json:"last_checked_at,omitempty"`
	CreatedBy         string     `gorm:"size:100" json:"created_by"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func (AIProviderProfile) TableName() string { return "ai_provider_profiles" }

// AIChatRun is a durable background generation. Its event stream is replayable
// after browser disconnects without re-executing tools.
type AIChatRun struct {
	ID               string     `gorm:"primaryKey;size:36" json:"id"`
	TenantID         uint       `gorm:"index;uniqueIndex:idx_ai_run_idempotency;not null" json:"tenant_id"`
	OwnerID          uint       `gorm:"index;uniqueIndex:idx_ai_run_idempotency;not null" json:"owner_id"`
	Owner            string     `gorm:"size:100;index;not null" json:"owner"`
	SessionID        uint       `gorm:"index;not null" json:"session_id"`
	ProfileID        *uint      `gorm:"index" json:"profile_id,omitempty"`
	Provider         string     `gorm:"size:32" json:"provider"`
	Model            string     `gorm:"size:200" json:"model"`
	IdempotencyKey   string     `gorm:"size:100;uniqueIndex:idx_ai_run_idempotency" json:"-"`
	Status           string     `gorm:"size:32;index;not null" json:"status"`
	Input            string     `gorm:"type:text" json:"-"`
	ContextAgentID   string     `gorm:"size:64;index" json:"context_agent_id,omitempty"`
	LastEventSeq     int64      `gorm:"default:0" json:"last_event_seq"`
	PromptTokens     int        `gorm:"default:0" json:"prompt_tokens"`
	CompletionTokens int        `gorm:"default:0" json:"completion_tokens"`
	ErrorCode        string     `gorm:"size:64" json:"error_code,omitempty"`
	ErrorMessage     string     `gorm:"type:text" json:"error_message,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	CreatedAt        time.Time  `gorm:"index" json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func (AIChatRun) TableName() string { return "ai_chat_runs" }

type AIChatRunEvent struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	RunID     string    `gorm:"uniqueIndex:idx_ai_run_seq;size:36;not null" json:"run_id"`
	Sequence  int64     `gorm:"uniqueIndex:idx_ai_run_seq;not null" json:"sequence"`
	Type      string    `gorm:"size:32;not null" json:"type"`
	Payload   string    `gorm:"type:text" json:"payload"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
}

func (AIChatRunEvent) TableName() string { return "ai_chat_run_events" }

type AIExecutionIntent struct {
	ID              string     `gorm:"primaryKey;size:36" json:"id"`
	TenantID        uint       `gorm:"index;not null" json:"tenant_id"`
	OwnerID         uint       `gorm:"index;not null" json:"owner_id"`
	Owner           string     `gorm:"size:100;index;not null" json:"owner"`
	SessionID       uint       `gorm:"index;not null" json:"session_id"`
	RunID           string     `gorm:"size:36;index;not null" json:"run_id"`
	ToolName        string     `gorm:"size:100;not null" json:"tool_name"`
	Risk            string     `gorm:"size:32;not null" json:"risk"`
	RequiredPerm    string     `gorm:"size:64" json:"required_permission,omitempty"`
	TargetSummary   string     `gorm:"size:500" json:"target_summary"`
	Arguments       string     `gorm:"type:text" json:"-"`
	ArgumentsDigest string     `gorm:"size:500" json:"arguments_summary"`
	Result          string     `gorm:"type:text" json:"-"`
	ResultDigest    string     `gorm:"size:500" json:"result_summary,omitempty"`
	Status          string     `gorm:"size:32;index;not null" json:"status"`
	TaskID          *uint      `gorm:"index" json:"task_id,omitempty"`
	DecidedBy       string     `gorm:"size:100" json:"decided_by,omitempty"`
	DecidedAt       *time.Time `json:"decided_at,omitempty"`
	CreatedAt       time.Time  `gorm:"index" json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (AIExecutionIntent) TableName() string { return "ai_execution_intents" }

type AIAttachment struct {
	ID          string    `gorm:"primaryKey;size:36" json:"id"`
	TenantID    uint      `gorm:"index;not null" json:"tenant_id"`
	OwnerID     uint      `gorm:"index;not null" json:"owner_id"`
	SessionID   uint      `gorm:"index;not null" json:"session_id"`
	Filename    string    `gorm:"size:255;not null" json:"filename"`
	MediaType   string    `gorm:"size:120" json:"media_type"`
	Size        int64     `json:"size"`
	Content     string    `gorm:"type:text" json:"-"`
	ContentHash string    `gorm:"size:64;index" json:"content_hash"`
	CreatedAt   time.Time `json:"created_at"`
}

func (AIAttachment) TableName() string { return "ai_attachments" }

type AIKnowledgeCollection struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	TenantID  uint      `gorm:"index;not null" json:"tenant_id"`
	OwnerID   uint      `gorm:"index;not null" json:"owner_id"`
	Name      string    `gorm:"size:160;not null" json:"name"`
	Shared    bool      `gorm:"index;default:false" json:"shared"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (AIKnowledgeCollection) TableName() string { return "ai_knowledge_collections" }

type AIKnowledgeSource struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	TenantID     uint      `gorm:"index;not null" json:"tenant_id"`
	OwnerID      uint      `gorm:"index;not null" json:"owner_id"`
	CollectionID uint      `gorm:"index;not null" json:"collection_id"`
	Name         string    `gorm:"size:255;not null" json:"name"`
	ContentHash  string    `gorm:"size:64;index" json:"content_hash"`
	ChunkCount   int       `json:"chunk_count"`
	CreatedAt    time.Time `json:"created_at"`
}

func (AIKnowledgeSource) TableName() string { return "ai_knowledge_sources" }

type AIKnowledgeChunk struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	TenantID     uint   `gorm:"index;not null" json:"tenant_id"`
	CollectionID uint   `gorm:"index;not null" json:"collection_id"`
	SourceID     uint   `gorm:"index;not null" json:"source_id"`
	Position     int    `gorm:"index;not null" json:"position"`
	Content      string `gorm:"type:text" json:"content"`
	SearchTokens string `gorm:"type:text" json:"-"`
}

func (AIKnowledgeChunk) TableName() string { return "ai_knowledge_chunks" }

func encryptAIFields(values ...*string) error {
	for _, value := range values {
		if *value == "" || strings.HasPrefix(*value, "FC2ENC:") {
			continue
		}
		encrypted, err := crypto.EncryptLoot(*value)
		if err != nil {
			return err
		}
		*value = encrypted
	}
	return nil
}

func decryptAIFields(values ...*string) error {
	for _, value := range values {
		if err := decryptField(value); err != nil {
			return err
		}
	}
	return nil
}

func (s *AIChatSession) BeforeSave(*gorm.DB) error     { return encryptAIFields(&s.Draft) }
func (s *AIChatSession) AfterFind(*gorm.DB) error      { return decryptAIFields(&s.Draft) }
func (m *AIChatMessage) BeforeSave(*gorm.DB) error     { return encryptAIFields(&m.Content) }
func (m *AIChatMessage) AfterFind(*gorm.DB) error      { return decryptAIFields(&m.Content) }
func (p *AIProviderProfile) BeforeSave(*gorm.DB) error { return encryptAIFields(&p.APIKey) }
func (p *AIProviderProfile) AfterFind(*gorm.DB) error  { return decryptAIFields(&p.APIKey) }
func (r *AIChatRun) BeforeSave(*gorm.DB) error {
	return encryptAIFields(&r.Input, &r.ErrorMessage)
}
func (r *AIChatRun) AfterFind(*gorm.DB) error {
	return decryptAIFields(&r.Input, &r.ErrorMessage)
}
func (e *AIChatRunEvent) BeforeSave(*gorm.DB) error { return encryptAIFields(&e.Payload) }
func (e *AIChatRunEvent) AfterFind(*gorm.DB) error  { return decryptAIFields(&e.Payload) }
func (i *AIExecutionIntent) BeforeSave(*gorm.DB) error {
	return encryptAIFields(&i.Arguments, &i.Result)
}
func (i *AIExecutionIntent) AfterFind(*gorm.DB) error {
	return decryptAIFields(&i.Arguments, &i.Result)
}
func (a *AIAttachment) BeforeSave(*gorm.DB) error     { return encryptAIFields(&a.Content) }
func (a *AIAttachment) AfterFind(*gorm.DB) error      { return decryptAIFields(&a.Content) }
func (c *AIKnowledgeChunk) BeforeSave(*gorm.DB) error { return encryptAIFields(&c.Content) }
func (c *AIKnowledgeChunk) AfterFind(*gorm.DB) error  { return decryptAIFields(&c.Content) }

func (AutoTagRule) TableName() string { return "auto_tag_rules" }

type StagerToken struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Token        string    `gorm:"uniqueIndex;size:512" json:"token"`
	ListenerID   uint      `gorm:"index" json:"listener_id"`
	Architecture string    `json:"arch"`
	OS           string    `json:"os"`
	Format       string    `json:"format"`
	Used         bool      `gorm:"default:false;index" json:"used"`
	ExpiresAt    time.Time `json:"expires_at"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (StagerToken) TableName() string { return "stager_tokens" }

type Script struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:255;not null" json:"name"`
	Description string    `gorm:"size:1024" json:"description"`
	Code        string    `gorm:"type:text;not null" json:"code"`
	Events      string    `gorm:"type:text" json:"events"`
	Enabled     bool      `gorm:"default:true" json:"enabled"`
	RunCount    int       `gorm:"default:0" json:"run_count"`
	LastRun     time.Time `json:"last_run"`
	CreatedBy   string    `gorm:"size:64" json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (Script) TableName() string { return "scripts" }

type Redirector struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// TenantID scopes a redirector to the tenant that registered it. A
	// redirector carries SSH credentials for the operator's redirector
	// infrastructure, so an unscoped listing disclosed another tenant's
	// targets. 0 = legacy/unscoped, the convention tenantScope already uses.
	TenantID    uint      `gorm:"index" json:"-"`
	Name        string    `gorm:"size:100" json:"name"`
	Host        string    `gorm:"size:255" json:"host"`
	Type        string    `gorm:"size:20" json:"type"`
	Status      string    `gorm:"size:20;default:'inactive'" json:"status"`
	Config      string    `gorm:"type:text" json:"config"`
	SSHUser     string    `gorm:"size:64" json:"ssh_user"`
	SSHPort     int       `gorm:"default:22" json:"ssh_port"`
	SSHKey      string    `gorm:"type:text" json:"-"`
	SSHPassword string    `gorm:"type:text" json:"-"`
	LastSeen    time.Time `json:"last_seen"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (Redirector) TableName() string { return "redirectors" }

// AgentLock represents a collaboration lock placed on an agent by an operator.
// Used to prevent two operators from driving the same agent simultaneously.
type AgentLock struct {
	ID       string    `gorm:"primaryKey;size:36" json:"id"`
	AgentID  string    `gorm:"uniqueIndex;size:36;not null" json:"agent_id"`
	LockedBy string    `gorm:"size:64;not null" json:"locked_by"`
	LockedAt time.Time `json:"locked_at"`
	// ExpiresAt bounds the lock (default 15m, refreshed by holder heartbeat):
	// a crashed browser must not pin an agent forever.
	ExpiresAt time.Time `gorm:"index" json:"expires_at"`
	Note      string    `gorm:"size:512" json:"note"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (AgentLock) TableName() string { return "agent_locks" }

// CloudCred stores cloud provider credentials stolen from an agent host
// (AWS / GCP / Azure access keys, tokens, etc.).
type CloudCred struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	AgentID   string    `gorm:"index;size:36;not null" json:"agent_id"`
	Provider  string    `gorm:"size:32;not null" json:"provider"` // aws | gcp | azure
	Type      string    `gorm:"size:64" json:"type"`              // e.g. access_key, token, sa_key
	Key       string    `gorm:"size:512" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	Extra     string    `gorm:"type:text" json:"extra"`
	CreatedAt time.Time `json:"created_at"`
}

func (CloudCred) TableName() string { return "cloud_creds" }

// ExtC2Channel stores configuration for External C2 channels (Discord, Slack).
type ExtC2Channel struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Type      string    `gorm:"size:20;not null" json:"type"` // "discord", "slack", or "telegram"
	BotToken  string    `gorm:"size:500" json:"bot_token"`
	ChannelID string    `gorm:"size:100" json:"channel_id"`
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

func (ExtC2Channel) TableName() string { return "extc2_channels" }

// SIEMRule is a user-defined correlation rule evaluated against SIEM events.
// Rules are stored in the DB so operators can author detection without code
// changes; the runtime EventCorrelator hot-reloads them.
type SIEMRule struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Name         string    `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Enabled      bool      `gorm:"default:true" json:"enabled"`
	Action       string    `gorm:"size:128;not null;index" json:"action"`  // SIEM event action to match (e.g. login_failed, implant_checkin)
	WindowSec    int       `gorm:"not null;default:300" json:"window_sec"` // correlation window in seconds
	Threshold    int       `gorm:"not null;default:5" json:"threshold"`    // events required within the window to alert
	AlertAction  string    `gorm:"size:128;not null;default:siem_alert" json:"alert_action"`
	AlertDetails string    `gorm:"size:512" json:"alert_details"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (SIEMRule) TableName() string { return "siem_rules" }

// AgentStatusEvent records when an agent changes online/offline status.
type AgentStatusEvent struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	AgentID   string    `gorm:"index:idx_status_agent_time,priority:1;size:36;not null" json:"agent_id"`
	Status    string    `gorm:"size:20;not null" json:"status"` // "online", "offline", "stale"
	Timestamp time.Time `gorm:"index:idx_status_agent_time,priority:2" json:"timestamp"`
}

func (AgentStatusEvent) TableName() string { return "agent_status_events" }

type UserSession struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	UserID            uint      `gorm:"index;not null" json:"user_id"`
	TokenHash         string    `gorm:"size:64;index;not null" json:"-"`
	IP                string    `gorm:"size:45" json:"ip"`
	UserAgent         string    `gorm:"size:512" json:"user_agent"`
	DeviceFingerprint string    `gorm:"size:128;index" json:"device_fingerprint,omitempty"`
	ExpiresAt         time.Time `json:"expires_at"`
	RevokedAt         time.Time `json:"revoked_at"`
	CreatedAt         time.Time `json:"created_at"`
}

func (UserSession) TableName() string { return "user_sessions" }

type PasswordHistory struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	UserID       uint      `gorm:"index;not null" json:"user_id"`
	PasswordHash string    `gorm:"size:256;not null" json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

func (PasswordHistory) TableName() string { return "password_history" }

// APIKey represents a programmatic API key for REST API authentication.
type APIKey struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index;not null" json:"user_id"`
	Name      string    `gorm:"size:128;not null" json:"name"`
	KeyHash   string    `gorm:"size:64;uniqueIndex;not null" json:"-"`
	Prefix    string    `gorm:"size:12;not null" json:"prefix"`
	LastUsed  time.Time `json:"last_used"`
	ExpiresAt time.Time `json:"expires_at"`
	Active    bool      `gorm:"default:true" json:"active"`
	// Scopes bounds the key to a comma-separated subset of Perm* values.
	// Empty string is a legacy full-access key (owner role decides).
	Scopes string `gorm:"size:1024;default:''" json:"scopes"`
	// AllowedCIDRs optionally binds the key to source networks
	// (comma-separated CIDRs or bare IPs). Empty means any source.
	AllowedCIDRs string    `gorm:"size:1024;default:''" json:"allowed_cidrs,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (APIKey) TableName() string { return "api_keys" }
