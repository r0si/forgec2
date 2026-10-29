package playbook

import (
	"encoding/json"
	"testing"
)

// TestNormaliseRefusesUnrunnableDefinitions covers the definitions that can never finish.
//
// Each of these produces a run that looks fine in a list and is dead on arrival: a cycle, a dependency nobody defined, a duplicate
// id that silently shadows its twin.
func TestNormaliseRefusesUnrunnableDefinitions(t *testing.T) {
	cases := []struct {
		name string
		run  Run
	}{
		{"no steps", Run{}},
		{"empty id", Run{Steps: []Step{{ID: ""}}}},
		{"duplicate id", Run{Steps: []Step{{ID: "a"}, {ID: "a"}}}},
		{"unknown dependency", Run{Steps: []Step{{ID: "a", Needs: []string{"ghost"}}}}},
		{"self dependency", Run{Steps: []Step{{ID: "a", Needs: []string{"a"}}}}},
		{"two-step cycle", Run{Steps: []Step{{ID: "a", Needs: []string{"b"}}, {ID: "b", Needs: []string{"a"}}}}},
		{"three-step cycle", Run{Steps: []Step{
			{ID: "a", Needs: []string{"c"}}, {ID: "b", Needs: []string{"a"}}, {ID: "c", Needs: []string{"b"}},
		}}},
	}
	for _, testCase := range cases {
		run := testCase.run
		if err := run.Normalise(); err == nil {
			t.Fatalf("%s: accepted an unrunnable playbook: %+v", testCase.name, run.Steps)
		}
	}

	// And the positive control: a valid graph normalises and gets its initial statuses.
	valid := Run{Steps: []Step{{ID: "recon"}, {ID: "exploit", Needs: []string{"recon"}}, {ID: "exfil", Approval: true}}}
	if err := valid.Normalise(); err != nil {
		t.Fatalf("valid playbook refused: %v", err)
	}
	if valid.Steps[0].Status != StatusPending {
		t.Fatalf("recon status = %q", valid.Steps[0].Status)
	}
	if valid.Steps[2].Status != StatusBlocked {
		t.Fatalf("an approval step should start blocked, got %q", valid.Steps[2].Status)
	}
}

// TestReadyRespectsDependenciesAndApproval pins what "runnable" means.
func TestReadyRespectsDependenciesAndApproval(t *testing.T) {
	run := Run{Steps: []Step{
		{ID: "recon"},
		{ID: "exploit", Needs: []string{"recon"}},
		{ID: "exfil", Needs: []string{"exploit"}, Approval: true},
	}}
	if err := run.Normalise(); err != nil {
		t.Fatalf("normalise: %v", err)
	}

	// 1. Only the root is ready at the start; the approval step is blocked, not ready.
	if ready := ids(run.Ready()); len(ready) != 1 || ready[0] != "recon" {
		t.Fatalf("ready at start = %v, want [recon]", ready)
	}

	// 2. A dependency that has not SUCCEEDED does not unlock its dependent - running is not done.
	if err := run.Start("recon"); err != nil {
		t.Fatalf("start recon: %v", err)
	}
	if ready := ids(run.Ready()); len(ready) != 0 {
		t.Fatalf("ready while recon runs = %v, want none", ready)
	}
	if err := run.Succeed("recon"); err != nil {
		t.Fatalf("succeed recon: %v", err)
	}
	if ready := ids(run.Ready()); len(ready) != 1 || ready[0] != "exploit" {
		t.Fatalf("ready after recon = %v, want [exploit]", ready)
	}

	// 3. Approving early must not jump the queue.
	if err := run.Approve("exfil"); err == nil {
		t.Fatal("approved a step whose dependency had not succeeded")
	}
	if err := run.Start("exfil"); err == nil {
		t.Fatal("started a blocked step")
	}
}

// TestApproveReleasesOnlyItsOwnStep covers the approval transition, including the refusal to approve what needs no approval.
func TestApproveReleasesOnlyItsOwnStep(t *testing.T) {
	run := Run{Steps: []Step{{ID: "recon"}, {ID: "exfil", Approval: true}}}
	if err := run.Normalise(); err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if err := run.Approve("recon"); err == nil {
		t.Fatal("approved a step that does not require approval")
	}
	if err := run.Approve("exfil"); err != nil {
		t.Fatalf("approve exfil: %v", err)
	}
	if ready := ids(run.Ready()); len(ready) != 2 {
		t.Fatalf("ready after approval = %v, want both", ready)
	}
	// Approving twice is a refusal, not a silent no-op: the second call means the caller lost track of the state.
	if err := run.Approve("exfil"); err == nil {
		t.Fatal("approved an already-approved step")
	}
}

// TestFailureCascadesAndHidesNoWork covers the cascade: a failed step skips its descendants and the report stops claiming they are
// still possible.
func TestFailureCascadesAndHidesNoWork(t *testing.T) {
	run := Run{Steps: []Step{
		{ID: "recon"},
		{ID: "exploit", Needs: []string{"recon"}},
		{ID: "exfil", Needs: []string{"exploit"}},
		{ID: "independent"},
	}}
	if err := run.Normalise(); err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if err := run.Start("exploit"); err == nil {
		t.Fatal("started exploit before recon succeeded")
	}
	for _, id := range []string{"recon"} {
		if err := run.Start(id); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
		if err := run.Succeed(id); err != nil {
			t.Fatalf("succeed %s: %v", id, err)
		}
	}
	if err := run.Start("exploit"); err != nil {
		t.Fatalf("start exploit: %v", err)
	}
	if err := run.Fail("exploit", "EDR blocked the loader"); err != nil {
		t.Fatalf("fail exploit: %v", err)
	}

	// The descendant is skipped and says why.
	var exfil Step
	for _, step := range run.Steps {
		if step.ID == "exfil" {
			exfil = step
		}
	}
	if exfil.Status != StatusSkipped || exfil.SkippedBecause != "exploit" {
		t.Fatalf("exfil = %+v, want skipped because exploit", exfil)
	}
	// The independent branch is untouched.
	if ready := ids(run.Ready()); len(ready) != 1 || ready[0] != "independent" {
		t.Fatalf("ready after the failure = %v, want [independent]", ready)
	}
	// The run reports failure first, whatever else could still run.
	if state := run.State(); state != "failed" {
		t.Fatalf("state = %q, want failed", state)
	}
	// Settled counts succeeded, failed AND skipped: a skipped branch is decided, and reporting it as still-possible work is the
	// "looks unfinished when it is over" error this state machine exists to avoid.
	if settled, total := run.Progress(); settled != 3 || total != 4 {
		t.Fatalf("progress = %d/%d, want 3/4 (recon succeeded, exploit failed, exfil skipped)", settled, total)
	}
}

// TestStateAndProgress covers the four summary states a caller switches on.
func TestStateAndProgress(t *testing.T) {
	run := Run{Steps: []Step{{ID: "a"}, {ID: "b", Needs: []string{"a"}}, {ID: "c", Approval: true}}}
	if err := run.Normalise(); err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if state := run.State(); state != "blocked" {
		t.Fatalf("state with an approval step = %q, want blocked", state)
	}
	if err := run.Approve("c"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if state := run.State(); state != "partial" {
		t.Fatalf("state with pending work = %q, want partial", state)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := run.Start(id); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
		if err := run.Succeed(id); err != nil {
			t.Fatalf("succeed %s: %v", id, err)
		}
	}
	if state := run.State(); state != "complete" {
		t.Fatalf("state at the end = %q, want complete", state)
	}
	if settled, total := run.Progress(); settled != 3 || total != 3 {
		t.Fatalf("progress = %d/%d", settled, total)
	}
}

// TestResumeFromStoredRun covers the property the whole thing exists for: a run picked up from storage continues where it stopped.
func TestResumeFromStoredRun(t *testing.T) {
	original := Run{ID: "run-1", Name: "AD path", Steps: []Step{
		{ID: "recon"},
		{ID: "exploit", Needs: []string{"recon"}},
		{ID: "exfil", Needs: []string{"exploit"}, Approval: true},
	}}
	if err := original.Normalise(); err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if err := original.Start("recon"); err != nil {
		t.Fatalf("start recon: %v", err)
	}
	if err := original.Succeed("recon"); err != nil {
		t.Fatalf("succeed recon: %v", err)
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// The process restarts: the run is decoded and normalised again.
	var resumed Run
	if err := json.Unmarshal(encoded, &resumed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := resumed.Normalise(); err != nil {
		t.Fatalf("normalise resumed: %v", err)
	}
	// Normalise must not reset a terminal status: what the run knew, it keeps.
	if resumed.Steps[0].Status != StatusSucceeded {
		t.Fatalf("recon status after resume = %q, want succeeded", resumed.Steps[0].Status)
	}
	if resumed.Steps[2].Status != StatusBlocked {
		t.Fatalf("exfil status after resume = %q, want blocked", resumed.Steps[2].Status)
	}
	if ready := ids(resumed.Ready()); len(ready) != 1 || ready[0] != "exploit" {
		t.Fatalf("ready after resume = %v, want [exploit]", ready)
	}
	// And the attempt counter survived, so "this has run once already" is answerable.
	if err := resumed.Start("exploit"); err != nil {
		t.Fatalf("start exploit: %v", err)
	}
	if resumed.Steps[1].Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", resumed.Steps[1].Attempts)
	}
}

// ids renders a step slice as ids, for readable assertions.
func ids(steps []Step) []string {
	out := make([]string, 0, len(steps))
	for _, step := range steps {
		out = append(out, step.ID)
	}
	return out
}
