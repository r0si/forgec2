package server

import (
	"sync"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
)

// TestFetchPendingTasksNeverCrossClaims pins the invariant behind the whole dispatch
// design: a task belongs to exactly one agent. Two agents beaconing at the same
// instant must not both receive the same row - for the second one that is a command
// executed on the wrong host.
//
// Redelivery to the SAME agent is by design (lost-response recovery, deduped agent
// side by task ID), so the assertion is per pair of agents, not "each task appears
// once overall".
func TestFetchPendingTasksNeverCrossClaims(t *testing.T) {
	s := mustTenantServer(t)
	const agentA = "a1000000-0000-0000-0000-000000000001"
	const agentB = "b2000000-0000-0000-0000-000000000002"
	seedIOCAgent(t, s, agentA)
	seedIOCAgent(t, s, agentB)

	const perAgent = 6
	for _, agent := range []string{agentA, agentB} {
		for i := 0; i < perAgent; i++ {
			task := db.Task{
				AgentID: agent, Type: "shell", Command: "whoami", Status: "pending",
				CreatedAt: time.Now(),
			}
			if err := s.db.Create(&task).Error; err != nil {
				t.Fatal(err)
			}
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	claimedBy := map[uint]map[string]bool{}
	for _, agent := range []string{agentA, agentB} {
		for i := 0; i < 4; i++ { // eight concurrent fetches in total
			wg.Add(1)
			go func(uuid string) {
				defer wg.Done()
				for _, t := range s.fetchPendingTasks(uuid, 3) {
					mu.Lock()
					if claimedBy[t.ID] == nil {
						claimedBy[t.ID] = map[string]bool{}
					}
					claimedBy[t.ID][uuid] = true
					mu.Unlock()
				}
			}(agent)
		}
	}
	wg.Wait()

	for id, agents := range claimedBy {
		if len(agents) > 1 {
			t.Fatalf("task %d was handed to more than one agent: %v", id, agents)
		}
	}

	// Every pending row is now claimed by the agent that owns it - nothing stranded.
	var stranded int64
	s.db.Model(&db.Task{}).Where("status = ? AND agent_id IN ?", "pending", []string{agentA, agentB}).Count(&stranded)
	if stranded != 0 {
		t.Fatalf("%d tasks were left pending after a burst of fetches", stranded)
	}
	var wrongOwner int64
	s.db.Model(&db.Task{}).
		Where("agent_id IN ? AND status = ? AND claimed_by <> agent_id", []string{agentA, agentB}, "running").
		Count(&wrongOwner)
	if wrongOwner != 0 {
		t.Fatalf("%d tasks are claimed by an agent they do not belong to", wrongOwner)
	}
}

// TestFetchPendingTasksDedupesWithinOneCall confirms a single beacon never receives
// the same task twice in one response: the claim update and the re-select have to
// agree, which is the reason the code returns the rows it claimed instead of
// re-querying "everything running".
func TestFetchPendingTasksDedupesWithinOneCall(t *testing.T) {
	s := mustTenantServer(t)
	const agent = "c3000000-0000-0000-0000-000000000003"
	seedIOCAgent(t, s, agent)

	for i := 0; i < 5; i++ {
		if err := s.db.Create(&db.Task{
			AgentID: agent, Type: "shell", Command: "whoami", Status: "pending", CreatedAt: time.Now(),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	got := s.fetchPendingTasks(agent, 5)
	if len(got) != 5 {
		t.Fatalf("first fetch returned %d tasks, want 5", len(got))
	}
	seen := map[uint]bool{}
	for _, task := range got {
		if seen[task.ID] {
			t.Fatalf("task %d appeared twice in one response", task.ID)
		}
		seen[task.ID] = true
	}

	// A second fetch may re-carry them (lost-response recovery) but must never exceed
	// the limit, and a task with no pending work left must not be duplicated.
	again := s.fetchPendingTasks(agent, 2)
	if len(again) > 2 {
		t.Fatalf("second fetch ignored the limit: %d tasks", len(again))
	}
}
