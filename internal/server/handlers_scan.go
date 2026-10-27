package server

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// handleScannerPage renders the network scanner page
func (s *Server) handleScannerPage(c *gin.Context) {
	stats := s.getNavStats(c)
	data := gin.H{
		"Title":     "ForgeC2 - Network Scanner",
		"ActiveNav": "scanner",
		"Stats":     stats,
	}
	for k, v := range stats {
		data[k] = v
	}

	var agents []db.Implant
	if err := s.db.Select("id, hostname, ip, status, last_seen").
		Where("status = ?", "online").
		Order("last_seen desc").Limit(500).Find(&agents).Error; err != nil {
		slog.Error("scanner: list agents", "err", err)
		agents = []db.Implant{}
	}
	data["agents"] = agents

	var results []db.ScanResult
	if err := s.db.Order("created_at desc").Limit(500).Find(&results).Error; err != nil {
		slog.Error("scanner: list results", "err", err)
		results = []db.ScanResult{}
	}
	uiResults := make([]gin.H, 0, len(results))
	for _, r := range results {
		uiResults = append(uiResults, gin.H{
			"ip": r.TargetIP, "port": r.Port, "protocol": r.Protocol,
			"status": r.State, "service": r.Service, "version": r.Version, "banner": r.Banner,
		})
	}
	data["results"] = uiResults

	var active []db.Task
	if err := s.db.Where("type = ? AND status IN ?", "portscan", []string{"pending", "running"}).
		Order("created_at desc").Limit(100).Find(&active).Error; err != nil {
		slog.Error("scanner: list active", "err", err)
		active = []db.Task{}
	}
	activeScans := make([]gin.H, 0, len(active))
	for _, t := range active {
		activeScans = append(activeScans, gin.H{
			"id": t.ID, "agent": t.AgentID, "target": t.Command,
			"type": t.Type, "status": t.Status, "progress": t.Progress,
			"started_at": t.CreatedAt,
		})
	}
	data["active_scans"] = activeScans

	var history []db.Task
	if err := s.db.Where("type = ? AND status IN ?", "portscan", []string{"completed", "failed"}).
		Order("created_at desc").Limit(100).Find(&history).Error; err != nil {
		slog.Error("scanner: list history", "err", err)
		history = []db.Task{}
	}
	hist := make([]gin.H, 0, len(history))
	for _, t := range history {
		hist = append(hist, gin.H{
			"id": t.ID, "target": t.Command, "type": t.Type,
			"status": t.Status, "created_at": t.CreatedAt,
		})
	}
	data["history"] = hist

	s.renderPageOrJSON(c, data)
}

// handleScanTask creates a new scan task for an agent
func (s *Server) handleScanTask(c *gin.Context) {
	agentID := c.PostForm("agent_id")
	target := c.PostForm("target")
	portRange := c.PostForm("port_range")
	topPorts := c.PostForm("top_ports") // number of top ports to scan

	// Validation
	if agentID == "" || target == "" {
		respondError(c, http.StatusBadRequest, "agent_id and target are required")
		return
	}

	// Parse port range or use top ports
	var ports []int
	if topPorts != "" {
		n, err := strconv.Atoi(topPorts)
		if err != nil || n <= 0 {
			respondError(c, http.StatusBadRequest, "invalid top_ports value")
			return
		}
		ports = getTopPorts(n)
	} else if portRange != "" {
		ports = parsePortRange(portRange)
		if len(ports) == 0 {
			respondError(c, http.StatusBadRequest, "invalid port range")
			return
		}
	} else {
		// Default: top 1000 ports
		ports = getTopPorts(1000)
	}

	if len(ports) > 10000 {
		respondError(c, http.StatusBadRequest, "too many ports, maximum 10000")
		return
	}

	// Build scan command
	portList := make([]string, len(ports))
	for i, p := range ports {
		portList[i] = strconv.Itoa(p)
	}

	scanType := c.PostForm("scan_type")
	command := fmt.Sprintf("%s:%s", target, strings.Join(portList, ","))

	// Route through issueAgentTask like every other agent-facing dispatch:
	// the bare Create used to skip the tenant scope, the agent-existence
	// check and the collaboration-lock/approval gates inside createTask.
	task := s.issueAgentTask(c, agentID, TaskSpec{Type: "portscan", Command: command, Shell: scanType})
	if task == nil {
		return
	}

	// dispatchTask stamps the calling operator as creator, writes the audit
	// record and broadcasts the task to operator sessions.
	s.dispatchTask(c, task, "scan_created",
		fmt.Sprintf("Scan task %d created targeting %s (ports: %d)", task.ID, target, len(ports)))
}

// handleScanResults returns scan results for a task
func (s *Server) handleScanResults(c *gin.Context) {
	taskID := c.Param("taskId")

	var results []db.ScanResult
	if err := s.db.Where("task_id = ?", taskID).Order("port asc").Limit(65535).Find(&results).Error; err != nil {
		slog.Error("Failed to query scan results", "err", err)
	}

	respond(c, gin.H{
		"results": results,
		"total":   len(results),
	})
}

// handleScanResultsByAgent returns all scan results for an agent
func (s *Server) handleScanResultsByAgent(c *gin.Context) {
	agentID := c.Param("agentId")

	var results []db.ScanResult
	if err := s.db.Where("agent_id = ?", agentID).Order("created_at desc").Limit(1000).Find(&results).Error; err != nil {
		slog.Error("Failed to query agent scan results", "err", err)
	}

	respond(c, gin.H{
		"results": results,
		"total":   len(results),
	})
}

// handleProcessScanResult processes scan results reported by agent
func (s *Server) handleProcessScanResult(c *gin.Context) {
	var req struct {
		TaskID  uint     `json:"task_id"`
		AgentID string   `json:"agent_id"`
		Results []string `json:"results"` // format: "port:protocol:state:service:version:banner"
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}

	// Parse and store results
	var results []db.ScanResult
	for _, resultStr := range req.Results {
		parts := strings.Split(resultStr, ":")
		if len(parts) < 3 {
			continue
		}

		port, err := strconv.Atoi(parts[0])
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		protocol := parts[1]
		state := parts[2]
		service := ""
		version := ""
		banner := ""

		if len(parts) > 3 {
			service = parts[3]
		}
		if len(parts) > 4 {
			version = parts[4]
		}
		if len(parts) > 5 {
			banner = strings.Join(parts[5:], ":")
		}

		results = append(results, db.ScanResult{
			AgentID:  req.AgentID,
			TaskID:   req.TaskID,
			TargetIP: "",
			Port:     port,
			Protocol: protocol,
			State:    state,
			Service:  service,
			Version:  version,
			Banner:   banner,
		})
	}

	if len(results) > 0 {
		if err := s.db.CreateInBatches(results, 500).Error; err != nil {
			slog.Error("Failed to save scan results", "agent_id", req.AgentID, "task", req.TaskID, "count", len(results), "err", err)
		}
	}

	respond(c, gin.H{})
}

// handleExportScanResults exports scan results as CSV
func (s *Server) handleExportScanResults(c *gin.Context) {
	taskID := c.Param("taskId")

	// Task-level gate: scan results inherit the owning task's tenant.
	// Task IDs are sequential integers — without this, any operator could
	// dump any tenant's recon output by enumeration.
	var task db.Task
	if err := s.tenantScope(s.db, c).Select("id").First(&task, "id = ?", taskID).Error; err != nil {
		respondError(c, http.StatusNotFound, "task not found")
		return
	}

	var results []db.ScanResult
	if err := s.db.WithContext(s.ctx).Where("task_id = ?", task.ID).Order("port asc").Find(&results).Error; err != nil {
		slog.Error("Failed to query scan results for export", "err", err)
	}
	s.LogAuditRecord(c, "scan_export", "task", taskID, "scan results exported as CSV", true, nil)

	// Build CSV
	// Build CSV through encoding/csv: the hand-built rows only escaped formula
	// leaders, so a comma in Service or Version (common: "Apache 2.4.1,
	// OpenSSL 1.1.1") shifted the columns and mapped a banner to the wrong port.
	var b strings.Builder
	writer := csv.NewWriter(&b)
	_ = writer.Write([]string{"Port", "Protocol", "State", "Service", "Version", "Banner"})
	for _, r := range results {
		_ = writer.Write([]string{
			strconv.Itoa(r.Port),
			csvSanitize(r.Protocol),
			csvSanitize(r.State),
			csvSanitize(r.Service),
			csvSanitize(r.Version),
			csvSanitize(r.Banner),
		})
	}
	writer.Flush()

	c.Header("Content-Disposition", "attachment; filename=scan_results.csv")
	c.Header("Content-Type", "text/csv")
	c.String(http.StatusOK, b.String())
}

// Helper: parse port range string (e.g., "1-100,443,8080")
func parsePortRange(rangeStr string) []int {
	var ports []int
	portSet := make(map[int]bool)

	parts := strings.Split(rangeStr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "-") {
			// Range
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				continue
			}
			start, _ := strconv.Atoi(rangeParts[0])
			end, _ := strconv.Atoi(rangeParts[1])
			if start > end || start < 1 || end > 65535 {
				continue
			}
			for p := start; p <= end; p++ {
				if !portSet[p] {
					ports = append(ports, p)
					portSet[p] = true
				}
			}
		} else {
			// Single port
			p, _ := strconv.Atoi(part)
			if p >= 1 && p <= 65535 && !portSet[p] {
				ports = append(ports, p)
				portSet[p] = true
			}
		}
	}

	return ports
}

// Helper: get top N common ports
func getTopPorts(n int) []int {
	topPorts := []int{
		21, 22, 23, 25, 53, 80, 110, 111, 135, 139, 143, 161, 389, 443, 445,
		514, 515, 587, 631, 636, 993, 995, 1080, 1433, 1434, 1521, 2049, 2082,
		2083, 2086, 2087, 2095, 2096, 3306, 3389, 5432, 5900, 5901, 6379, 8080,
		8443, 8888, 9090, 9200, 9300, 27017,
	}

	if n >= len(topPorts) {
		return topPorts
	}
	return topPorts[:n]
}
