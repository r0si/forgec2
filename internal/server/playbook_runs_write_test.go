package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestPlaybookRunWritePath covers creating a run and advancing it.
//
// The endpoint records; it does not dispatch. So the assertions are about what the state machine accepted and what the audit log
// says - and about the refusals, because a transition endpoint that answers 200 for a transition it did not apply is worse than one
// that fails.
func TestPlaybookRunWritePath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	type callResult struct {
		code int
		body map[string]interface{}
	}
	call := func(method, path string, body interface{}) callResult {
		var payload *bytes.Reader
		if body != nil {
			encoded, _ := json.Marshal(body)
			payload = bytes.NewReader(encoded)
		} else {
			payload = bytes.NewReader(nil)
		}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, path, payload)
		c.Request.Header.Set("Content-Type", "application/json")
		if rest := strings.TrimPrefix(path, "/api/playbooks/runs/"); rest != path && rest != "" {
			c.Params = gin.Params{{Key: "id", Value: strings.TrimSuffix(rest, "/transitions")}}
		}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		switch {
		case method == http.MethodPost && strings.HasSuffix(path, "/transitions"):
			s.handleTransitionPlaybookRun(c)
		case method == http.MethodPost:
			s.handleCreatePlaybookRun(c)
		default:
			s.handleGetPlaybookRun(c)
		}
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return callResult{code: w.Code, body: resp.Data}
	}

	// 1. A definition with a cycle is refused before anything is stored.
	cyclic := call(http.MethodPost, "/api/playbooks/runs", map[string]interface{}{
		"name": "cycle",
		"steps": []map[string]interface{}{
			{"id": "a", "needs": []string{"b"}},
			{"id": "b", "needs": []string{"a"}},
		},
	})
	if cyclic.code != http.StatusBadRequest {
		t.Fatalf("cyclic definition = %d, want 400", cyclic.code)
	}
	var stored int64
	if err := s.db.Model(&db.PlaybookRun{}).Count(&stored).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if stored != 0 {
		t.Fatalf("a refused definition was stored: %d run(s)", stored)
	}

	// 2. A valid definition is stored with its initial state.
	created := call(http.MethodPost, "/api/playbooks/runs", map[string]interface{}{
		"name": "AD path",
		"steps": []map[string]interface{}{
			{"id": "recon"},
			{"id": "exploit", "needs": []string{"recon"}},
			{"id": "exfil", "needs": []string{"exploit"}, "approval": true},
		},
	})
	if created.code != http.StatusOK {
		t.Fatalf("create = %d", created.code)
	}
	runID := itoaUint(uint(created.body["id"].(float64)))
	if created.body["state"] != "blocked" {
		t.Fatalf("initial state = %v, want blocked (the approval step waits)", created.body["state"])
	}

	// 3. A transition the state refuses is a 409, not a 200 and not a 400.
	early := call(http.MethodPost, "/api/playbooks/runs/"+runID+"/transitions", map[string]interface{}{
		"step": "exploit", "action": "start",
	})
	if early.code != http.StatusConflict {
		t.Fatalf("starting a dependent early = %d, want 409", early.code)
	}

	// 4. The valid sequence advances and reports the ready set.
	for _, transition := range []map[string]interface{}{
		{"step": "recon", "action": "start"},
		{"step": "recon", "action": "succeed"},
	} {
		result := call(http.MethodPost, "/api/playbooks/runs/"+runID+"/transitions", transition)
		if result.code != http.StatusOK {
			t.Fatalf("%v = %d", transition, result.code)
		}
	}
	advanced := call(http.MethodPost, "/api/playbooks/runs/"+runID+"/transitions", map[string]interface{}{
		"step": "exploit", "action": "start",
	})
	if advanced.code != http.StatusOK {
		t.Fatalf("start exploit = %d", advanced.code)
	}
	if advanced.body["settled"] != float64(1) || advanced.body["total"] != float64(3) {
		t.Fatalf("progress = %v/%v", advanced.body["settled"], advanced.body["total"])
	}
	// The note must say the endpoint records rather than dispatches: that is the whole security claim of this slice.
	if note, _ := advanced.body["note"].(string); !strings.Contains(note, "does not dispatch") {
		t.Fatalf("the answer does not state that nothing was dispatched: %q", note)
	}

	// 5. Failure with a reason is recorded and cascades.
	failed := call(http.MethodPost, "/api/playbooks/runs/"+runID+"/transitions", map[string]interface{}{
		"step": "exploit", "action": "fail", "reason": "EDR blocked the loader",
	})
	if failed.code != http.StatusOK {
		t.Fatalf("fail exploit = %d", failed.code)
	}
	detail := call(http.MethodGet, "/api/playbooks/runs/"+runID, nil)
	if detail.body["state"] != "failed" {
		t.Fatalf("state after the failure = %v", detail.body["state"])
	}

	// 6. An unknown action is refused rather than defaulted, and an unknown step is a 409 from the machine.
	for _, body := range []map[string]interface{}{
		{"step": "recon", "action": "explode"},
		{"step": "ghost", "action": "start"},
	} {
		result := call(http.MethodPost, "/api/playbooks/runs/"+runID+"/transitions", body)
		if result.code != http.StatusBadRequest && result.code != http.StatusConflict {
			t.Fatalf("%v = %d, want 400 or 409", body, result.code)
		}
	}

	// 7. Both writes are audited, so "who advanced the playbook" has an answer.
	for _, action := range []string{"playbook_run_create", "playbook_run_transition"} {
		var count int64
		if err := s.db.Model(&db.AuditLog{}).Where("action = ?", action).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", action, err)
		}
		if count == 0 {
			t.Fatalf("%s was not audited", action)
		}
	}
}
