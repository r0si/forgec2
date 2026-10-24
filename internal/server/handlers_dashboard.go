package server

import (
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

type HeatmapData struct {
	Day   int `json:"day"`
	Hour  int `json:"hour"`
	Count int `json:"count"`
}

type OSDistribution struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type TaskStatusData struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
	Color string `json:"color"`
}

type TrafficData struct {
	Labels   []string `json:"labels"`
	BytesIn  []int64  `json:"bytes_in"`
	BytesOut []int64  `json:"bytes_out"`
}

type CredentialTypeData struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
	Color string `json:"color"`
}

type AgentGeoData struct {
	Country string  `json:"country"`
	Count   int64   `json:"count"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
}

type TaskGanttItem struct {
	Agent    string `json:"agent"`
	Task     string `json:"task"`
	Start    int    `json:"start"`
	Duration int    `json:"duration"`
	Status   string `json:"status"`
	Color    string `json:"color"`
}

type AttackPathNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Type  string `json:"type"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
}

type AttackPathEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
	Type  string `json:"type"`
}

type AttackPathData struct {
	Nodes []AttackPathNode `json:"nodes"`
	Edges []AttackPathEdge `json:"edges"`
}

func (s *Server) handleDashboardActivityHeatmap(c *gin.Context) {
	rangeParam := c.DefaultQuery("range", "24h")

	var days int
	var startTime time.Time

	switch rangeParam {
	case "7d":
		days = 7
		startTime = time.Now().AddDate(0, 0, -7)
	case "30d":
		days = 30
		startTime = time.Now().AddDate(0, 0, -30)
	case "1h":
		days = 1
		startTime = time.Now().Add(-time.Hour)
	default:
		days = 1
		startTime = time.Now().Add(-24 * time.Hour)
	}

	type hourBucket struct {
		Day   int
		Hour  int
		Count int64
	}
	var buckets []hourBucket
	if err := s.tenantScope(s.db.Model(&db.Task{}), c).
		Select("CAST((julianday('now') - julianday(created_at)) AS INTEGER) as day, strftime('%H', created_at) as hour, COUNT(*) as count").
		Where("created_at >= ?", startTime).
		Group("day, hour").Scan(&buckets).Error; err != nil {
		handleQueryError(c, err, "Failed to query activity heatmap")
		return
	}

	// Build a map for O(1) lookup
	bucketMap := make(map[[2]int]int64, len(buckets))
	for _, b := range buckets {
		bucketMap[[2]int{b.Day, b.Hour}] = b.Count
	}

	heatmap := make([]HeatmapData, 0, days*24)
	for d := 0; d < days; d++ {
		for h := 0; h < 24; h++ {
			heatmap = append(heatmap, HeatmapData{
				Day:   d,
				Hour:  h,
				Count: int(bucketMap[[2]int{d, h}]),
			})
		}
	}

	respond(c, heatmap)
}

func (s *Server) handleDashboardOSDistribution(c *gin.Context) {
	type osCount struct {
		OS    string
		Count int64
	}

	var results []osCount
	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).
		Select("os, COUNT(*) as count").
		Group("os").
		Order("count DESC").
		Scan(&results).Error; err != nil {
		handleQueryError(c, err, "Failed to query OS distribution")
		return
	}

	distribution := make([]OSDistribution, 0)
	for _, r := range results {
		name := r.OS
		if name == "" {
			name = "Unknown"
		}
		distribution = append(distribution, OSDistribution{
			Name:  name,
			Count: r.Count,
		})
	}

	respond(c, distribution)
}

func (s *Server) handleDashboardTaskStatus(c *gin.Context) {
	var counts []struct {
		Status string
		Count  int64
	}
	if err := s.tenantScope(s.db.Model(&db.Task{}), c).
		Select("status, count(*) as count").Group("status").Limit(10).Find(&counts).Error; err != nil {
		// Zero counts here render as a clean dashboard: a failed
		// aggregate must not be displayed as real task statistics.
		handleQueryError(c, err, "Dashboard: failed to query task status counts")
		return
	}

	countMap := make(map[string]int64, len(counts))
	for _, c := range counts {
		countMap[c.Status] = c.Count
	}

	statusData := []TaskStatusData{
		{Name: "Completed", Count: countMap["completed"], Color: "#22c55e"},
		{Name: "Running", Count: countMap["running"], Color: "#f59e0b"},
		{Name: "Pending", Count: countMap["pending"], Color: "#6366f1"},
		{Name: "Failed", Count: countMap["failed"], Color: "#ef4444"},
	}

	respond(c, statusData)
}

func (s *Server) handleDashboardListenerTraffic(c *gin.Context) {
	rangeParam := c.DefaultQuery("range", "24h")

	var points int
	var labels []string
	var bucketStarts []time.Time

	switch rangeParam {
	case "7d":
		points = 7
		dayStart := time.Now().Truncate(24 * time.Hour)
		for i := 6; i >= 0; i-- {
			d := time.Now().AddDate(0, 0, -i)
			labels = append(labels, d.Format("01-02"))
			bucketStarts = append(bucketStarts, dayStart.AddDate(0, 0, -i))
		}
	case "30d":
		points = 30
		dayStart := time.Now().Truncate(24 * time.Hour)
		for i := 29; i >= 0; i-- {
			d := time.Now().AddDate(0, 0, -i)
			labels = append(labels, d.Format("01-02"))
			bucketStarts = append(bucketStarts, dayStart.AddDate(0, 0, -i))
		}
	case "1h":
		points = 12
		fiveMin := time.Now().Truncate(5 * time.Minute)
		for i := 11; i >= 0; i-- {
			ts := time.Now().Add(-time.Duration(i) * 5 * time.Minute)
			labels = append(labels, ts.Format("15:04"))
			bucketStarts = append(bucketStarts, fiveMin.Add(-time.Duration(i)*5*time.Minute))
		}
	default:
		points = 24
		hourStart := time.Now().Truncate(time.Hour)
		for i := 23; i >= 0; i-- {
			h := time.Now().Add(-time.Duration(i) * time.Hour)
			labels = append(labels, h.Format("15:00"))
			bucketStarts = append(bucketStarts, hourStart.Add(-time.Duration(i)*time.Hour))
		}
	}

	bytesIn := make([]int64, points)
	bytesOut := make([]int64, points)

	// Bucket sovereignty: each point covers [bucketStart, nextBucketStart),
	// which keeps label and value boundaries aligned.
	if s.trafficBytes != nil {
		bucketStep := time.Hour
		switch rangeParam {
		case "1h":
			bucketStep = 5 * time.Minute
		case "7d", "30d":
			bucketStep = 24 * time.Hour
		}
		for i := 0; i < points; i++ {
			end := bucketStarts[i].Add(bucketStep)
			if i+1 < points {
				end = bucketStarts[i+1]
			}
			in, out := s.trafficBytes.sumRange(bucketStarts[i], end)
			bytesIn[i] = in
			bytesOut[i] = out
		}
	}

	trafficData := TrafficData{
		Labels:   labels,
		BytesIn:  bytesIn,
		BytesOut: bytesOut,
	}

	respond(c, trafficData)
}

func (s *Server) handleDashboardCredentialTypes(c *gin.Context) {
	type typeCount struct {
		Type  string
		Count int64
	}

	var results []typeCount
	if err := s.tenantScope(s.db.Model(&db.CredentialEntry{}), c).
		Select("type, COUNT(*) as count").
		Group("type").
		Order("count DESC").
		Scan(&results).Error; err != nil {
		handleQueryError(c, err, "Failed to query credential types")
		return
	}

	colorMap := map[string]string{
		"cleartext": "#8b5cf6",
		"ntlm":      "#ec4899",
		"aes":       "#06b6d4",
		"kerberos":  "#f59e0b",
	}

	nameMap := map[string]string{
		"cleartext": "Plaintext",
		"ntlm":      "NTLM Hash",
		"aes":       "AES Key",
		"kerberos":  "Kerberos",
	}

	credTypes := make([]CredentialTypeData, 0)
	otherCount := int64(0)

	for _, r := range results {
		typ := strings.ToLower(r.Type)
		if color, ok := colorMap[typ]; ok {
			name := typ
			if n, ok := nameMap[typ]; ok {
				name = n
			}
			credTypes = append(credTypes, CredentialTypeData{
				Name:  name,
				Count: r.Count,
				Color: color,
			})
		} else {
			otherCount += r.Count
		}
	}

	if otherCount > 0 {
		credTypes = append(credTypes, CredentialTypeData{
			Name:  "Other",
			Count: otherCount,
			Color: "#64748b",
		})
	}

	respond(c, credTypes)
}

func (s *Server) handleDashboardAgentGeo(c *gin.Context) {
	type countryCount struct {
		Country string
		Count   int64
	}

	var results []countryCount
	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).
		Select("country, COUNT(*) as count").
		Where("country != ''").
		Group("country").
		Order("count DESC").
		Limit(10).
		Scan(&results).Error; err != nil {
		handleQueryError(c, err, "Failed to query agent geo")
		return
	}

	latLngMap := map[string][]float64{
		"China":   {35.8617, 104.1954},
		"USA":     {37.0902, -95.7129},
		"Germany": {51.1657, 10.4515},
		"Japan":   {36.2048, 138.2529},
		"UK":      {55.3781, -3.4360},
		"France":  {46.2276, 2.2137},
		"Korea":   {35.9078, 127.7669},
		"Russia":  {61.5240, 105.3188},
		"Brazil":  {14.2350, -51.9253},
		"India":   {20.5937, 78.9629},
	}

	geoData := make([]AgentGeoData, 0)
	for _, r := range results {
		lat := 0.0
		lng := 0.0
		if coords, ok := latLngMap[r.Country]; ok {
			lat = coords[0]
			lng = coords[1]
		}
		geoData = append(geoData, AgentGeoData{
			Country: r.Country,
			Count:   r.Count,
			Lat:     lat,
			Lng:     lng,
		})
	}

	respond(c, geoData)
}

func (s *Server) handleDashboardTaskGantt(c *gin.Context) {
	rangeParam := c.DefaultQuery("range", "24h")

	var startTime time.Time
	switch rangeParam {
	case "7d":
		startTime = time.Now().AddDate(0, 0, -7)
	case "30d":
		startTime = time.Now().AddDate(0, 0, -30)
	case "1h":
		startTime = time.Now().Add(-time.Hour)
	default:
		startTime = time.Now().Add(-24 * time.Hour)
	}

	var tasks []db.Task
	if err := s.tenantScope(s.db.Preload("Agent"), c).
		Where("created_at >= ?", startTime).
		Order("created_at ASC").
		Limit(20).
		Find(&tasks).Error; err != nil {
		handleQueryError(c, err, "Failed to query task gantt")
		return
	}

	ganttData := make([]TaskGanttItem, 0)

	for _, task := range tasks {
		agentName := task.Agent.Hostname
		if agentName == "" {
			agentName = task.AgentID[:8]
		}

		startOffset := int(task.CreatedAt.Sub(startTime).Minutes())
		duration := 5
		if task.UpdatedAt.After(task.CreatedAt) {
			duration = int(task.UpdatedAt.Sub(task.CreatedAt).Minutes())
			if duration < 1 {
				duration = 1
			}
		}

		color := "#6366f1"
		switch task.Status {
		case "completed":
			color = "#22c55e"
		case "running":
			color = "#f59e0b"
		case "failed":
			color = "#ef4444"
		case "pending":
			color = "#94a3b8"
		}

		taskType := task.Type
		if taskType == "" {
			taskType = "Unknown Task"
		}

		ganttData = append(ganttData, TaskGanttItem{
			Agent:    agentName,
			Task:     taskType,
			Start:    startOffset,
			Duration: duration,
			Status:   task.Status,
			Color:    color,
		})
	}

	respond(c, ganttData)
}

func (s *Server) handleDashboardAttackPath(c *gin.Context) {
	var agents []db.Implant
	if err := s.tenantScope(s.db.Select("id, hostname, os, ip, parent_id"), c).
		Limit(10).Find(&agents).Error; err != nil {
		// An empty node list here draws an attack path with no agents:
		// a failed read must not be rendered as "no path exists".
		handleQueryError(c, err, "Dashboard: failed to query attack path agents")
		return
	}

	nodes := make([]AttackPathNode, 0)
	edges := make([]AttackPathEdge, 0)

	entryID := "entry"
	nodes = append(nodes, AttackPathNode{
		ID:    entryID,
		Label: "Entry Point",
		Type:  "entry",
		X:     50,
		Y:     150,
	})

	for i, agent := range agents {
		agentID := "agent-" + strconv.Itoa(i)

		nodeType := "agent"
		if strings.Contains(strings.ToLower(agent.OS), "server") ||
			strings.Contains(strings.ToLower(agent.Hostname), "srv") ||
			strings.Contains(strings.ToLower(agent.Hostname), "server") {
			nodeType = "server"
		} else if strings.Contains(strings.ToLower(agent.Hostname), "dc") ||
			strings.Contains(strings.ToLower(agent.Hostname), "domain") {
			nodeType = "dc"
		}

		x := 200 + (i/3)*200
		y := 60 + (i%3)*80

		nodes = append(nodes, AttackPathNode{
			ID:    agentID,
			Label: agent.Hostname,
			Type:  nodeType,
			X:     x,
			Y:     y,
		})

		// Only graph relationships the server actually knows about: a real
		// parent->child beacon relationship (SMB), or initial-access for the
		// first tier. Invented "DCSync"/"DC-Target" pivots and credential-based
		// lateral edges are not rendered because they were never observed.
		if agent.ParentID != "" {
			parentIdx := -1
			for j, a := range agents {
				if a.ID == agent.ParentID {
					parentIdx = j
					break
				}
			}
			if parentIdx >= 0 {
				parentID := "agent-" + strconv.Itoa(parentIdx)
				edges = append(edges, AttackPathEdge{
					From:  parentID,
					To:    agentID,
					Label: "SMB",
					Type:  "lateral",
				})
			}
		} else if i < 2 {
			edges = append(edges, AttackPathEdge{
				From:  entryID,
				To:    agentID,
				Label: "Initial Access",
				Type:  "initial",
			})
		}
	}

	attackPath := AttackPathData{
		Nodes: nodes,
		Edges: edges,
	}

	respond(c, attackPath)
}
