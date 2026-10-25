package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func TestScreenMonitorLifecycleIsValidatedAndIdempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	srv := newTestFileServer(t, database)
	agent := seedImplant(t, database)

	form := url.Values{"interval": {"3"}, "quality": {"high"}}
	w, c := newFormContext(http.MethodPost, "/", &form)
	c.Params = gin.Params{{Key: "id", Value: agent.ID}}
	srv.handleStartScreenMonitor(c)
	assertStatus(t, w, http.StatusOK)
	response := assertSuccessJSON(t, w)
	if response["active"] != true || response["task_id"] == nil {
		t.Fatalf("unexpected start response: %s", w.Body.String())
	}

	var startTasks []db.Task
	if err := database.Where("agent_id = ? AND type = ?", agent.ID, "screen_stream_start").Find(&startTasks).Error; err != nil {
		t.Fatalf("load start task: %v", err)
	}
	if len(startTasks) != 1 || startTasks[0].Command != "3,high" {
		t.Fatalf("expected one 3,high task, got %#v", startTasks)
	}

	// Starting the same agent again must not consume another monitor slot or
	// queue a duplicate stream goroutine on the agent.
	w, c = newFormContext(http.MethodPost, "/", &form)
	c.Params = gin.Params{{Key: "id", Value: agent.ID}}
	srv.handleStartScreenMonitor(c)
	assertStatus(t, w, http.StatusOK)
	response = assertSuccessJSON(t, w)
	if response["already_active"] != true {
		t.Fatalf("expected already_active response: %s", w.Body.String())
	}
	var startCount int64
	database.Model(&db.Task{}).Where("agent_id = ? AND type = ?", agent.ID, "screen_stream_start").Count(&startCount)
	if startCount != 1 {
		t.Fatalf("duplicate start queued %d tasks", startCount)
	}

	w, c = newFormContext(http.MethodPost, "/", nil)
	c.Params = gin.Params{{Key: "id", Value: agent.ID}}
	srv.handleStopScreenMonitor(c)
	assertStatus(t, w, http.StatusOK)
	if srv.IsScreenMonitoring(agent.ID) {
		t.Fatal("agent remained registered after stop")
	}
	var preservedStartCount int64
	database.Model(&db.Task{}).Where("agent_id = ? AND type = ?", agent.ID, "screen_stream_start").Count(&preservedStartCount)
	if preservedStartCount != 1 {
		t.Fatalf("stop deleted screen history; start count=%d", preservedStartCount)
	}

	// Repeated stop is a successful no-op and must not queue more stop tasks.
	w, c = newFormContext(http.MethodPost, "/", nil)
	c.Params = gin.Params{{Key: "id", Value: agent.ID}}
	srv.handleStopScreenMonitor(c)
	assertStatus(t, w, http.StatusOK)
	var stopCount int64
	database.Model(&db.Task{}).Where("agent_id = ? AND type = ?", agent.ID, "screen_stream_stop").Count(&stopCount)
	if stopCount != 1 {
		t.Fatalf("idempotent stop queued %d tasks", stopCount)
	}
}

func TestScreenMonitorRejectsInvalidIntervalWithoutRegistering(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	srv := newTestFileServer(t, database)
	agent := seedImplant(t, database)

	form := url.Values{"interval": {"3000"}, "quality": {"medium"}}
	w, c := newFormContext(http.MethodPost, "/", &form)
	c.Params = gin.Params{{Key: "id", Value: agent.ID}}
	srv.handleStartScreenMonitor(c)
	assertStatus(t, w, http.StatusBadRequest)
	if srv.IsScreenMonitoring(agent.ID) {
		t.Fatal("invalid request registered a monitor")
	}
}

// TestScreenMonitorValidatesWidth proves the width parameter cannot inject
// extra fields into the agent's CSV stream command and that a valid width is
// canonicalized into the command.
func TestScreenMonitorValidatesWidth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	srv := newTestFileServer(t, database)
	agent := seedImplant(t, database)

	for _, width := range []string{"1920,evil", "abc", "0", "99999999", "-1", "1.5"} {
		form := url.Values{"interval": {"5"}, "quality": {"medium"}, "width": {width}}
		w, c := newFormContext(http.MethodPost, "/", &form)
		c.Params = gin.Params{{Key: "id", Value: agent.ID}}
		srv.handleStartScreenMonitor(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("width %q: status=%d, want 400 (body=%s)", width, w.Code, w.Body.String())
		}
	}
	var startCount int64
	database.Model(&db.Task{}).Where("agent_id = ? AND type = ?", agent.ID, "screen_stream_start").Count(&startCount)
	if startCount != 0 {
		t.Fatalf("invalid widths queued %d start tasks", startCount)
	}

	form := url.Values{"interval": {"5"}, "quality": {"medium"}, "width": {"1920"}}
	w, c := newFormContext(http.MethodPost, "/", &form)
	c.Params = gin.Params{{Key: "id", Value: agent.ID}}
	srv.handleStartScreenMonitor(c)
	assertStatus(t, w, http.StatusOK)

	var task db.Task
	if err := database.Where("agent_id = ? AND type = ?", agent.ID, "screen_stream_start").First(&task).Error; err != nil {
		t.Fatalf("load start task: %v", err)
	}
	if task.Command != "5,medium,1920" {
		t.Fatalf("stream command = %q, want canonical 5,medium,1920", task.Command)
	}
}

// TestScreenMonitorValidatesMime proves the mime parameter is validated and
// that a mime without a width is rejected instead of mis-aligning the agent's
// positional CSV stream command ("interval,quality[,width][,mime]").
func TestScreenMonitorValidatesMime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	srv := newTestFileServer(t, database)
	agent := seedImplant(t, database)

	cases := []struct {
		name string
		form url.Values
	}{
		{"unknown", url.Values{"interval": {"5"}, "quality": {"medium"}, "width": {"1920"}, "mime": {"exe"}}},
		{"injection", url.Values{"interval": {"5"}, "quality": {"medium"}, "width": {"1920"}, "mime": {"webp,evil"}}},
		{"mime without width", url.Values{"interval": {"5"}, "quality": {"medium"}, "mime": {"webp"}}},
	}
	for _, tc := range cases {
		form := tc.form
		w, c := newFormContext(http.MethodPost, "/", &form)
		c.Params = gin.Params{{Key: "id", Value: agent.ID}}
		srv.handleStartScreenMonitor(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d, want 400 (body=%s)", tc.name, w.Code, w.Body.String())
		}
	}
	var startCount int64
	database.Model(&db.Task{}).Where("agent_id = ? AND type = ?", agent.ID, "screen_stream_start").Count(&startCount)
	if startCount != 0 {
		t.Fatalf("invalid mime queued %d start tasks", startCount)
	}

	form := url.Values{"interval": {"5"}, "quality": {"medium"}, "width": {"1920"}, "mime": {"WebP"}}
	w, c := newFormContext(http.MethodPost, "/", &form)
	c.Params = gin.Params{{Key: "id", Value: agent.ID}}
	srv.handleStartScreenMonitor(c)
	assertStatus(t, w, http.StatusOK)

	var task db.Task
	if err := database.Where("agent_id = ? AND type = ?", agent.ID, "screen_stream_start").First(&task).Error; err != nil {
		t.Fatalf("load start task: %v", err)
	}
	if task.Command != "5,medium,1920,webp" {
		t.Fatalf("stream command = %q, want canonical 5,medium,1920,webp", task.Command)
	}
}

// TestScreenMonitorRejectsConcurrentOperations proves the single-flight
// reservation: while a start/stop is in flight (task creation outside the
// registry lock), a second request gets 409 instead of double-queueing.
func TestScreenMonitorRejectsConcurrentOperations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	srv := newTestFileServer(t, database)
	agent := seedImplant(t, database)
	key := strings.ToLower(agent.ID)

	srv.screenMonitorOps = map[string]string{key: "start"}
	form := url.Values{"interval": {"5"}, "quality": {"medium"}}
	w, c := newFormContext(http.MethodPost, "/", &form)
	c.Params = gin.Params{{Key: "id", Value: agent.ID}}
	srv.handleStartScreenMonitor(c)
	assertStatus(t, w, http.StatusConflict)

	var startCount int64
	database.Model(&db.Task{}).Where("agent_id = ? AND type = ?", agent.ID, "screen_stream_start").Count(&startCount)
	if startCount != 0 {
		t.Fatalf("in-flight start queued %d tasks", startCount)
	}

	// A stop against an in-flight stop is also refused.
	srv.screenMonitorImplants[agent.ID] = time.Now()
	srv.screenMonitorOps[key] = "stop"
	w, c = newFormContext(http.MethodPost, "/", nil)
	c.Params = gin.Params{{Key: "id", Value: agent.ID}}
	srv.handleStopScreenMonitor(c)
	assertStatus(t, w, http.StatusConflict)
	if !srv.IsScreenMonitoring(agent.ID) {
		t.Fatal("in-flight stop dropped the registration early")
	}
}

func TestLatestScreenFrameProvidesHTTPFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newContractDB(t)
	srv := newTestFileServer(t, database)
	agent := seedImplant(t, database)
	srv.screenMonitorImplants[agent.ID] = time.Now()
	srv.BroadcastScreenshot(agent.ID, "/9j/test-frame")

	w, c := newFormContext(http.MethodGet, "/", nil)
	c.Params = gin.Params{{Key: "id", Value: agent.ID}}
	srv.handleGetAgentScreenshot(c)
	assertStatus(t, w, http.StatusOK)

	var response struct {
		Image     string `json:"image"`
		CaptureID string `json:"capture_id"`
	}
	unmarshalData(t, w.Body.Bytes(), &response)
	if response.Image != "data:image/jpeg;base64,/9j/test-frame" || response.CaptureID == "" {
		t.Fatalf("unexpected fallback response: %s", w.Body.String())
	}
}
