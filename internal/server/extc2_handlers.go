package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

// upsertExtC2Channel inserts the config row for one (type, channel_id) pair
// or refreshes the existing one, returning the canonical row. Configure used
// to Create unconditionally: reconfiguring a channel accumulated rows, and a
// Start failure mid-way left a row advertising a channel that never came up.
// The unique index on (type, channel_id) makes the upsert atomic.
func (s *Server) upsertExtC2Channel(kind, channelID, botToken string) (*db.ExtC2Channel, error) {
	row := db.ExtC2Channel{Type: kind, ChannelID: channelID, BotToken: botToken, Enabled: true}
	err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "type"}, {Name: "channel_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"bot_token", "enabled"}),
	}).Create(&row).Error
	if err != nil {
		return nil, err
	}
	// The conflict-update path leaves row.ID zero, so re-read the canonical
	// row for a stable response id.
	var stored db.ExtC2Channel
	if err := s.db.Where("type = ? AND channel_id = ?", kind, channelID).First(&stored).Error; err != nil {
		return nil, err
	}
	return &stored, nil
}

func (s *Server) handleConfigureDiscordC2(c *gin.Context) {
	var req struct {
		BotToken  string `json:"bot_token" binding:"required"`
		ChannelID string `json:"channel_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "bot_token and channel_id are required")
		return
	}

	// Start before persisting: a failed Start must not leave a configured
	// row behind (the poller never came up, so the token is useless and
	// restore-on-boot would keep retrying it).
	discord := NewDiscordExternalC2(s, req.BotToken, req.ChannelID)
	if err := discord.Start(); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Start Discord C2"))
		return
	}
	channel, err := s.upsertExtC2Channel("discord", req.ChannelID, req.BotToken)
	if err != nil {
		discord.Stop() // roll the poller back: no row, no runner
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Save config"))
		return
	}
	// registerExtC2Runner stops any previous poller on the same key first.
	s.registerExtC2Runner(fmt.Sprintf("extc2-discord-%s", req.ChannelID), discord)

	s.LogAuditRecord(c, "extc2_discord_configure", "extc2", req.ChannelID, "Discord External C2 configured", true, nil)
	respond(c, gin.H{"channel_id": req.ChannelID, "id": channel.ID})
}

func (s *Server) handleConfigureSlackC2(c *gin.Context) {
	var req struct {
		BotToken  string `json:"bot_token" binding:"required"`
		ChannelID string `json:"channel_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "bot_token and channel_id are required")
		return
	}

	// Start before persisting: a failed Start must not leave a configured
	// row behind (see the Discord handler for the full rationale).
	slackC2 := NewSlackExternalC2(s, req.BotToken, req.ChannelID)
	if err := slackC2.Start(); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Start Slack C2"))
		return
	}
	channel, err := s.upsertExtC2Channel("slack", req.ChannelID, req.BotToken)
	if err != nil {
		slackC2.Stop() // roll the poller back: no row, no runner
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Save config"))
		return
	}
	// registerExtC2Runner stops any previous poller on the same key first.
	s.registerExtC2Runner(fmt.Sprintf("extc2-slack-%s", req.ChannelID), slackC2)

	s.LogAuditRecord(c, "extc2_slack_configure", "extc2", req.ChannelID, "Slack External C2 configured", true, nil)
	respond(c, gin.H{"channel_id": req.ChannelID, "id": channel.ID})
}

func (s *Server) handleConfigureTelegramC2(c *gin.Context) {
	var req struct {
		BotToken string `json:"bot_token" binding:"required"`
		ChatID   string `json:"chat_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "bot_token and chat_id are required")
		return
	}
	// Inbound sender checks compare numeric chat IDs (the only form the Bot
	// API reports): reject @usernames/handles at configure time so the
	// channel cannot be born unverifiable.
	if id, err := strconv.ParseInt(strings.TrimSpace(req.ChatID), 10, 64); err != nil || id == 0 {
		respondError(c, http.StatusBadRequest, "chat_id must be the numeric chat ID (e.g. -1001234567890), not a username")
		return
	}

	// Start before persisting: a failed Start must not leave a configured
	// row behind (see the Discord handler for the full rationale).
	telegramC2 := NewTelegramExternalC2(s, req.BotToken, req.ChatID)
	if err := telegramC2.Start(); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Start Telegram C2"))
		return
	}
	channel, err := s.upsertExtC2Channel("telegram", req.ChatID, req.BotToken)
	if err != nil {
		telegramC2.Stop() // roll the poller back: no row, no runner
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Save config"))
		return
	}
	// registerExtC2Runner stops any previous poller on the same key first.
	s.registerExtC2Runner(fmt.Sprintf("extc2-telegram-%s", req.ChatID), telegramC2)

	s.LogAuditRecord(c, "extc2_telegram_configure", "extc2", req.ChatID, "Telegram External C2 configured", true, nil)
	respond(c, gin.H{"chat_id": req.ChatID, "id": channel.ID})
}

func (s *Server) handleListExtC2Configs(c *gin.Context) {
	var channels []db.ExtC2Channel
	if err := s.db.Limit(500).Find(&channels).Error; err != nil {
		slog.Error("Failed to list external C2 channels", "err", err)
	}
	for i := range channels {
		channels[i].BotToken = "***REDACTED***"
	}
	respond(c, gin.H{"channels": channels})
}

func (s *Server) handleDeleteExtC2Config(c *gin.Context) {
	id := c.Param("id")
	var channel db.ExtC2Channel
	if err := s.db.First(&channel, id).Error; err != nil {
		respondError(c, http.StatusNotFound, "Channel not found")
		return
	}

	channelKey := fmt.Sprintf("extc2-%s-%s", channel.Type, channel.ChannelID)
	// Stop the LIVE poller too: without this the run loop kept reconnecting
	// with the "deleted" bot token and kept accepting relayed results until
	// process restart.
	s.stopExtC2Runner(channelKey)
	s.extC2ChannelsMu.Lock()
	delete(s.extC2Channels, channelKey)
	s.extC2ChannelsMu.Unlock()

	if err := s.db.Delete(&channel).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "External C2 delete"))
		return
	}
	s.LogAuditRecord(c, "extc2_config_delete", "extc2", channel.ChannelID, fmt.Sprintf("Deleted %s External C2 config", channel.Type), true, nil)
	respond(c, gin.H{})
}

func (s *Server) restoreExtC2Channels() {
	var channels []db.ExtC2Channel
	if err := s.db.Where("enabled = ?", true).Limit(500).Find(&channels).Error; err != nil {
		slog.Error("Failed to restore external C2 channels", "err", err)
	}

	for _, ch := range channels {
		switch ch.Type {
		case "discord":
			discord := NewDiscordExternalC2(s, ch.BotToken, ch.ChannelID)
			if err := discord.Start(); err != nil {
				slog.Error("Failed to restore Discord C2", "channel_id", ch.ChannelID, "error", err)
			} else {
				s.registerExtC2Runner(fmt.Sprintf("extc2-discord-%s", ch.ChannelID), discord)
				slog.Info("Restored Discord C2 channel", "channel_id", ch.ChannelID)
			}
		case "slack":
			slackC2 := NewSlackExternalC2(s, ch.BotToken, ch.ChannelID)
			if err := slackC2.Start(); err != nil {
				slog.Error("Failed to restore Slack C2", "channel_id", ch.ChannelID, "error", err)
			} else {
				s.registerExtC2Runner(fmt.Sprintf("extc2-slack-%s", ch.ChannelID), slackC2)
				slog.Info("Restored Slack C2 channel", "channel_id", ch.ChannelID)
			}
		case "telegram":
			telegramC2 := NewTelegramExternalC2(s, ch.BotToken, ch.ChannelID)
			if err := telegramC2.Start(); err != nil {
				slog.Error("Failed to restore Telegram C2", "chat_id", ch.ChannelID, "error", err)
			} else {
				s.registerExtC2Runner(fmt.Sprintf("extc2-telegram-%s", ch.ChannelID), telegramC2)
				slog.Info("Restored Telegram C2 channel", "chat_id", ch.ChannelID)
			}
		}
	}
}
