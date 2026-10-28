package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// handleDownloadTaskOutput serves one task's result as a plain text file.
//
// The console shows a task's output, but getting it anywhere else meant copying from the UI or writing a
// script against the JSON; for a large result - a directory listing, a wevtutil dump - a file is what an
// operator actually wants. The response carries the status and whether the stored result was truncated,
// so a reader can tell "empty because it failed" from "empty because nothing came back".
//
// GET /api/tasks/:taskId/output  (also /agents/:id/tasks/:taskId/output)
func (s *Server) handleDownloadTaskOutput(c *gin.Context) {
	taskID, err := strconv.ParseUint(c.Param("taskId"), 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid task id")
		return
	}

	var task db.Task
	query := s.tenantScope(s.db.Model(&db.Task{}), c)
	if agentID := strings.TrimSpace(c.Param("id")); agentID != "" {
		query = query.Where("agent_id = ?", agentID)
	}
	// Same legacy fallback as the detail route: pre-tenant tasks carry tenant 0, so a scoped lookup
	// misses rows the agent list still shows. The fallback re-gates on agent visibility.
	if err := query.First(&task, taskID).Error; err != nil {
		fallback := s.db.Where("1 = 1")
		if agentID := strings.TrimSpace(c.Param("id")); agentID != "" {
			fallback = fallback.Where("agent_id = ?", agentID)
		}
		var retry db.Task
		if ferr := fallback.First(&retry, taskID).Error; ferr != nil {
			respondError(c, http.StatusNotFound, "task not found")
			return
		}
		if !s.agentVisible(c, retry.AgentID) {
			respondError(c, http.StatusNotFound, "task not found")
			return
		}
		task = retry
	}

	body := task.Result
	if task.Error != "" {
		// Keep the failure with the output: a downloaded file that silently omits why it failed is
		// worse than no file.
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		body += "--- error ---\n" + task.Error
	}
	if strings.TrimSpace(body) == "" {
		respondError(c, http.StatusNotFound, "task has no output yet")
		return
	}

	c.Header("X-Task-Status", task.Status)
	c.Header("X-Task-Type", task.Type)
	if task.Status != "completed" {
		// The stored result of a finished run is final; anything else may still grow.
		c.Header("X-Task-Final", "false")
	} else {
		c.Header("X-Task-Final", "true")
	}
	name := fmt.Sprintf("task-%d-%s.txt", task.ID, sanitizeFilenamePart(task.Type))
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	// ServeContent instead of writing the bytes directly: it adds Range support (206 with Content-Range, and
	// Accept-Ranges on the full response), which is what makes a large result resumable, plus If-Range and HEAD
	// handling. The content type is set explicitly so ServeContent does not sniff it, and the modification time
	// comes from the task row so a resumed request can be validated.
	c.Header("Content-Type", "text/plain; charset=utf-8")
	modTime := task.UpdatedAt
	if modTime.IsZero() {
		modTime = task.CreatedAt
	}
	http.ServeContent(c.Writer, c.Request, name, modTime, strings.NewReader(body))
}

// sanitizeFilenamePart keeps a task type usable inside a Content-Disposition filename.
func sanitizeFilenamePart(raw string) string {
	out := make([]rune, 0, len(raw))
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "task"
	}
	return string(out)
}
