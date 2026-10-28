package server

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) handleTaskHistory(c *gin.Context) {
	p := parsePagination(c, DefaultTaskPageSize, MaxTaskPageSize)
	filterType := c.Query("type")
	filterStatus := c.Query("status")
	filterAgent := c.Query("agent")
	filterQuery := c.Query("q")
	filterClaimed := c.Query("claimed_by")

	// Build query with filters
	silentTypes := []string{"screen_stream_start", "screen_stream_stop", "ls"}
	query := s.db.Model(&db.Task{}).
		Where("type NOT IN ?", silentTypes)
	// Multi-tenant isolation: operators only see tasks in their tenant.
	query = s.tenantScope(query, c)
	if filterType != "" {
		query = query.Where("type = ?", filterType)
	}
	if filterStatus != "" {
		query = query.Where("status = ?", filterStatus)
	}
	if filterAgent != "" {
		query = query.Where("agent_id = ?", filterAgent)
	}
	if filterQuery != "" {
		// Keyword search spans command + result + error so operators can find
		// failed output without opening every row.
		like := "%" + escapeLike(filterQuery) + "%"
		query = query.Where(
			"(command LIKE ? ESCAPE '\\' OR result LIKE ? ESCAPE '\\' OR error LIKE ? ESCAPE '\\')",
			like, like, like,
		)
	}
	if filterClaimed != "" {
		// "me" resolves to the calling operator; anything else is a username.
		claimant := filterClaimed
		if filterClaimed == "me" {
			claimant = c.GetString("user")
		}
		query = query.Where("operator_claimed_by = ?", claimant)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to count tasks")
		return
	}

	var tasks []db.Task
	if err := query.
		Order("created_at desc").Offset(p.Offset).Limit(p.PageSize).Find(&tasks).Error; err != nil {
		handleQueryError(c, err, "Failed to query tasks")
		return
	}

	// Collect distinct task types for filter dropdown (degraded gracefully on error)
	var taskTypes []string
	if err := s.tenantScope(s.db.Model(&db.Task{}), c).
		Where("type NOT IN ?", silentTypes).
		Distinct("type").Pluck("type", &taskTypes).Error; err != nil {
		slog.Warn("Failed to pluck task types", "err", err)
		taskTypes = []string{}
	}

	// Collect agents for filter dropdown (degraded gracefully on error)
	var agents []db.Implant
	if err := s.tenantScope(s.db.Select("id, hostname, ip"), c).Order("hostname").Limit(500).Find(&agents).Error; err != nil {
		slog.Warn("Failed to query agent filter list", "err", err)
	}

	// Count failed tasks for "retry all" button (degraded gracefully on error)
	var failedCount int64
	if err := s.tenantScope(s.db.Model(&db.Task{}), c).Where("status = ?", "failed").Count(&failedCount).Error; err != nil {
		slog.Warn("Failed to count failed tasks", "err", err)
	}

	totalPages := int(total) / p.PageSize
	if int(total)%p.PageSize > 0 {
		totalPages++
	}

	stats := s.getNavStats(c)
	data := gin.H{
		"Title":          "ForgeC2 - Task History",
		"ActiveNav":      "tasks",
		"Tasks":          tasks,
		"Page":           p.Page,
		"PageSize":       p.PageSize,
		"Total":          total,
		"TotalPages":     totalPages,
		"FilterType":     filterType,
		"FilterStatus":   filterStatus,
		"FilterAgent":    filterAgent,
		"FilterQuery":    filterQuery,
		"FilterClaimed":  filterClaimed,
		"HasFailedTasks": failedCount > 0,
		"TaskTypes":      taskTypes,
		"Agents":         agents,
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

// handleExportTasks exports tasks as CSV for reporting.
//
// Resource discipline matters here: Result/Error blobs can each reach
// MaxResultSize (1 MiB) and AfterFind AES-GCM-decrypts every loaded row, so a
// naive "load everything then buffer the CSV" can hold ~10 GiB for the default
// ExportTaskLimit. This handler therefore (a) SELECTs only the columns the CSV
// uses, (b) preloads just the agent hostname, (c) streams rows to the response
// instead of buffering, and (d) honours ?limit= capped at ExportTaskLimit.
func (s *Server) handleExportTasks(c *gin.Context) {
	if !s.requireExportScope(c, "task_export", "task") {
		return
	}
	limit := ExportTaskLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			if n > ExportTaskLimit {
				n = ExportTaskLimit
			}
			limit = n
		}
	}
	// Revalidation before the query: a client that already holds this export gets a bodiless 304 and
	// costs neither side the read.
	exportETag := s.taskExportETag(c, limit)
	if exportETag != "" {
		c.Header("ETag", exportETag)
		c.Header("Cache-Control", "private, no-cache")
	}
	if ifNoneMatch(c, exportETag) {
		c.AbortWithStatus(http.StatusNotModified)
		return
	}

	var tasks []db.Task
	query := s.db.WithContext(s.ctx).
		Preload("Agent", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, hostname")
		}).
		Select("tasks.id, tasks.agent_id, tasks.type, tasks.command, tasks.result, tasks.error, tasks.status, tasks.created_at").
		Where("type NOT IN ?", []string{"screen_stream_start", "screen_stream_stop", "ls"})
	query = s.tenantScope(query, c)
	if err := query.Order("created_at desc").Limit(limit).Find(&tasks).Error; err != nil {
		handleQueryError(c, err, "Failed to export tasks")
		return
	}

	// Built into memory, then sent whole. Exporting is capped at ExportTaskLimit rows that are already
	// loaded here, so buffering costs nothing, and it buys the two things streaming could not: a failed
	// encode can be answered 500 instead of being appended to a 200 that looks complete, and the
	// response carries a Content-Length. A truncated CSV that still parses is worse than an error,
	// because the recipient cannot tell it is short.
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	// Task ID is the last column on purpose: it is additive for anyone parsing the existing header,
	// and without it a row could not be matched back to the task in the console - which is the first
	// thing an analyst does with an export.
	writer.Write([]string{"Time", "Agent", "Type", "Command", "Result", "Error", "Status", "Task ID", "Engagement"})

	// Engagement names for the agents in this export, in one query. The column exists because the agent
	// list can already be filtered by engagement: an export that cannot say which exercise a task belonged
	// to would contradict the filter that selected it.
	engagementNames := s.engagementNamesForTasks(c, tasks)

	for _, t := range tasks {
		agentName := ""
		if t.Agent.Hostname != "" {
			agentName = t.Agent.Hostname
		}
		writer.Write([]string{
			t.CreatedAt.Format("2006-01-02 15:04:05"),
			agentName,
			t.Type,
			csvSafe(t.Command),
			csvSafe(truncateString(redactSensitiveResultForAuxiliary(t.Type, t.Result), CSVResultTruncLen)),
			csvSafe(truncateString(t.Error, CSVErrorTruncLen)),
			t.Status,
			strconv.FormatUint(uint64(t.ID), 10),
			csvSafe(engagementNames[t.AgentID]),
		})
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		// Nothing has been written to the response yet, so this is a real error, not a comment.
		slog.Error("Failed to encode CSV export", "error", err)
		c.Header("ETag", "")
		c.Header("Cache-Control", "")
		handleQueryError(c, err, "Failed to encode the task export")
		return
	}

	s.LogAuditRecord(c, "task_export", "task", "", fmt.Sprintf("tasks exported as CSV (limit %d)", limit), true, nil)
	// A capped export looks exactly like a complete one, so say when it was cut short.
	if len(tasks) >= limit {
		c.Header("X-Task-Export-Truncated", "true")
	} else {
		c.Header("X-Task-Export-Truncated", "false")
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	// The timestamp keeps two exports from the same day from overwriting each other in the browser.
	c.Header("Content-Disposition", `attachment; filename="forgec2_tasks_`+time.Now().Format("2006-01-02_150405")+`.csv"`)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buf.Bytes())
}

func (s *Server) apiBulkTaskStatus(c *gin.Context) {
	var req struct {
		TaskIDs []uint `json:"task_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "task_ids required")
		return
	}
	if len(req.TaskIDs) > MaxTaskIDsPerRequest {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("max %d task IDs per request", MaxTaskIDsPerRequest))
		return
	}
	var tasks []db.Task
	query := s.db.Where("id IN ?", req.TaskIDs)
	query = s.tenantScope(query, c)
	if err := query.Select("id, status, result, error, updated_at").Find(&tasks).Error; err != nil {
		handleQueryError(c, err, "Failed to bulk query task status")
		return
	}
	result := make(map[uint]db.Task, len(tasks))
	for i := range tasks {
		result[tasks[i].ID] = tasks[i]
	}
	respond(c, result)
}

// engagementNamesForTasks maps agent id -> engagement name for the agents in an export.
//
// One query for the agents and one for the names, rather than a join per row: the export is capped but the
// per-row cost would still scale with the limit. A missing engagement (or an unreadable row) simply leaves
// the cell empty, which is the honest value - the task genuinely belongs to no exercise.
func (s *Server) engagementNamesForTasks(c *gin.Context, tasks []db.Task) map[string]string {
	out := make(map[string]string, len(tasks))
	ids := make([]string, 0, len(tasks))
	seen := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		if task.AgentID == "" || seen[task.AgentID] {
			continue
		}
		seen[task.AgentID] = true
		ids = append(ids, task.AgentID)
	}
	if len(ids) == 0 {
		return out
	}

	var agents []db.Implant
	if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Implant{}), c).
		Select("id", "engagement_id").Where("id IN ?", ids).Find(&agents).Error; err != nil {
		return out
	}
	engagementIDs := make([]uint, 0, len(agents))
	for _, agent := range agents {
		if agent.EngagementID != nil {
			engagementIDs = append(engagementIDs, *agent.EngagementID)
		}
	}
	if len(engagementIDs) == 0 {
		return out
	}

	var engagements []db.Engagement
	if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Engagement{}), c).
		Select("id", "name").Where("id IN ?", engagementIDs).Find(&engagements).Error; err != nil {
		return out
	}
	names := make(map[uint]string, len(engagements))
	for _, engagement := range engagements {
		names[engagement.ID] = engagement.Name
	}
	for _, agent := range agents {
		if agent.EngagementID == nil {
			continue
		}
		if name, ok := names[*agent.EngagementID]; ok {
			out[agent.ID] = name
		}
	}
	return out
}
