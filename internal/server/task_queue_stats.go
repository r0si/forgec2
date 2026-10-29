package server

import (
	"sort"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Task queue depth per beacon: which agent is backed up, and which one has stopped draining.
//
// Operators learn this the hard way today: a beacon that is asleep, offline or wedged keeps accepting tasks, and the console shows
// them as "pending" one page at a time. Every mature C2 surfaces the backlog per session for exactly this reason - the question is
// not "how many tasks failed" but "which host is not consuming work", and that is a QUEUE question.
//
// A stalled queue is the interesting signal: pending rows that are older than the sleep interval plus a margin mean the beacon is
// not polling, which is different from a slow command and needs a different response.
//
// GET /api/stats/tasks/queue

// queueStallGrace is how long a pending task may sit before the queue is called stalled, on top of the beacon's own sleep.
const queueStallGrace = 2 * time.Minute

// queueEntry is one agent's backlog.
type queueEntry struct {
	AgentID       string `json:"agent_id"`
	Hostname      string `json:"hostname,omitempty"`
	Pending       int    `json:"pending"`
	Running       int    `json:"running"`
	OldestPending int64  `json:"oldest_pending_seconds"`
	SleepSeconds  int    `json:"sleep_seconds,omitempty"`
	Stalled       bool   `json:"stalled"`
}

// handleTaskQueueStats reports the per-agent backlog.
func (s *Server) handleTaskQueueStats(c *gin.Context) {
	now := time.Now()

	var rows []struct {
		AgentID   string
		Pending   int
		Running   int
		OldestAge int64 // seconds, of the oldest pending task
	}
	query := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Task{}), c).
		Select(`agent_id,
			SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END) AS pending,
			SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END) AS running,
			COALESCE(MAX(CASE WHEN status = 'pending'
				THEN CAST(strftime('%s', ?) - strftime('%s', created_at) AS INTEGER) END), 0) AS oldest_age`,
			now.UTC().Format("2006-01-02 15:04:05")).
		Where("status IN ?", []string{"pending", "running"}).
		Group("agent_id")
	if err := query.Scan(&rows).Error; err != nil {
		handleQueryError(c, err, "Failed to compute the task queue")
		return
	}

	// The sleep interval is what makes a pending row old: a beacon that sleeps for five minutes is not stalled at four.
	sleeps := map[string]time.Duration{}
	hostnames := map[string]string{}
	for _, agent := range s.agentsOnQueue(c, rows) {
		sleep := time.Duration(agent.CurrentInterval) * time.Second
		if sleep < time.Second {
			sleep = time.Second
		}
		sleeps[agent.ID] = sleep
		hostnames[agent.ID] = agent.Hostname
	}

	entries := make([]queueEntry, 0, len(rows))
	queued := 0
	stalled := 0
	for _, row := range rows {
		sleep := sleeps[row.AgentID]
		entry := queueEntry{
			AgentID: row.AgentID, Hostname: hostnames[row.AgentID],
			Pending: row.Pending, Running: row.Running,
			OldestPending: row.OldestAge, SleepSeconds: int(sleep.Seconds()),
		}
		// Stalled means "the beacon should have polled by now": the age is what is compared, not the count, because one old task
		// is a stronger signal than ten new ones.
		if row.Pending > 0 && sleep > 0 && row.OldestAge > int64((sleep+queueStallGrace).Seconds()) {
			entry.Stalled = true
			stalled++
		}
		queued += row.Pending + row.Running
		entries = append(entries, entry)
	}

	// Busiest first: the executor is the host with the most queued work, and that is the row an operator wants.
	sortQueueEntries(entries)

	respond(c, gin.H{
		"agents":              entries,
		"queued":              queued,
		"stalled_agents":      stalled,
		"stall_grace_seconds": int(queueStallGrace.Seconds()),
		"note": "a pending task older than the beacon's sleep plus the grace period means it is not polling: " +
			"a queue problem, not a slow command",
	})
}

// agentsOnQueue loads the beacons the queue mentions, for their sleep interval and hostname.
//
// Only the rows in the answer: the fleet can be large, and the queue report is about the few agents that have work.
func (s *Server) agentsOnQueue(c *gin.Context, rows []struct {
	AgentID   string
	Pending   int
	Running   int
	OldestAge int64
}) []db.Implant {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.AgentID)
	}
	if len(ids) == 0 {
		return nil
	}
	var agents []db.Implant
	if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.Implant{}), c).
		Select("id, hostname, current_interval").Where("id IN ?", ids).Find(&agents).Error; err != nil {
		return nil
	}
	return agents
}

// sortQueueEntries orders by queued work, then by age, then by id, so two runs over the same data agree.
func sortQueueEntries(entries []queueEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		if (left.Pending + left.Running) != (right.Pending + right.Running) {
			return (left.Pending + left.Running) > (right.Pending + right.Running)
		}
		if left.OldestPending != right.OldestPending {
			return left.OldestPending > right.OldestPending
		}
		return left.AgentID < right.AgentID
	})
}
