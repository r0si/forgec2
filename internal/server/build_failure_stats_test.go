package server

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestBuildFailureStatsGroupsByCause covers the report that answers "is the toolchain broken, or is the code".
//
// The rows already carry the failure text, so the report classifies history with the same helper that produces `error_hint` - which is the
// property worth pinning: a single failed build and this aggregate must never describe one failure differently.
func TestBuildFailureStatsGroupsByCause(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)
	now := time.Now()

	seed := func(platform, errorText string, at time.Time) {
		if err := s.db.Create(&db.BuildLog{
			Platform: platform, Format: "exe", Status: "failed", Error: errorText, CreatedAt: at,
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// Two environment failures, one code failure, one unclassifiable, and - the important negative - a success and an old failure that
	// must not be counted.
	seed("windows", `go build failed: exec: "go": executable file not found in %PATH%`, now.Add(-time.Hour))
	seed("linux", `go build failed: exec: "go": executable file not found in %PATH%`, now.Add(-2*time.Hour))
	seed("windows", "go build failed: exit status 1\n./agent.go:42:2: undefined: ResolveNtSyscalls", now.Add(-3*time.Hour))
	seed("windows", "something nobody has seen before", now.Add(-4*time.Hour))
	if err := s.db.Create(&db.BuildLog{
		Platform: "windows", Format: "exe", Status: "success", CreatedAt: now.Add(-30 * time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed success: %v", err)
	}
	seed("windows", "go build failed: exit status 1", now.Add(-72*time.Hour)) // outside a 24h window

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/stats/builds/failures", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleBuildFailureStats(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			TotalFailed int    `json:"total_failed"`
			Summary     string `json:"summary"`
			WindowHours int    `json:"window_hours"`
			Causes      []struct {
				Code      string   `json:"code"`
				Advice    string   `json:"advice"`
				Count     int      `json:"count"`
				Platforms []string `json:"platforms"`
				Example   string   `json:"example"`
			} `json:"causes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}

	// 1. Only failures inside the window, and only failures.
	if resp.Data.TotalFailed != 4 {
		t.Fatalf("total_failed = %d, want 4 (success and the 72h-old row excluded)", resp.Data.TotalFailed)
	}
	if resp.Data.WindowHours != 24 {
		t.Fatalf("window_hours = %d", resp.Data.WindowHours)
	}

	// 2. Grouped by cause, most frequent first.
	byCode := map[string]int{}
	for _, cause := range resp.Data.Causes {
		byCode[cause.Code] += cause.Count
	}
	if byCode[HintToolchainMissing] != 2 || byCode[HintCompileError] != 1 || byCode[HintUnknown] != 1 {
		t.Fatalf("causes = %v", byCode)
	}
	if resp.Data.Causes[0].Code != HintToolchainMissing || resp.Data.Causes[0].Count != 2 {
		t.Fatalf("the most frequent cause is not first: %+v", resp.Data.Causes)
	}

	// 3. The environment cause says what to do, and names both platforms it happened on.
	env := resp.Data.Causes[0]
	if env.Advice == "" {
		t.Fatal("the environment cause carries no advice")
	}
	if len(env.Platforms) != 2 {
		t.Fatalf("platforms = %v, want both windows and linux", env.Platforms)
	}

	// 4. A sample is carried, and the summary states the leading cause - the reading nobody should have to derive.
	if env.Example == "" {
		t.Fatal("no example failure text")
	}
	if resp.Data.Summary == "" || !containsAny(resp.Data.Summary, HintToolchainMissing) {
		t.Fatalf("summary does not name the leading cause: %q", resp.Data.Summary)
	}

	// 5. A window with no failures says so, rather than serving an empty object and leaving the caller to guess.
	empty := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(empty)
	// One minute, not one hour: the newest seeded failure is exactly an hour old, so a 1h window sits ON the boundary and the row is
	// included or excluded depending on microseconds - a flake that would be read as a product bug.
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/stats/builds/failures?since=1m", nil)
	c2.Set("user", "op")
	c2.Set("user_role", db.RoleAdmin)
	c2.Set("user_id", uint(1))
	c2.Set(middleware.TenantIDContextKey, uint(1))
	s.handleBuildFailureStats(c2)
	var emptyResp struct {
		Data struct {
			TotalFailed int    `json:"total_failed"`
			Summary     string `json:"summary"`
			Causes      []struct {
				Code string `json:"code"`
			} `json:"causes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(empty.Body.Bytes(), &emptyResp); err != nil {
		t.Fatalf("decode empty: %v", err)
	}
	if emptyResp.Data.TotalFailed != 0 || emptyResp.Data.Summary == "" {
		t.Fatalf("an empty window = %+v", emptyResp.Data)
	}

	// 6. A malformed window is a 400, not a silently defaulted report.
	bad := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(bad)
	c3.Request = httptest.NewRequest(http.MethodGet, "/api/stats/builds/failures?since=whenever", nil)
	c3.Set("user", "op")
	c3.Set("user_role", db.RoleAdmin)
	c3.Set("user_id", uint(1))
	c3.Set(middleware.TenantIDContextKey, uint(1))
	s.handleBuildFailureStats(c3)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("malformed since = %d, want 400", bad.Code)
	}

	// 6b. The table can be taken away as a file, one row per (cause, platform), so the counts add up at the other end without a
	// caller splitting a comma-joined list again.
	csvRecorder := httptest.NewRecorder()
	cCsv, _ := gin.CreateTestContext(csvRecorder)
	cCsv.Request = httptest.NewRequest(http.MethodGet, "/api/stats/builds/failures?format=csv", nil)
	cCsv.Set("user", "op")
	cCsv.Set("user_role", db.RoleAdmin)
	cCsv.Set("user_id", uint(1))
	cCsv.Set(middleware.TenantIDContextKey, uint(1))
	s.handleBuildFailureStats(cCsv)
	if csvRecorder.Code != http.StatusOK {
		t.Fatalf("csv = %d (%s)", csvRecorder.Code, csvRecorder.Body.String())
	}
	csvRows, err := csv.NewReader(strings.NewReader(csvRecorder.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	if len(csvRows) < 2 || strings.Join(csvRows[0], ",") != "Cause,Advice,Count,Platforms,Example" {
		t.Fatalf("csv header = %v", csvRows[0])
	}
	// The environment cause happened on two platforms, so it must occupy two rows - and their counts must sum to the group count.
	rowsForToolchain, sum := 0, 0
	for _, row := range csvRows[1:] {
		if row[0] != HintToolchainMissing {
			continue
		}
		rowsForToolchain++
		count, convErr := strconv.Atoi(row[2])
		if convErr != nil {
			t.Fatalf("count %q: %v", row[2], convErr)
		}
		sum += count
	}
	if rowsForToolchain != 2 {
		t.Fatalf("the two-platform cause occupies %d row(s), want 2", rowsForToolchain)
	}
	if sum != 4 {
		t.Fatalf("the per-platform counts sum to %d, want the group total 4 (each cause is repeated per platform)", sum)
	}

	// 7. Both window spellings are accepted: a bare hour count and a duration.
	for _, spelling := range []string{"12", "12h", "90m"} {
		recorder := httptest.NewRecorder()
		c4, _ := gin.CreateTestContext(recorder)
		c4.Request = httptest.NewRequest(http.MethodGet, "/api/stats/builds/failures?since="+spelling, nil)
		c4.Set("user", "op")
		c4.Set("user_role", db.RoleAdmin)
		c4.Set("user_id", uint(1))
		c4.Set(middleware.TenantIDContextKey, uint(1))
		s.handleBuildFailureStats(c4)
		if recorder.Code != http.StatusOK {
			t.Fatalf("since=%s rejected: %d (%s)", spelling, recorder.Code, recorder.Body.String())
		}
	}
}
