package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type DiscordExternalC2 struct {
	server     *Server
	botToken   string
	channelID  string
	mu         sync.Mutex
	running    bool
	stopCh     chan struct{}
	sequence   atomic.Int64
	sessionID  string
	httpClient *http.Client
}

func NewDiscordExternalC2(s *Server, botToken, channelID string) *DiscordExternalC2 {
	return &DiscordExternalC2{
		server:     s,
		botToken:   botToken,
		channelID:  channelID,
		stopCh:     make(chan struct{}),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (d *DiscordExternalC2) Start() error {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return nil
	}
	d.running = true
	d.stopCh = make(chan struct{})
	d.mu.Unlock()

	channelID := "extc2-discord-" + d.channelID
	slog.Info("Discord External C2 starting", "channel_id", d.channelID)
	if d.server.cfg == nil || strings.TrimSpace(d.server.cfg.Crypto.ExtC2Key) == "" {
		// Fail closed like Telegram/Slack: without the HMAC key, channel
		// membership alone cannot authenticate relayed results.
		slog.Error("Discord ExtC2 refused: crypto.extc2_key is empty, relayed results would be unauthenticated")
		d.mu.Lock()
		d.running = false
		d.mu.Unlock()
		return fmt.Errorf("crypto.extc2_key is required for Discord External C2")
	}

	d.server.extC2ChannelsMu.Lock()
	d.server.extC2Channels[channelID] = &extC2WSChannel{
		ID:        channelID,
		Type:      "discord",
		CreatedAt: time.Now(),
	}
	d.server.extC2ChannelsMu.Unlock()

	go d.runLoop(channelID)
	return nil
}

func (d *DiscordExternalC2) runLoop(channelID string) {
	wait := 5 * time.Second
	const maxWait = 60 * time.Second
	for {
		d.connectAndRun(channelID)
		select {
		case <-d.stopCh:
			slog.Info("Discord External C2 stopped", "channel_id", d.channelID)
			return
		default:
			slog.Info("Discord External C2 reconnecting", "channel_id", d.channelID, "wait", wait)
			time.Sleep(wait)
			wait *= 2
			if wait > maxWait {
				wait = maxWait
			}
		}
	}
}

func (d *DiscordExternalC2) connectAndRun(channelID string) {
	defer d.cleanupChannel(channelID)
	gatewayURL := "wss://gateway.discord.gg/?v=10&encoding=json"
	header := http.Header{}
	header.Set("Bot", d.botToken)

	conn, _, err := websocket.DefaultDialer.Dial(gatewayURL, header)
	if err != nil {
		slog.Error("Discord gateway connect failed", "channel_id", d.channelID, "error", err)
		return
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		slog.Error("Discord gateway read Hello failed", "error", err)
		return
	}

	var hello struct {
		Op int `json:"op"`
		D  struct {
			HeartbeatInterval int `json:"heartbeat_interval"`
		} `json:"d"`
	}
	if err := json.Unmarshal(msg, &hello); err != nil {
		slog.Error("Discord gateway Hello parse failed", "error", err)
		return
	}

	heartbeatInterval := time.Duration(hello.D.HeartbeatInterval) * time.Millisecond
	if heartbeatInterval < 1000*time.Millisecond {
		heartbeatInterval = 41250 * time.Millisecond
	}

	heartbeatDone := make(chan struct{})
	var closeHeartbeatOnce sync.Once
	closeHeartbeat := func() { closeHeartbeatOnce.Do(func() { close(heartbeatDone) }) }

	go func() {
		defer closeHeartbeat()
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-d.stopCh:
				return
			case <-heartbeatDone:
				return
			case <-ticker.C:
				hb, ok := marshalJSONSafe(map[string]interface{}{
					"op": 1,
					"d":  int(d.sequence.Load()),
				})
				if !ok {
					continue
				}
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, hb); err != nil {
					slog.Error("Discord heartbeat send failed", "error", err)
					return
				}
			}
		}
	}()

	identify := map[string]interface{}{
		"op": 2,
		"d": map[string]interface{}{
			"token":   d.botToken,
			"intents": 513,
			"properties": map[string]interface{}{
				"os":      "windows",
				"browser": "forgec2",
				"device":  "forgec2",
			},
		},
	}
	identifyJSON, ok := marshalJSONSafe(identify)
	if !ok {
		closeHeartbeat()
		return
	}
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, identifyJSON); err != nil {
		slog.Error("Discord IDENTIFY send failed", "error", err)
		closeHeartbeat()
		return
	}

	taskSenderDone := make(chan struct{})

	d.server.extC2TaskMu.Lock()
	d.server.extC2Notify[channelID] = make(chan struct{}, 1)
	d.server.extC2TaskMu.Unlock()

	go func() {
		defer close(taskSenderDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			// Snapshot under the lock: the notify map is mutated concurrently
			// by QueueExtC2Task and channel cleanup goroutines.
			d.server.extC2TaskMu.Lock()
			notifyCh := d.server.extC2Notify[channelID]
			d.server.extC2TaskMu.Unlock()
			select {
			case <-d.stopCh:
				return
			case <-notifyCh:
			case <-ticker.C:
			}
			d.server.extC2TaskMu.Lock()
			tasks := d.server.extC2TaskQueue[channelID]
			d.server.extC2TaskQueue[channelID] = nil
			d.server.extC2TaskMu.Unlock()
			for _, task := range tasks {
				taskJSON, ok := marshalJSONSafe(task)
				if !ok {
					continue
				}
				d.sendRESTMessage(string(taskJSON))
			}
		}
	}()

	for {
		select {
		case <-d.stopCh:
			closeHeartbeat()
			<-heartbeatDone
			<-taskSenderDone
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			slog.Error("Discord gateway read error", "channel_id", d.channelID, "error", err)
			closeHeartbeat()
			<-heartbeatDone
			<-taskSenderDone
			return
		}

		var payload struct {
			Op int             `json:"op"`
			D  json.RawMessage `json:"d"`
			S  *int            `json:"s"`
			T  string          `json:"t"`
		}
		if err := json.Unmarshal(msg, &payload); err != nil {
			slog.Warn("Discord: failed to unmarshal gateway payload", "error", err)
			continue
		}

		if payload.S != nil {
			d.sequence.Store(int64(*payload.S))
		}

		switch payload.Op {
		case 1: // Heartbeat request
			hb, ok := marshalJSONSafe(map[string]interface{}{
				"op": 1,
				"d":  int(d.sequence.Load()),
			})
			if !ok {
				continue
			}
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			conn.WriteMessage(websocket.TextMessage, hb)
		case 7: // Reconnect
			slog.Info("Discord: reconnect requested")
			closeHeartbeat()
			<-heartbeatDone
			<-taskSenderDone
			return
		case 9: // Invalid Session
			slog.Warn("Discord: invalid session, reconnecting")
			closeHeartbeat()
			<-heartbeatDone
			<-taskSenderDone
			return
		case 10: // Hello (reconnect scenario, skip — already handled)
		case 11: // Heartbeat ACK
		}

		if payload.T == "READY" {
			var ready struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(payload.D, &ready); err != nil {
				slog.Warn("Discord: failed to unmarshal READY payload", "error", err)
			} else {
				d.sessionID = ready.SessionID
				slog.Info("Discord bot connected", "session_id", d.sessionID, "channel_id", d.channelID)
			}
		}

		if payload.T == "MESSAGE_CREATE" {
			var event struct {
				ChannelID string `json:"channel_id"`
				Content   string `json:"content"`
				Author    struct {
					ID  string `json:"id"`
					Bot bool   `json:"bot"`
				} `json:"author"`
			}
			if err := json.Unmarshal(payload.D, &event); err != nil {
				slog.Warn("Discord: failed to unmarshal MESSAGE_CREATE payload", "error", err)
			} else {
				if event.Author.Bot {
					continue
				}
				if event.ChannelID == d.channelID {
					d.processMessage(event.Content)
				}
			}
		}
	}
}

func (d *DiscordExternalC2) processMessage(content string) {
	var extMsg extC2WSMessage
	if err := json.Unmarshal([]byte(content), &extMsg); err != nil {
		return
	}
	if extMsg.Type == "result" {
		// Channel membership is not authentication: gate relayed results on
		// the extc2_key HMAC so any channel member cannot forge task output.
		if !d.server.verifyExtC2ResultHMAC(extMsg.AgentID, extMsg.TaskID, extMsg.ResultID, extMsg.Result, extMsg.HMAC) {
			slog.Warn("Discord ExtC2 result dropped: HMAC verification failed", "agent_id", extMsg.AgentID, "task_id", extMsg.TaskID)
			return
		}
		d.server.processExternalC2Result(extMsg.AgentID, extMsg.TaskID, extMsg.ResultID, extMsg.Result)
	}
}

// Discord rate-limit bounds. retry_after comes from the server; a missing or absurd value must not
// turn into an unbounded sleep.
const (
	discordRetryDefault = time.Second
	discordRetryMax     = 5 * time.Minute
)

func discordRetryDelay(retryAfterSeconds float64) time.Duration {
	if retryAfterSeconds <= 0 || math.IsNaN(retryAfterSeconds) || math.IsInf(retryAfterSeconds, 0) {
		return discordRetryDefault
	}
	wait := time.Duration(retryAfterSeconds * float64(time.Second))
	if wait > discordRetryMax {
		return discordRetryMax
	}
	return wait
}

func (d *DiscordExternalC2) sendRESTMessage(content string) {
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		url := fmt.Sprintf("https://discord.com/api/v10/channels/%s/messages", d.channelID)
		body, ok := marshalJSONSafe(map[string]string{"content": content})
		if !ok {
			return
		}

		req, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			slog.Error("Discord REST request create failed", "error", err)
			return
		}
		req.Header.Set("Authorization", "Bot "+d.botToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := d.httpClient.Do(req)
		if err != nil {
			slog.Error("Discord REST send failed", "error", err)
			return
		}

		if resp.StatusCode == 429 {
			var rl struct {
				RetryAfter float64 `json:"retry_after"`
			}
			wait := discordRetryDelay(0)
			if json.NewDecoder(resp.Body).Decode(&rl) == nil {
				wait = discordRetryDelay(rl.RetryAfter)
			} else {
				slog.Warn("Discord 429 rate limit but failed to decode retry_after, using default 1s")
			}
			resp.Body.Close()
			// Wait on the stop channel rather than time.Sleep: retry_after is server-controlled and
			// used to be honoured without a bound, so one absurd value could pin this sender for the
			// life of the process, and a shutdown could not interrupt it either.
			select {
			case <-d.stopCh:
				return
			case <-time.After(wait):
			}
			continue
		}

		if resp.StatusCode != 200 && resp.StatusCode != 201 {
			bodyBytes, _ := io.ReadAll(resp.Body)
			slog.Error("Discord REST error", "status", resp.StatusCode, "body", string(bodyBytes))
		}
		resp.Body.Close()
		return
	}
	slog.Warn("Discord REST rate limit retries exhausted", "max_retries", maxRetries)
}

func (d *DiscordExternalC2) cleanupChannel(channelID string) {
	d.server.extC2TaskMu.Lock()
	if q, ok := d.server.extC2TaskQueue[channelID]; ok && len(q) > 0 {
		slog.Warn("Discord ExtC2 channel closing with pending tasks", "channel", channelID, "count", len(q))
	}
	delete(d.server.extC2TaskQueue, channelID)
	delete(d.server.extC2Notify, channelID)
	d.server.extC2TaskMu.Unlock()
}

func (d *DiscordExternalC2) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.running {
		return
	}
	d.running = false
	close(d.stopCh)

	channelID := "extc2-discord-" + d.channelID
	d.server.extC2ChannelsMu.Lock()
	delete(d.server.extC2Channels, channelID)
	d.server.extC2ChannelsMu.Unlock()
}

func (d *DiscordExternalC2) IsRunning() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running
}
