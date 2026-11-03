package server

import (
	"time"
)

// Local copies of protocol types (agent package is not importable as it is package main + build constrained)
type beaconRequest struct {
	UUID            string            `json:"uuid"`
	Seq             uint64            `json:"seq,omitempty"` // frame sequence (mirrors the envelope)
	ProtocolVersion uint              `json:"pv,omitempty"`
	Info            map[string]string `json:"info,omitempty"`
	Results         []taskResult      `json:"results,omitempty"`
	AckTaskIDs      []uint            `json:"acks,omitempty"`
	TaskCapacity    *int              `json:"task_capacity,omitempty"`
	SocksData       []socksFrame      `json:"socks_data,omitempty"`
	Relayed         []relayedData     `json:"relayed,omitempty"`        // P2P: child results forwarded by parent
	RelayedFrames   []relayedFrame    `json:"relayed_frames,omitempty"` // P2P v2: opaque child envelopes

	// DroppedResults counts results the agent discarded before sending.
	// Surfaced in logs/audit so result gaps are visible, not silent.
	DroppedResults int `json:"dropped_results,omitempty"`

	// ECDH + AES-256-GCM fields (forward-secret encryption)
	ECDHPub   string `json:"ecdh_pub,omitempty"` // base64-encoded X25519 public key
	CipherB64 string `json:"c,omitempty"`        // base64(nonce + AES-256-GCM ciphertext)
}

type relayedData struct {
	AgentID    string       `json:"agent_id"` // child agent UUID
	Results    []taskResult `json:"results"`
	AckTaskIDs []uint       `json:"acks,omitempty"`
}

// relayedFrame is an opaque v2 beacon envelope from a P2P child, forwarded
// verbatim by its parent. It is authenticated end-to-end with the child's
// session key, so a malicious parent cannot forge or read it.
type relayedFrame struct {
	AgentID  string `json:"agent_id"` // child agent UUID
	Envelope []byte `json:"envelope"` // raw envelope JSON bytes
}

// relayedReply is an opaque v2 response envelope built by the server for a
// P2P child and forwarded verbatim by its parent.
type relayedReply struct {
	AgentID  string `json:"agent_id"`
	Envelope []byte `json:"envelope"`
}

type taskResult struct {
	TaskID   uint   `json:"task_id"`
	Type     string `json:"type"`
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
	Encoding string `json:"encoding,omitempty"`
	Filename string `json:"filename,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Offset   int64  `json:"offset,omitempty"`
	Path     string `json:"path,omitempty"`
	// ResultID is the agent-generated per-result id used for idempotent
	// processing of re-sent results (dropped frames are retried with a new
	// envelope seq, so dedupe on this instead).
	ResultID string `json:"rid,omitempty"`
	// Partial marks a streaming progress chunk for a running task (appended
	// to a capped tail instead of finalising the task).
	Partial bool `json:"partial,omitempty"`
	// MAC is the agent's file-transfer integrity chain link (hex).
	MAC string `json:"mac,omitempty"`
	// EncryptedWithTaskKey flags that Output was sealed with the per-task key.
	EncryptedWithTaskKey bool `json:"etk,omitempty"`
}

type task struct {
	ID        uint   `json:"id"`
	Type      string `json:"type"`
	Command   string `json:"command"`
	Encrypted bool   `json:"enc,omitempty"`
	Shell     string `json:"shell"`
	Path      string `json:"path,omitempty"`
	Data      string `json:"data,omitempty"`
	Offset    int64  `json:"offset,omitempty"`
	Size      int64  `json:"size,omitempty"`
	PrevMAC   string `json:"prev_mac,omitempty"`
	MAC       string `json:"mac,omitempty"`
	Key       string `json:"key,omitempty"`
}

type relayedTask struct {
	AgentID string `json:"agent_id"` // child agent UUID
	Tasks   []task `json:"tasks"`
}

type beaconResponse struct {
	Tasks           []task `json:"tasks"`
	ProtocolVersion uint   `json:"pv,omitempty"`
	// Tenant attribution is server-internal telemetry metadata. It lets the
	// HTTP traffic accumulator remain tenant-aware without another DB lookup
	// on every successful beacon.
	TenantID       uint `json:"-"`
	TenantResolved bool `json:"-"`
	// ProtocolDeprecated tells an agent that the server is serving it on a
	// version that is on its way out: it still works, but the agent has to
	// be rebuilt before the compatibility window closes. Set only when the
	// agent speaks a deprecated version.
	ProtocolDeprecated bool `json:"pv_deprecated,omitempty"`
	// ProtocolRejected marks a beacon the server refused because the agent's
	// protocol version is not served. The reason travels back with the
	// response so the agent (and the operator's log) can say why instead of
	// retrying in silence. Server-internal: it never reaches the wire.
	ProtocolRejected        bool   `json:"-"`
	ProtocolRejectionReason string `json:"-"`
	Seq                     uint64 `json:"seq,omitempty"`
	RegOK                   bool   `json:"reg_ok,omitempty"`
	Rekey                   bool   `json:"rekey,omitempty"`
	// LastSeq is the server's current accepted sequence for this agent. On a
	// successful beacon it lets the agent fast-forward if it ever drifted
	// behind; in a replay-rejection it is returned (MAC-signed) so a desynced
	// agent can resync instead of being permanently locked out.
	LastSeq uint64 `json:"last_seq,omitempty"`
	// Reregister is sent (with a valid response MAC) when the server received a
	// valid handshake from a v3 agent whose implant row was deleted server-side.
	// The handshake carries no identity key, so the agent must re-enroll with a
	// fresh registration frame (it holds the identity key locally).
	Reregister     bool           `json:"reregister,omitempty"`
	SocksFrames    []socksFrame   `json:"socks_frames,omitempty"`
	SocksFastMode  bool           `json:"socks_fast,omitempty"`
	Relayed        []relayedTask  `json:"relayed,omitempty"`         // P2P: tasks for children
	RelayedReplies []relayedReply `json:"relayed_replies,omitempty"` // P2P v2: opaque child response envelopes

	// Fleet kill-switch broadcast: both fields are set only while the
	// kill-switch is armed. KillSwitch is the per-arm token (hex) and
	// KillSwitchMAC its per-implant authentication tag
	// HMAC-SHA256(regKey-derived kill-switch key, token), so only the
	// server holding the registration key can order self-destruct.
	KillSwitch    string `json:"kill_switch,omitempty"`
	KillSwitchMAC string `json:"kill_switch_mac,omitempty"`

	// ECDH + AES-256-GCM fields
	ECDHPub   string `json:"ecdh_pub,omitempty"` // base64-encoded server X25519 public key
	CipherB64 string `json:"c,omitempty"`        // base64(nonce + AES-256-GCM ciphertext)
	Mac       string `json:"mac,omitempty"`      // auth frame response MAC: HMAC(regKey, agentUUID||seq||server_pub)

	// NetworkConfigOverWire delivers the agent's live network configuration at
	// registration, encrypted under the per-implant registration secret
	// (AES-256-GCM, key = HKDF(secret, "forgec2-network-config-v1")). The agent
	// decrypts it and overrides its compile-time defaults, so the operator can
	// rotate C2 endpoints / malleable profile without rebuilding the implant.
	// Empty for v2 (master-key) implants, which carry their config embedded.
	NetworkConfig string `json:"network_config,omitempty"`

	// SessionCipher is the session AEAD suite for subsequent encrypted frames
	// ("aes-gcm" or "chacha20"). Set on MAC'd auth responses so the agent can
	// pick the matching AEAD. Not covered by the response MAC (which signs
	// ecdh_pub only) — tampering worst-case forces a decrypt failure / rekey.
	// Empty means the default AES-GCM (older servers / upgrade compat).
	SessionCipher string `json:"sc,omitempty"`
}

// beaconEnvelope is the top-level transport envelope shared by HTTP, TCP and
// WebSocket beacons. Protocol v2: frames carry a monotonic per-agent sequence
// number and a Unix timestamp; the shared beacon key is never transmitted.
type beaconEnvelope struct {
	UUID        string `json:"uuid"`
	Seq         uint64 `json:"seq,omitempty"`
	TS          int64  `json:"ts,omitempty"`
	ECDHPub     string `json:"ecdh_pub,omitempty"`  // base64 X25519 public key (handshake/registration)
	CipherB64   string `json:"c,omitempty"`         // base64(nonce + AES-256-GCM ciphertext)
	Mac         string `json:"mac,omitempty"`       // HMAC(regKey, uuid||ecdh_pub||ts) for handshake frames
	IdentityPub string `json:"id_pub,omitempty"`    // registration: agent identity public key
	RegHMAC     string `json:"reg_hmac,omitempty"`  // registration: HMAC(regKey, uuid||id_pub||ts)
	SecretID    string `json:"secret_id,omitempty"` // v3 registration: per-implant secret id ("" = legacy v2 master-key path)

	// authed marks a rejected frame whose ciphertext passed AEAD
	// authentication (only the sequence window rejected it). Server-internal:
	// json ignores it, and resyncResponseFor uses it to tell a genuinely
	// desynced agent apart from unauthenticated garbage.
	authed bool
}

// beaconFrameKind classifies a decoded envelope.
type beaconFrameKind int

const (
	frameRejected  beaconFrameKind = iota
	frameEncrypted                 // ciphertext frame with an established session
	frameHandshake                 // authenticated ECDH handshake (rekey / restart recovery)
	frameRegister                  // one-time v2 registration binding the identity key
)

// beaconTSTolerance is the maximum clock skew accepted between the agent's
// frame timestamp and server time.
const beaconTSTolerance = 300 // seconds

// maxSeqJump is the base per-frame sequence advance before a frame is
// rejected as a replay flood / desync indicator. acceptSeq scales it up with
// the agent's sleep interval and time since its last accepted frame (a failed
// beacon advances the agent counter by ~8, so a long outage legitimately
// produces a large jump on recovery).
const maxSeqJump = 1000

// maxSeqJumpAbsolute caps the adaptive jump window: beyond this a frame is
// rejected no matter how long the agent was silent (defense-in-depth against
// a key-holding actor replay-flooding to burn the window).
const maxSeqJumpAbsolute = 1 << 20

// seqLockoutDuration is how long an agent stays locked out after a sequence
// jump exceeds the hard cap in acceptSeq. The lockout is in-memory and clears
// on restart; the operator can also recover via the admin reset path.
const seqLockoutDuration = 5 * time.Minute

// defaultRegSecretOrphanTTL is how long an unbound v3 registration secret is
// retained before the periodic sweep deletes it. A secret is created before the
// toolchain runs, so a failed build leaves an unbound row; a successfully
// deployed agent binds its secret on first check-in well within this window.
// Operators can override it with server.reg_secret_orphan_ttl_hours.
const defaultRegSecretOrphanTTL = 7 * 24 * time.Hour
